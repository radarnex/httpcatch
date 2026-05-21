# httpcatch

A single-binary HTTP traffic capture and inspection tool. It receives HTTP traffic (typically mirrored from a proxy or pushed by app middleware), redacts sensitive data, and stores the result for later inspection via API and web UI.

## Language

**httpcatch**:
The whole product — both the running service and the binary. (Previously brainstormed as `httptap`; renamed because the project is the receive-end of a mirror, not the tap itself — the proxy is the tap.)

**Lightweight**:
Operationally lightweight: ships as a single static binary, embeds its UI, and uses only SQLite for persistence. No external services required. _Avoid_: small in feature scope, low-memory, minimal codebase — those are not what "lightweight" guarantees here.

**Capture port**:
The HTTP listener that accepts ingested traffic. Every request to this port is treated as captured traffic; there are no reserved paths. Operators point their proxy's mirror at this port or POST structured events to it.

**Admin port**:
A separate HTTP listener for the inspect API, health checks, and the embedded UI. Not part of the capture surface.

**Ingestion contract**:
Wire-level HTTP. Any HTTP request reaching the **capture port** is captured. httpcatch does not provide its own mirroring — operators configure that on their proxy (Traefik, nginx, Envoy, etc.) using example configs shipped in this repo.

**Capture pipeline**:
Asynchronous. The **capture port** acks every request with `202 Accepted` after enqueuing it onto a bounded in-memory **capture queue**. Background workers drain the queue, redact, and persist. If the queue is full at enqueue time, the request is **dropped** silently; a `dropped_total` counter is incremented and surfaced via metrics and the UI.

**Sinks**:
Storage destinations for captured traffic. Operators enable any subset of `{memory, sqlite, stdout}` and writes fan out to every enabled sink. Each sink consumes the same redacted payload. **Memory** is a bounded ring buffer; **sqlite** is the durable indexed store; **stdout** is a JSON event emitter for external stream consumers.

**Inspect API**:
The read-side admin surface (`GET /requests`, `GET /requests/{id}`, search/filter). Reads are tiered: recent/live-tail queries served from **memory** if enabled, search and history from **sqlite**. If neither is enabled (e.g. stdout-only mode), inspect returns empty results — this is a legitimate stream-only configuration, not a misconfiguration.

**Events API**:
The structured app-side ingestion endpoint (`POST /events`) for app-emitted records that the wire-level **capture port** cannot see. Feeds the same capture pipeline as the capture port. Accepts either a single event object or an array of events. Two event types in the first release:

- **Response event** (`type: "response"`) — the response an app returned for a captured inbound request, carrying `correlation_id`, `service`, status, headers, body, `duration_ms`.
- **Outbound event** (`type: "outbound"`) — a downstream call the app made, carrying `correlation_id`, `service`, and both halves of the call (request and response) in one event plus `duration_ms`. `response` may be `null` when the call never completed.

Both event types reuse the same **correlation key** semantics as captured requests. `request` events and operator-defined custom event types are explicitly out of scope for the first release.

**Correlation key**:
The opaque string that links a captured request to its app-side events. Derived per record: `traceparent`'s trace-id is preferred; `X-Request-ID` is the fallback. `X-Correlation-ID` is not part of the contract. If neither header is present, httpcatch synthesizes a UUID and marks the record as `correlation: synthesized` — those records cannot be matched by later events, and a `captured_without_correlation_total` metric surfaces the gap. The proxy (not httpcatch) is responsible for injecting the header before mirroring.

**Redaction**:
The destructive transformation applied to captured payloads before they reach **sinks**. Supports rules over headers, query parameters, JSON paths, regex patterns, and cookies. Rules are **global** — one ruleset, applied once, every enabled sink sees the same redacted output. If zero rules are configured, captured data is stored as-is; this is a legitimate "unredacted mode" but httpcatch emits a prominent startup warning and a persistent UI banner so operators cannot stumble into it silently. Rule reload requires a process restart in the first release.

**Redaction dry-run**:
A `httpcatch redact --test` subcommand that evaluates the current ruleset against a sample request file and prints the before/after diff. Nothing is persisted. This is the supported way to validate rules before deploying them.

**Admin token**:
The single bearer token that authenticates the **inspect API**, the **events API**, and the embedded UI. Configured via env or config file. The **admin port** defaults to binding `127.0.0.1` and refuses to start on a non-loopback bind unless either a token is configured or the operator explicitly opts in with an insecure-mode flag. `/healthz` is always unauthenticated. A single httpcatch instance uses a single admin token regardless of how many **services** it captures.

**Service**:
The logical source a captured record is attributed to. A single httpcatch instance captures traffic for many services. For events posted via the **events API**, `service` is supplied in the event body. For mirrored traffic, `service` is derived in order: the `X-Httpcatch-Service` header (name configurable), else the `Host` header (lowercased, port stripped), else the literal `"unknown"`. Records with `service: "unknown"` are still stored and shown in the UI under that bucket; a `captured_without_service_total` metric surfaces how often the fallback chain bottoms out.

**Body cap**:
A global byte limit on stored bodies (default 1 MiB, configurable, `0` disables). Applies identically to captured-request bodies and event bodies. Bodies exceeding the cap are stored truncated to the first N bytes, with `body_truncated: true` and `body_original_size` recorded alongside. The record itself is never dropped on size.

**Retention**:
The trim policy for the SQLite store. Operators configure exactly one of time-based (`keep records younger than 7d`) or count-based (`keep the most recent 1000 records`) — the two are mutually exclusive. Retention is global; per-service overrides are not part of the first release. A background sweeper deletes records that fall outside the policy. The **memory** ring buffer has its own bound (count-based by construction) independent of this setting.

**Indexed dimensions**:
The fields a captured record (or event) is searchable and filterable by with index-backed performance, both in the **inspect API** and the UI: `timestamp`, `service`, `host`, `correlation_id`, `method`, `path`, `status` (events only), `source_ip`, `has_events` (captured requests only — whether at least one correlated event has arrived), `duration_ms` (events only).

**Scanned dimensions**:
Fields searchable but not indexed — answered by scanning the matching captured records. Correct but slower than indexed dimensions and degrading as the store grows; operators feeling the cost are expected to narrow the query first with an **indexed dimension** (typically a time range or `service`) before adding a scanned predicate. The captured-request body and headers are scanned dimensions. Per-field indexes for these are out of scope under the **lightweight** constraint (no embedded search engine; see ADR-0001).

**Search**:
The query shape accepted by the **inspect API** (`GET /requests`) and the UI search box. A query is a whitespace-separated list of terms, AND'd together; a `-` prefix negates a term. Each term is either field-qualified (`host:`, `path:`, `service:`, `body:`, `headers:`, `header.<name>:`, `method:`, `status:`, `source_ip:`, `correlation_id:`) or a bare freeform term. The freeform term unions over a fixed field set: `host`, `path`, `service` (matched exactly unless wildcarded) and the captured-request and event bodies and headers (substring). Structured fields — `method`, `status`, `source_ip`, `correlation_id` — are field-qualified only and do not participate in freeform. There are no `AND`/`OR`/`NOT` keywords and no grouping parens in this release; the upgrade path to a full KQL grammar is open and non-breaking.

**Per-header search**:
`headers:foo` scans the full headers JSON for a substring match in any key or value. `header.<name>:foo` matches when the named header's value contains `foo`; header names are canonicalised case-insensitively, so `header.User-Agent:` and `header.user-agent:` are equivalent. A missing header never matches; `-header.<name>:foo` matches rows where the header is absent or its values don't contain `foo`. Both forms apply across `captured_requests.headers`, `events.request_headers`, and `events.response_headers`. Wildcards in the header *name* part (`header.x-*:foo`) are not supported in this release.

**Wildcards**:
Tokens (freeform or field-qualified) accept `*` as a wildcard. `foo*` matches as a prefix; `*foo` and `*foo*` match as substring. Other glob metacharacters (`?`, `[…]`) are not supported. Wildcards on **scanned dimensions** collapse to the underlying substring (`body:*foo*` ≡ `body:foo`). Tokens may be quoted (`"foo bar"`) to preserve whitespace; inside quotes `*` is a literal character and the value is matched verbatim.

**Unindexed scan**:
A query whose shape forces an **indexed dimension** to be matched as a substring — i.e., a leading wildcard on `host`, `path`, or `service`, either named or implicit in the freeform term. Such queries bypass the field's index; cost scales with the total row count in the matching time range. The **inspect API** sets `X-Httpcatch-Scan: leading-wildcard-indexed` on these responses; the UI surfaces a warning chip beside the search box. Operators are expected to narrow with an indexed dimension (time range, `service`) before resorting to this shape.

## Example dialogue

> **Operator:** "Why is my UI empty even though traffic is flowing?"
> **httpcatch dev:** "What **sinks** do you have enabled? If you only have **stdout**, the **inspect API** has nothing to read — that's stream-only mode. Enable **memory** or **sqlite** and the UI will populate."
> **Operator:** "Got it. Also — why are some captured requests showing `service: unknown`?"
> **httpcatch dev:** "Your proxy isn't injecting `X-Httpcatch-Service`, and the `Host` header is missing or rewritten. Check the proxy config example for your mirror — there's a recipe in the repo."

## Flagged ambiguities

(none currently — the project is **httpcatch**; "sink" is specifically a storage destination; "mirror" is the transport mechanism by which the proxy delivers traffic to the **capture port**.)

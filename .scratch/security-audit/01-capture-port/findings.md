# Slice 1: Capture-port ingestion — security audit findings

**Scope:** anonymous HTTP listener on `0.0.0.0:capture_port`, body intake, enqueue, record construction, and the path through the worker pool. The capture port is the only unauthenticated network surface and the most attacker-reachable part of the system.

**Files reviewed end-to-end:**
- `internal/capture/server.go`
- `internal/capture/bodycap.go`
- `internal/capture/queue.go`
- `internal/capture/record.go`
- `internal/capture/correlation.go`
- `internal/capture/service.go`
- `internal/capture/counters.go`
- `internal/pipeline/worker.go`
- `internal/app/app.go` (capture-server construction)
- `internal/config/config.go` (defaults + validation)

**Method:** STRIDE-style walk on each transformation, focused on what an attacker who can reach the capture port can do with no credentials. Cross-slice concerns (XSS on UI render, SQLi in search) are noted but deferred to their respective slices.

**Severity scale:** Critical / High / Medium / Low / Info.

---

## Summary

| ID | Severity | Title |
|---|---|---|
| S-CAP-01 | ~~**High**~~ **FIXED** | Capture `http.Server` has no read/write/idle timeouts → slow-loris, fd/memory exhaustion |
| S-CAP-02 | ~~**High**~~ **FIXED** | `body_cap: 0` silently disables the body size limit; one request can OOM the process |
| S-CAP-03 | ~~**High**~~ **FIXED** | Oversize-body path drains the entire attacker payload before responding |
| S-CAP-04 | Medium | Capture port hard-bound to `0.0.0.0` with no override |
| S-CAP-05 | Medium | In-flight buffered bodies are bounded only by `queue_size × body_cap` (≈1 GiB at defaults) |
| S-CAP-06 | Medium | `source_ip` is the proxy's IP; field name implies client IP |
| S-CAP-07 | ~~Medium~~ | Attacker-controlled `Host` / service header lands in indexed `service` column without sanitisation |
| S-CAP-08 | Low | `X-Request-ID` accepted verbatim as `correlation_id` (no length / charset bound) |
| S-CAP-09 | Low | `service_header` config name is not validated — `Host` would conflate fallback chain |
| S-CAP-10 | Info | `r.URL.Path` and query are stored verbatim — downstream sinks/UI/search must escape |
| S-CAP-11 | Info | `dropped_total` is an attacker-influenceable metric; alerting must account for this |
| S-CAP-12 | Info | UUIDv4 via `google/uuid` is cryptographically random — acceptable for record IDs |

---

## S-CAP-01 — No HTTP timeouts on capture port (High) — FIXED

**Status:** Fixed. A configurable `timeouts` block (`read_header`/`read`/`write`/`idle`,
defaults 10s/60s/30s/120s, env overrides `HTTPCATCH_*_TIMEOUT`, `0` disables) is now
applied to both the capture server (`internal/app/app.go`) and the admin server
(`internal/admin/server.go`). `ReadHeaderTimeout` closes the slow-loris vector. The
admin server was hardened in the same change since it shared the identical gap.

**Location:** `internal/app/app.go:188`
```go
captureServer := &http.Server{Handler: a.Handler}
```

**Issue.** The `http.Server` is constructed with no `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, or `IdleTimeout`, and the default `MaxHeaderBytes` (1 MiB) is the only per-connection cap. An unauthenticated attacker that can reach the capture port can:
1. Open many TCP connections and dribble headers one byte every few seconds (classic slow-loris). Connections survive indefinitely, exhausting file descriptors and tying up goroutines.
2. Open many keep-alive connections and never send another request — no `IdleTimeout` means they live until the kernel reaps them.

This is the highest-impact issue in this slice because the capture surface is intentionally unauthenticated and intentionally exposed to anything the proxy mirrors to it. Once open-sourced, the default config will be deployed by operators who reasonably assume the binary has standard hardening.

**Repro.**
```
go run ./cmd/httpcatch &
for i in $(seq 1 5000); do
  ( exec 3<>/dev/tcp/127.0.0.1/8080
    printf 'GET / HTTP/1.1\r\nHost: x\r\n' >&3
    sleep 600 ) &
done
# observe: lsof -p <pid> | wc -l grows to ~5000, goroutine count rises
```

**Fix sketch.**
```go
captureServer := &http.Server{
    Handler:           a.Handler,
    ReadHeaderTimeout: 10 * time.Second,
    ReadTimeout:       30 * time.Second,
    WriteTimeout:      15 * time.Second,
    IdleTimeout:       60 * time.Second,
    // MaxHeaderBytes defaults to 1 MiB; consider tightening to 64 KiB.
}
```
All four timeouts should be configurable. `ReadHeaderTimeout` is the single most important one — it's the slow-loris defence and has no legitimate reason to be high. Same fix applies to the admin server (slice 4 will revisit).

---

## S-CAP-02 — `body_cap: 0` silently disables the body limit (High) — FIXED

**Status:** Fixed. `UnboundedBodyCapWarning` constant added to `internal/app/app.go`; `EmitStartupWarnings` emits it when `a.Cfg.BodyCap == 0`. The warning text names the concrete risk (unbounded `io.ReadAll`, OOM). Runtime behaviour of `CapBody` is unchanged — `body_cap: 0` still disables the cap; the warning makes that visible at startup. Tests in `internal/app/integration_test.go` assert the warning fires when `body_cap=0` and does not fire at the default (1 MiB).

**Location:** `internal/capture/bodycap.go:6-12`, `internal/config/config.go:403-405`
```go
// bodycap.go
if capBytes <= 0 {
    body, err = io.ReadAll(r)   // unbounded
    ...
}

// config.go Validate()
if c.BodyCap < 0 {
    errs = append(errs, fmt.Errorf("body_cap: must be >= 0, got %d", c.BodyCap))
}
```

**Issue.** The config validator rejects negative values but permits `0`. The code treats `<= 0` as "unbounded `io.ReadAll`". CONTEXT.md documents `0` as "disables", and that is the intended semantic — but the documentation lives outside the binary. An operator setting `body_cap: 0` (or `HTTPCATCH_BODY_CAP=0`) gets a process where one POST of a multi-GiB body allocates that body in memory and enqueues it onto the capture queue.

This is the only feature in the codebase where the dangerous value is also a plausible typo. Compare `unredacted mode` (zero rules), which prints a prominent startup warning and a persistent UI banner — `body_cap: 0` has neither.

**Repro.**
```
HTTPCATCH_BODY_CAP=0 go run ./cmd/httpcatch &
head -c $((2 * 1024 * 1024 * 1024)) /dev/urandom | \
  curl --data-binary @- http://127.0.0.1:8080/
# process RSS climbs past 2 GiB; OOM kill likely.
```

**Fix sketch.** Mirror the unredacted-mode pattern:
1. Emit a startup warning when `body_cap == 0` (`UnboundedBodyWarning` constant in `app/app.go`).
2. Surface a persistent banner in the UI.
3. Optionally: require an explicit `--insecure-unbounded-body` flag (parallel to `admin.insecure_listen`) before `body_cap: 0` is honoured.
4. Document `body_cap` in the README with an explicit "do not set to 0 in production" callout.

---

## S-CAP-03 — Oversize-body drain reads the full attacker payload (High) — FIXED

**Status:** Fixed. `CapBody` no longer calls `io.Copy(io.Discard, r)` after the cap
is exceeded. The function reads at most `capBytes+1` bytes total and returns
immediately. When `BodyTruncated` is true, `BodyOriginalSize` is now a sentinel
equal to `body_cap+1`, meaning "at least body_cap+1 bytes"; the exact wire length
is not known and was not measured. Field-level comments on `BodyOriginalSize` in
all four record types (`CapturedRequest`, `ResponseEvent`, `OutboundRequestHalf`,
`OutboundResponseHalf`) document this contract.

Why no drain budget: the auditor's alternative (8 MiB drain budget) still allows
DoS at 8 MiB × concurrent-conns. Go's `net/http` bounds post-handler reads at
256 KiB (`maxPostHandlerReadBytes`) before closing the connection, so leaving the
body unread is safe — the server does not block.

Tests added in `internal/capture/bodycap_test.go`:
- `TestCapBody_OversizeDoesNotDrain` — trip-wire reader proves no drain occurs
- `TestCapBody_AtCapNoTruncate` — boundary: exactly-at-cap body is not truncated
- `TestCapBody_UnderCap` — sanity: under-cap body returned in full
- `TestCapBody_CapDisabled` — cap=0 and cap=-1 still read in full
- `TestCapBody_OversizeReportsSentinelSize` — renames the previous
  `TestCapBody_StreamsRemainderWithoutBuffering`; asserts `originalSize==cap+1`

Integration tests updated in `internal/app/integration_test.go`:
- `TestIntegration_EndToEnd_BodyCapShape` `over-*` cases: `wantOrig` changed from
  `cap+512` to `cap+1` (the sentinel value).

UI updated in `internal/admin/ui_views.go` and templates: truncation badges now
render `≥ N bytes` (via `BodyOriginalSizeLabel()` / `OutboundRequestBodyOriginalSizeLabel()` /
`OutboundResponseBodyOriginalSizeLabel()` helpers) instead of the raw integer, making
the "lower bound" semantic visible to operators.

**Location:** `internal/capture/bodycap.go:14-27`
```go
body, err = io.ReadAll(io.LimitReader(r, int64(capBytes)+1))
...
if len(body) <= capBytes {
    return body, len(body), false, nil
}
extra, err := io.Copy(io.Discard, r)   // drains the rest
if err != nil {
    return nil, 0, false, err
}
return body[:capBytes], capBytes + 1 + int(extra), true, nil
```

**Issue.** Once the cap is exceeded, the handler reads the remaining body in full to compute `body_original_size`. There is no upper bound on this drain. An attacker can advertise (chunked) or stream a 50 GiB body and force the server to read every byte off the wire before sending `202 Accepted`. Memory stays bounded (`io.Discard`), but:
- TCP bandwidth and CPU are consumed at line rate per connection.
- The connection occupies a server goroutine for the full duration.
- The 202 ack is delayed by the entire body — a behavioural fingerprint that operators relying on "every request acks fast" may not expect.

Combined with the absence of read timeouts (S-CAP-01), an attacker can dribble that 50 GiB at 1 KB/s and tie up a goroutine for weeks.

**Repro.**
```
go run ./cmd/httpcatch &
# Send 10 GiB over loopback; observe TCP RX bytes climb on the listener.
yes A | tr -d '\n' | head -c $((10 * 1024 * 1024 * 1024)) | \
  curl -H 'Transfer-Encoding: chunked' --data-binary @- http://127.0.0.1:8080/
```

**Fix sketch.** Stop reading once the cap is exceeded plus a small "drain budget":
```go
const maxDrain = 8 * 1024 * 1024  // 8 MiB beyond the cap is enough to size-bucket
extra, err := io.Copy(io.Discard, io.LimitReader(r, maxDrain))
truncatedSize := capBytes + 1
if extra >= maxDrain {
    // we don't know the exact size; record "at least N"
    return body[:capBytes], capBytes + 1 + maxDrain, true, nil
}
return body[:capBytes], capBytes + 1 + int(extra), true, nil
```
Or simpler: store `body_original_size = capBytes + 1` with a new `body_original_size_known: false` flag and stop reading immediately. Losing exact size on oversized requests is a fair trade for closing this DoS vector.

---

## S-CAP-04 — Capture port hard-bound to `0.0.0.0` (Medium)

**Location:** `internal/app/app.go:182`
```go
addr := fmt.Sprintf("0.0.0.0:%d", a.Cfg.CapturePort)
```

**Issue.** Unlike the admin port (which has a bind policy + `insecure_listen` guard), the capture port has no configurable bind address. Operators cannot:
- Restrict the capture port to a private interface (e.g. WireGuard mesh, internal NIC).
- Restrict it to loopback in a setup where the proxy runs as a sidecar on the same host.

Open-sourcing the project amplifies this: operators who reasonably expect "bind to loopback by default, opt in to public" will instead find the capture surface exposed on every interface. The capture surface is unauthenticated by design; binding to public networks should be an explicit operator choice.

**Fix sketch.**
1. Add `capture_bind` to `Config` (mirror `admin.bind`), default `0.0.0.0:8080`.
2. Document in `CONTEXT.md` and the README that the capture port is unauthenticated and should be bound only to networks the proxy reaches.
3. Optionally: apply the same bind-policy guard as admin — refuse non-loopback bind unless `insecure_listen: true`. This is more controversial because mirrored traffic is the primary use case; recommend it as a separate ADR.

**Status:** FIXED — 2026-06-06 — `internal/config/config.go` (`CaptureBind string` added to `Config`; `CaptureBind *string` added to `rawConfig`; `applyRaw` copies it; `applyEnv` handles `HTTPCATCH_CAPTURE_BIND`; normalization in `Load` derives `0.0.0.0:<capture_port>` when empty; `Validate` checks the resulting `host:port` format and port range), `internal/app/app.go` (capture listener now uses `a.Cfg.CaptureBind` directly), `internal/config/config_test.go` (six new unit tests), `internal/app/integration_test.go` (one new integration test), `examples/basic.yaml`, `examples/redact.yaml` (commented `capture_bind` knob added); default behavior (0.0.0.0 bind) is unchanged.

---

## S-CAP-05 — Buffered-body memory bound is `queue_size × body_cap` (Medium)

**Location:** `internal/capture/queue.go`, `internal/capture/server.go:41`

**Issue.** The queue caps record count but not bytes. At defaults (`queue_size=1024`, `body_cap=1 MiB`) the worst-case in-flight memory is ≈1 GiB if workers stall (slow SQLite, slow stdout consumer, paused debugger). The drop-on-full policy bounds the *count* but the byte ceiling is multiplicative.

This isn't an attack — it's a capacity hazard that compounds with S-CAP-01/02/03. An attacker can pin workers (slow stdout consumer reading at 1 KB/s) and push queue depth to capacity with full-size bodies.

**Fix sketch.**
- Document the worst-case formula prominently in CONTEXT.md and README sizing guidance: `worst_case_bytes ≈ queue_size × body_cap`.
- Optional: add a `max_in_flight_bytes` setting; drop-on-full also fires when the byte budget is hit. Cheap to add and removes the multiplicative footgun.

**Status:** FIXED — 2026-06-06 — docs-only (per scope decision); deferred work (optional `max_in_flight_bytes` knob) documented in this status block. `CONTEXT.md` updated: **Capture pipeline** entry extended with the `queue_size × body_cap` worst-case formula; **Body cap** entry cross-references it.

---

## S-CAP-06 — `source_ip` is the proxy IP, but the name implies client IP (Medium)

**Location:** `internal/capture/server.go:53-56`
```go
sourceIP, _, splitErr := net.SplitHostPort(r.RemoteAddr)
if splitErr != nil {
    sourceIP = r.RemoteAddr
}
```

**Issue.** `source_ip` is read directly from `r.RemoteAddr`. With the standard topology (proxy mirrors to capture port), this is always the proxy's IP. The field name suggests the originating client's IP — operators inspecting captures for forensic purposes (e.g. "which IP hit this endpoint") will misread it.

Worse: if an operator fronts the capture port with a TCP-level proxy (HAProxy in TCP mode, Cloudflare Spectrum), `source_ip` becomes the L4 proxy's IP — a third hop that means nothing forensic.

PROXY-protocol / `X-Forwarded-For` parsing on the capture port is **out of scope** for this finding (it opens header-trust questions that should be addressed in their own ADR). The fix here is documentation.

**Fix sketch.**
- Document in CONTEXT.md that `source_ip` is the *peer* of the capture port — i.e. usually the proxy — and that client IP, if needed, must be injected by the proxy into a header (operator's choice of name).
- Consider renaming the JSON field to `peer_ip` before v1; renaming after open-sourcing is a breaking change.

**Status:** FIXED — 2026-06-06 — docs-only (per scope decision); deferred work (field rename to `peer_ip`, PROXY-protocol / XFF parsing) documented in this status block. `CONTEXT.md` updated: **Capture port** entry extended with a sentence clarifying that `source_ip` is the TCP peer of the capture port (typically the proxy) and that client-IP forwarding is the proxy's responsibility.

---

## S-CAP-07 — Attacker-controlled headers land in indexed `service` column unsanitised (Medium)

**Location:** `internal/capture/service.go:24-44`

**Issue.** `IdentifyService` returns the configured-header value or the `Host` header verbatim (modulo trim + lowercase for `Host`). An attacker controls both. The returned `service` string is:
- Stored verbatim into SQLite (indexed column) and the memory ring.
- Used as a filter/group key in the inspect API and UI.
- Emitted to stdout sink (JSON).

Concrete consequences:
1. **Cardinality attack:** an attacker sends a million requests with unique random service values, ballooning the index and degrading search performance for legitimate operators. Cheap to mount, hard to clean up without a DB rebuild.
2. **Renderer abuse:** values like `<script>…</script>`, ANSI escapes, or null bytes flow into UI templates (slice 5 will verify escaping) and the stdout JSON stream (where downstream consumers may not be defensive).
3. **Log injection:** service values are emitted into structured logs from the worker pool. CRLF in the value can break log line boundaries depending on the slog handler.

The capture-side fix is light validation; the heavy lifting is at the render sites (slices 5 and 7).

**Fix sketch.**
- Reject (or sanitise to `unknown` + increment a counter) service values containing control characters (`<' '`, `0x7f`).
- Cap length at e.g. 256 bytes; longer values become `unknown`.
- Lowercase / canonicalise `service` (already done for the `Host` path; do it for the header path too for consistency).
- Slice 5 must verify that all UI templates HTML-escape the value. Slice 7 must confirm stdout-sink JSON encoding never produces unescaped control chars.

**Status:** FIXED — 2026-06-06 — added `sanitiseServiceLabel` helper in `internal/capture/service.go` that trims, rejects control chars and values exceeding 256 bytes, and lowercases; applied to both header and Host paths in `IdentifyService`; new test cases added to `TestIdentifyService` in `internal/capture/service_test.go`.

---

## S-CAP-08 — `X-Request-ID` accepted verbatim as `correlation_id` (Low)

**Location:** `internal/capture/correlation.go:35-37`
```go
if rid := strings.TrimSpace(headers.Get(RequestIDHeader)); rid != "" {
    return rid, CorrelationSourceRequestID
}
```

**Issue.** Traceparent values are hex-validated and length-bounded (`traceIDHexLen = 32`). `X-Request-ID` is taken as-is — no length cap, no charset filter. An attacker can:
- Send a 1 MiB `X-Request-ID` and have it stored as the correlation key (compounded by the `headers` map already containing it).
- Pick a correlation ID matching a legitimate downstream request, causing the inspect UI to group attacker traffic with legitimate traffic.
- Embed control chars / HTML in the correlation ID — same downstream-render concern as S-CAP-07.

**Fix sketch.** Mirror the traceparent treatment:
```go
const maxRequestID = 256
if rid := strings.TrimSpace(headers.Get(RequestIDHeader)); rid != "" {
    if len(rid) <= maxRequestID && isPrintableASCII(rid) {
        return rid, CorrelationSourceRequestID
    }
    // malformed → fall through to synthesised UUID
}
```
Where `isPrintableASCII` rejects everything below `0x20` (except, debatably, none) and above `0x7e`. Charset can be more permissive (UTF-8 minus controls) — the key invariant is "no controls, bounded length".

---

## S-CAP-09 — `service_header` config name not validated (Low)

**Location:** `internal/config/config.go:392-419`, `internal/capture/service.go:24-32`

**Issue.** `service_header` defaults to `X-Httpcatch-Service` but accepts any string. Setting it to `Host` collapses the two-tier lookup into one and produces undefined behaviour (the attacker controls the `Host` header). Setting it to a header name with control chars or the empty string is silently coerced. Setting it to `Authorization` (or any auth-bearing header) leaks credentials into the indexed `service` column.

This is a configuration footgun rather than an attack, but it's the kind of footgun audit checklists flag.

**Fix sketch.** Validate at config-load time:
- Must match HTTP token chars (RFC 7230).
- Reject names known to carry credentials (`Authorization`, `Cookie`, `Proxy-Authorization`).
- Reject `Host` explicitly (used as the fallback already).

---

## S-CAP-10 — `r.URL.Path` and query stored verbatim (Info)

**Location:** `internal/capture/server.go:80-81`

`Path` and `Query` are stored as the attacker sent them. This is correct behaviour for a capture tool — the whole point is to see what hit the proxy. The defensive burden is on every render site (UI templates, stdout JSON, search highlighting) to escape properly. Slice 5 (UI) and slice 7 (sinks) will verify those.

No change at this slice. Recording the assumption explicitly so the cross-slice burden is visible.

---

## S-CAP-11 — `dropped_total` is attacker-influenceable (Info)

**Location:** `internal/capture/queue.go:18-25`

An attacker who can reach the capture port can drive `dropped_total` upward arbitrarily by saturating the queue. Operators alerting on this metric must understand it reflects ingestion pressure, not necessarily a system fault. Document this in the metrics README, and prefer rate alerts over absolute thresholds.

No code change needed.

---

## S-CAP-12 — UUID generation is cryptographically random (Info)

**Location:** `internal/capture/server.go:77` (record ID), `correlation.go:38` (synthesised correlation), `cmd/httpcatch/main.go` (admin token path, not in scope here)

`github.com/google/uuid`'s `NewString()` uses `crypto/rand`. No issue. Recording so the audit's positive findings are also explicit.

---

## Cross-slice references

- **Slice 2 (Redaction):** verify ReDoS / catastrophic backtracking on operator-supplied regex; S-CAP-02 + an unredacted-mode warning gap would compound.
- **Slice 3 (Inspect + SearchQL → SQL):** verify that `service`, `path`, `host`, `correlation_id`, `headers` (all attacker-controlled per this slice) cannot reach a string-concat SQL fragment.
- **Slice 5 (UI):** verify HTML escaping of every field listed in S-CAP-07, S-CAP-08, S-CAP-10 in `internal/admin/ui_views.go`.
- **Slice 7 (Sinks):** confirm stdout-sink JSON encoding handles control chars; confirm SQLite uses parameter binding for every insert path.

## What I did *not* do in this slice

- No code changes. Audit pass only.
- Did not run `govulncheck` / `staticcheck` / `gosec` — those are the parallel tooling pass and produce findings independent of vertical slices.
- Did not load-test S-CAP-01/03 against a running binary; reasoning is from the source, and the fix sketches are conservative enough that empirical confirmation can wait until the fix PR.
- Did not look at TLS termination — the capture port is plaintext HTTP by design, and the threat model assumes the proxy ↔ capture link is on a trusted network. Worth documenting in the README.

## Recommended next actions

1. **Land S-CAP-01 immediately** (timeouts) — one-line risk, one-line fix, no API impact.
2. **Land S-CAP-02 and S-CAP-03 together** in a single hardening PR.
3. **Decide S-CAP-04 via ADR** — bind-policy semantics for the capture port deserve a written decision before open-sourcing.
4. **Defer S-CAP-05–09** to a follow-up hardening pass; none are emergencies, but all should be closed before a v1.0 tag.
5. **Proceed to slice 2 (redaction)** next, since the same input that flows through this slice is consumed by redaction rules and we have a fresh mental model of the threat surface.

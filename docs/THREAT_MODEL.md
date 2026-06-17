# httpcatch threat model

This document describes the security posture of httpcatch — the assets it protects, the boundaries it draws, the threats it expects, and the mitigations it relies on. Terms in **bold** are defined in `CONTEXT.md`.

httpcatch is a diagnostic-grade traffic recorder. It is not a regulatory-grade capture system, not an IDS, and not a privilege boundary against the operator who runs it. The threat model is shaped by those choices.

## Scope and non-goals

In scope:

- The two HTTP listeners — the unauthenticated **capture port** and the bearer-authed **admin port**.
- The in-process pipeline that moves a record from **capture port** (or **events API**) through redaction to **sinks**.
- The persistent **sqlite** sink and the bounded **memory** ring; the **stdout** sink as a stream egress.
- The embedded UI and **inspect API** that read those sinks.
- The CLI subcommands that ship with the binary (`serve`, `redact --test`).
- The configuration surface — YAML file, environment variables, validation, and effective-config reporting.
- The build supply chain — `go.mod`, container image, Helm chart, and example configs distributed in this repo.

Out of scope:

- **Local-operator threats.** The binary trusts whoever runs it. An operator with read access to the host can read the SQLite store, the config file, and the admin token. We harden file permissions and bind defaults but do not defend against a hostile local user.
- **Transport encryption between the proxy and httpcatch.** The **capture port** is plaintext HTTP. Mirroring traffic over an untrusted network is the deployer's problem; the expected topology is sidecar / private network. The **admin port** likewise expects TLS termination upstream when exposed off-loopback.
- **Multi-tenancy.** A single httpcatch instance has a single **admin token** and a single ruleset; everything authenticated sees everything captured. Per-service isolation is out of scope for the first release (see ADR-0003).
- **Guaranteed capture.** The **capture pipeline** drops silently when the **capture queue** is full (ADR-0002). That choice is intentional and incompatible with regulatory capture requirements.
- **Side-channel resistance beyond constant-time token comparison.** Redaction timing, queue-depth-via-latency, and similar oracles are not defended against.

## Assets

The model protects four assets, listed by sensitivity:

1. **Captured payloads.** Bodies, headers, query strings, and cookies of mirrored requests and posted events. Treated as potentially containing secrets (bearer tokens, session cookies, PII). The whole **redaction** subsystem exists to make this asset safe to persist.
2. **The admin token.** A single static bearer credential that gates the **inspect API**, the **events API** (when bearer-only mode is used), and the UI. Compromise grants full read of captured payloads and the ability to forge events.
3. **Persisted sink files.** The on-disk SQLite database and its WAL/SHM siblings. They hold the redacted payload but also indices and join keys that an attacker with read access can use for traffic analysis.
4. **Operator-configured rules.** Redaction rules, retention policy, body cap, and bind addresses. Misconfiguration is a likelier route to a leak than code-level vulnerability; the configuration loader rejects dangerous shapes and the binary warns loudly when defenses are disabled.

## Trust boundaries

httpcatch draws four boundaries.

**Network → capture port.** Untrusted ingress. Anything that can reach the **capture port** can submit captured traffic. The default bind is `0.0.0.0:8080`; deployers narrow it via `capture_bind`. The port enforces server-level read/write/header/idle timeouts and a per-request body cap before the payload is buffered or queued.

**Network → admin port.** Authenticated ingress. The default bind is loopback. Binding off-loopback requires either a configured **admin token** or an explicit insecure-mode opt-in; the server refuses to start otherwise. Bearer comparison is constant-time and length-agnostic. Session cookies are issued by the form-login flow and bound to per-IP and global token-bucket rate limits. Cross-site POSTs to the auth endpoints are blocked by an Origin / `Sec-Fetch-Site` check before any token comparison runs.

**Capture port → pipeline.** Same process, but the boundary is interesting: the pipeline must not let an attacker-shaped payload corrupt internal state. Worker goroutines recover from redaction panics so a malformed rule on one record cannot shrink the worker pool. The bounded queue isolates per-request memory cost from total in-flight memory cost.

**Pipeline → sinks.** Same process. Records are redacted exactly once before sink fan-out, so every enabled sink sees the same payload. Sink writes are best-effort with per-sink error counters; a single failing sink (full disk, broken stdout pipe) does not stop the others or stall the pipeline.

## Actors

Five concrete actors inform the model.

**The proxy.** Trusted to deliver mirrored traffic faithfully. httpcatch captures the `source_ip` of the TCP peer — the proxy itself, not the originating client. Client-IP forwarding via `X-Forwarded-For` or PROXY protocol is the proxy's responsibility; httpcatch does not parse or trust those headers.

**Application code emitting events.** Trusted to know its own correlation ID and content type but treated as potentially buggy. The **events API** validates payload size, batch count, per-event body size, and correlation-ID shape, and falls back to header-derived correlation when the explicit value is malformed.

**A network attacker who reaches the capture port.** Can submit forged HTTP, including oversize bodies, slow-read attacks, and headers designed to poison the indexed `service` column. Defenses: HTTP server timeouts, body cap with truncation after `body_cap + 1` bytes (no full-drain), bounded queue with silent drop, and sanitisation of the `service` label (control-char rejection, length cap, lowercase normalisation).

**A network attacker who reaches the admin port.** Without the token, blocked by the bearer/middleware. With a stolen token, fully authorised — there is no second factor. Defenses against credential brute force: per-IP and global token buckets, constant-time comparison, and a 32-char minimum token.

**A browser-resident attacker** (e.g. a malicious page open in the operator's browser while a session is active). Cannot read JSON responses (no cross-origin scripting against a same-origin admin), cannot frame the UI (`X-Frame-Options: DENY` + CSP `frame-ancestors 'none'`), cannot inject inline scripts (CSP `script-src 'self'` with all templates externalised), and cannot CSRF the auth endpoints (Origin / `Sec-Fetch-Site` check).

## Threats and mitigations by component

### Capture port

The **capture port** is the only unauthenticated surface and the largest threat-bearing boundary. It exposes httpcatch to anything routable to the bound interface.

The Go `http.Server` carries explicit `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, and `IdleTimeout` — the connection-holding attacks (Slowloris and family) cannot stall a worker. Bodies are bounded by the configured `body_cap`; bodies that exceed the cap are stored truncated, with `body_truncated: true`, after the server reads at most `body_cap + 1` bytes. The capture port still acknowledges the request with `202`; it does not return `413` for over-cap bodies. The binary emits a startup warning when `body_cap: 0` is configured to make the operator's decision visible.

The bound interface is operator-configurable through `capture_bind` (env `HTTPCATCH_CAPTURE_BIND`); the default remains `0.0.0.0:<capture_port>` to preserve drop-in behaviour but the knob exists for deployers who route the proxy over loopback or a private interface. Worst-case in-flight memory is `queue_size × body_cap` — when workers stall and the queue fills with full-sized bodies. The figure is documented in `CONTEXT.md` so operators size their containers against it.

The `source_ip` column is the TCP peer of the capture port, not the originating client. The field name is preserved for backward compatibility but `CONTEXT.md` calls out the distinction; deployments that need real client IPs must run a PROXY-protocol-aware proxy and inject an explicit correlation header.

Caveats: `X-Request-ID` is accepted verbatim as the **correlation key** with no shape validation; the `service_header` config name is not validated as a syntactically legal HTTP header name.

### Redaction

Redaction is a defense in depth. It runs once on every record before sink fan-out, so an unredacted body never reaches storage. The implementation is content-type-agnostic by design: regex rules apply to every non-empty body regardless of declared `Content-Type` (closing the multipart/octet-stream bypass) and JSON-path rules apply to any body that parses as valid JSON regardless of declared type. The error counter ticks only when the operator declared JSON but the body fails to parse — a misconfiguration signal — so a non-JSON-declared body that happens to be invalid does not generate noise.

Cookie and query rules apply uniformly to captured requests and to event variants (response, outbound). Query-parameter matching is case-insensitive; headers were already case-insensitive. The redaction worker recovers from panics; a malformed rule on one record cannot drain the pool, and a `httpcatch_worker_panics_total` counter surfaces the event.

The `httpcatch redact --test` dry-run masks before-side header/query/cookie values as `<masked: N chars>` by default (parallel to the body's `%d bytes` rendering) so running the test against a real capture does not echo unredacted secrets to terminal scrollback or CI logs. Operators opt back into cleartext with the explicit `--show-values` flag; the after-side stays cleartext so buggy redaction rules remain visible. The `--test` path argument is unconfined by design — the subcommand is intended for local-operator use.

Caveat: JSON redaction complexity is bounded only by `body_cap`; pathological JSON shapes under the cap may consume disproportionate CPU.

### Capture pipeline

The pipeline is the seam between unauthenticated ingress and trusted persistence. Its main job is to keep one ill-behaved record from affecting another.

The **capture queue** is bounded; on overflow, the request is dropped silently and `dropped_total` is incremented (the silent-drop choice is ADR-0002, made explicitly for the mirror's fire-and-forget semantics). Worker goroutines wrap the redact+sink work in a `recover()` so a panic counts to `httpcatch_worker_panics_total` and the worker continues — the pool cannot shrink to zero. Sinks fan out serially per record; per-sink write failures are counted in `httpcatch_sink_write_errors_total{sink="..."}` without dropping the record from the other sinks' perspective.

Caveat: the serial fan-out means a slow sink stalls all sinks for that record; back-pressure into the queue translates to drop-on-full.

### Sinks

Each sink is responsible for the safety of its own destination.

**Memory** is a bounded count-capped ring. Memory worst case is `memory_capacity × body_cap`, documented alongside the queue figure.

**SQLite** files are created with mode `0o600` and `O_NOFOLLOW` on Unix (`syscall.O_NOFOLLOW`; the constant is `0` on Windows where the call is a no-op) so an attacker-planted symlink at the configured `sqlite_path` cannot redirect writes. The WAL and SHM siblings are chmodded to `0o600` after the SQLite driver creates them. The parent directory's mode is checked once at sink open; a world-writable parent emits a `slog.Warn` so operators see the misconfiguration even if the data file itself is protected. A background sweeper trims by retention policy (time- or count-based, exclusive); the policy is global, per-service overrides are not part of the first release.

**Stdout** uses `encoding/json` which escapes control characters into `\uXXXX` form — the on-the-wire stream is safe for terminal consumers. The stdout writer holds a process-global mutex to keep newline-delimited frames intact; that mutex is the back-pressure source if a downstream consumer stalls.

Caveat: `jsonHeaderPath` is the one query-builder site that uses `Sprintf` rather than `?` binding; it is safe because the interpolated value is a bound output rather than a user-supplied input, but it is the only place the property holds by construction rather than by mechanism.

### Inspect API and SearchQL

The read-side is the SQL surface; everything attacker-shaped that survives redaction can be queried through it by a holder of the admin token.

All values are bound with `?` placeholders; no value is concatenated into SQL. LIKE needles escape `%`, `_`, and `\` before binding and every LIKE clause carries `ESCAPE '\\'`, so an operator searching for `100%` matches literal `100%` rather than "100" followed by any suffix. Time-range and exact-service narrowing are enforced for unindexed scans: a query whose shape forces a leading-wildcard match on `host`, `path`, or `service` is rejected `400` unless paired with a `since`/`until` range or an exact `service:` predicate. The response carries `X-Httpcatch-Scan: leading-wildcard-indexed` when the scan is unavoidable; the UI surfaces a warning chip. A configurable `inspect.query_timeout` (default 5 s, env `HTTPCATCH_INSPECT_QUERY_TIMEOUT`) bounds runtime.

Caveats: `since <= until` is not validated; raw driver errors are surfaced verbatim in 500 responses; cursor `id` segments are unvalidated free text.

### UI

The UI runs on the admin port behind the same authentication as the API. Server-side rendering uses `html/template` with auto-escaping; client-side rendering of attacker-controlled fields uses `textContent` rather than `innerHTML`.

Every HTML response carries `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`, plus `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and `Referrer-Policy: no-referrer`. The theme-bootstrap script and the login-page styles are loaded from `/static/` so the policy holds without `'unsafe-inline'`. JSON responses carry `nosniff` and an explicit `Content-Type: application/json; charset=utf-8`.

The auth POSTs (`/auth/login`, `/auth/logout`) are wrapped in an Origin / `Sec-Fetch-Site` check that blocks cross-site form submissions: `Sec-Fetch-Site` of `same-origin` or `none` passes; `same-site` also requires a matching `Origin` host; anything else is rejected `403` and counted in `httpcatch_auth_failures_total{reason="csrf_blocked"}`. The check is the chosen approach instead of a CSRF token in the form (one fewer template invariant to maintain).

Caveat: body, header, and path rendering in `<pre>` blocks does not strip control characters or ANSI escapes — they pass through to the terminal-emulator-flavoured display in a way that is visually misleading but not exploitable through CSP.

### Admin authentication

The bearer comparison in `checkBearer` is `subtle.ConstantTimeCompare` with no length short-circuit, so a length mismatch reveals no information beyond the constant-time path. Form-login session cookies are issued with `HttpOnly`, `SameSite=Lax`, and `Secure` when the configured `session_secure: true`; the binary emits a startup warning when the bind is non-loopback and `session_secure: false` because the cookie can be sniffed on the wire in that combination. `session_ttl` is validated `> 0`.

A token-bucket rate limiter gates every auth attempt — per-IP capacity 5 refilled across 30 seconds, global capacity 20 refilled across 1 second. Bucket sweeps evict idle IPs after 10 minutes. Failures are counted by reason in `httpcatch_auth_failures_total{reason="invalid_token"|"rate_limited"|"csrf_blocked"}`.

Caveats: the session sweeper interval is decoupled from `session_ttl` and there is no live-session cap; `/metrics` is intentionally unauthenticated and exposes the build version, operational counters, and — via the `prometheus/client_golang` Go and process collectors — Go runtime and process metrics (`go_*`, `process_*`) such as goroutine count, heap size, GC pauses, open file descriptors, and resident memory; session expiry uses wall-clock `time.Now`, so a clock step-back extends lifetime.

### Events API

The events endpoint is bearer-only — there is no form-login path. The payload is bounded by `max_events_payload` (default 1 MiB, zero disables with a startup warning) before unmarshal; arrays are bounded by `max_events_per_batch` (default 1000, zero disables with a startup warning) checked after JSON unmarshal but before per-item materialisation, so an oversized batch is rejected `413` without allocating the per-record slice. Each event's body is bounded by `body_cap` and rejected if oversize. Type confusion is prevented structurally — the wire format requires a `type` discriminator and an unknown value is rejected with `unknown_type`; missing types are rejected with `missing_type`. Caller-supplied `correlation_id` is bounded at 256 bytes and required to be printable ASCII; on violation, the handler falls through to header-derived or synthesized correlation. Queue-full handling differentiates 202 (none dropped), 207 (partial), and 503 (all dropped) with a `events_dropped_queue_full_total` counter.

Caveats: the JSON decoder does not use `DisallowUnknownFields` and duplicate keys are last-wins; raw `encoding/json` parse-error strings are echoed to the caller; caller `timestamp` is accepted without sanity bounds.

### Configuration and supply chain

Configuration precedence is: defaults → YAML file → environment variables → validation. The validator rejects negative caps, zero `session_ttl`, malformed bind strings, sub-32-char admin tokens, and host/port shapes that fail `net.SplitHostPort`. The effective-config UI page masks the admin token (`***`) before render and the field is never logged. Example YAMLs do not ship a default `admin.token` value; the comment points at `HTTPCATCH_ADMIN_TOKEN`.

Caveats: the env layer can silently weaken a hardened YAML posture; the Helm schema does not constrain `0`-disables fields; sample scripts embed scanner-tripping fake credentials; Helm security contexts are unconstrained objects.

## Residual risk

Beyond the per-component caveats listed above, three classes of risk are intentionally not addressed:

1. **The operator running the binary is fully trusted.** Reading the SQLite store, the admin token from env, or the live process memory is not a defended-against threat. Run httpcatch as a dedicated unprivileged user if that matters for the deployment.
2. **The capture-port plaintext channel.** Mirroring traffic over an untrusted network leaks it whether or not httpcatch redacts on arrival. Place the proxy and httpcatch on the same private network, the same host, or terminate TLS on a sidecar before reaching the capture port.
3. **Silent drop on capture-queue overflow.** When the queue is full, the request is acknowledged with `202` and discarded. Watch `dropped_total` and the UI banner; assume per-request capture is best-effort, not guaranteed.

## Operator hardening checklist

The deployed posture depends on the deployer doing several things the binary cannot do alone:

- Set `HTTPCATCH_ADMIN_TOKEN` to a 32+-character random secret. Do not ship a default token in any environment.
- Bind the **admin port** to loopback or a private interface (`HTTPCATCH_ADMIN_BIND`); set `session_secure: true` when binding off-loopback.
- Bind the **capture port** to the interface the proxy uses (`HTTPCATCH_CAPTURE_BIND`); do not leave `0.0.0.0` on a public network.
- Configure redaction rules before pointing real traffic at the instance. Use `httpcatch redact --test` (default-masked) against a representative sample to verify coverage; pass `--show-values` only when investigating a specific surviving cleartext.
- Configure retention (`sinks.retention.max_age` or `sinks.retention.max_count`) so the SQLite store does not grow unbounded.
- Run on a dedicated unprivileged user; ensure the SQLite directory is not world-writable (the binary will log a warning if it is).
- Keep `body_cap` and `max_events_payload` non-zero in production; the binary warns at startup when either is disabled.
- Monitor `httpcatch_dropped_total`, `httpcatch_events_dropped_queue_full_total`, `httpcatch_auth_failures_total{reason=...}`, `httpcatch_sink_write_errors_total{sink=...}`, and `httpcatch_redaction_errors_total`. They are the canonical signals that a defense engaged.

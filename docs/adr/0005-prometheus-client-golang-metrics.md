# Prometheus metrics via client_golang

The `/metrics` endpoint is built on `github.com/prometheus/client_golang`: a custom `prometheus.Collector` bridges the atomic counters that the capture handler, pipeline, and admin server already maintain, a native `prometheus.Histogram` records pipeline processing duration, and the standard Go and process collectors are registered alongside them.

We chose client_golang over the hand-written text exposition it replaces because the hand-rolled `fmt.Fprintf` exposition had to re-implement escaping, `# HELP`/`# TYPE` framing, and label formatting by hand — work that is easy to get subtly wrong and that the library does correctly and keeps current with the exposition format. The bridge collector keeps the existing counter ownership intact (the counters remain plain atomics owned by their subsystems) while delegating serialization to the library. Registering `collectors.NewGoCollector()` and `collectors.NewProcessCollector(...)` adds `go_*` and `process_*` runtime and process metrics — goroutine count, heap size, GC pauses, open file descriptors, resident memory — at no extra wiring cost.

The migration normalizes two metric names to follow Prometheus conventions, which `promlinter` enforces now that client_golang is a direct dependency:

- `httpcatch_orphans_total` becomes `httpcatch_orphans`. Orphan count is a gauge sampled at scrape time (it rises and falls as events reconcile), and the `_total` suffix is reserved for monotonic counters. The old name violated that convention.
- `httpcatch_captured_total` is added, owned by the capture `Counters` and incremented only on a successful capture-handler enqueue. It is deliberately not incremented inside `Queue.Enqueue`, because the admin Events API shares the same queue and its submissions must not be counted as captured requests.

`httpcatch_worker_panics_total` is also exposed, sourced from the worker pool's recovered-panic count.

## Consequences

- **Breaking for `/metrics` consumers.** Dashboards and alerts that scrape `httpcatch_orphans_total{type=...}` must move to `httpcatch_orphans{type=...}`; the old series is no longer emitted. This ships in the 0.2.0 metrics migration and is called out as a breaking change in the release notes.
- The content type now carries an escaping parameter negotiated by the library (for example `text/plain; version=0.0.4; charset=utf-8; escaping=underscores`). Consumers must match on the `text/plain; version=0.0.4` prefix rather than the exact string.
- Label values are serialized in the library's canonical (alphabetical) order, so multi-label series such as `httpcatch_build_info` render labels sorted rather than in declaration order.
- The endpoint exposes Go runtime and process internals (`go_*`, `process_*`) in addition to httpcatch's own counters. `/metrics` remains intentionally unauthenticated; see the threat model.

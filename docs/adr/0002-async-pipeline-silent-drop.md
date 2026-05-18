# Async capture pipeline with silent drop on overflow

Every request to the **capture port** is acknowledged with `202 Accepted` immediately after being enqueued onto a bounded in-memory **capture queue**. Background workers drain the queue, apply redaction, and write to the enabled **sinks**. When the queue is full at enqueue time, the request is dropped silently; a `dropped_total` counter is incremented and surfaced through the metrics endpoint and a UI banner.

We chose async over a synchronous "receive → redact → store → respond" pipeline because the proxy's mirror connection waits for httpcatch to respond before releasing it, even though the proxy doesn't act on the response. Coupling that connection's lifetime to storage latency turned httpcatch into a backpressure source on the proxy's connection pool — exactly the kind of risk we promised mirroring would avoid. Async caps per-request latency at "accept and ack" (microseconds) and decouples httpcatch from storage stalls.

We chose silent drop over `503 Service Unavailable` because the mirror is fire-and-forget at the original-request level — it does not retry, does not propagate the failure to the client, and is not actionable on a per-request status. A 503 communicates nothing useful and pretends the operator can do something about it in the moment; a metric + UI banner communicates the same fact in a place where operators actually look (and where it aggregates across drops).

## Consequences

- Drops are invisible at the wire level. The only signal is `dropped_total` (and the UI banner). Documentation and the UI must make this loud enough that operators don't miss it during incidents.
- Queue size is a tunable. The default needs to balance memory cost against burst tolerance; the right value depends on traffic shape.
- `202` is the canonical response code; treating any other code as failure on the proxy side is a misconfiguration.
- "Was this request captured?" is not an answerable question per-request — only "what percentage of requests in this window were dropped?" This is acceptable for a diagnostic tool; it would not be for an audit-grade capture.

# Multi-service single instance with service identity derived from headers

A single httpcatch instance captures traffic from many services. Every captured record and every event carries a `service` label. For app-pushed events, `service` is provided in the event body. For mirrored traffic, `service` is derived in order: a configurable header (default `X-Httpcatch-Service`), else the `Host` header (lowercased, port stripped), else the literal `"unknown"`. There is one **admin token** per instance — shared across services — not a token per service.

We chose this over one-instance-per-service because the operational story for a single-binary-per-service model gets ugly quickly: N services in a cluster means N httpcatch processes, N storage volumes, N config files, N UI URLs to bookmark. With UI + search + metrics in the first release, the operator wants one UI to look at, with a service filter, not N tabs. The trade-off accepted is that the SQLite store now holds the union of all services' traffic — that pressure is managed by **retention** and the **body cap**.

We chose `X-Httpcatch-Service` → `Host` → `"unknown"` rather than per-port mapping (one capture port per service) because header-based identification keeps httpcatch's configuration trivial (one capture port, one admin port — always) and pushes the service-labeling work to the proxy, which already knows which route maps to which service. Per-port would have grown the firewall/network-policy surface linearly with service count.

Records with `service: "unknown"` are still stored and shown in the UI under that bucket, and `captured_without_service_total` makes the misconfiguration visible.

## Consequences

- The `service` label is high-cardinality but not unbounded. It is one of the **indexed dimensions**, and the UI's primary navigation pivots around it.
- A single admin token grants access to every service's traffic in the instance. Operators who need per-service isolation must run separate httpcatch instances — this is documented, not enforced.
- The proxy is part of the contract for correct service labeling. Recipes shipped in the repo include the header injection for each supported proxy.
- The `"unknown"` bucket is a deliberate forgiveness mechanism, not a misconfiguration. Removing it would force operators to fully configure their proxy before seeing any data, which works against the "drop httpcatch on a host and point traffic at it" first-run experience.

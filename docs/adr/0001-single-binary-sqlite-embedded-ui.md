# Single-binary architecture with embedded UI and SQLite-only storage

httpcatch ships as one static Go binary that contains the capture/ingest server, the inspect API, the embedded web UI, the metrics endpoint, and a SQLite-backed store. There are no external service dependencies — no Postgres, no separate UI service, no message broker, no search cluster.

This was a deliberate choice when defining what "lightweight" means for this project. We weighed three readings: lightweight-to-operate (one binary, deploy anywhere), lightweight-to-embed/scale (small per-instance footprint but expects external Postgres and a separate UI service), and lightweight-in-feature-scope (small in what it does). With UI, search, and metrics committed to the first release, the feature-scope reading was no longer defensible, and the embed/scale reading would have moved httpcatch into the same operational class as full observability tools — which contradicts the goal.

## Consequences

- The SQLite ceiling is real. Operators will hit it (single-digit-to-low-hundreds of millions of rows, depending on workload) and there is no built-in escape hatch. Retention policies and the **service** label exist to manage this.
- Embedding the UI in the binary means UI changes ship as Go-binary releases. There is no decoupled web frontend deploy.
- "Scale out to many instances" is the answer to load — not "scale up one big instance." This aligns with the multi-service-per-instance model in ADR-0003 only up to the SQLite ceiling per instance; very large operators will run multiple httpcatch instances.
- Backing technology swaps (Postgres, Elastic, ClickHouse) are explicitly out of scope. Anyone proposing one is proposing a different product.

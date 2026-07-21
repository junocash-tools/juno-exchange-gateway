---
title: Best practices
---

- Keep the gateway's public surface to one HTTPS or mTLS endpoint.
- Keep seeds and spending keys offline; the online planner is intentionally watch-only.
- Use one registered UFVK and durable address mapping per wallet policy.
- Default to 100 confirmations and process reorg lifecycle events.
- Poll with opaque cursors, deduplicate events, and checkpoint atomically with ledger writes.
- Use a unique, stable idempotency key for each withdrawal attempt.
- Keep scanner witness mode on `auto`; monitor lag and shard-cache progress.
- Use Postgres for production and RocksDB for a single-host deployment.
- Pin component images by digest and test every release on regtest.
- Back up gateway state and customer address mappings; rebuild scanner data from the UFVK and birthday when needed.
- Test deposit, reorg, withdrawal, expiry, recovery, and rollback runbooks regularly.

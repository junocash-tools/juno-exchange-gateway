---
title: Upgrade and rollback
---

1. Record current image digests and `/v1/version`.
2. Back up gateway and scanner state.
3. Read migration and API notes.
4. Test the exact images on regtest.
5. Pull digest-pinned images and recreate services.
6. Wait for readiness, then test tip, address balance, deposits, lookup, and broadcast.

```bash
docker compose pull
docker compose up -d
docker compose ps
```

Do not mix networks or reuse data volumes during an upgrade test.

For rollback, restore the previous image set and its compatible state snapshot together. A newer database migration may not be readable by an older binary. Recheck scanner lag and reconcile cursors before reopening traffic.

---
title: Upgrade and rollback
---

1. Record current image digests and `/v1/version`.
2. Stop new attempts and back up gateway/coordinator state, signer journal, installation manifest, exchange withdrawal ledger, and scanner state.
3. Read migration and API notes.
4. Test the exact images on regtest.
5. Pull digest-pinned images and recreate services.
6. Wait for both listeners' readiness, then test tip, address balance, deposits, create/sign, lookup, and broadcast.

```bash
docker compose pull
docker compose up -d
docker compose ps
```

For automated withdrawals, use `-f compose.yaml -f compose.automation.yaml` for every command so the signer, private listener, and persistent journal stay in the deployment set.

Do not mix networks or reuse data volumes during an upgrade test.

For rollback, restore the previous image set and its compatible gateway/coordinator state snapshot together. Never roll the immutable signer journal backward or discard entries newer than the database snapshot. A newer database migration may not be readable by an older binary. Recheck scanner lag, recover every outstanding attempt, and reconcile cursors before reopening traffic.

Do not roll a reconstructed, still-sealed database back to a release that predates the coordinator recovery seal: the older binary will not enforce it. Keep the coordinator disabled or complete reconciliation and the audited unseal on the current release first.

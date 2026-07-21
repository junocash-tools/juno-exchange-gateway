---
title: Backups and recovery
---

## Back up

| Data | Requirement |
| --- | --- |
| Offline seed or signer material | Required to spend; encrypted and offline |
| Installation-state directory | Required to preserve installation identity and address high-water marks |
| `wallets.json` and birthday heights | Required to derive addresses and rescan |
| Exchange address-to-account mapping | Required for customer attribution |
| Gateway database | Preferred for labels, cursor key, and idempotency receipts |
| Scanner database | Recommended for faster recovery; otherwise rebuild from chain |
| Node data | Optional; otherwise resync the node |

Stop the gateway for a simple consistent SQLite snapshot, including WAL state. Use native consistent Postgres backups. Keep backup directories at mode `0700` and files at `0600`, encrypt copies, and test restores. The gateway process uses umask `0077`; backup tooling must preserve or reapply these permissions.

The installation manifest contains UFVK fingerprints, not raw UFVKs. It changes before each address index is issued. Keep it on write-through durable storage outside Compose volumes, or reconcile every allocation against an independent append-only exchange address record.

The gateway detects a missing manifest, a mismatched installation identity, and a database that is behind the current manifest. It cannot detect a jointly restored, internally consistent old manifest and old database. That pair could reuse indices allocated after the snapshot, so never serve when both copies may be stale.

## Scanner-only rebuild

Scanner data is reproducible from the same UFVK, birthday, network, and canonical chain:

1. Stop financial traffic and the gateway.
2. Start a fully synced indexed node and an empty scanner on the same network.
3. Start the gateway with its original installation manifest and gateway database.
4. Wait for backfill and readiness.
5. Reconcile deposits. Scanner startup rotates the event epoch, so restart polling without the old cursor and replay idempotently when the API returns `cursor_reset_required`.

Run only one active gateway allocator for an installation. A cold or warm standby must restore the database, manifest, and exchange-held address high-water marks consistently, then reconcile allocations and deposits before receiving traffic.

## Gateway database recovery

Normal serve never rebuilds an empty or incomplete gateway database. Prefer a verified database backup. If none is usable, keep the original installation manifest and the same `wallets.json`.

First calculate the deterministic registry checksum. The default target is each manifest high-water. Supply a larger known next index only when an independently retained exchange mapping proves it:

```bash
docker compose run --no-deps --rm gateway recovery-checksum \
  --next-index hot=125000
```

Compare `mapping_sha256`, `installation_id`, network, wallet IDs, and next indices with the recovery record. Then run recovery with the exact checksum:

```bash
docker compose run --no-deps --rm gateway recover \
  --acknowledge I_UNDERSTAND_RECOVERY_REBUILDS_JUNO_ADDRESS_STATE \
  --mapping-sha256 '<64-lowercase-hex>' \
  --next-index hot=125000
```

Omit `--next-index` to use manifest values. Overrides can only increase a high-water mark. Recovery derives and verifies every address, rebuilds the registry with empty labels, and binds the database to the original installation ID. Restore customer labels and ownership from the exchange mapping.

If a restore may contain a stale manifest and database:

1. Keep allocation and financial APIs offline.
2. Restore the newest durable manifest, or find the greatest index ever issued for each wallet in the append-only exchange allocation record.
3. Discard the stale gateway database and run the audited checksum and recovery flow with a conservative `--next-index` above every index that may have been issued.
4. Reconcile the rebuilt registry and customer ownership before serving.

If neither source proves a safe high-water mark, remain offline. Do not guess and do not run `init`.

After scanner loss too, start the empty scanner and recovered gateway, wait for full rescan and readiness, then reconcile balances and deposits before reopening traffic.

Never use `init` to replace a lost database or manifest. If the original manifest and its backups are lost, keep address allocation and financial APIs offline; the installation ID and safe high-water cannot be inferred from an empty database.

## Signer recovery

Recover signing material only inside the isolated signing environment. Prove it with a non-production plan or regtest before restoring withdrawals. Never copy recovered keys into the online appliance.

---
title: Backups and recovery
---

## Back up

| Data | Requirement |
| --- | --- |
| Offline seed or signer material | Required to spend; encrypted and offline |
| Installation-state directory | Required to preserve installation identity and address high-water marks |
| `wallets.json` and birthday heights | Required to derive addresses and rescan |
| Credential-name registry and bearer-token recovery | Required to recreate authentication; preserve each broadcaster `name` exactly, while its token and hash may rotate |
| Exchange address-to-account mapping | Required for customer attribution |
| Exchange withdrawal ledger | Required for outstanding attempts: approvals, plan digests, selected note-ID reservations, txids, expiries, broadcast principal names, and idempotency keys |
| Gateway database | Preferred for labels, cursor key, and idempotency receipts |
| Scanner database | Recommended for faster recovery; otherwise rebuild from chain |
| Node data | Optional; otherwise resync the node |

Stop the gateway for a simple consistent SQLite snapshot, including WAL state. Use native consistent Postgres backups. Keep backup directories at mode `0700` and files at `0600`, encrypt copies, and test restores. The gateway process uses umask `0077`; backup tooling must preserve or reapply these permissions.

Keep bearer secrets in the exchange secret manager and back up `auth.json` only as encrypted configuration. During recovery, recreate every broadcaster credential with the exact prior `name`; its token may rotate under that name. A different name creates a different durable idempotency namespace.

The installation manifest contains UFVK fingerprints, not raw UFVKs. It changes before each address index is issued. Keep it on write-through durable storage outside Compose volumes, or reconcile every allocation against an independent append-only exchange address record.

The gateway detects a missing manifest, a mismatched installation identity, and a database that is behind the current manifest. It cannot detect a jointly restored, internally consistent old manifest and old database. That pair could reuse indices allocated after the snapshot, so never serve when both copies may be stale.

## Scanner-only rebuild

Confirmed scanner data is reproducible from the same UFVK, birthday, network, and canonical chain. Mempool-only observations and old unmined expiry history may not be reconstructible after data loss.

Prefer a scanner rebuild when importing event data written by a scanner version that omitted `diversifier_index` for address index zero. The gateway has a narrow compatibility check for an exact registered index-zero address, but it will not guess any other missing address identity.

1. Stop financial traffic and the gateway.
2. Start a fully synced indexed node and an empty scanner on the same network.
3. Start the gateway with its original installation manifest and gateway database.
4. Wait for backfill and readiness.
5. Reconcile deposits. Scanner startup rotates the event epoch, so restart polling without the old cursor and replay idempotently when the API returns `409 cursor_reset_required`.

Preserve the gateway database so its cursor signing key survives recovery. If that key is lost, old cursors fail authentication with `400 invalid_request`; this is intentionally indistinguishable from tampering. During an audited recovery, stop the poller, discard its old cursor explicitly, restart without one, and replay by stable `deposit_id`. Never turn an unexpected `400` into automatic replay.

Run only one active gateway allocator for an installation. A cold or warm standby must restore the database, manifest, and exchange-held address high-water marks consistently, then reconcile allocations and deposits before receiving traffic.

## Outstanding withdrawal recovery

Do not infer outstanding attempts from scanner data alone.

1. Stop new planning and signing.
2. Restore the exchange withdrawal ledger and gateway idempotency database.
3. Look up every signed txid with its authorized `wallet_id`.
4. For a broadcast timeout or retryable error, submit the identical wallet ID, raw bytes, expected txid, credential principal name, and idempotency key.
5. If the tx is absent while `chain_height <= expiry_height`, keep its selected note IDs reserved.
6. If the original broadcast operation is known complete and the same txid is later orphaned or absent while canonical height is strictly below expiry, persist a fresh rebroadcast-operation key and resubmit the identical signed bytes only when policy leaves enough blocks to mine. A completed old key only replays its stored receipt. Never use a fresh key to resolve an uncertain call.
7. Release only after `chain_height > expiry_height`, node lookup is absent, expired, or orphaned, wallet effects are reconciled, and [selected-note status](../capabilities/selected-note-status.md) returns the complete recorded ID set as `unspent`. If the attempt was ever mined/orphaned, also require `chain_height >= orphaned_at_height + configured_confirmations` with no later remine event, so the replacement branch is final under exchange policy (default `100`). If the fork height is unavailable, do not release automatically. An `unknown`, `pending`, or `spent` item stays locked; attach a note spent by another transaction to that txid instead of returning it to available inventory. Node lookup can remain `orphaned` rather than transition to `expired`.
8. Keep mined transactions provisional until the configured finality threshold. On unconfirmation or orphaning, reverse finality-dependent actions and keep the reservation.

After scanner loss, wait for full backfill before reconciling. The node can re-expose a live mempool transaction, but do not assume an old dropped mempool attempt or its lifecycle events will be recreated. A node restart with `JUNO_NODE_PERSIST_MEMPOOL=0` can likewise forget an accepted unmined transaction. In either case, keep selected note IDs reserved and follow the same txid lookup, identical-byte rebroadcast, and strict post-expiry release rules above. The exchange ledger remains the source of truth for the withdrawal attempt.

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

Omit `--next-index` to use manifest values. Overrides can only increase a high-water mark. Recovery derives and verifies every address, rebuilds the registry with empty labels, and binds the database to the original installation ID. Customer labels and ownership must remain available in the exchange mapping. There is no gateway label import or update API, so recovered gateway responses keep empty labels; restore a verified gateway database backup instead when gateway-held labels are required.

Audited reconstruction also creates an empty idempotency-receipt table. Every pre-loss broadcast key has lost its replay and conflict record, and reusing one would be treated as a new operation. Before reopening broadcast, inventory every planned or signed attempt from the exchange withdrawal ledger, look up each txid, reconcile selected note reservations and expiry, and classify any formerly uncertain call. Keep broadcast closed when acceptance cannot be proven. Only after reconciliation may the exchange record a fresh operation key; submit the identical signed bytes only under the rebroadcast rules for that known attempt. A scanner replay cannot reconstruct gateway idempotency receipts.

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

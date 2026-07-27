---
title: Backups and recovery
---

## Back up

| Data | Requirement |
| --- | --- |
| Signer seed/material and signer journal | Required to spend and to recover one immutable result per attempt; encrypted and isolated |
| Signer wallet/account/network/UFVK bindings | Required to prove the restored seed matches each allowed wallet before the socket opens |
| Installation-state directory | Required to preserve installation identity and address high-water marks |
| `wallets.json`, accounts, and birthday heights | Required to derive addresses, plan for the correct account, and rescan |
| Credential-name registry and bearer-token recovery | Required to recreate authentication; preserve each coordinator/broadcaster `name` exactly, while its token and hash may rotate |
| Exchange address-to-account mapping | Required for customer attribution |
| Exchange withdrawal ledger | Required for outstanding attempts: approvals, plan digests, selected note-ID reservations, txids, expiries, broadcast principal names, and idempotency keys |
| Gateway/coordinator database | Required for automated attempts, exact plans/results, active reservations, labels, cursor key, and idempotency receipts |
| Scanner database | Recommended for faster recovery; otherwise rebuild from chain |
| Node data | Optional; otherwise resync the node |

Stop transaction creation and the gateway for a simple consistent SQLite snapshot, including WAL state. Snapshot the signer journal without deleting newer immutable entries; stopping the signer gives the simplest consistent drill. Use native consistent Postgres backups for the scanner. Keep backup directories at mode `0700` and files at `0600`, encrypt copies, and test restores. The gateway process uses umask `0077`; backup tooling must preserve or reapply these permissions.

Keep bearer secrets in the exchange secret manager and back up `auth.json` only as encrypted configuration. During recovery, recreate every coordinator and broadcaster credential with the exact prior `name`; its token may rotate under that name. A different coordinator name cannot own the old attempt, and a different name creates a different durable idempotency namespace.

The installation manifest contains UFVK fingerprints, not raw UFVKs. It changes before each address index is issued. Keep it on write-through durable storage outside Compose volumes, or reconcile every allocation against an independent append-only exchange address record.

The gateway detects a missing manifest, a mismatched installation identity, and a database that is behind the current manifest. It cannot detect a jointly restored, internally consistent old manifest and old database. That pair could reuse indices allocated after the snapshot, so never serve when both copies may be stale.

## Scanner-only rebuild

Confirmed scanner data is reproducible from the same UFVK, birthday, network, and canonical chain. Mempool-only observations and old unmined expiry history may not be reconstructible after data loss. A scanner rebuild does not rebuild coordinator attempts, note reservations, exact plans, signed results, exchange approvals, or signer journals.

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

1. Stop new creation and broadcast.
2. Restore the exchange withdrawal ledger, gateway/coordinator database, installation manifest, and signer journal. Never truncate the journal to match an older database.
3. Start the signer and coordinator privately, but keep new creation closed. Recovery workers resume `planning`, `reserved`, `signing`, and `signing_unknown` with the stored attempt ID, exact plan, and digest.
4. Poll every attempt. A signer-journal replay of the same ID/digest safely recovers the original signed result; a digest conflict is an incident, not permission to re-sign.
5. Look up every signed txid with its authorized `wallet_id`. For an uncertain broadcast, submit the identical wallet ID, raw bytes, expected txid, credential principal name, and idempotency key.
6. If the tx is absent before expiry, keep its selected note IDs reserved. If a completed broadcast later becomes orphaned/absent while still valid, a fresh operation key may rebroadcast only the identical bytes after the original call is reconciled.
7. Let the coordinator release automatically only when height is at least `expiry_height + configured_confirmations`, node/scanner tips match exactly, readiness/history/pending-spend attestations are complete, and the full selected-ID set is `unspent`. Any missing, `unknown`, `pending`, or `spent` item stays locked.
8. Keep mined transactions provisional until the configured finality threshold, default `100`. On unconfirmation or orphaning, reverse finality-dependent actions and keep the reservation.

After scanner loss, wait for full backfill before reconciling. The node can re-expose a live mempool transaction, but do not assume an old dropped mempool attempt or its lifecycle events will be recreated. A node restart with `JUNO_NODE_PERSIST_MEMPOOL=0` can likewise forget an accepted unmined transaction. In either case, keep selected note IDs reserved and follow the same txid lookup, identical-byte rebroadcast, and strict post-expiry release rules above. The exchange ledger remains the source of truth for the withdrawal attempt.

## Gateway/coordinator database recovery

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

Audited reconstruction creates empty broadcast-receipt, transaction-attempt, event, and active-reservation tables. A scanner replay cannot reconstruct any of them.

Before reopening broadcast, inventory every planned or signed attempt from the exchange ledger and signer journal, look up each txid, reconcile raw bytes, selected notes, expiry, and every formerly uncertain call. A lost receipt makes an old broadcast key appear new; keep broadcast closed when acceptance cannot be proven.

Do not enable coordinator creation against reconstructed empty attempt tables while any pre-loss attempt can still sign, mine, reorg, or hold an input. There is no attempt-import API. Keep each old selected note ID externally locked until its transaction is final or the same strict post-expiry proof shows it unspent. Only after all pre-loss attempts are terminally reconciled may the empty coordinator state begin new attempts.

If a restore may contain a stale manifest and database:

1. Keep allocation and financial APIs offline.
2. Restore the newest durable manifest, or find the greatest index ever issued for each wallet in the append-only exchange allocation record.
3. Discard the stale gateway database and run the audited checksum and recovery flow with a conservative `--next-index` above every index that may have been issued.
4. Reconcile the rebuilt registry and customer ownership before serving.

If neither source proves a safe high-water mark, remain offline. Do not guess and do not run `init`.

After scanner loss too, start the empty scanner and recovered gateway, wait for full rescan and readiness, then reconcile balances and deposits before reopening traffic.

Never use `init` to replace a lost database or manifest. If the original manifest and its backups are lost, keep address allocation and financial APIs offline; the installation ID and safe high-water cannot be inferred from an empty database.

## Signer recovery

Recover signing material and the immutable signer journal only inside the isolated signing environment. Preserve journal entries newer than a database snapshot: they may be the only proof that signing began. Restore owner-only socket/journal permissions, then prove health and same-attempt replay with a non-production plan or regtest before restoring withdrawals. Never copy recovered keys into the gateway/coordinator container.

---
title: Troubleshooting
---

Start with:

```bash
docker compose ps
docker compose logs --since=15m gateway juno-scan junocashd
```

| Symptom | Check | Action |
| --- | --- | --- |
| Startup rejects wallet | UFVK prefix and `JUNO_NETWORK` | Use the matching mainnet, testnet, or regtest wallet file |
| `401` / `403` | Token, scope, wallet grant | Reload the exact least-privilege credential |
| Readiness `503` | Error code, node sync, scanner lag/backfill, `pending_spends_ready` | Keep financial traffic closed; after restart or reorg, wait for the scanner's current-tip mempool reconciliation; otherwise repair the named dependency |
| Balance `404` | Wallet and allocation record | Query only gateway-allocated addresses under that wallet |
| Deposit not final | Confirmations and chain height | Wait for the configured threshold; default `100` |
| Deposit unconfirmed/orphaned | Lifecycle event | Apply the compensating ledger action |
| Cursor `409 cursor_reset_required` | Scanner event epoch changed | Restart without the cursor and replay idempotently by stable deposit identity |
| Cursor `400` | Malformed, cross-wallet, wrong-key, or otherwise unauthenticated cursor | Fix the request; during audited key-loss recovery, discard it explicitly; never auto-reset on `400` |
| Planner selects an already planned note | Exchange reservation ledger | Atomically reserve `notes[].note_id` values or run only one complete lifecycle per wallet |
| Planner uses immature notes | `--minconf` and component version | Use the default `100` or pass the documented policy explicitly |
| Node rejects fee | Planner fee and node policy | Use the default multiplier `20`; revalidate after version changes |
| `note_decrypt_failed` while signing | Plan, seed, account, coin type, network | Stop; verify the approved plan belongs to the isolated signing wallet |
| External finalize rejects signatures | Version, action indexes, hex lengths, duplicates | Return exactly one valid signature per request without changing the prepared transaction |
| Broadcast `409` | Error code, payload, `Retry-After` | For `idempotency_in_progress`, wait then retry identical input; use a new key only for a new signed attempt or reconciled orphan/drop rebroadcast |
| Broadcast result uncertain | `retryable`, txid, original key/body | Retry identical input; never rebuild first |
| Transaction `404` | Lowercase txid, node sync/index, wallet query | Retry with authorized `wallet_id`; keep reservations until expiry is strictly passed |
| Pending remains at expiry height | Current height vs `expiry_height` | Expected: wait until `chain_height > expiry_height` |
| Selected-note epoch changed | Status `event_epoch` vs deposit checkpoint | Stop release, reset the old deposit cursor, replay idempotently, then request a fresh complete status batch |
| Transaction orphaned | Node and wallet lifecycle | Reverse finality-dependent actions and keep inputs reserved. Before expiry, deliberately rebroadcast identical bytes only under the reconciled fresh-key procedure. After expiry, also wait until the replacement branch is final from `orphaned_at_height`, then reconcile every selected note before release |
| Consolidation has insufficient funds | Selected note sum vs action fee | Raise eligible value, reduce input count, or exclude uneconomic notes |
| Witness planning is slow/fails | Scanner lag, shard cache, anchor freshness | Wait for readiness and rebuild a fresh plan |

Correlate gateway errors with `request_id` and `X-Request-ID`. Do not paste secrets or full sensitive bodies into support channels.

---
title: Troubleshooting
---

Start with:

```bash
docker compose ps
docker compose logs --since=15m gateway juno-scan junocashd
```

With automated withdrawals, use the overlay and include signer logs:

```bash
docker compose -f compose.yaml -f compose.automation.yaml ps
docker compose -f compose.yaml -f compose.automation.yaml logs --since=15m gateway txsign
```

| Symptom | Check | Action |
| --- | --- | --- |
| Startup rejects wallet | UFVK prefix and `JUNO_NETWORK` | Use the matching mainnet, testnet, or regtest wallet file |
| `401 unauthorized` | Missing, malformed, or unknown bearer token | Send the configured plaintext bearer token over TLS; do not send its SHA-256 value |
| `403 credential lacks the required scope` | Credential scopes | Add only the operation scope the client needs, then recreate the gateway container because auth is loaded at startup |
| `403 credential is not authorized for this wallet` | Requested wallet ID versus `wallets.json[].wallet_id` and the credential's `wallets` array | Use an exact, case-sensitive registered ID in the URL, query, or body, and grant that ID or `"*"`; `hot` does not match `exchange-hot`, and `admin` does not bypass wallet grants |
| Readiness `503` | Error code, node sync, scanner lag/backfill, `pending_spends_ready` | Keep financial traffic closed; after restart or reorg, wait for the scanner's current-tip mempool reconciliation; otherwise repair the named dependency |
| Coordinator readiness `503` | Gateway state, planner executable, signer socket/journal | Stop new attempts; keep polling existing IDs after the dependency is restored |
| Coordinator says recovery is sealed | Recent gateway database reconstruction and pre-loss withdrawal ledger | Reconcile every old attempt/note, run `recovery-unseal-coordinator` with the recovered installation ID and audit reference, then wait for private readiness |
| Balance `404` | Wallet and allocation record | Query only gateway-allocated addresses under that wallet |
| Deposit not final | Confirmations and chain height | Wait for the configured threshold; default `100` |
| Deposit unconfirmed/orphaned | Lifecycle event | Apply the compensating ledger action |
| Cursor `409 cursor_reset_required` | Scanner event epoch changed | Restart without the cursor and replay idempotently by stable deposit identity |
| Cursor `400` | Malformed, cross-wallet, wrong-key, or otherwise unauthenticated cursor | Fix the request; during audited key-loss recovery, discard it explicitly; never auto-reset on `400` |
| Attempt stays `planning` | `data.error`, scanner/node readiness, balance, reservation conflicts | If retryable, keep the same attempt; otherwise wait for `failed_unsigned`, fix/reapprove, and use a new key |
| Attempt is `signing_unknown` | Signer health and journal permissions | Keep notes locked and poll the same ID; never replan, cancel, or delete journal state |
| Cancel returns `409 attempt_not_cancellable` | Attempt state and signer journal | Signing may have started; keep polling and do not create a replacement |
| SDK wait times out | Stored attempt ID or original create key/body | Poll the same ID, or replay the exact create request/key to recover it; timeout does not cancel |
| `503 expiry_status_unavailable` | Canonical node tip/network/IBD state | Restore a healthy node and retry the same attempt; raw material is withheld and selected notes remain reserved |
| Planner uses immature notes | `JUNO_GATEWAY_DEFAULT_CONFIRMATIONS` and component version | Keep the default `100` outside disposable tests |
| Node rejects fee | Planner fee and node policy | Use the default multiplier `20`; revalidate after version changes |
| `note_decrypt_failed` while signing | Plan, seed, account, coin type, network | Stop; verify the approved plan belongs to the isolated signing wallet |
| External finalize rejects signatures | Version, action indexes, hex lengths, duplicates | Return exactly one valid signature per request without changing the prepared transaction |
| Broadcast `409` | Error code, payload, `Retry-After` | For `idempotency_in_progress`, wait then retry identical input; use a new key only for a new signed attempt or reconciled orphan/drop rebroadcast |
| Broadcast result uncertain | `retryable`, txid, original key/body | Retry identical input; never rebuild first |
| Transaction `404` | Lowercase txid, node sync/index, wallet query | Retry with authorized `wallet_id`; keep reservations until coordinator state becomes `released` or `final` |
| Pending remains at expiry height | Current height vs `expiry_height` | Expected. Scanner needs strict `> expiry`; coordinator release additionally needs `expiry_height + confirmations` and exact unspent proof |
| Selected-note epoch changed | Status `event_epoch` vs deposit checkpoint | Stop release, reset the old deposit cursor, replay idempotently, then request a fresh complete status batch |
| Transaction orphaned | Node and wallet lifecycle | Reverse finality-dependent actions and keep inputs reserved. Before expiry, deliberately rebroadcast identical bytes only under the reconciled fresh-key procedure. After expiry, also wait until the replacement branch is final from `orphaned_at_height`, then reconcile every selected note before release |
| Consolidation has insufficient funds | Selected note sum vs action fee | Raise eligible value, reduce input count, or exclude uneconomic notes |
| Witness planning is slow/fails | Scanner lag, shard cache, anchor freshness | Wait for readiness and rebuild a fresh plan |

Correlate gateway errors with `request_id` and `X-Request-ID`. Do not paste secrets or full sensitive bodies into support channels.

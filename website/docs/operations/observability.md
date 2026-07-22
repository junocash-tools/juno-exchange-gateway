---
title: Observability
---

## Health

| Endpoint | Auth | Use |
| --- | --- | --- |
| `GET /v1/health/live` | No | Process liveness only |
| `GET /v1/health/ready` | `read` | Financial traffic gate |
| `GET /v1/version` | `read` | Build and component versions |
| `GET /v1/network/tip` | `read` | Node and scanner positions |

Readiness checks gateway state, network identity, node sync, scanner lag, pending-spend reconciliation, and wallet history. It requires scanner `pending_spends_ready: true` for the current event epoch and exact scanner tip. With the default complete-history policy it also requires an explicit scanner `history_complete: true` attestation, and returns `200` only when each wallet backfill is `complete` with `next_height` beyond the scanner tip. Never route financial traffic from liveness alone.

Scanner `GET /v1/wallets/{wallet_id}/backfill` is the authoritative diagnostic for birthday, next and target heights, state, last error, and update time.

## Logs

Gateway stdout is structured JSON with request ID, route, status, byte count, duration, principal, and remote IP. Restrict log access and do not record tokens, UFVKs, plans, raw transactions, memos, or signing material.

## Withdrawal signals

Reservations live in the exchange ledger, not the gateway. Export at least:

- reserved note-ID count and oldest reservation age by wallet
- attempts by planned, signed, broadcast-uncertain, mempool, mined, final, orphaned, and expired state
- blocks remaining to each signed transaction's expiry
- broadcast retries, `idempotency_in_progress`, `node_rpc_error`, and rejected transactions
- mempool age and confirmation depth for outstanding withdrawals
- unconfirmation/orphan events after credit or withdrawal finality
- note count and value distribution for consolidation planning

At the exact expiry height a pending note is still locked. If an absent transaction's scanner pending marker remains after `chain_height > expiry_height`, alert. For an exchange reservation, alert only after its release prerequisites are satisfied: strict expiry plus full reconciliation for a never-mined attempt, or strict expiry plus replacement-branch finality from `orphaned_at_height` and full note reconciliation for an attempt that was ever mined or orphaned. See [note selection and reservations](../transactions/note-selection-and-reservations.md).

## Alerts

- readiness `503`, node initial block download, or network mismatch
- scanner lag above the configured maximum, `pending_spends_ready: false`, stalled backfill, or shard-cache failure
- repeated cursor/event-epoch resets or reorg lifecycle events
- sustained `429`, `5xx`, broadcast uncertainty, or idempotency storage errors
- outstanding reservations near or beyond expiry
- storage capacity, backup, and restore-test failures

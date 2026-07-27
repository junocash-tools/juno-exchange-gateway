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

The coordinator has its own private `GET /v1/health/live` and `GET /v1/health/ready`. Its readiness requires `plan` and checks durable state, the planner executable, and signer UDS health. Gate withdrawal creation on both public financial readiness and private coordinator readiness.

Scanner `GET /v1/wallets/{wallet_id}/backfill` is the authoritative diagnostic for birthday, next and target heights, state, last error, and update time.

## Logs

Public gateway stdout is structured JSON with request ID, route, status, byte count, duration, principal, and remote IP. Preserve the request IDs returned by both listeners and monitor coordinator recovery/state errors plus signer health. Restrict log access and do not record tokens, UFVKs, plans, raw transactions, txids, addresses, memos, or signing material.

## Withdrawal signals

Active reservations live in the gateway/coordinator database; the exchange ledger must retain the matching business record. Export at least:

- reserved note-ID count and oldest reservation age by wallet
- attempts by `planning`, `reserved`, `signing`, `signing_unknown`, `signed`, `broadcast`, `mined`, `final`, `orphaned`, `expired_pending_reconciliation`, `released`, `failed_unsigned`, and `cancelled`
- oldest `planning`, `signing`, and `signing_unknown` age plus attempt-level error code
- blocks remaining to each signed transaction's expiry
- broadcast retries, `idempotency_in_progress`, `node_rpc_error`, and rejected transactions
- mempool age and confirmation depth for outstanding withdrawals
- unconfirmation/orphan events after credit or withdrawal finality
- note count and value distribution for consolidation planning

At the exact expiry height a pending note is still locked. The coordinator does not release merely at the next block: it waits until height is at least `expiry_height + configured_confirmations` and the exact ready scanner tip proves the complete selected set unspent. Alert when an attempt remains `expired_pending_reconciliation` or `orphaned` after that boundary. See [note selection and reservations](../transactions/note-selection-and-reservations.md).

## Alerts

- readiness `503`, node initial block download, or network mismatch
- scanner lag above the configured maximum, `pending_spends_ready: false`, stalled backfill, or shard-cache failure
- repeated cursor/event-epoch resets or reorg lifecycle events
- sustained `429`, `5xx`, broadcast uncertainty, or idempotency storage errors
- outstanding reservations near or beyond expiry
- signer readiness failure, repeated `signing_unknown`, journal conflict, or coordinator recovery backlog
- storage capacity, backup, and restore-test failures

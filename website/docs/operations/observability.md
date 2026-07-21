---
title: Observability
---

## Health endpoints

| Endpoint | Auth | Use |
| --- | --- | --- |
| `GET /v1/health/live` | No | Process liveness only |
| `GET /v1/health/ready` | `read` | Financial traffic gate |
| `GET /v1/version` | `read` | Build and API version |
| `GET /v1/network/tip` | `read` | Node/scanner position |

Readiness verifies gateway state, network identity, node sync, scanner lag, and wallet history. It returns `200` only when every scanner wallet is `complete` with `next_height` beyond the scanner tip. Route financial requests only then.

For operator diagnosis, scanner `GET /v1/wallets/{wallet_id}/backfill` reports the authoritative birthday, next and target heights, state, last error, and update time. The gateway mirrors this progress; it does not override scanner state.

## Logs

Gateway logs are structured JSON. Each request includes request ID, route, status, byte count, duration, principal, and remote IP. Collect container stdout and restrict access because metadata can still be sensitive.

Alert on:

- readiness `503`
- scanner lag above the configured maximum
- node initial block download or network mismatch
- scanner shard-cache failures or stalled progress
- backfill state `error` or a `next_height` that stops advancing
- repeated reorg lifecycle events
- sustained `429`, `5xx`, or broadcast uncertainty
- storage and idempotency errors

Do not treat liveness as readiness.

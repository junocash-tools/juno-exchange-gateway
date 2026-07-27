---
title: HTTP API
---

All routes are under `/v1`. Except liveness, send:

```http
Authorization: Bearer <token>
Accept: application/json
X-Request-ID: <optional-correlation-id>
```

JSON requests also require `Content-Type: application/json`. Parameters such as `charset=utf-8` are accepted; JSON-like types such as `application/jsonp` are not. Wrong methods on known routes return the authenticated JSON `405 method_not_allowed` envelope.

## Routes

| Method | Route | Scope |
| --- | --- | --- |
| `GET` | `/v1/health/live` | public |
| `GET` | `/v1/health/ready` | `read` |
| `GET` | `/v1/version` | `read` |
| `GET` | `/v1/network/tip` | `read` |
| `POST` | `/v1/wallets/{wallet_id}/addresses` | `address` + wallet |
| `GET` | `/v1/wallets/{wallet_id}/addresses/{address}/balance` | `read` + wallet |
| `GET` | `/v1/wallets/{wallet_id}/notes/summary` | `treasury` + wallet |
| `POST` | `/v1/wallets/{wallet_id}/notes/status` | `treasury` + wallet |
| `GET` | `/v1/wallets/{wallet_id}/deposits` | `read` + wallet |
| `GET` | `/v1/transactions/{txid}` | `read`; add `withdrawal` + wallet grant for `wallet_id`, and `raw` for raw hex |
| `POST` | `/v1/transactions/broadcast` | `broadcast` + wallet grant + `Idempotency-Key` |

There are no build, approve, or sign routes on this public listener. Automated creation uses the separately bound [private coordinator API](./coordinator-http.md), normally through the Node.js SDK. The exchange then submits only its signed result to the public broadcast route. See [build, sign, and broadcast](../transactions/build-sign-broadcast.md).

## Envelopes

Success:

```json
{
  "status": "ok",
  "data": {},
  "request_id": "req_..."
}
```

Error:

```json
{
  "status": "error",
  "error": {
    "code": "scanner_not_ready",
    "message": "financial reads are not ready",
    "retryable": true,
    "details": {}
  },
  "request_id": "req_..."
}
```

Use `error.retryable`, not the status alone. Preserve a valid `X-Request-ID` or record the returned ID.

| Status | Meaning |
| --- | --- |
| `400` | Invalid input |
| `401` / `403` | Missing authentication or authorization |
| `404` | Unknown resource |
| `409` | Safe cursor/history reset or idempotency conflict/in progress |
| `413` | JSON body exceeds `JUNO_GATEWAY_MAX_JSON_BODY_BYTES`, or broadcast exceeds `JUNO_GATEWAY_MAX_BROADCAST_BODY_BYTES` |
| `422` | Safe response cap or transaction validation/rejection |
| `429` | Rate limit |
| `500` | Internal gateway or durable-state failure; stop automatic processing, preserve the exact request/checkpoint, and alert |
| `502` | Invalid scanner response, upstream node failure, or uncertain broadcast |
| `503` | Financial dependency not ready |

An `internal` error deliberately has `retryable: false`: the exchange must not loop blindly while gateway state may be unhealthy. Preserve the current deposit cursor or broadcast idempotency key, diagnose readiness and storage, then follow the capability-specific reconciliation procedure. Never manufacture a new cursor, signed attempt, or idempotency key to bypass a `500`.

The public OpenAPI document is served as `openapi.yaml`. The separate private contract is `coordinator.openapi.yaml`.

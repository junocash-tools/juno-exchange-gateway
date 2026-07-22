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
| `GET` | `/v1/wallets/{wallet_id}/deposits` | `read` + wallet |
| `GET` | `/v1/transactions/{txid}` | `read`; add `withdrawal` + wallet grant for `wallet_id`, and `raw` for raw hex |
| `POST` | `/v1/transactions/broadcast` | `broadcast` + wallet grant + `Idempotency-Key` |

There are no public build, approve, or sign routes. Use the private `juno-txbuild` CLI and isolated `juno-txsign`; see [build, sign, and broadcast](../transactions/build-sign-broadcast.md). A future planner service must meet the [private planner criteria](../transactions/note-selection-and-reservations.md#criteria-for-a-private-planner-service).

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
| `413` | Broadcast body limit exceeded |
| `422` | Safe response cap or transaction validation/rejection |
| `429` | Rate limit |
| `502` | Invalid scanner response, upstream node failure, or uncertain broadcast |
| `503` | Financial dependency not ready |

The OpenAPI document is served with the source repository at `api/openapi.yaml`.

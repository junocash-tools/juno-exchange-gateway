---
title: HTTP API
---

All endpoints are under `/v1`. Except liveness, send:

```http
Authorization: Bearer <token>
Accept: application/json
```

JSON requests require media type `application/json`; parameters such as `charset=utf-8` are allowed, but JSON-like types such as `application/jsonp` are rejected. Wrong methods on known routes require authentication and return the normal JSON error envelope with `405 method_not_allowed`.

## Routes

| Method | Route | Scope |
| --- | --- | --- |
| `GET` | `/v1/health/live` | public |
| `GET` | `/v1/health/ready` | `read` |
| `GET` | `/v1/version` | `read` |
| `GET` | `/v1/network/tip` | `read` |
| `POST` | `/v1/wallets/{wallet_id}/addresses` | `address` + wallet |
| `GET` | `/v1/wallets/{wallet_id}/addresses/{address}/balance` | `read` + wallet |
| `GET` | `/v1/wallets/{wallet_id}/deposits` | `read` + wallet |
| `GET` | `/v1/transactions/{txid}` | `read`; `raw` for raw hex |
| `POST` | `/v1/transactions/broadcast` | `broadcast` |

## Envelope

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

Use `retryable`, not only the HTTP status, when deciding whether to retry. Preserve a valid `X-Request-ID` or use the one returned by the gateway.

Common statuses are `400` invalid input, `401` missing/invalid auth, `403` missing scope, `404` unknown resource, `409` idempotency conflict/in progress, `422` rejected transaction, `429` rate limit, `502` upstream node failure, and `503` financial data not ready.

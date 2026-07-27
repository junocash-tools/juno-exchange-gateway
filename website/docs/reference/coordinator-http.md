---
title: Private coordinator API
---

The coordinator is the private transaction-creation API. It accepts approved outputs, durably reserves notes, calls the isolated signer, and returns signed raw hex. It never broadcasts and must not be internet-facing.

Use [`@junocash-tools/exchange-sdk`](https://github.com/junocash-tools/juno-exchange-sdk) for Node.js integrations. This page defines the underlying wire contract. The complete schema is [coordinator.openapi.yaml](pathname:///coordinator.openapi.yaml).

## Authentication

Every route except `GET /v1/health/live` requires:

```http
Authorization: Bearer <coordinator-token>
Accept: application/json
```

The credential needs `plan` or `admin` plus a grant for the source wallet. Only the credential `name` that created an attempt can read or cancel it. The private listener uses the same `auth.json` format as the public gateway, but it should have a separate token.

## Create

```http
POST /v1/transaction-attempts
Authorization: Bearer <coordinator-token>
Content-Type: application/json
Idempotency-Key: withdrawal-1842-attempt-1
X-Request-ID: withdrawal-1842-create

{
  "wallet_id": "hot",
  "approval_reference": "withdrawal:1842",
  "outputs": [
    {
      "to_address": "j1...",
      "amount_zat": "250000",
      "memo_hex": "6869"
    }
  ]
}
```

`wallet_id` is the source. There is no `addressFrom`: Orchard spends consume notes controlled by a registered wallet/UFVK, not funds attached to one visible address. Amounts are canonical base-10 zatoshi strings. Memos are optional lowercase hex, at most 512 bytes.

A new or active replay returns `202`:

```json
{
  "status": "ok",
  "data": {
    "attempt_id": "txn_0123456789abcdef0123456789abcdef",
    "state": "planning",
    "wallet_id": "hot",
    "approval_reference": "withdrawal:1842",
    "created_at": "2026-07-27T12:00:00Z",
    "updated_at": "2026-07-27T12:00:00Z"
  },
  "request_id": "withdrawal-1842-create"
}
```

Persist `attempt_id` before polling. Reusing the same key and normalized body returns the same attempt with `Idempotency-Replayed: true`. A terminal replay returns `200`. Reusing the key for another payload returns `409 idempotency_conflict`.

## Poll

```http
GET /v1/transaction-attempts/txn_0123456789abcdef0123456789abcdef
Authorization: Bearer <coordinator-token>
```

When `state` is `signed`, `data` contains the broadcast inputs and reconciliation metadata:

```json
{
  "status": "ok",
  "data": {
    "attempt_id": "txn_0123456789abcdef0123456789abcdef",
    "state": "signed",
    "wallet_id": "hot",
    "approval_reference": "withdrawal:1842",
    "change_address": "j1...",
    "plan_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "fee_zat": "200000",
    "expiry_height": 920041,
    "selected_note_ids": [
      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:0"
    ],
    "txid": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
    "raw_tx_hex": "00aa",
    "orchard_output_action_indices": [0],
    "orchard_change_action_index": 1,
    "created_at": "2026-07-27T12:00:00Z",
    "updated_at": "2026-07-27T12:00:02Z"
  },
  "request_id": "req_018f"
}
```

Persist `change_address` and verify outgoing and change effects against that registered same-wallet address. `orchard_output_action_indices` follows request output order; persist the mapping and do not assume Orchard action order. `orchard_change_action_index` is omitted when there is no change action.

## Cancel

```http
POST /v1/transaction-attempts/txn_0123456789abcdef0123456789abcdef/cancel
Authorization: Bearer <coordinator-token>
Content-Type: application/json

{}
```

Only `planning` or `reserved` can become `cancelled`. This proves signing did not start and releases any selected-note reservation. Cancellation of `signing`, `signing_unknown`, or a later state returns `409 attempt_not_cancellable`. Do not replace such an attempt.

## States

| State | Exchange action |
| --- | --- |
| `planning` | Poll. A retryable attempt-level `error` may explain a dependency delay. |
| `reserved` | Notes and exact plan are durable; poll and do not create a competing spend. |
| `signing` | Poll. Cancellation is forbidden. |
| `signing_unknown` | Keep polling the same ID. A completed signer-journal entry replays the original result; an unresolved pending entry stays locked for operator recovery. Never replan or release its notes. |
| `signed` | Persist every returned field, then submit `raw_tx_hex` and `txid` to the public broadcast API. |
| `broadcast` | Transaction is in the node mempool; keep monitoring. |
| `mined` | Mined below the configured finality threshold; keep the withdrawal provisional. |
| `final` | Reached finality, default 100 confirmations. Terminal success. |
| `orphaned` | Keep locked. The same bytes may still be valid before expiry. |
| `expired_pending_reconciliation` | Expiry was observed, but release proof is incomplete. Keep locked. |
| `released` | Post-expiry node/scanner proof showed every selected note unspent. A new attempt is allowed. |
| `failed_unsigned` | Signing provably did not begin; reservations were released. Fix the cause and use a new key. |
| `cancelled` | Provably unsigned cancellation; terminal. |

An attempt-level asynchronous failure appears inside `data.error` even though the HTTP envelope has `status: "ok"`:

```json
{
  "code": "planner_timeout",
  "message": "transaction planning timed out",
  "retryable": true
}
```

Poll retryable states with bounded backoff. Never turn a client timeout into a new attempt; query the stored `attempt_id`.

## Error envelope

HTTP errors use the same top-level shape as the public gateway:

```json
{
  "status": "error",
  "error": {
    "code": "idempotency_conflict",
    "message": "idempotency key was used with a different payload",
    "retryable": false
  },
  "request_id": "req_018f"
}
```

Retry only when `error.retryable` is true, and preserve the exact body, principal, and idempotency key. A `500 internal` is deliberately not retryable: stop new attempts, preserve the current ID/key, and repair durable state.

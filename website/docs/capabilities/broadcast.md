---
title: Broadcast a signed transaction
---

The gateway accepts a fully signed raw transaction. It does not build, approve, or sign it. Required scope: `broadcast`, with access to the submitted `wallet_id`.

## Request

```bash
curl --fail-with-body -X POST \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: withdrawal-1842-attempt-1' \
  -d '{
    "wallet_id": "hot",
    "raw_tx_hex": "<lowercase-hex>",
    "expected_txid": "<64-lowercase-hex>"
  }' \
  "$GATEWAY_URL/v1/transactions/broadcast"
```

`wallet_id` must be registered and granted to the credential. It binds authorization, audit, and the idempotent request identity; the gateway does not infer which wallet a raw shielded transaction spends. The key is scoped to the authenticated principal. It must start with a letter or digit; the remaining 127 characters may also use `.`, `_`, `:`, `/`, or `-`. Unknown JSON fields are rejected.

## Responses

A transaction newly accepted by the node returns `202`:

```json
{
  "status": "ok",
  "data": {
    "wallet_id": "hot",
    "txid": "<64-lowercase-hex>",
    "state": "mempool",
    "accepted": true,
    "already_known": false
  },
  "request_id": "req_..."
}
```

An idempotent replay or a transaction already in the mempool or active chain returns `200` with `accepted: false` and `already_known: true`. `state` is `mempool`, `confirmed`, or `known`; `known` means the node reported it as known before lookup could resolve a concrete state.

An `orphaned` lookup does not short-circuit a newly claimed broadcast operation. The gateway resubmits the exact approved bytes so the node can accept them back into the mempool or return a current rejection.

A completed idempotency receipt is immutable: replaying its key returns the stored result without contacting the node. Deliberate rebroadcast after a later orphan or mempool drop therefore uses a fresh **rebroadcast-operation key**, while keeping the same wallet ID, signed raw bytes, expected txid, withdrawal attempt, and nullifier reservations:

1. Resolve any uncertain original HTTP outcome with the original key.
2. Look up the txid with its authorized wallet. Confirm `orphaned`, or confirm it is absent. Require canonical height to remain strictly less than `expiry_height` and leave enough blocks for the exchange's mining policy.
3. Persist a new operation key linked to the same signed attempt, for example `withdrawal-1842-attempt-1-rebroadcast-1`.
4. Submit the identical request body under that new key.

If the deliberate rebroadcast is uncertain, retry its identical request and its new key. Never rebuild or change bytes to resolve uncertainty.

## Retry rules

- After a timeout, `idempotency_in_progress`, `node_rpc_error`, `node_not_ready`, or `rate_limited`, retry the identical wallet ID, raw hex, expected txid, principal, and key.
- For `idempotency_in_progress` and `rate_limited`, wait for the `Retry-After` header. The former also returns the same value in `error.details.retry_after_seconds`.
- A different body under the same key returns non-retryable `409 idempotency_conflict`.
- A completed receipt is durable, immutable, and can replay without contacting the node; its `state` is the stored broadcast result, not a current chain lookup.
- Use a new key only for a newly built signed attempt or the deliberate, reconciled pre-expiry rebroadcast operation above. Never use a new key to resolve an uncertain HTTP outcome.

Example error:

```json
{
  "status": "error",
  "error": {
    "code": "idempotency_in_progress",
    "message": "an identical broadcast is still being resolved",
    "retryable": true,
    "details": {"retry_after_seconds": 30}
  },
  "request_id": "req_..."
}
```

| Status | Codes | Action |
| --- | --- | --- |
| `400` | `invalid_request` | Fix headers, JSON, hex, or txid format |
| `401` / `403` | `unauthorized`, `forbidden` | Fix credentials or scope; do not retry unchanged |
| `404` | `not_found` | Fix the registered wallet ID; do not broadcast under another wallet merely to bypass the error |
| `409` | `idempotency_in_progress`, `idempotency_conflict` | For in-progress, wait `Retry-After` and retry identical input; conflicts are final |
| `413` | `invalid_request` | Reduce the request below the configured body limit |
| `422` | `expected_txid_mismatch`, `transaction_rejected` | Stop; inspect or rebuild the transaction |
| `429` | `rate_limited` | Back off, then retry identical input |
| `502` | `node_rpc_error` | Result may be uncertain; retry identical input |
| `503` | `node_not_ready` | Wait for readiness, then retry identical input |

The gateway does not know the withdrawal, approved recipients, selected notes, or plan digest. Bind withdrawal ID, attempt, plan digest, txid, raw-transaction hash, and idempotency key in the exchange ledger before broadcast. Then reconcile by [transaction lookup](./transaction-lookup.md).

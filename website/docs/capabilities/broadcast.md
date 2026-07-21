---
title: Broadcast a signed transaction
---

The gateway accepts a fully signed raw transaction. It does not build or sign.

```bash
curl --fail-with-body -X POST \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: withdrawal-1842-attempt-1' \
  -d '{
    "raw_tx_hex": "<lowercase-hex>",
    "expected_txid": "<64-lowercase-hex>"
  }' \
  "$GATEWAY_URL/v1/transactions/broadcast"
```

Required scope: `broadcast`.

The idempotency key is mandatory. Reuse the same key and identical payload after a timeout or `retryable: true` response. A different payload with the same key returns `409`.

The gateway checks the expected transaction ID, detects already-known transactions, and stores completed receipts. A newly accepted transaction returns `202`; an idempotent replay or already-known transaction returns `200`.

Never place a seed, key, recipient, amount, or transaction plan in this request. Unknown fields are rejected.

---
title: Look up transactions
---

Query a lowercase transaction ID:

```bash
TXID='<64-lowercase-hex>'
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/transactions/$TXID"
```

The result reports mempool or confirmed state, confirmations, block metadata, expiry height, serialized size, and Orchard action count when available.

Add `wallet_id=hot` to include scanner effects visible to that wallet:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/transactions/$TXID?wallet_id=hot"
```

`include_raw=true` includes raw transaction hex and requires the additional `raw` scope. Normal lookup requires `read`.

Historical arbitrary transaction lookup depends on the node's transaction index. The appliance enables the required node configuration.

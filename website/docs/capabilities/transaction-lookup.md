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

The result reports mempool, confirmed, orphaned, or expired state, plus confirmations, block metadata, expiry height, serialized size, and Orchard action count when available.

Add `wallet_id=hot` to include scanner effects visible to that wallet:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/transactions/$TXID?wallet_id=hot"
```

`include_raw=true` includes raw transaction hex and requires the additional `raw` scope. Normal lookup requires `read`.

When `wallet_id` is authorized and the node has dropped a transaction, the scanner's latest valid lifecycle event can return terminal `orphaned` or `expired` state with all wallet effects. A later nonterminal event cancels that fallback. Raw lookup remains node-only.

Historical arbitrary transaction lookup depends on the node's transaction index. The appliance enables the required node configuration.

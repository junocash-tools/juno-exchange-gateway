---
title: Read the chain tip
---

Read the node tip and scanner position together:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/network/tip"
```

The response includes `height`, `hash`, `block_time`, header height, initial-sync state, verification progress, scanner height, and scanner lag.

Required scope: `read`.

Example response:

```json
{
  "status": "ok",
  "data": {
    "network": "mainnet",
    "height": 920000,
    "hash": "<64-lowercase-hex>",
    "block_time": 1784631000,
    "headers": 920000,
    "initial_sync": false,
    "verification_progress": 1,
    "scanner_height": 919999,
    "scanner_lag": 1
  },
  "request_id": "req_018f"
}
```

Use this route for monitoring and block-height decisions such as strict transaction-expiry release. Do not use it as the traffic gate: a plausible tip does not prove scanner history, wallet backfill, or gateway state is ready. Require `200` from `/v1/health/ready` before address allocation or any financial read.

On `502 node_rpc_error` or retryable `503 scanner_not_ready`, retain the last observation for diagnostics but pause new financial decisions. Heights can decrease or hashes can change during a reorg; never infer finality from height alone.

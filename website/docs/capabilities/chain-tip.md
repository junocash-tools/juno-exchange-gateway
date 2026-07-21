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

Use `/v1/health/ready` as the traffic gate. A plausible node tip alone does not mean balances or deposits are ready.

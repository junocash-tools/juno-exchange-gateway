---
title: Poll deposits
---

The v1 integration is polling-based. Each wallet has an opaque, durable cursor.

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/wallets/hot/deposits?limit=100"
```

Pass the returned `next_cursor` to the next request:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/wallets/hot/deposits?limit=100&cursor=$CURSOR"
```

Optional filters are `status`, `txid`, and an owned `address`. `limit` is `1` to `1000`.

## Lifecycle

| Status | Meaning | Ledger action |
| --- | --- | --- |
| `detected` | Note found in a scanned block | Record, do not make final |
| `confirmed` | Reached the configured threshold | Credit according to policy |
| `unconfirmed` | Reorg moved a confirmed note below threshold | Reverse or hold the credit |
| `orphaned` | The containing block left the active chain | Reverse the deposit |

Delivery is at least once. Use `event_id` to deduplicate deliveries and `deposit_id` to apply lifecycle changes to the same note. Commit the ledger change and cursor checkpoint atomically. Do not advance the cursor before the ledger write is durable.

The cursor is wallet-specific and must be treated as opaque. A cursor copied to another wallet is rejected.

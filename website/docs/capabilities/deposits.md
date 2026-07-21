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

The production ledger consumer must poll without filters. Optional `status`, `txid`, and owned `address` filters are for diagnostics. If automation uses a filter, it needs a separate complete cursor and cannot replace the unfiltered stream. `limit` is `1` to `1000`.

A cursor is bound to the wallet and the exact filter set. Keep `status`, `txid`, and `address` unchanged while advancing it.

## Lifecycle

| Status | Meaning | Ledger action |
| --- | --- | --- |
| `detected` | Note found in a scanned block | Record, do not make final |
| `confirmed` | Reached the configured threshold | Credit according to policy |
| `unconfirmed` | Reorg moved a confirmed note below threshold | Reverse or hold the credit |
| `orphaned` | The containing block left the active chain | Reverse the deposit |

Delivery is at least once. Within one event epoch, use `(deposit_id,event_id)` only to deduplicate transport delivery. Use stable `deposit_id` as the ledger idempotency key for credit, unconfirm, and orphan actions across epoch resets and scanner rebuilds. Commit the ledger change and unfiltered cursor checkpoint atomically. Do not advance the cursor before the ledger write is durable.

Treat the cursor as opaque. Every scanner process start rotates the event epoch, so an old cursor returns `409 cursor_reset_required`. Restart once without a cursor, replay idempotently by stable `deposit_id`, and then persist the new cursor. Reconcile the ledger after a database rebuild.

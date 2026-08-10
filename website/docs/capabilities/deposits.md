---
title: Poll deposits
---

The v1 integration is polling-based. Each wallet has an opaque, durable cursor. Required scope: `read`, with access to that wallet. Run one cursor owner per wallet, or serialize multiple workers through one durable checkpoint.

```bash
WALLET_ID=exchange-hot
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/wallets/$WALLET_ID/deposits?limit=100"
```

Example response:

```json
{
  "status": "ok",
  "data": {
    "deposits": [
      {
        "deposit_id": "exchange-hot:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:0",
        "event_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:81",
        "wallet_id": "exchange-hot",
        "txid": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        "action_index": 0,
        "address": "j1example",
        "diversifier_index": 42,
        "amount_zat": 100000000,
        "status": "confirmed",
        "block_height": 919901,
        "confirmed_height": 920000,
        "observed_at": "2026-07-21T12:00:00Z"
      }
    ],
    "next_cursor": "eyJ2IjoyLCJ3IjoiZXhjaGFuZ2UtaG90IiwiZSI6ImJiYiJ9.signature",
    "delivery": "at_least_once",
    "event_epoch": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
  },
  "request_id": "req_018f"
}
```

`amount_zat` is an integer number of zatoshis. One transaction can contain several deposit actions, so never deduplicate by `txid` alone.

- `deposit_id` is stable: `wallet_id:txid:action_index`. Use it as the exchange ledger idempotency key across lifecycle changes and scanner rebuilds.
- `event_id` is `event_epoch:event_number`. Use it only for transport deduplication within one epoch.
- `block_height` is the containing block. Status-specific responses can also include `confirmed_height`, `rollback_height`, or `orphaned_at_height`.

## Ledger lifecycle

| Status | Meaning | Exchange action |
| --- | --- | --- |
| `detected` | An external note to an allocated address was scanned | Record pending; do not make final |
| `confirmed` | The note reached the configured threshold | Apply an idempotent credit transition by `deposit_id` |
| `unconfirmed` | A reorg moved a confirmed note below the threshold | Reverse, lock, or hold the prior credit |
| `orphaned` | The containing block left the active chain | Reverse the deposit and retain its audit history |

The lifecycle is not strictly one-way. A re-mined transaction can produce `detected` and `confirmed` again after `unconfirmed` or `orphaned`. Apply each transition idempotently against the current state and keep the full history. The gateway reports only external deposits to addresses allocated by this installation; internal change is excluded.

The scanner always supplies `diversifier_index`, including index `0`, and the gateway verifies it against the allocation ledger. For compatibility with older scanner data that omitted a zero value, the gateway infers `0` only when the exact recipient address is already registered at index `0`; every other missing or mismatched identity fails closed with `502 scanner_not_ready`.

`detected` is a mined note, normally at its first scanned confirmation; it is not a mempool notification. With the appliance defaults, `confirmed` arrives at 100 confirmations. The threshold is `JUNO_SCAN_CONFIRMATIONS` and must equal `JUNO_GATEWAY_DEFAULT_CONFIRMATIONS`. Poll continuously while waiting—do not sleep for 100 blocks—because reorg and other deposits share the same ordered stream.

## Production polling loop

Maintain one unfiltered checkpoint per wallet:

1. Start without `cursor`, or load the last committed `next_cursor`.
2. Request `GET /v1/wallets/{wallet_id}/deposits?limit=100` with that cursor.
3. Process `deposits` in response order. Apply credit or reversal by stable `deposit_id`.
4. In the same exchange database transaction, commit every ledger change and the returned `next_cursor`.
5. Repeat. On an empty page, persist the cursor and pause briefly before polling again.

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/wallets/$WALLET_ID/deposits?limit=100&cursor=$CURSOR"
```

There is no `has_more` field. Keep polling. An empty page can still advance `next_cursor` because the gateway safely skips scanner events that do not belong to allocated addresses. Persist every successful cursor, even when `deposits` is empty.

Delivery is at least once. Never checkpoint before the corresponding ledger transaction is durable. If the process stops after commit, replaying the same events is safe because ledger mutations are keyed by `deposit_id`.

The optional `status`, `txid`, and owned `address` filters are for diagnostics. They bind the cursor to that exact filter set and must not replace the unfiltered ledger stream. `limit` is `1` to `1000` and defaults to `100`.

## Cursor errors and retries

A cursor is bound to its wallet, scanner event epoch, and exact filters. Treat it as opaque.

Every scanner process start rotates the event epoch. An older cursor returns:

```json
{
  "status": "error",
  "error": {
    "code": "cursor_reset_required",
    "message": "scanner event history was reset; restart without a cursor and idempotently replay deposits",
    "retryable": false,
    "details": {
      "action": "restart_without_cursor",
      "event_epoch": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    }
  },
  "request_id": "req_018f"
}
```

On `409 cursor_reset_required`, discard the cursor, restart once without it, replay by `deposit_id`, and persist the new cursor. Reconcile the exchange ledger after a scanner database rebuild.

Changing filters while reusing a cursor returns `409 cursor_filter_mismatch` with action `restart_without_cursor_or_restore_filters`. Restore the original filters or start a separate diagnostic cursor. A malformed cursor or one for another wallet returns `400 invalid_request`.

For `429`, honor `Retry-After` and retain the current cursor. For retryable `502`/`503`, retain the cursor and use bounded exponential backoff with jitter. A `502` for malformed or inconsistent scanner data needs an alert if it persists because retries cannot repair upstream data.

For `500 internal`, stop the poller, retain the last **successfully committed** cursor, and alert the gateway/storage operator. The failed response has no usable page cursor. After gateway state is healthy, retry from that same committed cursor; at-least-once replay and `deposit_id` idempotency make this safe. Never advance, skip a page, or start a second production cursor to bypass any error.

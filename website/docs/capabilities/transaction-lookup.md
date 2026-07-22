---
title: Look up transactions
---

Node-only lookup requires `read`. Adding `wallet_id` requires both `read` and `withdrawal`, plus access to that wallet. `include_raw=true` additionally requires `raw`.

## Request

```bash
TXID='<64-lowercase-hex>'
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/transactions/$TXID?wallet_id=hot"
```

Omit `wallet_id` for node-only state. An authorized wallet adds every sanitized scanner effect for that txid. Scanner nullifiers and raw payloads are never returned. Add `include_raw=true` only when raw bytes are needed; it disables scanner-only fallback.

## Response

```json
{
  "status": "ok",
  "data": {
    "transaction": {
      "txid": "<64-lowercase-hex>",
      "state": "confirmed",
      "confirmations": 101,
      "block_hash": "<64-lowercase-hex>",
      "block_height": 919900,
      "block_time": 1784630000,
      "expiry_height": 920100,
      "serialized_size": 2048,
      "orchard_action_count": 2
    },
    "wallet_id": "hot",
    "wallet_effects": [
      {
        "event_id": 91,
        "kind": "SpendConfirmed",
        "observed_height": 920000,
        "observed_at": "2026-07-21T12:00:00Z",
        "wallet_id": "hot",
        "txid": "<64-lowercase-hex>",
        "state": "confirmed",
        "block_height": 919901,
        "confirmations": 100,
        "required_confirmations": 100,
        "confirmed_height": 920000,
        "amount_zat": 100000000,
        "source_note": {
          "txid": "<source-note-txid>",
          "action_index": 0,
          "block_height": 919000
        }
      }
    ]
  },
  "request_id": "req_..."
}
```

Fields unavailable from the node or scanner are omitted.

## Wallet effects

The array is ordered by scanner event ID. Common fields identify the event, wallet, transaction, observed scanner height/time, and lifecycle state. Kind-specific fields are:

| Kinds | Additional fields | Exchange use |
| --- | --- | --- |
| `Deposit*` | action, amount, allocated address, diversifier, block and lifecycle heights | Deposit reconciliation; the dedicated deposit poll remains the ledger feed |
| `Spend*` | amount and sanitized `source_note` identity | Track which wallet notes the withdrawal consumed without receiving nullifiers |
| `OutgoingOutput*` | action, amount, destination, memo when present, OVK/recipient scopes, expiry/lifecycle heights | Reconcile withdrawal outputs, change, mining, reorg, and expiry |

`Event` is initial observation, `Confirmed` is the configured threshold, `Unconfirmed` is a reorg below that threshold, `Orphaned` means the mined block left the active chain, and `OutgoingOutputExpired` means a previously observed mempool output expired after node height passed its expiry. `event_id` orders effects only within the current scanner history and can reset after scanner recovery. Reconcile durably by wallet, txid, kind, action or source-note identity, and observed lifecycle order. Do not treat the absence of an effect as proof that a transaction is safe to replace.

| State | Meaning | Exchange action |
| --- | --- | --- |
| `mempool` | Accepted but not mined | Keep inputs reserved |
| `confirmed` | Mined with one or more confirmations | Keep provisional until the configured threshold; default `100` |
| `orphaned` | Node reports a conflicted or stale-block tx, or terminal wallet fallback applies after node removal | Reverse finality-dependent actions. Before expiry, keep inputs reserved and follow the deliberate [rebroadcast procedure](./broadcast.md#retry-rules) if policy permits. After expiry, also wait until canonical height is at least `orphaned_at_height + configured_confirmations`; then reconcile every selected note and release only those that are unspent and non-pending |
| `expired` | Latest valid wallet event is expired, node no longer has the tx, and node height is greater than expiry | Reconcile effects and selected-note state. If it was previously mined/orphaned, first satisfy the same fork-finality wait; release only unspent, non-pending notes |

A later nonterminal wallet event cancels terminal scanner fallback. An `orphaned` state is not guaranteed to be renamed `expired`. Expiry alone is insufficient after prior mining because the stale block can return; require both the strict expiry boundary and finality of the replacement branch from `orphaned_at_height`. If the wallet effect does not supply that fork height, require manual chain evidence instead of releasing automatically. Then reconcile source-note state. A mined transaction can return to mempool or disappear during a reorg, so retain lifecycle history.

## Errors

```json
{
  "status": "error",
  "error": {
    "code": "event_history_reset",
    "message": "scanner event history changed during transaction lookup; retry the request",
    "retryable": true,
    "details": {"event_epoch": "<64-lowercase-hex>"}
  },
  "request_id": "req_..."
}
```

| Status | Codes | Action |
| --- | --- | --- |
| `400` | `invalid_request` | Fix txid or query parameter |
| `401` / `403` | `unauthorized`, `forbidden` | Fix credentials, scope, or wallet grant |
| `404` | `not_found` | Verify node sync/index; terminal fallback also needs an authorized `wallet_id` and `include_raw=false` |
| `409` | `event_history_reset` | Retry the whole lookup |
| `422` | `wallet_effects_limit_exceeded` | Investigate the event volume or raise the configured cap deliberately |
| `429` | `rate_limited` | Back off and retry |
| `500` | `internal` | Pause reconciliation, preserve reservations, and alert; retry only after gateway state is healthy |
| `502` | `node_rpc_error` | Retry after the node recovers |
| `503` | readiness errors | Keep financial decisions paused and retry after readiness |

A failed lookup never proves absence, expiry, or spendability. In particular, do not release selected note-ID reservations after `500`, `502`, or `503`.

Historical arbitrary lookup requires the node transaction index enabled by the appliance.

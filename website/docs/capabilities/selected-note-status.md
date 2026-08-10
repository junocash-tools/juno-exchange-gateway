---
title: Reconcile selected notes
---

Use selected-note status to reconcile the exact inputs recorded in a withdrawal or consolidation plan. It answers whether each source note is currently unknown, unspent, pending-spent, or mined-spent. It does not select notes, reserve them, or decide that releasing them is safe.

Required scope: `treasury`, with access to the wallet. Keep this route on the exchange's private integration network.

## Request

Copy `notes[].note_id` from the approved plan into the exchange withdrawal record before signing. Query those same IDs as one batch:

```bash
WALLET_ID=exchange-hot
curl -sS -X POST \
  -H "Authorization: Bearer $TREASURY_TOKEN" \
  -H "Accept: application/json" \
  -H "Content-Type: application/json" \
  "$GATEWAY_URL/v1/wallets/$WALLET_ID/notes/status" \
  -d '{
    "note_ids": [
      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:0",
      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:3"
    ]
  }'
```

The request must contain 1 to 200 unique canonical IDs. An ID is the creating Orchard outpoint `lowercase_txid:action_index`; decimal zero is exactly `0`, leading zeroes are invalid, and the action index must fit `uint32`. The 200-item limit matches the planner and signer input ceiling. Extra JSON fields are rejected.

Query one attempt's complete selected-note set at a time. Do not split it into smaller requests and then combine snapshots.

## Response

```json
{
  "status": "ok",
  "data": {
    "wallet_id": "exchange-hot",
    "event_epoch": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
    "as_of_node_height": 920109,
    "as_of_scanner_height": 920109,
    "as_of_scanner_hash": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
    "scanner_lag": 0,
    "statuses": [
      {
        "note_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:0",
        "state": "pending",
        "source_height": 919800,
        "value_zat": 500000000,
        "pending_spent_txid": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
        "pending_spent_at": "2026-07-21T12:00:00Z",
        "pending_spent_expiry_height": 920120
      },
      {
        "note_id": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:3",
        "state": "spent",
        "source_height": 919850,
        "value_zat": 250000000,
        "spent_txid": "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
        "spent_height": 920010,
        "spent_confirmed_height": 920109
      }
    ]
  },
  "request_id": "req_018f"
}
```

`statuses` has exactly the same length and order as `note_ids`. The gateway rejects a reordered, partial, duplicate, or inconsistent scanner result instead of returning partial data. No nullifier, address, memo, UFVK, or spending key is exposed.

Compare `event_epoch` with the epoch durably checkpointed by the exchange's deposit consumer. If it differs, scanner history was rotated before this request: stop automatic release, complete the documented cursor reset and idempotent replay, and then request a fresh status batch. The endpoint's in-call `409` protects only against a snapshot changing during this call.

| State | Fields | Exchange action |
| --- | --- | --- |
| `unknown` | `note_id` only | Stop. Never release. Verify the wallet, plan, birthday, scanner history, event epoch, and source ID. Restore or rescan if necessary. |
| `unspent` | source height and value | One note-level prerequisite is satisfied. Continue the complete release procedure below; this state alone is not authorization to release. |
| `pending` | source fields, pending txid/time, and expiry when known | Keep reserved. Link the txid to the attempt, look it up, and monitor mining or strict expiry. If expiry is absent, the scanner keeps the mark sticky: do not release from the exchange's recorded expiry alone; investigate and keep polling until the API reports another state. |
| `spent` | source fields, spending txid/height, and confirmation height when reached | Keep consumed. If the txid is expected, close against that attempt when policy permits. If it differs, attach the note to the actual transaction and investigate; never return it to inventory. `spent_confirmed_height` is scanner lifecycle evidence, not release authorization. |

For every item, require the returned source identity to match the requested plan ID. If the exchange recorded source values when reserving the plan, compare `value_zat` as well. The current `TxPlan v0` does not expose source-note values separately, so do not invent them from output or change amounts. Any mismatch against a value the exchange actually recorded is an incident, not a retry condition.

## Safe release procedure

This API deliberately has no `safe_to_release` field. It cannot know every signed copy, approval, HSM job, or exchange ledger reservation.

Before releasing selected notes:

1. Serialize the wallet lifecycle or prove the IDs are atomically disjoint from all other live reservations.
2. Inventory every plan and signed transaction that selected any of these IDs. Do not release while an unknown signed copy can still be accepted.
3. For a never-mined attempt, require canonical height to be strictly greater than its expiry and reconcile transaction lookup and wallet effects.
4. If an attempt was ever mined or orphaned, also require replacement-branch finality through `orphaned_at_height + configured_confirmations`, with no later remine. If that height is unavailable, require manual chain evidence.
5. Ensure the returned event epoch matches the exchange's fully reconciled deposit-consumer epoch. Then call this endpoint with the complete recorded ID set and require every item to be `unspent`; `unknown`, `pending`, and `spent` all fail closed.
6. In one exchange database transaction, record the returned event epoch, scanner height/hash, node height, request ID, and status batch, then transition only those still-owned reservations. Abort if another attempt changed any reservation.

The result is a point-in-time observation. A mempool spend can appear after the response, which is why the exchange's signed-attempt inventory, wallet serialization, and atomic reservation update remain mandatory.

## Errors and retries

| Result | Action |
| --- | --- |
| `400 invalid_request` | Fix the JSON, count, duplicate, or canonical-ID error. Do not drop an ID to make the request pass. |
| `403 forbidden` | Use a `treasury` credential granted to this wallet. |
| `404 not_found` | The configured wallet is unknown; verify bootstrap and do not release. |
| `409 scanner_snapshot_changed` | Discard the whole response and retry the complete batch with bounded backoff. |
| `413 invalid_request` | The body exceeds `JUNO_GATEWAY_MAX_JSON_BODY_BYTES`; with at most 200 canonical IDs this normally indicates malformed input or proxy expansion. |
| `429 rate_limited` | Honor `Retry-After`; keep reservations unchanged. |
| `500 internal` | Stop automatic release, preserve the request and ledger state, and repair gateway durable state. |
| retryable `502` or `503` | Keep every reservation and retry the complete batch. Persistent `502` indicates a scanner contract fault. |

Never replace a failed status call with the aggregate note summary. The summary intentionally contains no note IDs and is only for liquidity and consolidation monitoring.

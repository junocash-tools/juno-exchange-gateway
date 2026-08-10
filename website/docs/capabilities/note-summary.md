---
title: Monitor wallet note liquidity
---

Use the aggregate note summary to decide when a wallet is becoming fragmented. It exposes counts and values, not note IDs, nullifiers, memos, or addresses.

```bash
WALLET_ID=exchange-hot
curl -sS \
  -H "Authorization: Bearer $TREASURY_TOKEN" \
  "$GATEWAY_URL/v1/wallets/$WALLET_ID/notes/summary?min_confirmations=100&min_note_zat=100001"
```

Required scope: `treasury`, with access to `exchange-hot`.

`min_confirmations` defaults to `100` and may be `0` through `JUNO_GATEWAY_MAX_CONFIRMATIONS`. `min_note_zat` defaults to `0` and must be a non-negative integer. Use the same values as the planner policy when comparing this summary with a future plan.

```json
{
  "status": "ok",
  "data": {
    "wallet_id": "exchange-hot",
    "min_confirmations": 100,
    "min_note_zat": 100001,
    "as_of_node_height": 920000,
    "as_of_scanner_height": 920000,
    "as_of_scanner_hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "scanner_lag": 0,
    "total_unspent": {"note_count": 87, "value_zat": 42000000000},
    "spendable": {
      "note_count": 63,
      "value_zat": 39000000000,
      "smallest_note_zat": 150000,
      "largest_note_zat": 4000000000
    },
    "immature": {"note_count": 8, "value_zat": 1200000000},
    "pending_spend": {
      "note_count": 4,
      "value_zat": 1500000000,
      "known_expiry_count": 4,
      "next_expiry_height": 920035,
      "last_expiry_height": 920039
    },
    "below_min_note": {"note_count": 12, "value_zat": 300000000},
    "witness_unavailable": {"note_count": 0, "value_zat": 0}
  },
  "request_id": "req_018f"
}
```

The five operational buckets partition `total_unspent`. Each note enters the first matching bucket in this exact order: `pending_spend`, `immature`, `witness_unavailable`, `below_min_note`, then `spendable`. The buckets never overlap; for example, an immature note with a pending nullifier is counted only in `pending_spend`.

| Bucket | Meaning | Exchange action |
|---|---|---|
| `spendable` | Mature, non-pending, positioned notes at or above `min_note_zat` | Planner candidates; still subject to amount, fee, and the 200-input limit |
| `immature` | Non-pending and below `min_confirmations` | Wait; do not reserve or spend |
| `pending_spend` | Scanner saw the note's nullifier in a mempool transaction | Treat as unavailable until mined or cleared after expiry |
| `below_min_note` | Mature, non-pending, positioned note below the requested floor, plus zero-value notes | Include only after checking marginal fee economics |
| `witness_unavailable` | Mature, non-pending note has no spend position | Alert; the planner cannot use it |

`pending_spend` is not an offline plan reservation. A note becomes pending only after the node accepts a signed transaction and the scanner observes its nullifier. Mempool observation is not a reservation-release point. Without a durable reservation ledger, serialize the complete plan, approve, sign, broadcast, and terminal-reconciliation lifecycle per wallet. With one, atomically prove every new plan's selected note IDs are disjoint and keep each reservation until that attempt is final or meets the strict post-expiry release rule. Follow [note selection and reservations](../transactions/note-selection-and-reservations.md).

`smallest_note_zat` and `largest_note_zat` are omitted when `spendable.note_count` is zero. Pending expiry heights are omitted when `known_expiry_count` is zero; a pending note without a known expiry still appears in the pending count and value.

The scanner calculates every bucket and `as_of_scanner_hash` in one atomic database snapshot, including current mempool-pending state. The gateway requires that exact height and hash to match a stable scanner health and canonical node view around the aggregate. A concurrent chain snapshot change returns retryable `409 scanner_snapshot_changed`. `422 note_summary_limit_exceeded` means the inventory exceeds `JUNO_GATEWAY_NOTE_SUMMARY_MAX_NOTES`; the gateway never returns a truncated total. Raise the cap deliberately or consolidate in smaller batches.

Use this endpoint for alerts and [consolidation decisions](../transactions/consolidation.md). `juno-txbuild` remains authoritative for exact selection and fees. To reconcile the exact IDs selected by an existing plan, use [selected-note status](./selected-note-status.md); never infer one note's state from these aggregates.

For `409` or retryable `502`/`503`, discard the response and retry the complete request with bounded backoff. A persistent `502` means the scanner aggregate violates the contract and needs operator repair. A `403` means the credential lacks `treasury` scope or the wallet grant; `404` means the configured wallet ID is unknown. Never substitute a stale summary to authorize a withdrawal.

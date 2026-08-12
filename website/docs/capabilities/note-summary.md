---
title: Read wallet balance and liquidity
description: Read server-calculated totals across every address controlled by one registered wallet.
---

This is the wallet-wide balance endpoint. It calculates totals across every unspent note controlled by one registered wallet, regardless of which wallet-controlled address received each note. The exchange does not need to enumerate addresses or add their balances.

Different allocated addresses are expected to have different [per-address balances](address-balances.md). They receive separate notes, while the wallet's spending authority can spend eligible notes across all those addresses. This endpoint exposes only aggregate counts and values—not note IDs, nullifiers, memos, or recipient addresses.

## REST API

Use the same confirmation threshold and minimum-note floor as the private coordinator planner. With the shipped planner policy, those values are `100` and `0`:

```bash
WALLET_ID=exchange-hot
MIN_CONFIRMATIONS=100
MIN_NOTE_ZAT=0

curl --fail-with-body \
  -H "Authorization: Bearer $TREASURY_TOKEN" \
  "$GATEWAY_URL/v1/wallets/$WALLET_ID/notes/summary?min_confirmations=$MIN_CONFIRMATIONS&min_note_zat=$MIN_NOTE_ZAT"
```

Required scope: `treasury`, with access to `exchange-hot`.

For a planner-aligned view, `min_confirmations` must match `JUNO_GATEWAY_DEFAULT_CONFIRMATIONS`, which the coordinator passes to the planner, and `min_note_zat` must match `JUNO_COORDINATOR_MIN_NOTE_ZAT`. Their shipped defaults are `100` and `0`. Passing them explicitly makes the policy used for the snapshot auditable. If either configured planner value changes, update this call at the same time.

For diagnostics, `min_confirmations` may be `0` through `JUNO_GATEWAY_MAX_CONFIRMATIONS`, and `min_note_zat` may be any non-negative integer. When omitted, they use the gateway's configured confirmation default and a minimum-note floor of `0`.

Do not substitute the example consolidation floor of `100001` unless the coordinator is configured with that exact floor. A summary using different values does not describe the same candidate set that a new withdrawal plan will see.

## Node.js SDK

The supported SDK exposes the same atomic endpoint as `GatewayClient.getWalletBalance`:

```js
import {GatewayClient} from '@junocash-tools/exchange-sdk';

const gateway = new GatewayClient({
  baseUrl: process.env.JUNO_GATEWAY_URL,
  authToken: process.env.JUNO_TREASURY_TOKEN,
});

const balance = await gateway.getWalletBalance('exchange-hot', {
  minConfirmations: 100,
  minNoteZat: '0',
});

console.log(balance.totalUnspent.valueZat);
console.log(balance.spendable.valueZat);
```

The method returns the REST response's `data` object with camel-case names such as `walletId`, `totalUnspent`, `pendingSpend`, and `asOfScannerHeight`; it does not return the outer `status` and `request_id` envelope. All SDK zatoshi values—including `minNoteZat`, every `valueZat`, `smallestNoteZat`, and `largestNoteZat`—are canonical decimal strings. Counts, confirmations, heights, and lag are numbers. Pass the request's `minNoteZat` as a decimal string or `bigint`, never a JavaScript `number`. The SDK uses the gateway defaults when either option is omitted.

For an executable integration check, configure `JUNO_GATEWAY_URL`, a treasury-scoped `JUNO_GATEWAY_TOKEN`, `JUNO_WALLET_ID`, `JUNO_MIN_CONFIRMATIONS`, and `JUNO_MIN_NOTE_ZAT`, then run the packaged `examples/get-wallet-balance.mjs`.

## Response

```json
{
  "status": "ok",
  "data": {
    "wallet_id": "exchange-hot",
    "min_confirmations": 100,
    "min_note_zat": 0,
    "as_of_node_height": 920000,
    "as_of_scanner_height": 920000,
    "as_of_scanner_hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "scanner_lag": 0,
    "total_unspent": {"note_count": 87, "value_zat": 42000000000},
    "spendable": {
      "note_count": 75,
      "value_zat": 39300000000,
      "smallest_note_zat": 50000,
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
    "below_min_note": {"note_count": 0, "value_zat": 0},
    "witness_unavailable": {"note_count": 0, "value_zat": 0}
  },
  "request_id": "req_018f"
}
```

Amounts are integer zatoshis (`zat`) in REST responses. `total_unspent.value_zat` and `spendable.value_zat` answer different questions:

- `total_unspent.value_zat` is the complete unspent on-chain value controlled by the wallet. It includes immature, pending-spend, below-floor, and witness-unavailable notes. It is not the amount currently available for a new withdrawal.
- `spendable.value_zat` is the subset that meets this request's confirmation and minimum-note policy and is not scanner-pending or missing a spend position. It is a liquidity indicator and planner candidate total, not an authorization or promise that one transaction can spend that amount.

The five operational buckets partition `total_unspent` without overlap. Each note enters the first matching bucket in this exact order: `pending_spend`, `immature`, `witness_unavailable`, `below_min_note`, then `spendable`. For example, an immature note with a pending nullifier is counted only in `pending_spend`.

| Bucket | Meaning | Exchange action |
|---|---|---|
| `spendable` | Mature, non-pending, positioned notes at or above `min_note_zat` | Planner candidates; still subject to amount, fee, and the 200-input limit |
| `immature` | Non-pending and below `min_confirmations` | Wait; do not reserve or spend |
| `pending_spend` | Scanner saw the note's nullifier in a mempool transaction | Treat as unavailable until mined or cleared after expiry |
| `below_min_note` | Mature, non-pending, positioned note below the requested floor, plus zero-value notes | Include only after checking marginal fee economics |
| `witness_unavailable` | Mature, non-pending note has no spend position | Alert; the planner cannot use it |

Even with planner-aligned parameters, `spendable` does not subtract the coordinator's durable reservations for other attempts. It also does not calculate the fee for a requested payment or prove that enough value fits within the 200-input transaction limit. A fragmented wallet may therefore report enough aggregate spendable value while a particular transaction still returns `insufficient_balance` or `too_many_inputs`.

Use `getWalletBalance` for monitoring, alerts, and a non-authoritative preflight. `CoordinatorClient.createRawTransaction` remains authoritative: it excludes active reservations, selects the exact notes, calculates the fee, enforces transaction limits, and either creates the attempt or returns the exact planning failure. Never debit a customer, approve a withdrawal, or skip transaction creation solely from this summary.

`pending_spend` is not an offline plan reservation. A note becomes pending only after the node accepts a signed transaction and the scanner observes its nullifier. Mempool observation is not a reservation-release point. Without a durable reservation ledger, serialize the complete plan, approve, sign, broadcast, and terminal-reconciliation lifecycle per wallet. With one, atomically prove every new plan's selected note IDs are disjoint and keep each reservation until that attempt is final or meets the strict post-expiry release rule. Follow [note selection and reservations](../transactions/note-selection-and-reservations.md).

`smallest_note_zat` and `largest_note_zat` are omitted when `spendable.note_count` is zero. Pending expiry heights are omitted when `known_expiry_count` is zero; a pending note without a known expiry still appears in the pending count and value.

The scanner calculates every bucket and `as_of_scanner_hash` in one atomic database snapshot, including current mempool-pending state. The gateway requires that exact height and hash to match a stable scanner health and canonical node view around the aggregate. A concurrent chain snapshot change returns retryable `409 scanner_snapshot_changed`. `422 note_summary_limit_exceeded` means the inventory exceeds `JUNO_GATEWAY_NOTE_SUMMARY_MAX_NOTES`; the gateway never returns a truncated total. Raise the cap deliberately or consolidate in smaller batches.

Use note count and distribution for [consolidation decisions](../transactions/consolidation.md). To reconcile the exact IDs selected by an existing plan, use [selected-note status](./selected-note-status.md); never infer one note's state from these aggregates.

This is an on-chain wallet view, not the exchange's customer ledger or available-to-withdraw liability balance. The exchange ledger remains authoritative for customer credits, holds, withdrawals, and liabilities.

For `409` or retryable `502`/`503`, discard the response and retry the complete request with bounded backoff. A persistent `502` means the scanner aggregate violates the contract and needs operator repair. A `403` means the credential lacks `treasury` scope or the wallet grant; `404` means the configured wallet ID is unknown. Never substitute a stale summary to authorize a withdrawal.

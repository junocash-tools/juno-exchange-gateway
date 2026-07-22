---
title: Consolidation and sweeping
---

Consolidation reduces note count. It is an operator maintenance action, not a free fee optimization.

## Commands

| Command | Inputs | Output |
| --- | --- | --- |
| `sweep` | Every eligible note | One destination containing total input less fee |
| `consolidate` | Up to `--max-spends`, default `50` | One destination containing selected input less fee |
| `rebalance` | Selected notes sufficient for explicit outputs | Several operator-controlled destinations plus optional change |

Example:

First allocate and persist a same-UFVK operator address with an internal purpose such as `treasury:consolidation-1`; never reuse a customer deposit address. Then use that address for both `--to` and `--change-address`:

```bash
umask 077
install -d -m 0700 tmp
docker compose --profile operator run --rm txbuild consolidate \
  --wallet-id hot \
  --coin-type 8133 \
  --account 0 \
  --to '<registered-treasury-address>' \
  --change-address '<registered-treasury-address>' \
  --max-spends 50 \
  --fee-multiplier 20 \
  --minconf 100 \
  > tmp/consolidation.txplan.json
```

`consolidate` tries the greatest feasible input count up to `--max-spends`. It prefers smaller notes and substitutes larger notes only when needed to cover the fee. At least two eligible notes are required. The planner and signer both limit a transaction to 200 inputs and 200 total outputs, including change.

## Economics

With the bundled node policy, a transaction fee is:

```text
100,000 × max(2, spend count, output count) zat
```

A ten-input, one-output consolidation therefore costs `1,000,000` zat. It also leaves one new note that costs an input action when later spent. Consolidating the same notes immediately before one future spend usually increases total fees; it moves cost earlier and can make later withdrawals smaller and more predictable.

Consolidate when note count threatens input limits, signing time, proof time, or operational latency. Do not run it only because many notes exist. Before approval compare:

- consolidation fee now
- expected future input-action fees without consolidation
- remaining value after fee
- expiry window and maintenance urgency
- current reserved and pending notes

Use `--min-note-zat` carefully. It excludes small notes rather than cleaning them up. Under the pinned fee policy, every input above the two-action floor adds `100,000` zat. A starting floor of `100001` avoids selecting notes worth no more than their marginal fee, but the exchange must decide whether to strand smaller notes or recover them in a deliberately approved batch.

`sweep` has no `--max-spends`; it fails with `too_many_inputs` when the eligible set exceeds 200. Count notes first and run bounded consolidations when needed.

## When to run it

Do not broadcast consolidation on a blind cron. Evaluate the [note summary](../capabilities/note-summary.md) hourly and after large deposit batches, then apply a documented policy. A conservative starting point is:

| Spendable notes | Action |
| --- | --- |
| Below `50` | No maintenance |
| `50` to `74` | Warning; check withdrawal demand and fees |
| `75` to `149` | Schedule off-peak consolidation |
| `150` or more | Critical; pause large new plans and consolidate before the 200-input ceiling |

Aim for at most `30` spendable notes after maintenance. These are operational defaults, not protocol rules; tune them from withdrawal size, note distribution, proof latency, and fee budget.

Run only one plan lifecycle per wallet. After each batch, wait until the scanner marks its inputs pending-spent; waiting for one confirmation before the next batch is safer. Refresh the summary and repeat only while the threshold and fee policy still justify it. Do not immediately consolidate each customer deposit: batching at an off-peak, jittered time reduces cost and timing correlation.

## Privacy and operations

A consolidation transaction shows that many nullifiers were authorized together and creates a conspicuous timing pattern, even though Orchard values and recipient details remain shielded from public observers. Large regular sweeps can also concentrate wallet activity into fewer notes.

- send the output only to an operator-controlled address under the same spending UFVK
- use a dedicated registered `purpose=treasury` or `purpose=internal_change` address, never a customer deposit address; see [address allocation](../capabilities/address-allocation.md#change-and-treasury-addresses)
- do not credit the output as an external deposit
- avoid a predictable public schedule when operations permit
- reserve every selected nullifier through the full sign and broadcast lifecycle
- sign with the same offline or external spend-authority flow as withdrawals
- monitor mining, finality, expiry, and reorgs by txid

Run representative consolidation and sweep plans on regtest after changing node, planner, signer, fee, or input-count policy.

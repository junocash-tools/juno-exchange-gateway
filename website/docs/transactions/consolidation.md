---
title: Consolidation and sweeping
---

Consolidation reduces note count. It is an operator maintenance action, not a free fee optimization.

## Automation boundary

The private coordinator API currently creates explicit-output withdrawal transactions. It does not expose `sweep`, `consolidate`, or `rebalance`, because maintenance needs operator-controlled treasury destinations and a separately approved economic/privacy policy.

Run the commands below only as an operator fallback. Pause new coordinator creation for the source wallet and wait until every coordinator attempt is `final`, `released`, `failed_unsigned`, or `cancelled`. Do not run a CLI maintenance plan beside active coordinator reservations, and do not disguise a sweep as a customer withdrawal request.

## Operator commands

| Command | Inputs | Output |
| --- | --- | --- |
| `sweep` | Every eligible note | One destination containing total input less fee |
| `consolidate` | Up to `--max-spends`, default `50` | One destination containing selected input less fee |
| `rebalance` | Selected notes sufficient for explicit outputs | Several operator-controlled destinations plus optional change |

Example:

First allocate and persist a same-UFVK operator address with an internal purpose such as `treasury:consolidation-1`; never reuse a customer deposit address. Then use that address for both `--to` and `--change-address`:

Replace each example attempt ID with a new durable exchange attempt ID. Every command below fails if its attempt directory or output already exists. If that happens, stop and reconcile the existing attempt; never overwrite it.

```bash
set -euo pipefail
set -o noclobber
umask 077
install -d -m 0700 "$PWD/tmp"
ATTEMPT_DIR="$PWD/tmp/consolidation-1842-attempt-1"
mkdir -m 0700 "$ATTEMPT_DIR"

docker compose --profile operator run --rm txbuild consolidate \
  --wallet-id exchange-hot \
  --coin-type 8133 \
  --account 0 \
  --to '<registered-treasury-address>' \
  --change-address '<registered-treasury-address>' \
  --max-spends 50 \
  --fee-multiplier 20 \
  --minconf 100 \
  > "$ATTEMPT_DIR/txplan.json"
```

`consolidate` tries the greatest feasible input count up to `--max-spends`. It prefers smaller notes and substitutes larger notes only when needed to cover the fee. At least two eligible notes are required. The planner and signer both limit a transaction to 200 inputs and 200 total outputs, including change.

Sweep every eligible note when the input count is at most 200:

```bash
set -euo pipefail
set -o noclobber
umask 077
install -d -m 0700 "$PWD/tmp"
ATTEMPT_DIR="$PWD/tmp/sweep-1843-attempt-1"
mkdir -m 0700 "$ATTEMPT_DIR"

docker compose --profile operator run --rm -T txbuild sweep \
  --wallet-id exchange-hot \
  --coin-type 8133 \
  --account 0 \
  --to '<registered-treasury-address>' \
  --fee-multiplier 20 \
  --minconf 100 \
  > "$ATTEMPT_DIR/txplan.json"
```

For a multi-tier rebalance, create the output file once inside a fresh attempt directory with only operator-controlled destinations:

```bash
set -euo pipefail
set -o noclobber
umask 077
install -d -m 0700 "$PWD/tmp"
ATTEMPT_DIR="$PWD/tmp/rebalance-1844-attempt-1"
mkdir -m 0700 "$ATTEMPT_DIR"

jq -n '
  [
    {"to_address":"<registered-hot-address>","amount_zat":"200000000"},
    {"to_address":"<registered-warm-address>","amount_zat":"800000000"}
  ]
' > "$ATTEMPT_DIR/outputs.json"

docker compose --profile operator run --rm -T \
  -v "$ATTEMPT_DIR/outputs.json:/work/outputs.json:ro" \
  txbuild rebalance \
  --wallet-id exchange-hot \
  --coin-type 8133 \
  --account 0 \
  --outputs-file /work/outputs.json \
  --change-address '<registered-hot-change-address>' \
  --fee-multiplier 20 \
  --minconf 100 \
  > "$ATTEMPT_DIR/txplan.json"
```

The signer proves ownership only for a nonzero change output. It validates the network but does not prove that explicit `--to` or `--outputs-file` destinations belong to the exchange. Before approval, require every explicit sweep, consolidation, or rebalance destination to match the exchange treasury registry exactly. A mistyped external destination is otherwise a valid irreversible payment.

For `consolidate` and `sweep`, use a gateway-allocated destination under the same UFVK that owns the inputs. A `rebalance` may send to a different hot, warm, or cold UFVK only when that wallet was registered in this installation before `init` and its exact destination was allocated and recorded as an operator tier address. Keep any change under the source UFVK. Record the source debit and each target credit as one internal transfer keyed by txid and the signer-provided action mapping; never route them through customer deposit credit.

The scanner classifies transaction origin once across all wallets registered in the installation. For a registered source-to-registered-target rebalance, it stores the target note as internal and emits no external deposit lifecycle. This suppression depends on both wallet registrations, not on the address label. If a target is outside the installation, the gateway cannot monitor or reconcile its receipt; use a separate documented custody flow instead of this rebalance procedure.

After any `consolidate`, `sweep`, or `rebalance` plan, use the operator-fallback controls, signed-raw broadcast API, and txid reconciliation described in [build, sign, and broadcast](./build-sign-broadcast.md). Keep automated creation paused until the maintenance attempt is final or passes the strict post-expiry release proof.

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

For ordinary withdrawals, the coordinator can safely run disjoint attempts because it atomically reserves selected note IDs. The operator CLI cannot join that reservation transaction, so maintenance requires the exclusive wallet window above. Refresh the summary before each batch and repeat only while the threshold and fee policy still justify it. Do not immediately consolidate each customer deposit: batching at an off-peak, jittered time reduces cost and timing correlation.

## Privacy and operations

A consolidation transaction shows that many nullifiers were authorized together and creates a conspicuous timing pattern, even though Orchard values and recipient details remain shielded from public observers. Large regular sweeps can also concentrate wallet activity into fewer notes.

- for `consolidate` and `sweep`, send the output only to an operator-controlled address under the same spending UFVK; for `rebalance`, use only the pre-approved registered tier addresses described above and keep change under the source UFVK
- use a dedicated registered `purpose=treasury` or `purpose=internal_change` address, never a customer deposit address; see [address allocation](../capabilities/address-allocation.md#change-and-treasury-addresses)
- do not credit the output as an external deposit
- avoid a predictable public schedule when operations permit
- reserve every selected `notes[].note_id` through the full sign and broadcast lifecycle
- sign with the same offline or external spend-authority flow as withdrawals
- monitor mining, finality, expiry, and reorgs by txid

Run representative consolidation and sweep plans on regtest after changing node, planner, signer, fee, or input-count policy.

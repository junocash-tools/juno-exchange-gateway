---
title: Note selection and reservations
---

Orchard notes are indivisible inputs. Safe withdrawal automation must select enough value, prevent concurrent reuse, and keep every signed attempt locked until its outcome is final.

## Eligible notes

With scanner-backed planning, `juno-txbuild` considers incoming notes that are:

- unspent and not marked as a pending spend
- at or above `--minconf`
- at or above `--min-note-zat`, when set
- returned for the requested wallet; the plan records the requested account

The planner defaults `--minconf` to `100`, matching the appliance default finality policy. Scanner-backed planning avoids rebuilding the full Orchard commitment tree for every command.

## Deterministic selection

For `send`, `send-many`, and `rebalance`, selection is deterministic:

1. one exact note with no change
2. the smallest one-note fit with change
3. an exact two-note pair with no change
4. the smallest two-note fit with change
5. largest notes first until outputs plus the recomputed fee are covered

Ties use transaction ID and action index. Fee calculation is repeated as the spend and output counts change. Small change can be absorbed into the fee with `--min-change-zat`.

`sweep` selects every eligible note. `consolidate` uses the bounded policy described in [consolidation and sweeping](./consolidation.md).

## Current reservation gap

Planning is read-only. Neither the gateway nor scanner claims selected notes when a plan is created. The scanner excludes a note only after it observes a transaction spending that note in the node mempool.

Therefore two plans created before mempool observation can select the same nullifiers. Serializing only the planner command is not enough; serialization must cover the complete plan, approve, sign, and broadcast lifecycle, with at most one outstanding plan per wallet.

For concurrent withdrawals, reserve every selected nullifier atomically in the exchange withdrawal ledger before approval. A reservation should record at least:

- wallet ID, withdrawal ID, attempt ID, and idempotent plan ID
- note identity and nullifier
- plan digest, outputs, fee, and expiry height
- signed txid and broadcast idempotency key when known
- state and timestamps

Reject a plan if any selected nullifier is already reserved. Rebuild the plan from a fresh ready snapshot instead of silently substituting notes after approval.

## Pending and release rules

The scanner marks a known note pending after observing its nullifier in the mempool. If that transaction disappears:

- with a known expiry, pending remains set while `chain_height <= expiry_height`
- pending clears only when `chain_height > expiry_height`
- with no known expiry, pending clears when the transaction is absent
- when mined, pending is replaced by the mined-spent state

At the exact expiry height the note is still locked. Do not release it until the next block has made the strict `>` condition true.

Apply the same conservative rule to the exchange reservation. An unsigned canceled plan can be released under a controlled approval policy. Once raw signed bytes may exist, keep the reservation until the transaction is final, or until canonical node height is greater than its expiry and lookup confirms it was not mined.

## Retry, expiry, and reorg lifecycle

| State | Required action |
| --- | --- |
| Planned/reserved | Verify policy and plan digest; do not create a competing plan |
| Signed | Protect raw bytes and keep notes reserved |
| Broadcast uncertain | Retry the same wallet ID, raw bytes, expected txid, principal, and idempotency key |
| Mempool | Keep reserved; monitor txid and expiry height |
| Mined below finality | Keep reserved and apply the exchange's provisional policy |
| Final at the configured threshold | Close the attempt; scanner retains the spent state |
| Unconfirmed after reorg | Reverse finality-dependent actions and keep reserved |
| Orphaned | Keep reserved; after reconciling the completed original broadcast, the identical signed bytes may be submitted under a fresh rebroadcast-operation key only while canonical height is strictly below expiry |
| Expired at `chain_height > expiry_height` | Reconcile node and wallet effects, then release and create a new attempt if needed |

The scanner emits spend and outgoing-output events for mining, confirmation-threshold, unconfirmation, and orphan transitions. It also emits outgoing-output expiry after observing it. A reorg can move a mined transaction back to mempool or remove it; never treat one confirmation as the appliance's 100-confirmation finality.

## Criteria for a private planner service

An HTTP planner must remain internal and should not be a stateless wrapper around the current CLI. Before deployment it needs:

- atomic selection and reservation in one durable transaction
- wallet-scoped service authentication and rate limits
- idempotent plan creation and status lookup
- registered, server-selected change and network validation
- amount, output-count, fee, expiry, memo, and input-count policy limits
- exact node/scanner network, tip, anchor, and history readiness checks
- approval-bound plan hashes and broadcast association
- restart, backup, expiry, and reorg reconciliation

Until those controls exist, use the private CLI with one full lifecycle at a time or an external ledger that supplies equivalent reservations.

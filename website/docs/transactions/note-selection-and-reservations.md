---
title: Note selection and reservations
---

Orchard notes are indivisible inputs. The private coordinator selects enough value, atomically reserves the selected note IDs, and keeps them locked until the outcome is safe.

## Eligible notes

The planner considers scanner notes that are:

- owned by the requested registered `wallet_id`
- unspent and not observed as a pending spend
- at or above the confirmation policy, default `100`
- at or above `JUNO_COORDINATOR_MIN_NOTE_ZAT`, when nonzero
- backed by a usable Orchard witness
- not already in the coordinator's durable reservation table

The exchange supplies no input list and no source address. It supplies `wallet_id`, approved outputs, and an approval reference. The coordinator allocates a registered change address under that wallet's UFVK.

## Selection order

For withdrawals, the planner chooses deterministically:

1. one exact note with no change
2. the smallest one-note fit with change
3. an exact two-note pair with no change
4. the smallest two-note fit with change
5. largest notes first until outputs plus the recomputed fee are covered

Ties use source transaction ID and action index. Fee calculation is repeated when input/output counts change. Change below `JUNO_COORDINATOR_MIN_CHANGE_ZAT` is added to the fee instead of creating a note.

Each selected input has a canonical reservation ID:

```text
<64-lowercase-hex-source-txid>:<base-10-action-index>
```

The action index has no leading zero unless it is exactly `0`. `action_nullifier` is not a reservation key.

## Atomic reservation

Planning and reservation happen before signing:

1. the coordinator reads every active reservation for the wallet
2. it runs `juno-txbuild` with those IDs excluded
3. in one database transaction, it stores the exact plan/digest and inserts every selected ID under a uniqueness constraint
4. if another attempt won a note, it discards that plan and replans with a fresh exclusion set, up to `JUNO_COORDINATOR_MAX_REPLANS`
5. only a fully reserved plan can enter signing

The coordinator also serializes planning per wallet in one process. The database uniqueness constraint is the durable safety control across crashes. Never run two active coordinator instances over independent copies of the same state database.

The scanner's `pending` state is additional chain evidence, not the reservation mechanism. A coordinator reservation exists before signed bytes reach the mempool; scanner pending begins only after the node observes a nullifier.

## Reservation lifecycle

| Attempt state | Reservation rule |
| --- | --- |
| `planning` | No selected-note reservation yet; a change address may already be allocated. |
| `reserved` | Exact plan and all selected IDs are locked. Cancellation can still prove unsigned and release them. |
| `signing` / `signing_unknown` | Locked. Cancellation and replacement are forbidden. |
| `signed` / `broadcast` / `mined` | Locked while the bytes can mine or a mined block can reorg. |
| `final` | Transaction reached the configured finality threshold; active reservations are removed. |
| `expired_pending_reconciliation` / `orphaned` | Locked until complete post-expiry proof. |
| `released` | Every selected note was proven unspent at the required stable tip; replacement is allowed. |
| `failed_unsigned` / `cancelled` | Signing provably did not begin; active reservations are removed. |

`signing_unknown` is deliberately not a failure. The isolated signer keeps a durable journal keyed by attempt ID and plan digest. The coordinator repeats only that exact request. A completed journal entry replays the original result; an unresolved pending entry stays locked and requires operator recovery. Background replay is limited to once per minute per unknown attempt; an idempotent client replay may prompt the same journal lookup sooner. The coordinator never replans or releases notes merely because a signer call timed out or a later replay is busy or rejected.

## Expiry and pending spends

The default transaction expiry is the planning tip plus one block plus `JUNO_COORDINATOR_EXPIRY_OFFSET=40`. Always use the returned `expiry_height`; it is fixed in the signed transaction, and blocks may advance before broadcast.

At `chain_height == expiry_height`, the transaction is still valid and every note stays locked. The scanner clears a never-mined known-expiry pending marker only after `chain_height > expiry_height`.

After a healthy canonical node tip passes `expiry_height`, an absent, mempool-only, or orphaned attempt becomes `expired_pending_reconciliation`. The SDK no longer returns its raw bytes as broadcastable. This state change does not release notes.

The coordinator is stricter. Automatic release requires all of the following:

- canonical node height is at least `expiry_height + configured_confirmations` (default another `100` blocks)
- node and scanner report the same exact ready height/hash
- scanner history and pending-spend reconciliation are complete
- the complete recorded note-ID set is returned, with every item exactly `unspent`

Any `pending`, `spent`, `unknown`, missing, duplicate, or mismatched item keeps the whole attempt locked. This extra finality window protects against a previously mined stale block returning in a reorg. Do not override it with a timer or manual database deletion.

## Failure and replacement

- `failed_unsigned` means the coordinator proved signing did not start and released the reservation. Correct the cause, obtain a new approval, and create a new idempotency key.
- `cancelled` is allowed only from `planning` or `reserved`. Treat `409 attempt_not_cancellable` as evidence that signing may have started.
- a client timeout changes nothing. Retry creation with the same key/body or poll the saved attempt ID.
- an uncertain broadcast keeps the same signed bytes, expected txid, credential principal, and broadcast key.
- there is no RBF or CPFP path. Never sign a competing fee bump with the same notes.
- before expiry, a reconciled dropped/orphaned transaction may only be rebroadcast as the identical bytes under the public broadcast rules.
- after expiry, create a replacement only after coordinator state is `released`.

The exchange should still persist the attempt ID, exact outputs, plan digest, selected note IDs, txid, raw-transaction digest, expiry, and both creation/broadcast idempotency keys. The coordinator database protects note reuse; the exchange ledger remains the business source of withdrawal ownership and approval.

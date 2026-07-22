---
title: Build, sign, and broadcast
---

`juno-txbuild` plans online, `juno-txsign` signs in an isolated environment, and the public gateway accepts only the signed raw transaction.

## Choose a planning command

| Command | Result |
| --- | --- |
| `send` | One withdrawal output plus optional change |
| `send-many` | Several withdrawal outputs from a JSON file plus optional change |
| `sweep` | Every eligible note sent to one address, less the fee |
| `consolidate` | Up to `--max-spends` eligible notes combined into one output, less the fee |
| `rebalance` | Several operator-controlled outputs from a JSON file plus optional change |

All commands produce the same versioned `TxPlan` JSON consumed by the signer. `consolidate` is represented as a rebalance plan.

## 1. Build online

The Compose operator profile can reach the private node and scanner. It has no published port and is not a public API:

```bash
set -euo pipefail
umask 077
set -o noclobber
install -d -m 0700 "$PWD/tmp"
ATTEMPT_DIR="$PWD/tmp/withdrawal-1842"
mkdir -m 0700 "$ATTEMPT_DIR"

docker compose --profile operator run --rm -T txbuild send \
  --wallet-id hot \
  --coin-type 8133 \
  --account 0 \
  --to 'j1...' \
  --amount-zat 250000 \
  --change-address 'j1...' \
  --fee-multiplier 20 \
  --minconf 100 \
  > "$ATTEMPT_DIR/txplan.json"
```

The attempt directory and every file name are one-shot. `mkdir` and `noclobber` must fail when the attempt or a redirected artifact already exists; choose a new attempt ID instead of overwriting evidence.

Use coin type `8133` on mainnet, `8134` on testnet, and `8135` on regtest. `0` asks the planner to infer it from the node.

For `send-many` and `rebalance`, pass a JSON array with `--outputs-file`. This standalone `send-many` alternative creates both its input and plan once inside a fresh attempt directory.

One transaction may contain at most 200 total Orchard outputs. Change counts toward that ceiling, so a batch that produces change may contain at most 199 explicit destinations; 200 explicit destinations are valid only when the selected inputs and fee produce no change.

```bash
set -euo pipefail
set -o noclobber
umask 077
install -d -m 0700 "$PWD/tmp"
ATTEMPT_DIR="$PWD/tmp/withdrawal-batch-1843-attempt-1"
mkdir -m 0700 "$ATTEMPT_DIR"

jq -n '
  [
    {"to_address":"j1...","amount_zat":"250000"},
    {"to_address":"j1...","amount_zat":"400000","memo_hex":"6869"}
  ]
' > "$ATTEMPT_DIR/outputs.json"

docker compose --profile operator run --rm -T \
  -v "$ATTEMPT_DIR/outputs.json:/work/outputs.json:ro" \
  txbuild send-many \
  --wallet-id hot --coin-type 8133 --account 0 \
  --outputs-file /work/outputs.json --change-address 'j1...' \
  --fee-multiplier 20 --minconf 100 \
  > "$ATTEMPT_DIR/txplan.json"
```

Use `rebalance` instead of `send-many` when the destinations are operator-controlled wallet tiers. See [consolidation and sweeping](./consolidation.md) for the one-output maintenance commands.

Successful planning writes one `TxPlan v0` object. This is the approval and signer input, not a signed transaction:

```jsonc
{
  "version": "v0",
  "kind": "withdrawal",
  "wallet_id": "hot",
  "coin_type": 8133,
  "account": 0,
  "chain": "main",
  "branch_id": 3370586197,
  "anchor_height": 920000,
  "anchor": "<64-hex>",
  "expiry_height": 920041,
  "outputs": [
    {"to_address":"j1...","amount_zat":"250000"}
  ],
  "change_address": "j1...",
  "fee_zat": "200000",
  "notes": [
    {
      "note_id": "<source-txid>:<source-action-index>",
      "action_nullifier": "<64-hex>",
      "cmx": "<64-hex>",
      "position": 18273,
      "path": [/* 32 Merkle siblings */],
      "ephemeral_key": "<64-hex>",
      "enc_ciphertext": "<hex>"
    }
  ]
}
```

The bundled planner always identifies each selected input as `note_id = txid:action_index`. Use `note_id` as the exchange reservation key. Before approval, require every selected note to have a unique canonical ID matching `<64-lowercase-hex-txid>:<base-10-action-index>` with no leading zero in a multi-digit action index. Reject the plan if an ID is missing, malformed, or repeated. The signer independently enforces that structural rule, but it cannot see or mutate the exchange reservation ledger; the exchange must still reserve the IDs atomically before approval. `action_nullifier` is decryption context from the Orchard action that created the note; it is not the nullifier produced when this transaction later spends that note and must not be used as the reservation identity.

Treat the complete plan as sensitive operational data. Hash the exact file bytes, bind the digest to the withdrawal attempt and approval, then verify the same digest after transfer:

```bash
set -euo pipefail
set -o noclobber
umask 077
ATTEMPT_DIR="$PWD/tmp/withdrawal-1842"
test -s "$ATTEMPT_DIR/txplan.json"

sha256sum "$ATTEMPT_DIR/txplan.json" | awk '{print $1}' > "$ATTEMPT_DIR/txplan.sha256"
test "$(sha256sum "$ATTEMPT_DIR/txplan.json" | awk '{print $1}')" = "$(tr -d '\r\n' < "$ATTEMPT_DIR/txplan.sha256")"
```

Do not parse and reserialize the plan between approval and signing. A non-zero planner exit returns a versioned JSON error when `--json` is used; common codes are `invalid_request`, `insufficient_balance`, `too_many_inputs`, `no_liquidity_in_hot`, and `not_found`.

### Required policy checks

- `--minconf` defaults to `100`, matching the appliance default financial policy. Use a lower value only under a documented spend policy.
- `--fee-multiplier` defaults to `20`. With the planner's 5,000-zat base, this matches the bundled `junocashd` 0.9.12 policy of `100,000 × max(2, logical actions)` zat on every network. Recheck the default when either component changes.
- Outputs include change when calculating logical actions. `--fee-add-zat` adds to the multiplied fee. `--min-change-zat` adds smaller change to the fee instead of creating a note.
- The default expiry is `tip + 1 + 40`; the minimum offset is `4`. A pending spend clears for expiry only after node height is strictly greater than `expiry_height`.
- The pinned transaction stack has no RBF or CPFP fee-bump path for these Orchard spends. Choose the fee before approval and signing. For a stuck accepted transaction, keep its notes reserved and either rebroadcast the identical bytes under the rules below or wait for strict expiry and reconciliation before building a new plan. Never sign a competing fee-bump while earlier raw bytes remain valid.
- Use a dedicated gateway-registered change address under the spending UFVK. An `internal_change:*` label records exchange purpose; the allocated address itself uses Orchard external scope, and the pinned scanner therefore reports `recipient_scope: "external"` and `ovk_scope: "external"`. Same-wallet transaction-origin suppression keeps this change out of the external deposit feed. The planner accepts the address supplied by the exchange; the signer independently rejects a change address outside the signing seed or external-signing UFVK and rejects address/network mismatches.
- Inspect destination, amount, memo, change, selected notes, fee, anchor, branch ID, network, and expiry before approval.

Planning does not reserve notes. Follow [note selection and reservations](./note-selection-and-reservations.md) before creating more than one outstanding plan.

Direct signing and external spend-authority signing are alternatives. Bind the chosen mode to the approved attempt and use only that mode. Once signed bytes or external signing requests may have escaped, do not switch modes or run either command again for the same plan.

## 2. Sign directly offline

On the isolated signer host, put the verified plan at `tmp/withdrawal-1842/txplan.json` and the protected seed at `hot.seed`. Pin the signer image by the digest recorded in the release manifest, disable networking, and mount both inputs read-only:

Run every offline command from a dedicated non-root signer OS account that owns the owner-only inputs and output directories. The `--user` override maps the container to that account; never invoke this flow from UID `0`.

```bash
set -euo pipefail
set -o noclobber
umask 077
TXSIGN_IMAGE=ghcr.io/junocash-tools/juno-exchange-gateway-txsign@sha256:<verified-digest>
ATTEMPT_DIR="$PWD/tmp/withdrawal-1842"
SIGN_DIR="$ATTEMPT_DIR/direct"
test "$(id -u)" -ne 0
mkdir -m 0700 "$SIGN_DIR"
chmod 0600 "$ATTEMPT_DIR/txplan.json" "$PWD/hot.seed"

docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges \
  --user "$(id -u):$(id -g)" \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  -v "$ATTEMPT_DIR/txplan.json:/work/txplan.json:ro" \
  -v "$PWD/hot.seed:/run/secrets/juno-seed:ro" \
  -v "$SIGN_DIR:/work/output:rw" \
  "$TXSIGN_IMAGE" sign \
  --txplan /work/txplan.json \
  --seed-file /run/secrets/juno-seed \
  --out /work/output/rawtx.hex \
  --out-result /work/output/signed.json \
  --action-indices \
  --json > /dev/null

jq -e '
  .version == "v1" and .status == "ok" and
  (.data.txid | test("^[0-9a-f]{64}$")) and
  (.data.raw_tx_hex | test("^[0-9a-f]+$")) and
  (.data.orchard_output_action_indices | type == "array")
' "$SIGN_DIR/signed.json" > /dev/null
test "$(tr -d '\r\n' < "$SIGN_DIR/rawtx.hex")" = \
  "$(jq -er '.data.raw_tx_hex' "$SIGN_DIR/signed.json")"
```

Success is machine-readable:

```json
{
  "version": "v1",
  "status": "ok",
  "data": {
    "txid": "<64-lowercase-hex>",
    "raw_tx_hex": "<lowercase-hex>",
    "fee_zat": "200000",
    "orchard_output_action_indices": [0],
    "orchard_change_action_index": 1
  }
}
```

A validation or signing failure returns a non-zero exit status:

```json
{
  "version": "v1",
  "status": "err",
  "error": {"code": "sign_failed", "message": "<reason>"}
}
```

`orchard_output_action_indices` is aligned to `txplan.outputs` order. `orchard_change_action_index` is the actual change action or `null`. Persist this mapping so the exchange can reconcile recipient outputs without assuming action order. Keep the seed, complete signer output, and intermediate artifacts offline. Export only the approved `raw_tx_hex`, txid, and required reconciliation metadata.

## External spend-authority flow

`ext-prepare` builds a proven transaction from the plan and UFVK without a spending key. An external signer or TSS system signs each returned Orchard spend request, then `ext-finalize` assembles the raw transaction.

Orchard PCZT proving requires NU6.2. The bundled 0.9.12 node therefore activates branch `5437f330` at regtest height 1; verify `getblockchaininfo.consensus.chaintip` before a local signing test that uses another regtest node. This override is for regtest only—never copy it to testnet or mainnet.

On mainnet or testnet before NU6.2 activates, `ext-prepare` fails closed with `prepare_failed` and reason `external_signing_branch_unsupported`. Do not change public-network activation parameters. Use the direct offline `sign` flow until the network reaches NU6.2, or keep external signing disabled.

```bash
set -euo pipefail
set -o noclobber
umask 077
TXSIGN_IMAGE=ghcr.io/junocash-tools/juno-exchange-gateway-txsign@sha256:<verified-digest>
ATTEMPT_DIR="$PWD/tmp/withdrawal-1842"
EXT_DIR="$ATTEMPT_DIR/external"
test "$(id -u)" -ne 0
mkdir -m 0700 "$EXT_DIR"
chmod 0600 "$ATTEMPT_DIR/txplan.json" "$PWD/wallet.ufvk"

docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges \
  --user "$(id -u):$(id -g)" \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  -v "$ATTEMPT_DIR/txplan.json:/work/txplan.json:ro" \
  -v "$PWD/wallet.ufvk:/run/secrets/wallet.ufvk:ro" \
  -v "$EXT_DIR:/work/output:rw" \
  "$TXSIGN_IMAGE" ext-prepare \
  --txplan /work/txplan.json \
  --ufvk-file /run/secrets/wallet.ufvk \
  --out-prepared /work/output/prepared.json \
  --out-requests /work/output/requests.json \
  --out-result /work/output/prepare-result.json \
  > /dev/null

jq -e '.version == "v0" and (.requests | type == "array" and length > 0)' \
  "$EXT_DIR/requests.json" > /dev/null
jq -e '.version == "v1" and .status == "ok"' \
  "$EXT_DIR/prepare-result.json" > /dev/null
sha256sum "$EXT_DIR/prepared.json" "$EXT_DIR/requests.json"
```

Use the same digest-pinned signer image recorded in the release provenance. `ext-prepare` needs the UFVK but no spending key and needs no network; the command above gives it read-only inputs and one owner-only output directory. Transfer only the exact `requests.json` bytes and an authenticated approval record to the external signer. Keep `prepared.json` with the coordinator and never send the UFVK to a signer that does not require it.

`ext-prepare` always returns JSON. Treat `prepared.json` as opaque and retain its exact bytes through finalization. In one protected approval record, bind the withdrawal attempt ID, exact plan digest, exact prepared-file digest, exact requests-file digest, network, coin type, account, and required action-index set. The external signer must receive that record through an independently authenticated approval channel, such as a signed approval manifest delivered by the approval system or an authenticated read from that system. Do not trust a sidecar supplied only by the online coordinator. The prepared artifact also contains three coordination mappings:

- `orchard_output_action_indices`, aligned to `txplan.outputs`
- `orchard_change_action_index`, or `null`
- `orchard_required_spend_action_indices`, the exact actions that need signatures

These semantic output/change roles are not authenticated by Orchard spend-authority signatures. Trust them only from the exact `ext-prepare` bytes whose SHA-256 digest was stored in the exchange's protected approval record. Rehash and compare those bytes before finalization and before using the mappings for reconciliation.

`requests.json` is the plain signing-request file passed to the external signer. It contains one request for every required Orchard spend action:

```json
{
  "version": "v0",
  "requests": [
    {"action_index":0,"sighash":"<64-hex>","alpha":"<64-hex>","rk":"<64-hex>"}
  ]
}
```

`prepare-result.json` is the separately persisted command-result envelope, also emitted to stdout for compatibility. It includes both artifacts for inspection, but it is not the file supplied to the external signer:

```jsonc
{
  "version": "v1",
  "status": "ok",
  "data": {
    "prepared_tx": {/* complete PreparedTx v0 object; do not edit */},
    "signing_requests": {
      "version": "v0",
      "requests": [
        {"action_index":0,"sighash":"<64-hex>","alpha":"<64-hex>","rk":"<64-hex>"}
      ]
    }
  }
}
```

The external signer must return one signature for every requested action:

```json
{
  "version": "v0",
  "signatures": [
    {"action_index": 0, "spend_auth_sig": "<128-lowercase-hex>"}
  ]
}
```

Store the exact returned submission once as `$EXT_DIR/sigs.json` with mode `0600`. The external signer must refuse an existing output path. Do not merge, reorder, reconstruct, or overwrite signature entries between the external signer and finalization.

Before producing any signature share, the external system must hash the exact received `requests.json` bytes and require equality with the requests digest in the authenticated approval record. It must also require that record to bind the expected attempt, plan and prepared digests, network, coin type, account, action count, and unique `action_index` set. Reject any missing or mismatched field. Never sign a request batch reconstructed or edited outside `ext-prepare`.

### External signer contract

For each request, the HSM or TSS implementation must:

1. select the Orchard spend-authorizing key for the plan's `coin_type` and `account`
2. decode `alpha` as the canonical Pallas scalar and randomize that key with it
3. derive the randomized RedPallas verification key and require its 32-byte encoding to equal `rk`
4. create an Orchard `SpendAuth` RedPallas signature over the exact 32-byte `sighash`—do not prefix or hash it again
5. return the 64-byte signature as 128 lowercase hex characters under the same `action_index`

Generic Ed25519, ECDSA, or bridge `sign-digest` signatures are not compatible. The submission must have `version: "v0"`, exactly one unique signature for every `orchard_required_spend_action_indices` entry, and no other indices. Use a reviewed Orchard/RedPallas implementation; a distributed signer must implement the same randomized-key contract without reconstructing key shares online.

For regtest integration only, the pinned `juno-txsign` source builds a seed-backed reference helper. It validates `rk` before producing the same submission shape:

```bash
set -euo pipefail
set -o noclobber
umask 077
ATTEMPT_DIR="$PWD/tmp/withdrawal-1842"
EXT_DIR="$ATTEMPT_DIR/external"
test "$(id -u)" -ne 0

cargo build --locked --release \
  --manifest-path ../juno-txsign/rust/juno-tx/Cargo.toml \
  --bin juno_orchard_spendauth_sign

test ! -e "$EXT_DIR/sigs.json"
../juno-txsign/rust/juno-tx/target/release/juno_orchard_spendauth_sign \
  --requests "$EXT_DIR/requests.json" \
  --coin-type 8135 \
  --account 0 \
  --seed-file "$PWD/hot.seed" \
  --out "$EXT_DIR/sigs.json"
chmod 0600 "$EXT_DIR/sigs.json"
```

This helper is a test oracle, not a production HSM or TSS service.

```bash
set -euo pipefail
set -o noclobber
umask 077
TXSIGN_IMAGE=ghcr.io/junocash-tools/juno-exchange-gateway-txsign@sha256:<verified-digest>
ATTEMPT_DIR="$PWD/tmp/withdrawal-1842"
EXT_DIR="$ATTEMPT_DIR/external"
FINAL_DIR="$ATTEMPT_DIR/final"
test "$(id -u)" -ne 0
mkdir -m 0700 "$FINAL_DIR"

docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges \
  --user "$(id -u):$(id -g)" \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  -v "$EXT_DIR/prepared.json:/work/prepared.json:ro" \
  -v "$EXT_DIR/sigs.json:/work/sigs.json:ro" \
  -v "$FINAL_DIR:/work/output:rw" \
  "$TXSIGN_IMAGE" ext-finalize \
  --prepared-tx /work/prepared.json \
  --sigs /work/sigs.json \
  --out /work/output/rawtx.hex \
  --out-result /work/output/signed.json \
  --json > /dev/null

jq -e '
  .version == "v1" and .status == "ok" and
  (.data.txid | test("^[0-9a-f]{64}$")) and
  (.data.raw_tx_hex | test("^[0-9a-f]+$"))
' "$FINAL_DIR/signed.json" > /dev/null
test "$(tr -d '\r\n' < "$FINAL_DIR/rawtx.hex")" = \
  "$(jq -er '.data.raw_tx_hex' "$FINAL_DIR/signed.json")"
```

Finalization also needs no network or spending key. Rehash `prepared.json` and require it to match the approval record before this command; validate `$FINAL_DIR/signed.json`, require its `raw_tx_hex` to equal `$FINAL_DIR/rawtx.hex`, and verify the returned txid and fee before moving only the approved signed result back online.

With `--json`, success uses the same envelope as direct signing:

```json
{
  "version": "v1",
  "status": "ok",
  "data": {
    "txid": "<64-lowercase-hex>",
    "raw_tx_hex": "<lowercase-hex>",
    "fee_zat": "200000"
  }
}
```

Verify that `txid` and `fee_zat` match the approved attempt. `ext-finalize` deliberately does not accept `--action-indices`; use only the digest-bound output and change mappings retained from the approved `prepared.json`. It rejects structurally invalid prepared metadata, missing or duplicate action signatures, invalid signatures, and out-of-range or colliding action indices. It cannot authenticate a structurally valid swap of semantic output/change labels, which is why the exchange-side digest comparison is mandatory.

The actual spend-authority signer and its key shares stay isolated. Do not confuse this Orchard flow with `sign-digest serve`, which is a separate bridge digest-signing feature and is not a transaction-planning API.

### Signer failures

| Envelope code | Typical cause | Action |
| --- | --- | --- |
| `invalid_request` | Missing/conflicting file or key input, unreadable JSON | Fix local input; do not broadcast |
| `sign_failed` | Invalid plan, wrong seed/account, `address_network_mismatch`, `change_address_not_owned`, `note_decrypt_failed`, fee or witness failure | Stop and reconcile the approved plan with the signing wallet |
| `prepare_failed` | External UFVK/network/change mismatch or invalid plan/proof input | Stop; create no signing shares |
| `finalize_failed` | Missing, duplicate, malformed, or invalid spend-authority signature | Reject the signature set; do not reuse a changed prepared transaction |
| `io_error` | Output collision, storage failure, or stdout failure | Stop. Do not overwrite or rerun signing until the outcome is classified by the rules below |

Signer-managed output paths are reserved before cryptographic work, owner-only, synced, and published without replacement. A post-result failure deliberately leaves a `.juno-txsign-pending` marker. Never delete that marker merely to make a retry pass. Stdout remains available for compatibility but is not crash-durable; production workflows should use every applicable file-output flag and validate the complete `--out-result` before export.

- A reservation error before `sign`, `ext-prepare`, or `ext-finalize` starts creates no new transaction. Investigate the existing target or pending marker before assigning a new attempt.
- If a complete `--out-result` exists and matches the other committed outputs, treat it as the authoritative result even if stdout failed. Do not sign again.
- If the error says output state is uncertain, a pending marker remains, or stdout failed without a complete result, quarantine every partial artifact and keep all selected notes reserved. Do not sign again unless an audited review proves no valid bytes escaped; otherwise reconcile every known signed variant through finality or strict expiry.

## 3. Broadcast and reconcile

Return the approved raw hex and txid to the online side. Set `SIGNED_RESULT` to the transferred durable result: direct signing writes `$ATTEMPT_DIR/direct/signed.json`, while external finalization writes `$ATTEMPT_DIR/final/signed.json`. Submit the exact result with a stable idempotency key, then reconcile the txid through [transaction lookup](../capabilities/transaction-lookup.md).

```bash
set -euo pipefail
set -o noclobber
umask 077
ATTEMPT_DIR="$PWD/tmp/withdrawal-1842"
SIGNED_RESULT="$ATTEMPT_DIR/direct/signed.json"
# For external signing, use: SIGNED_RESULT="$ATTEMPT_DIR/final/signed.json"
RAW_TX_HEX="$(jq -er '.data.raw_tx_hex' "$SIGNED_RESULT")"
EXPECTED_TXID="$(jq -er '.data.txid' "$SIGNED_RESULT")"

jq -n --arg wallet hot --arg raw "$RAW_TX_HEX" --arg txid "$EXPECTED_TXID" \
  '{wallet_id:$wallet,raw_tx_hex:$raw,expected_txid:$txid}' \
  > "$ATTEMPT_DIR/broadcast.json"

curl --fail-with-body -X POST \
  -H "Authorization: Bearer $GATEWAY_BROADCAST_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: withdrawal-1842-attempt-1' \
  --data-binary "@$ATTEMPT_DIR/broadcast.json" \
  "$GATEWAY_URL/v1/transactions/broadcast"
```

The block above is for the first submission only. Persist the `wallet_id`, exact request body, principal name, and idempotency key before the call. A timeout is uncertain: rerun only its `curl` command against the existing `broadcast.json`, using the same key and principal. Never regenerate the body or rebuild because the first HTTP response was lost.

After a completed operation, the same key always replays its durable receipt without contacting the node. If later lookup proves that the txid is orphaned or absent while canonical height is still strictly below expiry, keep the inputs reserved and deliberately resubmit the same signed bytes under a fresh rebroadcast-operation key linked to this attempt. Require enough remaining blocks for the exchange's mining policy. Do not use that exception for an uncertain original call; resolve uncertainty with the original key first.

See [signed-raw broadcast](../capabilities/broadcast.md) for complete requests, responses, and retry rules.

## Why build and sign are not public APIs

The public gateway is watch-only and deliberately accepts no recipient, amount, plan, seed, or signing request. This keeps withdrawal authorization in the exchange and spending authority in the isolated signer.

The current planner is an on-demand private CLI. A future private planner service is reasonable only after it adds atomic selected-note reservations, wallet-scoped authentication, idempotent plan IDs, server-selected registered change, fee and expiry policy limits, exact node/scanner consistency checks, durable lifecycle recovery, and an approval-bound plan digest. It must remain off the public ingress network. The signer must remain offline or behind an equivalently isolated HSM/TSS boundary.

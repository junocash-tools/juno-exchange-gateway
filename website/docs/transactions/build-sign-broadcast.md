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
umask 077
ATTEMPT_DIR="$PWD/tmp/withdrawal-1842"
install -d -m 0700 "$ATTEMPT_DIR"

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

Use coin type `8133` on mainnet, `8134` on testnet, and `8135` on regtest. `0` asks the planner to infer it from the node.

For `send-many` and `rebalance`, pass a JSON array with `--outputs-file`:

```json
[
  {"to_address":"j1...","amount_zat":"250000"},
  {"to_address":"j1...","amount_zat":"400000","memo_hex":"6869"}
]
```

One transaction may contain at most 200 total Orchard outputs. Change counts toward that ceiling, so a batch that produces change may contain at most 199 explicit destinations; 200 explicit destinations are valid only when the selected inputs and fee produce no change.

```bash
docker compose --profile operator run --rm -T \
  -v "$PWD/tmp/withdrawal-1842/outputs.json:/work/outputs.json:ro" \
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

Treat the complete plan as sensitive operational data. Hash the exact file bytes, bind the digest to the withdrawal attempt and approval, then verify the same digest after transfer:

```bash
sha256sum "$ATTEMPT_DIR/txplan.json" | awk '{print $1}' > "$ATTEMPT_DIR/txplan.sha256"
test "$(sha256sum "$ATTEMPT_DIR/txplan.json" | awk '{print $1}')" = "$(tr -d '\r\n' < "$ATTEMPT_DIR/txplan.sha256")"
```

Do not parse and reserialize the plan between approval and signing. A non-zero planner exit returns a versioned JSON error when `--json` is used; common codes are `invalid_request`, `insufficient_balance`, `too_many_inputs`, `no_liquidity_in_hot`, and `not_found`.

### Required policy checks

- `--minconf` defaults to `100`, matching the appliance default financial policy. Use a lower value only under a documented spend policy.
- `--fee-multiplier` defaults to `20`. With the planner's 5,000-zat base, this matches the bundled `junocashd` 0.9.12 policy of `100,000 × max(2, logical actions)` zat on every network. Recheck the default when either component changes.
- Outputs include change when calculating logical actions. `--fee-add-zat` adds to the multiplied fee. `--min-change-zat` adds smaller change to the fee instead of creating a note.
- The default expiry is `tip + 1 + 40`; the minimum offset is `4`. A pending spend clears for expiry only after node height is strictly greater than `expiry_height`.
- Use a dedicated gateway-registered change address under the spending UFVK. An `internal_change:*` label records exchange purpose; the allocated address itself uses Orchard external scope, and the pinned scanner therefore reports `recipient_scope: "external"` and `ovk_scope: "external"`. Same-wallet transaction-origin suppression keeps this change out of the external deposit feed. The planner accepts the address supplied by the exchange; the signer independently rejects a change address outside the signing seed or external-signing UFVK and rejects address/network mismatches.
- Inspect destination, amount, memo, change, selected notes, fee, anchor, branch ID, network, and expiry before approval.

Planning does not reserve notes. Follow [note selection and reservations](./note-selection-and-reservations.md) before creating more than one outstanding plan.

## 2. Sign directly offline

On the isolated signer host, put the verified plan at `tmp/withdrawal-1842/txplan.json` and the protected seed at `hot.seed`. Pin the signer image by the digest recorded in the release manifest, disable networking, and mount both inputs read-only:

```bash
TXSIGN_IMAGE=ghcr.io/junocash-tools/juno-exchange-gateway-txsign@sha256:<verified-digest>
ATTEMPT_DIR="$PWD/tmp/withdrawal-1842"

docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges \
  --user "$(id -u):$(id -g)" \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  -v "$ATTEMPT_DIR/txplan.json:/work/txplan.json:ro" \
  -v "$PWD/hot.seed:/run/secrets/juno-seed:ro" \
  "$TXSIGN_IMAGE" sign \
  --txplan /work/txplan.json \
  --seed-file /run/secrets/juno-seed \
  --action-indices \
  --json > "$ATTEMPT_DIR/signed.json"
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

```bash
ATTEMPT_DIR="$PWD/tmp/withdrawal-1842"

juno-txsign ext-prepare \
  --txplan "$ATTEMPT_DIR/txplan.json" \
  --ufvk-file "$PWD/wallet.ufvk" \
  --out-prepared "$ATTEMPT_DIR/prepared.json" \
  --out-requests "$ATTEMPT_DIR/requests.json" \
  > "$ATTEMPT_DIR/prepare-result.json"

sha256sum "$ATTEMPT_DIR/prepared.json" "$ATTEMPT_DIR/requests.json"
```

`ext-prepare` always returns JSON. Treat `prepared.json` as opaque, retain its exact bytes through finalization, and bind both file digests to the approved withdrawal attempt. The prepared artifact also contains three coordination mappings:

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

`prepare-result.json` is a separate command-result envelope written to stdout. It includes both artifacts for inspection, but it is not the file supplied to the external signer:

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

Before signing, the external system must verify the approved plan/prepared digest, network, action count, and unique `action_index` set. Never sign a request batch reconstructed or edited outside `ext-prepare`.

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
cargo build --locked --release \
  --manifest-path ../juno-txsign/rust/juno-tx/Cargo.toml \
  --bin juno_orchard_spendauth_sign

../juno-txsign/rust/juno-tx/target/release/juno_orchard_spendauth_sign \
  --requests "$ATTEMPT_DIR/requests.json" \
  --coin-type 8135 \
  --account 0 \
  --seed-file "$PWD/hot.seed" \
  --out "$ATTEMPT_DIR/sigs.json"
```

This helper is a test oracle, not a production HSM or TSS service.

```bash
juno-txsign ext-finalize \
  --prepared-tx "$ATTEMPT_DIR/prepared.json" \
  --sigs "$ATTEMPT_DIR/sigs.json" \
  --out "$ATTEMPT_DIR/rawtx.hex" \
  --json > "$ATTEMPT_DIR/signed.json"
```

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
| `io_error` | Protected artifact could not be read or written | Repair local storage and retry the same approved artifact |

## 3. Broadcast and reconcile

Return the approved raw hex and txid to the online side. Submit them with a stable idempotency key, then reconcile the txid through [transaction lookup](../capabilities/transaction-lookup.md).

```bash
RAW_TX_HEX="$(jq -er '.data.raw_tx_hex' "$ATTEMPT_DIR/signed.json")"
EXPECTED_TXID="$(jq -er '.data.txid' "$ATTEMPT_DIR/signed.json")"

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

Persist the `wallet_id`, exact request body, principal name, and idempotency key before the call. A timeout is uncertain: retry the identical body with the same key and principal. Never rebuild merely because the first HTTP response was lost.

After a completed operation, the same key always replays its durable receipt without contacting the node. If later lookup proves that the txid is orphaned or absent while canonical height is still strictly below expiry, keep the inputs reserved and deliberately resubmit the same signed bytes under a fresh rebroadcast-operation key linked to this attempt. Require enough remaining blocks for the exchange's mining policy. Do not use that exception for an uncertain original call; resolve uncertainty with the original key first.

See [signed-raw broadcast](../capabilities/broadcast.md) for complete requests, responses, and retry rules.

## Why build and sign are not public APIs

The public gateway is watch-only and deliberately accepts no recipient, amount, plan, seed, or signing request. This keeps withdrawal authorization in the exchange and spending authority in the isolated signer.

The current planner is an on-demand private CLI. A future private planner service is reasonable only after it adds atomic nullifier reservations, wallet-scoped authentication, idempotent plan IDs, server-selected registered change, fee and expiry policy limits, exact node/scanner consistency checks, durable lifecycle recovery, and an approval-bound plan digest. It must remain off the public ingress network. The signer must remain offline or behind an equivalently isolated HSM/TSS boundary.

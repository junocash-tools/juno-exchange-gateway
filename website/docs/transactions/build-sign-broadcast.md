---
title: Build, sign, and broadcast
---

`juno-txbuild` is an online planner. `juno-txsign` is the offline signer. The gateway broadcasts the signed result.

## 1. Build online

The operator profile can reach the private node and scanner but is not a public service:

```bash
mkdir -p tmp/withdrawal-1842

docker compose --profile operator run --rm txbuild send \
  --wallet-id hot \
  --coin-type 8133 \
  --account 0 \
  --to 'j1...' \
  --amount-zat 250000 \
  --change-address 'j1...' \
  --minconf 100 > tmp/withdrawal-1842/txplan.json
```

The example is mainnet. Use coin type `8134` on testnet and `8135` on regtest; `0` asks the planner to infer it.

Inspect the destination, amount, change, fee, anchor, and expiry height before approval. The default expiry offset is 40 blocks. Choose the final fee before signing; Orchard transactions do not support normal fee bumping.

## 2. Sign offline

Transfer `txplan.json` to the signing environment over an authenticated process. Verify it again, then sign:

```bash
juno-txsign sign \
  --txplan ./txplan.json \
  --seed-file ./seed.b64 \
  --json > ./signed.json
```

Keep the seed and signer output off the online host. External spend-authority signing is also supported by `juno-txsign ext-prepare` and `ext-finalize`.

## 3. Broadcast

Return only `raw_tx_hex` and `txid` from `signed.json` to the online side. Submit them as `raw_tx_hex` and `expected_txid` with a stable idempotency key.

See [signed-raw broadcast](../capabilities/broadcast.md).

## Operational controls

- enforce withdrawal approval before signing
- bind the idempotency key to the internal withdrawal and attempt number
- stop if the plan changed after approval
- track expiry and release reserved notes only after expiry or confirmed replacement policy
- reconcile the txid through [transaction lookup](../capabilities/transaction-lookup.md)

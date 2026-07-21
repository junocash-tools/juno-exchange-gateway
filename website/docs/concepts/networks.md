---
title: Networks
---

Set `JUNO_NETWORK` to exactly one network. Node chain, scanner HRP, UFVK, and generated addresses must agree.

| Network | `JUNO_NETWORK` | Address prefix | UFVK prefix | Coin type | Node chain |
| --- | --- | --- | --- | --- | --- |
| Mainnet | `mainnet` | `j1...` | `jview1...` | `8133` | `main` |
| Testnet | `testnet` | `jtest1...` | `jviewtest1...` | `8134` | `test` |
| Regtest | `regtest` | `jregtest1...` | `jviewregtest1...` | `8135` | `regtest` |

Use separate databases, volumes, credentials, and wallet files for each network. A mismatch fails startup or readiness checks.

Mainnet and testnet require bearer credentials. Regtest can run without them for isolated local testing.

The default confirmation threshold is **100 on every network**. Lower it only in a disposable regtest environment when a test requires faster lifecycle transitions.

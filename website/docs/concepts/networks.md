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

Mainnet and testnet require bearer credentials. The public gateway can run anonymously only on an isolated regtest. The private coordinator always requires a credential with `plan` or `admin` plus a wallet grant.

The default confirmation threshold is **100 on every network**. The coordinator uses it for input eligibility, finality, and post-expiry release proof. Lower it only in a disposable regtest environment when a test requires faster lifecycle transitions.

The coordinator derives coin type from the configured network and checks every destination, UFVK, node chain, scanner HRP, plan, and signed result. Clients never supply a coin type. Keep separate state databases, signer journals, wallet/account files, credentials, and idempotency keys per network.

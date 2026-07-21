---
title: Architecture and trust boundary
---

Only the gateway is public. Every other service belongs on a private network.

```text
Exchange ledger ──HTTPS/mTLS──> Gateway ──> junocashd
                                  │
                                  ├───────> juno-scan ──> scanner database
                                  └───────> juno-addrgen

Operator network: juno-txbuild ──> offline juno-txsign ──> signed raw tx
                                                            │
                                                            └──> Gateway broadcast
```

The gateway stores:

- registered watch-only wallets and allocated-address state
- an opaque cursor signing key
- broadcast idempotency receipts

The scanner stores chain-derived notes, events, witnesses, and caches. `junocashd` remains the source of chain truth.

## Hard boundary

The online stack may contain a UFVK. It must never contain:

- a seed, mnemonic, spending key, or signer share
- a request to sign
- an endpoint that accepts recipients, amounts, or a transaction plan

Withdrawals are planned online, signed in an isolated environment, and returned to the gateway as a signed raw transaction. The gateway only validates and broadcasts that blob.

## Data sensitivity

A UFVK cannot spend funds, but it reveals wallet activity. Protect it as confidential exchange data. Restrict scanner and node access to the appliance network and redact request bodies from proxy logs.

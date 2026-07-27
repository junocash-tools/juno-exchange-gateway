---
title: Architecture and trust boundary
---

The public gateway is watch-only. Transaction creation runs on a separate private listener, and spending authority stays in a network-disabled signer.

```text
Public or exchange API network
  Exchange ──HTTPS/mTLS──> Gateway ──> junocashd
                              │
                              ├──> juno-scan ──> scanner database
                              └──> juno-addrgen

Private withdrawal network
  Exchange ──HTTPS/mTLS──> Coordinator listener
                              ├──> juno-txbuild ──> scanner/node reads
                              ├──> gateway SQLite attempts/reservations
                              └──owner-only UDS──> juno-txsign serve-txplan
                                                     ├── network disabled
                                                     ├── seed read-only
                                                     └── durable signer journal
```

## Public gateway boundary

The public listener can:

- allocate watch-only addresses and read registered-wallet chain data
- return deposits, note summaries, selected-note status, and transaction lifecycle
- validate and broadcast already-signed raw bytes

It has no build or sign route and receives no destination/amount signing request. Node RPC, scanner, databases, planner, coordinator, and signer must not be public.

## Private coordinator boundary

The private listener accepts an already-authorized wallet ID and output list. It:

- validates the configured network, wallet grant, amount/memo/output limits, and idempotency key
- allocates registered same-wallet change
- selects eligible notes and atomically reserves their canonical IDs
- stores the exact plan and `sha256:<digest>` before signing
- calls the signer over a Unix-domain socket
- recovers durable attempts and reservations after restart
- returns signed raw hex but never broadcasts it

The exchange must authorize before `POST /v1/transaction-attempts`, then persist and check the signed result before using the separate public broadcast endpoint.

## Signer boundary

The signer is not an exchange-facing API. `juno-txsign serve-txplan` listens only on an owner-only Unix socket shared with the coordinator. Its container has no network and receives only an attempt ID, exact plan bytes, and plan digest. Before opening the socket, it derives the UFVK from its seed and verifies every exact wallet ID/account/network/UFVK binding.

The seed, mnemonic, spending key, or signer share must never enter the exchange application, gateway, coordinator, planner, scanner, node, or their logs. The signer owns a durable immutable journal so a retry of the same attempt/digest returns the original result. A different digest for an existing attempt fails closed.

## Stored state

| Component | Durable state |
| --- | --- |
| Gateway/coordinator SQLite | wallet/address registry, cursor key, broadcast receipts, transaction attempts, exact plans/results, active note reservations |
| Scanner database | chain-derived notes, pending spends, events, witnesses, and caches |
| Signer journal | one immutable signing outcome per attempt/digest; no request destinations separate from the plan |
| Exchange ledger | customer ownership, withdrawal approvals, attempt IDs, output intent, signed-result metadata, broadcast keys, and accounting |
| `junocashd` | canonical chain and mempool truth |

The exchange ledger remains the business source of truth. Coordinator state prevents input reuse and recovers signing; scanner state can be rebuilt from the same UFVK/birthday/canonical chain, but outstanding attempts and signer outcomes cannot be inferred safely from a rescan alone.

## Data sensitivity

A UFVK cannot spend funds, but it reveals wallet activity. Plans reveal selected inputs, destinations, amounts, memos, fee, and change. Raw signed bytes remain spend-capable until expiry. Keep all three confidential, redact request/response bodies, and log only request IDs and non-sensitive state transitions.

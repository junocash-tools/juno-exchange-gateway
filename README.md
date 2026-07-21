# Juno Exchange Gateway

`juno-exchange-gateway` is the single public, watch-only HTTP API for a Juno Cash exchange integration. It exposes node tip, registered address allocation and balances, durable-cursor deposits, transaction lookup, and signed raw transaction broadcast. It never accepts or stores seeds, spending keys, signing requests, recipients, amounts, or transaction plans.

The supported networks are `mainnet`, `testnet`, and `regtest`. Every configured wallet is registered by UFVK; balance and deposit address filters are limited to addresses allocated by this gateway for that wallet.

Run all core tests with:

```sh
go test ./...
```

Runtime configuration is environment-based. `JUNO_GATEWAY_WALLETS_FILE` points to a JSON file containing `wallets`, and `JUNO_GATEWAY_AUTH_FILE` points to a JSON file containing scoped bearer credentials. Outside regtest, authentication is mandatory. The full configuration and operations reference is published with the appliance documentation.

The gateway SQLite state contains the allocated-address registry, diversifier counters, opaque-cursor key, wallet backfill mirror, and broadcast idempotency receipts. Back it up together with the scanner database while the appliance is stopped, or use a storage snapshot with SQLite WAL consistency. Scanner data is recoverable from `junocashd` using the same UFVK and `birthday_height`; recovery stays unready until the scanner's persisted backfill progress reaches its tip. Gateway state should still be restored because discarding it loses address ownership and idempotency history even though the addresses themselves are deterministically derivable.

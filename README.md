# Juno Exchange Gateway

`juno-exchange-gateway` is the single public, watch-only HTTP API for a Juno Cash exchange integration. It exposes node tip, registered address allocation and balances, durable-cursor deposits, transaction lookup, and signed raw transaction broadcast. It never accepts or stores seeds, spending keys, signing requests, recipients, amounts, or transaction plans.

The supported networks are `mainnet`, `testnet`, and `regtest`. Every configured wallet is registered by UFVK; balance and deposit results are limited to addresses allocated by this gateway for that wallet. A wallet ID is permanently bound to the SHA-256 fingerprint of its first UFVK. UFVK replacement, duplicate UFVKs, and later birthday heights are rejected; an earlier birthday safely rewinds backfill.

Run all core tests with:

```sh
go test ./...
```

Runtime configuration is environment-based. `JUNO_GATEWAY_WALLETS_FILE` points to a JSON file containing `wallets`, and `JUNO_GATEWAY_AUTH_FILE` points to a JSON file containing scoped bearer credentials. Outside regtest, authentication is mandatory. `JUNO_GATEWAY_WALLET_EFFECTS_MAX_EVENTS` defaults to `10000`; transaction enrichment fails explicitly instead of truncating above this cap. The full configuration and operations reference is published with the appliance documentation.

The gateway SQLite state contains the allocated-address registry, diversifier counters, opaque-cursor key, wallet/UFVK binding, backfill mirror, and broadcast idempotency receipts. Back it up together with the scanner database while the appliance is stopped, or use a storage snapshot with SQLite WAL consistency. Scanner data is recoverable from `junocashd` using the same UFVK and `birthday_height`; recovery stays unready until persisted backfill reaches the node tip. A fresh scanner database has a new event epoch, so clients receive `cursor_reset_required` and restart deposit polling without a cursor.

Deposit cursors are signed and bound to wallet, scanner event epoch, and the `address`, `txid`, and `status` filters. Keep those filters unchanged while advancing a cursor; otherwise restart without it.

Gateway state is not reconstructible from the scanner: losing it loses address ownership and risks reissuing diversifier index zero. Startup therefore refuses to recreate a wallet when the scanner still knows that wallet. Restore the gateway backup before allocation resumes.

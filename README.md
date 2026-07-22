# Juno Exchange Gateway

Watch-only HTTP API for Juno Cash exchange deposits and withdrawals. It supports mainnet, testnet, and regtest, and exposes:

- registered address allocation and balances
- aggregate wallet note liquidity for consolidation decisions
- durable-cursor deposit lifecycle events
- chain tip and transaction lookup
- signed raw transaction broadcast

The online gateway never accepts seeds, spending keys, recipients, amounts, signing requests, or transaction plans. Planning is an online private-operator workflow; signing stays offline. See the [operator documentation](https://junocash-tools.github.io/juno-exchange-gateway/) and [OpenAPI contract](https://junocash-tools.github.io/juno-exchange-gateway/openapi.yaml).

## Quickstart

Create `config/wallets.json` and `config/auth.json` from the examples, then set their absolute host paths in `.env`. Create a separate installation-state bind and initialize it once:

```sh
install -m 0600 .env.example .env
sudo chown 10001:10001 config/wallets.json config/auth.json
sudo chmod 0600 config/wallets.json config/auth.json
sudo install -d -o 10001 -g 10001 -m 0700 installation-state
docker compose run --no-deps --rm gateway init \
  --acknowledge I_UNDERSTAND_THIS_CREATES_A_NEW_JUNO_INSTALLATION
docker compose up -d
docker compose ps
```

The gateway image always runs as UID:GID `10001:10001`. The owner-only wallet, auth, and installation-state paths must be readable, and the state directory writable, by that identity. Adjust the host mapping only when Docker user-namespace remapping is enabled; never make these paths world-readable or world-writable.

`init` refuses an existing installation. Normal serve requires the external manifest and a gateway database bound to its installation ID. The manifest stores network, wallet IDs, UFVK fingerprints, and crash-safe address high-water marks; it never stores raw UFVKs or keys. Keep it private and back it up separately from Compose volumes.

The gateway reserves each address index in the manifest before writing the database. Failures may skip indices but cannot reuse them. A missing, incomplete, stale, or mismatched gateway database fails closed. Prefer a consistent database backup; the audited `recovery-checksum` and `recover` commands can rebuild the deterministic address registry from the same UFVKs up to verified high-water marks. Customer ownership and labels still come from the exchange ledger.

Scanner data is reproducible from the same UFVK, birthday, network, and canonical chain. Every scanner process start rotates the event epoch. Old cursors then return `cursor_reset_required`; restart without a cursor and replay by stable deposit identity.

## Test

```sh
make test
```

The release test includes unit checks, Compose validation, documentation build, Postgres smoke coverage, and a manual regtest deposit/withdrawal/reorg/recovery flow.

Documentation: [junocash-tools.github.io/juno-exchange-gateway](https://junocash-tools.github.io/juno-exchange-gateway/)

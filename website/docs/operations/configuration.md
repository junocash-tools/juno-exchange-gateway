---
title: Configuration reference
---

Copy `.env.example` to `.env`. Testnet and mainnet reject the shipped RPC and scanner placeholders. Durations use values such as `500ms`, `15s`, and `2m`.

## Appliance

| Variable | Default | Purpose |
| --- | --- | --- |
| `JUNO_NETWORK` | `regtest` | `mainnet`, `testnet`, or `regtest` |
| `JUNO_GATEWAY_BIND` | `127.0.0.1` | Published gateway interface |
| `JUNO_GATEWAY_PORT` | `8080` | Published gateway port |
| `JUNO_GATEWAY_WALLETS_FILE` | `./config/wallets.json` | Host wallet registry file |
| `JUNO_GATEWAY_AUTH_FILE` | `./config/auth.json` | Host credential file |
| `JUNO_INSTALLATION_STATE_DIR` | `./installation-state` | Separate host directory for installation identity and address high-water state |
| `JUNO_RPC_USER` | `juno-rpc` | Private node RPC user |
| `JUNO_RPC_PASSWORD` | placeholder | Private node RPC password |
| `JUNO_SCAN_API_BEARER_TOKEN` | placeholder | Gateway-to-scanner token |
| `JUNO_NODE_PERSIST_MEMPOOL` | `1` | Preserve the node mempool across restarts; E2E sets `0` to exercise expiry |
| `JUNO_POSTGRES_DB` | `junoscan` | Postgres database |
| `JUNO_POSTGRES_USER` | `junoscan` | Postgres user |
| `JUNO_POSTGRES_PASSWORD` | required in Postgres mode | Postgres password; URI-reserved symbols are supported |
| `JUNO_NODE_IMAGE` | published `main` tag | `junocashd` image |
| `JUNO_SCAN_IMAGE` | published `main` tag | scanner image |
| `JUNO_GATEWAY_IMAGE` | published `main` tag | gateway image |
| `JUNO_TXBUILD_IMAGE` | published `main` tag | operator planner image |
| `JUNO_TXSIGN_IMAGE` | published `main` tag | separately invoked offline signer image |
| `JUNO_POSTGRES_IMAGE` | pinned Postgres digest | Postgres image |

Use digest-pinned image references in production.

## Source builds and documentation

These settings identify local source builds and the documentation site. They are not gateway runtime policy.

| Variable or build argument | Default | Purpose |
| --- | --- | --- |
| `JUNOCASH_VERSION` | `0.9.12` | Node release downloaded by the development image |
| `JUNOCASH_LINUX64_SHA256` | `41f74d…ec386` | Verify the downloaded node archive |
| `JUNO_ADDRGEN_REF` | `4a2b3a3…db834` | Address-deriver source and version manifest |
| `JUNO_ADDRGEN_REPO` | `junocash-tools/juno-addrgen` | Direct gateway-image build argument |
| `JUNO_SCAN_REF` | pinned release SHA | Scanner commit recorded in the version manifest |
| `JUNO_TXBUILD_REF` | `e7b1d5c…c75f` | Planner source and version manifest |
| `JUNO_TXBUILD_REPO` | `junocash-tools/juno-txbuild` | Direct planner-image build argument |
| `JUNO_TXSIGN_REF` | `29a58cc…ae6890` | Offline signer source and published image |
| `JUNO_TXSIGN_REPO` | `junocash-tools/juno-txsign` | Offline signer-image build argument |
| `JUNO_GATEWAY_VERSION` | `dev` | Gateway version recorded in the binary |
| `JUNO_GATEWAY_REVISION` | `local` | Gateway commit recorded in the binary |
| `JUNO_GATEWAY_BUILD_TIME` | empty | UTC build time recorded in the binary |
| `GITHUB_REPOSITORY` | GitHub-provided repository | Derive the Pages URL, base path, and source link |
| `DOCS_URL` | GitHub Pages origin | Override the Docusaurus canonical origin |
| `DOCS_BASE_URL` | repository Pages path | Override the Docusaurus base path |

Use full commit SHAs and the published node checksum. Release builds should set version, revision, build time, and component refs from a clean checkout.

## Internal service wiring

Compose sets `JUNO_DATADIR`, `JUNO_RPC_URL`, `JUNO_RPC_PASS`, `JUNO_RPC_PORT`, `JUNO_SCAN_URL`, and `JUNO_SCAN_BEARER_TOKEN` inside the node and planner containers. The Postgres override derives `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`, and `PGSSLMODE` from the appliance settings; the password is not embedded in a URI. These are internal wiring, not host operator toggles.

## Gateway

The Compose appliance sets private URLs and paths. Direct gateway deployments may set every variable below.

| Variable | Default | Purpose |
| --- | --- | --- |
| `JUNO_GATEWAY_NETWORK` | `regtest` | Gateway network identity |
| `JUNO_GATEWAY_LISTEN` | `:8080` | HTTP listen address |
| `JUNO_GATEWAY_STATE_DSN` | SQLite under `/var/lib/juno-gateway` | Address, cursor-key, and idempotency state |
| `JUNO_GATEWAY_INSTALLATION_STATE` | `/var/lib/juno-installation/manifest.json` | Absolute external installation manifest path |
| `JUNO_GATEWAY_NODE_RPC_URL` | `http://junocashd:8232` | Private node RPC URL |
| `JUNO_GATEWAY_NODE_RPC_USER` | empty | Node RPC user |
| `JUNO_GATEWAY_NODE_RPC_PASSWORD` | empty | Node RPC password |
| `JUNO_GATEWAY_SCANNER_URL` | `http://juno-scan:8080` | Private scanner URL |
| `JUNO_GATEWAY_SCANNER_TOKEN` | empty | Scanner bearer token |
| `JUNO_GATEWAY_ADDRGEN_PATH` | `/usr/local/bin/juno-addrgen` | Address derivation binary |
| `JUNO_GATEWAY_WALLETS_FILE` | required | Wallet JSON path |
| `JUNO_GATEWAY_AUTH_FILE` | required outside regtest | Credential JSON path |
| `JUNO_GATEWAY_DEFAULT_CONFIRMATIONS` | `100` | Positive default financial-read and scanner-event threshold |
| `JUNO_GATEWAY_MAX_CONFIRMATIONS` | `10000` | Maximum request override |
| `JUNO_GATEWAY_MAX_SCANNER_LAG` | `2` | Readiness lag limit |
| `JUNO_GATEWAY_REQUIRE_COMPLETE_HISTORY` | `true` | Gate financial reads on scanner history |
| `JUNO_GATEWAY_MAX_JSON_BODY_BYTES` | `1048576` | Ordinary JSON body limit |
| `JUNO_GATEWAY_MAX_BROADCAST_BODY_BYTES` | `4194304` | Broadcast body limit |
| `JUNO_GATEWAY_READ_TIMEOUT` | `15s` | Read request deadline |
| `JUNO_GATEWAY_BROADCAST_TIMEOUT` | `30s` | Broadcast request deadline |
| `JUNO_GATEWAY_UPSTREAM_TIMEOUT` | `10s` | Node/scanner client deadline |
| `JUNO_GATEWAY_SHUTDOWN_TIMEOUT` | `15s` | Graceful shutdown deadline |
| `JUNO_GATEWAY_HTTP_READ_HEADER_TIMEOUT` | `5s` | Maximum time to receive HTTP headers |
| `JUNO_GATEWAY_HTTP_READ_TIMEOUT` | `30s` | Maximum time to receive an HTTP request, including its body |
| `JUNO_GATEWAY_HTTP_WRITE_TIMEOUT` | `45s` | Maximum HTTP response write window; must exceed the broadcast deadline |
| `JUNO_GATEWAY_HTTP_IDLE_TIMEOUT` | `60s` | Keep-alive idle timeout |
| `JUNO_GATEWAY_READ_RATE_RPS` | `50` | Read bucket refill per second |
| `JUNO_GATEWAY_READ_RATE_BURST` | `100` | Read bucket capacity |
| `JUNO_GATEWAY_BROADCAST_RATE_RPS` | `2` | Broadcast bucket refill per second |
| `JUNO_GATEWAY_BROADCAST_RATE_BURST` | `5` | Broadcast bucket capacity |
| `JUNO_GATEWAY_TRUST_PROXY_HEADERS` | `false` | Trust first `X-Forwarded-For` address |
| `JUNO_GATEWAY_IDEMPOTENCY_LEASE` | `30s` | In-progress broadcast claim lease; minimum `1s` |
| `JUNO_GATEWAY_BACKFILL_BATCH_SIZE` | `10000` | Maximum heights per wallet backfill request |
| `JUNO_GATEWAY_BACKFILL_YIELD` | `250ms` | Pause between backfill batches |
| `JUNO_GATEWAY_BACKFILL_TIMEOUT` | `10m` | Deadline for one backfill request |
| `JUNO_GATEWAY_WALLET_EFFECTS_MAX_EVENTS` | `10000` | Fail-closed cap for one transaction's wallet effects |

Rate buckets are separate and keyed by credential plus client IP. Set `JUNO_GATEWAY_TRUST_PROXY_HEADERS=true` only when direct access is blocked and a trusted proxy overwrites forwarding headers.

A credential `name` is also its durable broadcast-idempotency namespace. Rotate its token without changing the name. Before renaming a broadcast principal, reconcile every outstanding withdrawal attempt because the new name creates a new namespace.

## Scanner

The appliance exposes the operational subset below in `.env`.

| Variable | Appliance default | Purpose |
| --- | --- | --- |
| `JUNO_SCAN_CONFIRMATIONS` | `100` | Height at which confirmation events emit |
| `JUNO_SCAN_POLL_INTERVAL` | `2s` | Poll fallback when ZMQ is unavailable |
| `JUNO_SCAN_DB_DRIVER` | `rocksdb` | `rocksdb` or `postgres` in the appliance |
| `JUNO_SCAN_DB_PATH` | managed volume | RocksDB path |
| `JUNO_SCAN_DB_DSN` | empty | Postgres DSN |
| `JUNO_SCAN_MAX_READY_LAG` | `2` | Scanner readiness lag limit |
| `JUNO_SCAN_SHARD_CACHE_ENABLED` | `true` | Enable 4096-leaf Orchard shard roots |
| `JUNO_SCAN_SHARD_CACHE_BATCH` | `4` | Roots computed per background pass |
| `JUNO_SCAN_SHARD_CACHE_POLL` | `5s` | Cache worker interval |
| `JUNO_SCAN_SHARD_CACHE_YIELD` | `25ms` | Yield between root computations |
| `JUNO_SCAN_WITNESS_MODE` | `auto` | `auto`, `shard`, `subtree`, or `legacy` |

Keep `auto` in production. The other witness modes are diagnostic controls. Disabling the shard cache preserves the subtree and legacy fallbacks but increases witness work.

Readiness requires `JUNO_SCAN_CONFIRMATIONS` to be positive and exactly equal to `JUNO_GATEWAY_DEFAULT_CONFIRMATIONS`. This prevents deposit events and default balance reads from using different finality policies.

Direct scanner deployments also support:

| Variable | Scanner default | Purpose |
| --- | --- | --- |
| `JUNO_SCAN_LISTEN` | `127.0.0.1:8080` | Internal HTTP address |
| `JUNO_SCAN_API_BEARER_TOKEN` | empty | Internal API bearer token |
| `JUNO_SCAN_RPC_URL` | `http://127.0.0.1:8232` | Node RPC URL |
| `JUNO_SCAN_RPC_USER` / `JUNO_SCAN_RPC_PASS` | empty | Node RPC credential |
| `JUNO_SCAN_UA_HRP` | derived from network | Optional `j`, `jtest`, or `jregtest` override |
| `JUNO_SCAN_NETWORK` | `auto` | Explicit or HRP-derived network |
| `JUNO_SCAN_ZMQ_HASHBLOCK` | empty | Optional node `hashblock` endpoint |
| `JUNO_SCAN_DB_SCHEMA` | empty | Optional Postgres schema |
| `JUNO_SCAN_DB_URL` | local Postgres URL | Deprecated alias for `JUNO_SCAN_DB_DSN` |
| `JUNO_SCAN_BROKER_DRIVER` | `none` | `none`, `kafka`, `nats`, or `rabbitmq` |
| `JUNO_SCAN_BROKER_URL` | empty | Broker connection string |
| `JUNO_SCAN_BROKER_TOPIC` | `juno.scan.events` | Topic, subject, or queue |
| `JUNO_SCAN_BROKER_POLL_INTERVAL` | `500ms` | Outbox polling interval |
| `JUNO_SCAN_BROKER_BATCH_SIZE` | `1000` | Outbox batch, maximum 5000 |

Broker and MySQL drivers require tagged scanner builds and are not part of the polling-only appliance contract.

## Wallet file

Each wallet requires a unique `wallet_id`, a network-matching `ufvk`, and non-negative `birthday_height`. Keep the earliest reliable birthday; a later value can miss deposits during recovery.

## Credential file

Each credential has a unique `name`, exactly one of `token` or lowercase `token_sha256`, at least one scope, and at least one wallet. Plain tokens must be at least 24 characters; hashes are preferred.

The tracked auth example uses an all-zero hash that is rejected outside regtest. Replace it with the SHA-256 hash of a strong bearer token before testnet or mainnet startup.

| Scope | Access |
| --- | --- |
| `read` | readiness, version, tip, balances, deposits, transaction lookup |
| `address` | allocate deposit addresses |
| `broadcast` | submit signed raw transactions |
| `raw` | include raw hex in transaction lookup |
| `admin` | satisfies any endpoint scope; wallet restrictions still apply |

Use explicit wallet IDs. Reserve `"*"` for tightly controlled operational credentials.

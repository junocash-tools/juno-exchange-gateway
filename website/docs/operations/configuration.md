---
title: Configuration reference
---

Create an owner-only environment file with `install -m 0600 .env.example .env`. Testnet and mainnet reject the shipped RPC and scanner placeholders. Durations use values such as `500ms`, `15s`, and `2m`.

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
| `JUNO_NODE_PERSIST_MEMPOOL` | `1` | Preserve the node mempool across restarts; keep `1` in production, while E2E sets `0` to exercise expiry |
| `JUNO_POSTGRES_DB` | `junoscan` | Postgres database |
| `JUNO_POSTGRES_USER` | `junoscan` | Postgres user |
| `JUNO_POSTGRES_PASSWORD` | required in Postgres mode | Postgres password; URI-reserved symbols are supported |
| `JUNO_NODE_IMAGE` | published `main` tag | `junocashd` image |
| `JUNO_SCAN_IMAGE` | published `main` tag | scanner image |
| `JUNO_GATEWAY_IMAGE` | published `main` tag | gateway image |
| `JUNO_TXBUILD_IMAGE` | published `main` tag | Operator-fallback planner image; the automation overlay also supplies the planner binary to the coordinator |
| `JUNO_TXSIGN_IMAGE` | published `main` tag | Network-disabled UDS signer in the automation overlay, or one-shot operator fallback |
| `JUNO_POSTGRES_IMAGE` | pinned Postgres digest | Postgres image |

Use digest-pinned image references in production.

When `JUNO_NETWORK=regtest`, the bundled node entrypoint adds `-nuparams=5437f330:1` so the pinned 0.9.12 node reaches NU6.2 for Orchard PCZT proving. This flag is intentionally not configurable through Compose and is never passed for testnet or mainnet; their consensus activation schedules come from the node.

Keep `JUNO_NODE_PERSIST_MEMPOOL=1` in production. With `0`, a node restart can forget an accepted but unmined withdrawal even though its signed bytes remain valid. Do not release its selected note IDs: look up the txid, keep the original broadcast operation's uncertainty rules, and deliberately rebroadcast the identical bytes only when the original result is reconciled and canonical height remains below expiry. Release reservations only after the strict post-expiry checks in [Backups and recovery](./recovery.md). Use `0` only for disposable expiry tests or a documented recovery drill.

## Source builds and documentation

These settings identify local source builds and the documentation site. They are not gateway runtime policy.

| Variable or build argument | Default | Purpose |
| --- | --- | --- |
| `JUNOCASH_VERSION` | `0.9.12` | Node release downloaded by the development image |
| `JUNOCASH_LINUX64_SHA256` | `41f74d…ec386` | Verify the downloaded node archive |
| `JUNO_ADDRGEN_REF` | `4a2b3a361c7c1cc3e15891b0befb2eb3dfddb834` | Address-deriver source and version manifest |
| `JUNO_ADDRGEN_REPO` | `junocash-tools/juno-addrgen` | Direct gateway-image build argument |
| `JUNO_SCAN_REF` | `3f6009fe16d15faa0da5a8962e0f5dad30307135` | Scanner commit recorded in the version manifest |
| `JUNO_TXBUILD_REF` | `05fb761c92ccb9a5da1cafec1e56c3cdca7ca20a` | Planner source and version manifest |
| `JUNO_TXBUILD_REPO` | `junocash-tools/juno-txbuild` | Direct planner-image build argument |
| `JUNO_TXSIGN_REF` | `121ef749383e27fb82c302e96cd55b70611817fc` | Offline signer source and published image |
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
| `JUNO_GATEWAY_STATE_DSN` | SQLite under `/var/lib/juno-gateway` | Address, cursor-key, broadcast-idempotency, attempt, and note-reservation state |
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
| `JUNO_GATEWAY_MAX_CONFIRMATIONS` | `10000` | Maximum request override; startup rejects values above the API hard ceiling of `10000` |
| `JUNO_GATEWAY_MAX_SCANNER_LAG` | `2` | Readiness lag limit |
| `JUNO_GATEWAY_REQUIRE_COMPLETE_HISTORY` | `true` | Require an explicit scanner `history_complete: true` attestation; keep enabled in production |
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
| `JUNO_GATEWAY_NOTE_SUMMARY_MAX_NOTES` | `100000` | Fail-closed cap for one wallet aggregate note summary; valid range `1` to `1000000` |

Startup enforces these tuning constraints:

- listen address and state DSN are non-empty; the installation-state path is an absolute file path; node and scanner URLs are valid HTTP(S) URLs; at least one wallet is registered
- scanner lag is non-negative; ordinary and broadcast body limits are each at least `1024` bytes
- default and maximum confirmations are positive, the default cannot exceed the maximum, and the maximum cannot exceed `10000`
- all request, upstream, shutdown, HTTP, backfill, and rate-limit values are positive; the idempotency lease is at least `1s`
- HTTP read timeout is at least the read-header timeout, and HTTP write timeout is strictly greater than the broadcast timeout
- backfill batch size and wallet-effects event cap are each `1` to `100000`; the note-summary cap is `1` to `1000000`
- outside regtest, node RPC user/password and a scanner token are mandatory; the two secrets must be distinct, non-placeholder values of at least 24 characters
- outside regtest, both the gateway database and installation manifest must use persistent, non-memory paths

Rate buckets are separate and keyed by credential plus client IP. Set `JUNO_GATEWAY_TRUST_PROXY_HEADERS=true` only when direct access is blocked and a trusted proxy overwrites forwarding headers.

A credential `name` is also its durable broadcast-idempotency namespace. Rotate its token without changing the name. Before renaming a broadcast principal, reconcile every outstanding withdrawal attempt because the new name creates a new namespace.

## Private transaction coordinator

The gateway binary and base stack keep the coordinator disabled. `compose.automation.yaml` enables it and publishes a separate loopback port. Put any remote access behind a private TLS/mTLS path. The coordinator uses `JUNO_GATEWAY_DEFAULT_CONFIRMATIONS` for planner minimum confirmations and finality/release proof; the production default is `100`.

| Variable | Binary / automation default | Purpose |
| --- | --- | --- |
| `JUNO_COORDINATOR_ENABLED` | `false` / `true` | Enable the private creation API and recovery workers |
| `JUNO_COORDINATOR_BIND` | overlay: `127.0.0.1` | Host interface published by Compose; keep loopback unless a private proxy/network policy protects it |
| `JUNO_COORDINATOR_PORT` | overlay: `8081` | Host port published by Compose |
| `JUNO_COORDINATOR_LISTEN` | `127.0.0.1:8081` / `0.0.0.0:8081` in container | Private HTTP listener; must differ from `JUNO_GATEWAY_LISTEN` |
| `JUNO_COORDINATOR_TXBUILD_PATH` | `/usr/local/bin/juno-txbuild` | Absolute planner executable path |
| `JUNO_COORDINATOR_SIGNER_SOCKET` | `/run/juno-signer/signer.sock` | Absolute owner-only `juno-txsign serve-txplan` Unix socket |
| `JUNO_COORDINATOR_WORK_DIR` | `/var/lib/juno-gateway/coordinator-work` | Absolute mode-`0700` transient planner workspace |
| `JUNO_COORDINATOR_PLAN_TIMEOUT` | `2m` | Deadline for change allocation and each planner run |
| `JUNO_COORDINATOR_SIGN_TIMEOUT` | `10m` | Deadline for one signer-journal request; timeout becomes `signing_unknown`, never a release |
| `JUNO_COORDINATOR_MAX_BODY_BYTES` | `1048576` | Creation JSON limit; valid range 1 KiB to 8 MiB |
| `JUNO_COORDINATOR_MAX_OUTPUTS` | `199` | Maximum requested outputs, range 1 to 199; one action remains available for change |
| `JUNO_COORDINATOR_MAX_AMOUNT_ZAT` | `2100000000000000` | Maximum sum of requested output amounts per attempt |
| `JUNO_COORDINATOR_EXPIRY_OFFSET` | `40` | Expiry blocks after the planner's next height; minimum 4 |
| `JUNO_COORDINATOR_FEE_MULTIPLIER` | `20` | Multiplier applied to the planner base fee; must be positive |
| `JUNO_COORDINATOR_FEE_ADD_ZAT` | `0` | Additional fee in zatoshi |
| `JUNO_COORDINATOR_MIN_NOTE_ZAT` | `0` | Exclude smaller otherwise-eligible inputs; zero disables the floor |
| `JUNO_COORDINATOR_MIN_CHANGE_ZAT` | `0` | Add smaller change to the fee instead of creating a change note |
| `JUNO_COORDINATOR_MAX_REPLANS` | `3` | Fresh selection attempts after reservation races; valid range 1 to 20 |
| `JUNO_COORDINATOR_RATE_RPS` | `5` | Per-credential private-API bucket refill per second |
| `JUNO_COORDINATOR_RATE_BURST` | `10` | Per-credential private-API bucket capacity |

The work directory is not a ledger or backup. Exact approved plan bytes, digests, attempt states, signed results, and active note reservations are durable in the gateway state database. Temporary planner files are owner-only and removed after each run.

`JUNO_COORDINATOR_ENABLED` cannot bypass the post-recovery creation seal. After reconstructing a lost gateway database, follow the audited reconciliation and `recovery-unseal-coordinator` procedure; there is intentionally no environment toggle for it.

The planner receives node/scanner credentials through its environment, never command arguments. The coordinator sends the signer only `attempt_id`, exact plan bytes, and `sha256:<64-lowercase-hex>` over the Unix socket. It never receives or stores the seed.

### Signer overlay

The overlay starts `txsign` with no Docker network, a read-only root, a read-only base64 seed file, a read-only bindings file, an owner-only socket, and a persistent immutable-result journal.

| Variable | Default | Purpose |
| --- | --- | --- |
| `JUNO_TXSIGN_SEED_FILE` | `./config/seed.b64` | Host base64 seed file; regular, non-symlink, mode `0400` or `0600`, owned by the deployment account; never commit it |
| `JUNO_TXSIGN_BINDINGS_FILE` | `./config/signer-bindings.json` | Host wallet/account/network/UFVK allowlist; mode `0400` or `0600`, owned by the deployment account |
| `JUNO_TXSIGN_MAX_CONCURRENCY` | `1` | Active signings; keep one unless capacity and custody policy justify more, maximum 16 |
| `JUNO_TXSIGN_MAX_BODY_BYTES` | `5242880` | UDS HTTP request limit |
| `JUNO_TXSIGN_MAX_PLAN_BYTES` | `3145728` | Decoded exact TxPlan limit; must fit inside the body limit |
| `JUNO_TXSIGN_SHUTDOWN_TIMEOUT` | `10m` | Time allowed for an in-flight durable result before shutdown |

Create `signer-bindings.json` from the exact watch-only wallet configuration:

```json
{
  "version": "v1",
  "bindings": [
    {
      "wallet_id": "hot",
      "account": 0,
      "network": "mainnet",
      "ufvk": "jview1..."
    }
  ]
}
```

The container entrypoint accepts both read-only host files at mode `0400` or `0600`, copies them into private tmpfs as UID:GID `10001:10001` at mode `0600`, drops capabilities, and starts the signer. The signer derives the UFVK from its seed for every configured network/account and compares it with each exact binding before opening the socket. A wrong seed, wallet ID, account, network, or UFVK fails closed. Share only the socket volume with the gateway; the seed, bindings, and journal mounts belong only to `txsign`.

Enabling the coordinator also requires:

- at least one credential with `plan` or `admin` and a wallet grant, including on regtest
- an executable planner path, available signer socket, persistent gateway state, and a distinct private listener
- one active gateway/coordinator process for the SQLite database; do not run independent active replicas
- the same network, UFVK account, node, scanner, and confirmation policy across all components

See [Build, sign, and broadcast](../transactions/build-sign-broadcast.md) for the integration flow and [Private coordinator API](../reference/coordinator-http.md) for the wire contract.

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

Readiness requires `JUNO_SCAN_CONFIRMATIONS` to be positive and exactly equal to `JUNO_GATEWAY_DEFAULT_CONFIRMATIONS`. It also requires scanner `ready=true`, `pending_spends_ready=true`, and scanner-reported lag equal to the lag independently derived from the gateway node tip and scanner height. The pending-spend attestation is emitted only after a successful mempool reconciliation for the current event epoch and exact scanner tip, so a restart or reorg cannot briefly expose stale note availability. This prevents deposit events and default balance reads from using different finality policies or inconsistent chain positions.

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

Each wallet requires a unique `wallet_id`, a unique network-matching `ufvk`, a non-negative `birthday_height`, and the signer account (`account`, default `0`, below `2147483648`).

Wallet IDs and credential names must match `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`. A UFVK is limited to `4096` bytes. Both `wallets.json` and `auth.json` must be owner-only regular files, not symlinks, contain exactly one JSON object, reject unknown fields, and be no larger than `1 MiB`; use mode `0600`.

| Network | Required UFVK prefix |
| --- | --- |
| `mainnet` | `jview1...` |
| `testnet` | `jviewtest1...` |
| `regtest` | `jviewregtest1...` |

Keep the earliest reliable birthday; a later value can miss deposits during recovery. Wallet IDs are durable API and ledger identities, not display names.

The one-time `init` binds the exact wallet set, IDs, UFVK fingerprints, birthdays, accounts, and network into the installation manifest. Any later addition, removal, or identity change fails startup. Legacy v1 manifests remain valid only when every configured account is `0`; a nonzero account requires a v2 installation identity. There is no post-init wallet-onboarding operation; prepare every required wallet first. See [Wallet and authentication setup](../getting-started/wallet-and-auth.md).

## Credential file

Each credential has a unique `name`, exactly one of `token` or lowercase `token_sha256`, at least one scope, and at least one wallet. Plain tokens must be at least 24 characters; hashes are preferred. Every plaintext token and effective SHA-256 token hash must also be unique across credential names. Startup rejects duplicates because each name defines a distinct authorization and broadcast-idempotency principal.

The tracked auth example separates reader, allocator, treasury, and broadcaster duties. Its repeated-digit hashes are placeholders rejected outside regtest. Replace each enabled credential with the hash of its own strong token, and remove credentials the deployment does not use.

| Scope | Access |
| --- | --- |
| `read` | readiness, version, tip, balances, deposits, and node-only transaction lookup |
| `address` | allocate deposit addresses |
| `broadcast` | submit signed raw transactions |
| `plan` | create, poll, and cancel private coordinator attempts |
| `treasury` | aggregate note summary plus batch status for selected IDs already recorded in a withdrawal or consolidation attempt; no nullifiers, memos, addresses, or wallet-wide raw-note list |
| `withdrawal` | wallet-enriched transaction lookup and sanitized wallet effects; combine with `read` |
| `raw` | include raw hex in transaction lookup |
| `admin` | satisfies any endpoint scope; wallet restrictions still apply |

`read` does not imply `plan`, `treasury`, or `withdrawal`. Give `plan` only to the approved withdrawal service, use a token separate from public broadcast, and use explicit wallet IDs. Reserve `"*"` for tightly controlled operational credentials.

Credential files are loaded at startup. Presented tokens are SHA-256 hashed and compared in constant time with the stored effective hash. Rotate a token under the same credential `name` and recreate the gateway container. The name is the coordinator creator/owner and a durable creation/broadcast idempotency namespace; changing it prevents access to old coordinator attempts and can make an old broadcast request appear new. For an overlapping non-financial rotation, use a temporary second name, switch clients, then remove the old credential.

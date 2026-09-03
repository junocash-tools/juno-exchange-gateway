---
title: Docker operations
---

The default appliance runs `junocashd`, `juno-scan`, and the gateway. Only the gateway port is published.

Base Compose pulls the published GHCR `main` images, so a fresh host does not need source checkouts. Image publishing also creates commit-addressed `sha-<gateway-commit>` and release-version tags. The scanner image is built from the exact scanner commit recorded by the release workflow.

## Host compatibility

The complete appliance is release-tested for Linux AMD64. Gateway, scanner, planner, and signer images are also published for Linux ARM64, but the official `junocashd` archive is AMD64-only. ARM64 hosts therefore require AMD64 emulation for the node and are not a native production target until an official ARM64 node artifact is available.

## Storage modes

Before the first start, create the external installation-state bind and run one-time init:

```bash
sudo chown 10001:10001 "$JUNO_GATEWAY_WALLETS_FILE" "$JUNO_GATEWAY_AUTH_FILE"
sudo chmod 0600 "$JUNO_GATEWAY_WALLETS_FILE" "$JUNO_GATEWAY_AUTH_FILE"
sudo install -d -o 10001 -g 10001 -m 0700 "$JUNO_INSTALLATION_STATE_DIR"
docker compose run --no-deps --rm gateway init \
  --acknowledge I_UNDERSTAND_THIS_CREATES_A_NEW_JUNO_INSTALLATION
```

The fixed gateway runtime identity is UID:GID `10001:10001`. The commands above make Linux bind mounts usable without a root runtime or broad permissions. With Docker user-namespace remapping, substitute the mapped host IDs.

Do not run `init` for an existing installation. Use the recovery procedure after database loss.

RocksDB is the default single-host scanner store:

```bash
docker compose up -d
```

Postgres is recommended for production:

```bash
docker compose -f compose.yaml -f compose.postgres.yaml up -d
```

The Postgres override creates a private database service and persistent volume. MySQL is not a supported appliance mode.

Run one active gateway allocator for each installation manifest and SQLite database. Do not use active-active replicas, shared NFS state, or independent copies of the same manifest. The scanner can use production Postgres, but gateway failover is cold or warm standby only: restore a consistent gateway database and manifest plus the exchange's external address high-water marks, then reconcile before enabling allocation or broadcast traffic.

## Local builds

```bash
docker compose -f compose.yaml -f compose.dev.yaml build
docker compose -f compose.yaml -f compose.dev.yaml up -d
```

Tags can move. The `main` tag is development/evaluation only. Production deployments must set every `JUNO_*_IMAGE` to a verified immutable digest before `docker compose up -d`.

## Verify a release

Download `juno-exchange-gateway-<version>.tar.gz`, `release-provenance.json`, and `SHA256SUMS` from the GitHub Release. Verify and extract the digest-pinned appliance:

```bash
sha256sum --check --ignore-missing SHA256SUMS
tar -xzf juno-exchange-gateway-<version>.tar.gz
cd juno-exchange-gateway-<version>
cp config/wallets.example.json config/wallets.json
cp config/auth.example.json config/auth.json
chmod 0600 release.env config/wallets.json config/auth.json
# Edit release.env and both JSON files: select the network, set strong distinct
# credentials, replace example wallet/auth data, and confirm host paths.
sudo chown 10001:10001 config/wallets.json config/auth.json
sudo install -d -o 10001 -g 10001 -m 0700 installation-state
docker compose --env-file release.env run --no-deps --rm gateway init \
  --acknowledge I_UNDERSTAND_THIS_CREATES_A_NEW_JUNO_INSTALLATION
docker compose --env-file release.env up -d
```

For production scanner Postgres, add `-f compose.yaml -f compose.postgres.yaml` to both Compose commands and set its password in `release.env`.

Inspect each image by its release tag. Confirm its registry digest matches `release-provenance.json` before pinning `image@sha256:...` in the deployment environment:

```bash
IMAGE=ghcr.io/junocash-tools/juno-exchange-gateway-gateway:1.0.0
docker buildx imagetools inspect "$IMAGE" --format '{{json .Manifest}}' | jq .
```

Buildx can also read the attached BuildKit provenance and SPDX SBOM:

```bash
docker buildx imagetools inspect "$IMAGE" --format '{{json .Provenance}}' | jq .
docker buildx imagetools inspect "$IMAGE" --format '{{json .SBOM}}' | jq .
```

These attestations describe the build and packages; they are not release signatures. Verify the image digest, pinned source revisions, and gateway/scanner schema locks in the release manifest.

Release Dockerfiles pin every base tag to an OCI digest. To update a base, inspect the tag, review the new index, then replace the matching `tag@sha256:...` references in one change:

```bash
BASE=debian:bookworm-slim
docker buildx imagetools inspect "$BASE" --format '{{json .Manifest}}' | jq -r .digest
```

Use the multi-platform index digest for images built for both AMD64 and ARM64. Re-run the release tests after every base-image update.

## Routine commands

```bash
docker compose ps
docker compose logs --since=15m gateway juno-scan junocashd
docker compose restart gateway
docker compose down
```

`docker compose down` keeps named volumes. Do not add `--volumes` during routine operations.

The installation-state directory is a host bind, not a Compose volume. Keep it on durable storage and back it up privately after address allocations.

The `txbuild` service runs only on demand as an operator fallback:

```bash
docker compose --profile operator run --rm txbuild --help
```

## Automated withdrawal overlay

The base stack remains watch-only and signer-free. Enable automated creation with the dedicated overlay:

```bash
cp config/signer-bindings.example.json config/signer-bindings.json
# Copy the base64 seed from the isolated key workflow to config/seed.b64.
# Make signer-bindings.json exactly match wallets.json wallet/account/network/UFVK.
chmod 0400 config/seed.b64 config/signer-bindings.json

docker compose -f compose.yaml -f compose.automation.yaml run --no-deps --rm gateway init \
  --acknowledge I_UNDERSTAND_THIS_CREATES_A_NEW_JUNO_INSTALLATION
docker compose -f compose.yaml -f compose.automation.yaml up -d
```

Never create a second installation when enabling automation on an initialized base stack; omit the `init` command in that case. Never commit `seed.b64` or the populated bindings file.

This keeps the coordinator inside the gateway process on its distinct private listener and starts `txsign` as a long-running `serve-txplan` service. The signer has no Docker network. Its entrypoint accepts owner-only host seed/bindings at mode `0400` or `0600`, copies them into private tmpfs as UID:GID `10001:10001` at mode `0600`, drops capabilities, shares only an owner-only Unix-socket volume with the gateway, and stores its immutable journal on persistent owner-only storage. The gateway/coordinator remains seedless.

Use the same file pair for `init`, `ps`, `logs`, restart, and shutdown commands. Give the coordinator a `plan` credential and expose its listener only to the exchange's private HTTPS/mTLS path. Do not publish the signer socket, mount it into the exchange application, or add the signer to a TCP network.

For a source build, put the development override last and enable its automation profile:

```bash
docker compose \
  -f compose.yaml -f compose.automation.yaml -f compose.dev.yaml \
  --profile automation up -d --build
```

Before enabling the overlay, back up the seed, signer journal, gateway database, installation manifest, and exchange withdrawal ledger. A lost journal or attempt database cannot be reconstructed safely from scanner state.

## One-shot signer fallback

For controlled maintenance or incident fallback, invoke the same signer image with networking disabled, a read-only root, and narrow read-only input mounts:

```bash
SIGN_OUTPUT_DIR="$PWD/tmp/withdrawal-1842/direct"
test "$(id -u)" -ne 0
mkdir -m 0700 "$SIGN_OUTPUT_DIR"
docker run --rm --network none --read-only --cap-drop ALL \
  --security-opt no-new-privileges \
  --user "$(id -u):$(id -g)" \
  --tmpfs /tmp:rw,noexec,nosuid,size=64m \
  -v "$PWD/plan.json:/work/plan.json:ro" \
  -v "$PWD/seed:/work/seed:ro" \
  -v "$SIGN_OUTPUT_DIR:/work/output:rw" \
  "$JUNO_TXSIGN_IMAGE" sign \
  --txplan /work/plan.json --seed-file /work/seed \
  --out /work/output/rawtx.hex \
  --out-result /work/output/signed.json \
  --action-indices --json > /dev/null
```

Run this from a dedicated non-root signer OS account that owns mode-`0400` or `0600` inputs and the mode-`0700` output directory; `--user` maps the container to it. Never use UID `0`. Every fallback attempt and output path is one-shot. Pause coordinator creation for that wallet, wait for active attempts to become terminal, and do not mix a CLI plan with coordinator reservations. The signer refuses overwrite and leaves a fail-closed pending marker after an uncertain output commit. Follow [Build, sign, and broadcast](../transactions/build-sign-broadcast.md) for validation and recovery rules.

The gateway joins the internal backend and a dedicated ingress bridge. The base stack publishes only the public gateway port; the automation overlay adds the separately bound coordinator port on the same seedless gateway container. The ingress bridge disables Docker's default outbound masquerading and inter-container communication. This is hardening, not an egress firewall. Enforce outbound policy at the host or infrastructure layer. Scanner joins backend and the separate internal storage network; Postgres joins storage only. Node, scanner, signer, and databases have no TCP host ports.

## Regtest validation

`make test` runs unit checks, Compose validation, the docs build, a reserved-character Postgres connection smoke test, and the full regtest deposit, withdrawal, reorg, scanner-recovery, and gateway-loss guard. The release E2E requires clean gateway, scanner, address, planner, signer, and key-tool checkouts so recorded commits match tested binaries. Signing runs in a container with Docker networking disabled. Private evidence and disposable seeds stay under ignored `tmp/` paths with owner-only permissions.

The bundled `junocashd` 0.9.13 entrypoint activates NU6.2 at regtest height 1 with `-nuparams=5437f330:1`. Its default regtest schedule otherwise remains on NU6.1, while the shipped Orchard PCZT proving flow requires NU6.2. The E2E reads `getblockchaininfo` and requires `consensus.chaintip` and `consensus.nextblock` to be `5437f330` before each signing step.

This is a fixed, regtest-only compatibility setting, not an operator toggle. The entrypoint does not pass `-nuparams` on testnet or mainnet; those networks must follow the node's built-in activation schedules.

Set `KEEP_STACK=1` only when diagnosing a failed local run. `SKIP_GATEWAY_LOSS_TEST=1` skips the destructive gateway-loss guard and is for focused debugging, not release validation.

## Exposure

The public default is `127.0.0.1:8080`; the automation coordinator default is `127.0.0.1:8081`. To serve remote traffic, place separate TLS/mTLS policy in front of each authorized path. Set a non-loopback `JUNO_COORDINATOR_BIND` only on a private exchange network, never on general public ingress. Never publish node RPC, ZMQ, scanner, signer, or Postgres ports.

Use Docker Engine 28 or newer for the localhost publishing boundary. On older engines, a localhost-published port may be reachable from the same layer-2 segment; upgrade or enforce a host firewall before deployment.

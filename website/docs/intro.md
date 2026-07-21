---
slug: /
title: Quickstart
description: Run the Juno Exchange Gateway appliance and make the first API call.
---

The appliance exposes one watch-only HTTP API for exchange deposits and withdrawals. It supports Juno Cash mainnet, testnet, and regtest.

## Before you start

Prepare:

- Docker Engine with Compose v2
- one UFVK and its birthday height
- a scoped bearer token outside regtest
- persistent storage for the node, scanner, and gateway state
- a separate host directory for installation state

The online appliance never needs a seed or spending key.

## Configure a wallet

Create `wallets.json` with mode `0600`:

```json
{
  "wallets": [
    {
      "wallet_id": "hot",
      "ufvk": "jviewregtest1...",
      "birthday_height": 0
    }
  ]
}
```

Create `auth.json` with mode `0600`. Mainnet and testnet require at least one credential:

```json
{
  "credentials": [
    {
      "name": "exchange-api",
      "token_sha256": "<64-lowercase-hex-sha256>",
      "scopes": ["read", "address", "broadcast"],
      "wallets": ["hot"]
    }
  ]
}
```

An isolated anonymous regtest may use `{"credentials":[]}`. The file is still required by Compose.

Generate the hash without storing the token in shell history:

```bash
read -rsp 'Bearer token: ' TOKEN; echo
printf %s "$TOKEN" | shasum -a 256
unset TOKEN
```

## Start the appliance

Set the host file paths and network in `.env`, then start:

```bash
JUNO_NETWORK=regtest
JUNO_GATEWAY_WALLETS_FILE=/absolute/path/wallets.json
JUNO_GATEWAY_AUTH_FILE=/absolute/path/auth.json
JUNO_INSTALLATION_STATE_DIR=/absolute/path/installation-state
JUNO_GATEWAY_BIND=127.0.0.1
JUNO_GATEWAY_PORT=8080
```

Create the installation-state directory once, then initialize it. The acknowledgement must match exactly.

```bash
sudo chown 10001:10001 /absolute/path/wallets.json /absolute/path/auth.json
sudo chmod 0600 /absolute/path/wallets.json /absolute/path/auth.json
sudo install -d -o 10001 -g 10001 -m 0700 /absolute/path/installation-state
docker compose run --no-deps --rm gateway init \
  --acknowledge I_UNDERSTAND_THIS_CREATES_A_NEW_JUNO_INSTALLATION
docker compose up -d
docker compose ps
```

The gateway image runs as UID:GID `10001:10001`. Keep all three paths owner-only and owned by that identity. If Docker user-namespace remapping is enabled, use its mapped host UID/GID instead. Never use `0644` or `0777` as a workaround.

`init` succeeds once. Normal serving fails before init and after gateway database loss until an audited recovery is completed. Back up the installation-state directory privately and separately from Compose volumes.

For a local source build:

```bash
docker compose -f compose.yaml -f compose.dev.yaml build
docker compose -f compose.yaml -f compose.dev.yaml run --no-deps --rm gateway init \
  --acknowledge I_UNDERSTAND_THIS_CREATES_A_NEW_JUNO_INSTALLATION
docker compose -f compose.yaml -f compose.dev.yaml up -d
```

Use immutable image digests in production. See [Docker operations](operations/docker.md).

## Check readiness

```bash
export GATEWAY_URL=http://127.0.0.1:8080
export GATEWAY_TOKEN='<bearer-token>'

curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/health/ready"
```

Regtest permits anonymous access only when no credentials are configured. Keep authentication enabled whenever the port is reachable by another host.

Next: [allocate a deposit address](capabilities/address-allocation.md), then [poll deposits](capabilities/deposits.md).

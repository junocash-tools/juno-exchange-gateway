---
title: Wallet and authentication setup
---

Set up wallet identity and API credentials before the one-time gateway `init`. The online appliance needs a UFVK, never a seed or spending key.

## 1. Choose the wallet identity

For each wallet, choose:

- a stable `wallet_id` used by the API and exchange ledger
- the UFVK for the configured network
- the earliest reliable birthday height

| Network | UFVK prefix |
| --- | --- |
| Mainnet | `jview1...` |
| Testnet | `jviewtest1...` |
| Regtest | `jviewregtest1...` |

The complete wallet set, wallet IDs, UFVK fingerprints, and birthdays are sealed into the installation manifest by `init`. Adding, removing, or changing a wallet later makes startup fail. Prepare every required wallet before initialization.

Use a birthday at or before the wallet's first possible receipt. An earlier height only costs scan time; a later height can permanently omit historical deposits until the scanner is rebuilt with the correct birthday.

## 2. Derive the UFVK offline

In an isolated environment, use a separately verified `juno-keys` build:

```bash
umask 077
juno-keys seed new --out ./hot.seed
juno-keys --json ufvk from-seed \
  --seed-file ./hot.seed \
  --network mainnet
```

Example response:

```json
{
  "version": "v1",
  "status": "ok",
  "data": {
    "ufvk": "jview1...",
    "ua_hrp": "j",
    "coin_type": 8133,
    "account": 0
  }
}
```

Copy only `data.ufvk` into the wallet file. Confirm `ua_hrp`, `coin_type`, and `account` match the intended signing policy before moving it to the online host.

Use `--network testnet` or `--network regtest` for those networks. Verify the reported HRP, coin type, account, and UFVK prefix before transfer.

The seed is spending authority. Keep it offline and back it up according to the exchange key-management policy. Transfer only the UFVK to the gateway host. Do not copy, mount, or enter the seed in the gateway, scanner, node, or planner containers.

## 3. Write the wallet file

Create `wallets.json`:

```json
{
  "wallets": [
    {
      "wallet_id": "hot",
      "ufvk": "jview1...",
      "birthday_height": 910000
    }
  ]
}
```

Wallet IDs and UFVKs must be unique. Use a network-matching UFVK and a non-negative birthday.

## 4. Create least-privilege credentials

Generate each bearer token in a secret manager or other protected environment. Put only its lowercase SHA-256 hash in `auth.json`:

```bash
read -rsp 'Bearer token: ' TOKEN; echo
printf %s "$TOKEN" | shasum -a 256
unset TOKEN
```

Example:

```json
{
  "credentials": [
    {
      "name": "deposit-reader",
      "token_sha256": "<64-lowercase-hex-sha256>",
      "scopes": ["read"],
      "wallets": ["hot"]
    },
    {
      "name": "address-allocator",
      "token_sha256": "<64-lowercase-hex-sha256>",
      "scopes": ["address"],
      "wallets": ["hot"]
    },
    {
      "name": "withdrawal-monitor",
      "token_sha256": "<64-lowercase-hex-sha256>",
      "scopes": ["read", "withdrawal"],
      "wallets": ["hot"]
    }
  ]
}
```

| Scope | Use |
| --- | --- |
| `read` | readiness, version, tip, owned-address balances, deposits, and node-only transaction lookup |
| `address` | allocate deposit addresses |
| `broadcast` | submit a signed raw transaction |
| `treasury` | call the [wallet-level aggregate note summary](../capabilities/note-summary.md) for consolidation; does not expose raw notes |
| `withdrawal` | add a wallet ID and sanitized wallet effects to transaction lookup; must be combined with `read` |
| `raw` | add raw transaction hex to transaction lookup |
| `admin` | satisfy any operation scope; wallet restrictions still apply |

`treasury` and `withdrawal` are intentionally separate from ordinary reads. Give them only to the services that need wallet-wide liquidity or withdrawal effects. Avoid `raw`, `treasury`, `withdrawal`, and `admin` for ordinary deposit readers.

Every credential needs a unique, stable `name`, one token or hash, at least one scope, and at least one wallet. Its bearer secret must also be unique across credential names. This applies to plaintext `token` values and to the effective SHA-256 values stored as `token_sha256`. Startup rejects duplicates because the credential name defines the authorization and broadcast-idempotency principal. Prefer explicit wallet IDs; reserve `"*"` for tightly controlled operations.

Outside regtest, authentication is mandatory. An isolated regtest can set `{"credentials":[]}`, which grants anonymous access to all operations. Never use anonymous mode on a reachable port.

Send the plaintext token only in `Authorization: Bearer <token>` over TLS. The gateway compares its SHA-256 hash and never returns the credential. Missing, malformed, or unknown credentials return `401 unauthorized`; a valid credential without the required scope or wallet grant returns `403 forbidden`. Do not retry either response unchanged.

## 5. Initialize and verify

Set the host paths in `.env`, protect the files, initialize once, and start:

```bash
sudo chown 10001:10001 /absolute/path/wallets.json /absolute/path/auth.json
sudo chmod 0600 /absolute/path/wallets.json /absolute/path/auth.json
sudo install -d -o 10001 -g 10001 -m 0700 /absolute/path/installation-state

docker compose run --no-deps --rm gateway init \
  --acknowledge I_UNDERSTAND_THIS_CREATES_A_NEW_JUNO_INSTALLATION
docker compose up -d
```

Wait for authenticated readiness, then allocate the first address:

```bash
export GATEWAY_URL=https://juno-gateway.example.com
export GATEWAY_READ_TOKEN='<deposit-reader-token>'
export GATEWAY_ADDRESS_TOKEN='<address-allocator-token>'

curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_READ_TOKEN" \
  "$GATEWAY_URL/v1/health/ready"

curl --fail-with-body -X POST \
  -H "Authorization: Bearer $GATEWAY_ADDRESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"label":"customer-1842"}' \
  "$GATEWAY_URL/v1/wallets/hot/addresses"
```

Persist the returned address, wallet ID, and diversifier index in the exchange ledger before giving the address to a customer.

## Token rotation

Configuration is loaded at process start. To rotate a token without changing the broadcaster's durable idempotency namespace:

1. replace `token_sha256` under the same credential `name`
2. recreate the gateway container
3. switch the client to the new token at that cutover
4. verify the old token is rejected

For overlapping `read`, `address`, `treasury`, or `withdrawal` rotation, add a temporary credential with a different name, move clients, then remove the old credential. A different broadcaster name creates a different idempotency namespace; drain and reconcile outstanding withdrawal attempts before using that pattern for `broadcast`.

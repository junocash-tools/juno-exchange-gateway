---
title: Security
---

## Network controls

- expose only the gateway
- terminate HTTPS at a maintained reverse proxy or load balancer
- prefer mTLS between the exchange and proxy
- block direct access when proxy headers are trusted
- keep node RPC, scanner, ZMQ, and databases on private networks
- use the supplied ingress bridge to disable Docker's default outbound masquerading and inter-container communication
- enforce outbound-deny policy at the host or infrastructure layer; disabling masquerading is not an egress firewall
- private node and scanner clients ignore ambient HTTP proxies and reject redirects

The gateway serves HTTP itself; TLS and mTLS are edge responsibilities.

## Authentication

Mainnet and testnet refuse to start without credentials and reject the shipped RPC/scanner placeholders. Use long random bearer tokens, store only SHA-256 hashes in `auth.json`, and rotate credentials by recreating the gateway container with the updated file.

Give separate credentials to deposit readers, address allocators, and broadcasters. Limit each credential to required wallets. Do not grant `raw` or `admin` by default.

## Secrets and keys

The online stack must never receive a seed, mnemonic, spending key, or signer share. Keep offline signer backups encrypted and geographically separated. A UFVK is watch-only but privacy-sensitive.

Do not put tokens, UFVKs, raw transaction bodies, or plans in access logs. Mount wallet and auth files read-only with mode `0600` on the host. The gateway sets process umask `0077` before opening state.

Keep the installation-state directory mode `0700` and its manifest mode `0600`. It stores installation identity, UFVK fingerprints, and address high-water marks, never raw UFVKs or keys. Treat it as integrity-critical and back it up privately, separately from gateway and scanner databases.

## Request controls

The gateway rejects unknown JSON fields, enforces body limits, has separate read/broadcast rate buckets, and emits a request ID. Preserve `X-Request-ID` across the proxy for incident correlation.

Broadcast additionally requires a payload-bound idempotency key and expected txid. This limits retries; it does not replace withdrawal authorization or offline review.

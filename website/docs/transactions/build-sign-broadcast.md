---
title: Build, sign, and broadcast
---

Production withdrawals are automated through the Node.js SDK:

1. the exchange authorizes a withdrawal and asks the **private coordinator** for signed raw hex
2. the exchange durably stores and checks the signed result
3. the exchange sends the exact bytes to the **public gateway** broadcast API
4. the exchange tracks the txid through mining, finality, expiry, and reorgs

The exchange application never runs Docker or a console command per withdrawal.

## Why two APIs?

Transaction creation can spend funds, so it is an API—but not a public one. The coordinator accepts destinations and amounts on a separate private listener protected by the `plan` scope and wallet grants. It selects and reserves notes, runs the planner, and talks to an isolated signer over an owner-only Unix socket.

The public gateway remains watch-only. It accepts only already-signed bytes for broadcast. This split lets the exchange persist and verify the exact signed result before network submission and prevents an internet-facing service from reaching spending authority.

```text
Exchange withdrawal service
  ├── private HTTPS/mTLS ──> coordinator ──UDS──> network-disabled signer
  │                              │
  │                              ├──> planner
  │                              ├──> scanner/node reads
  │                              └──> durable attempts + note reservations
  └── public HTTPS/mTLS ───> gateway broadcast + transaction lookup
```

## Before the first call

1. Derive the seed and UFVK in the isolated key environment.
2. Register the UFVK, birthday, and signer account under a stable wallet ID in `wallets.json`, then run the one-time gateway `init`.
3. Create separate owner-only bearer credentials: `plan` for the private coordinator and `broadcast` for the public gateway, each granted only the required wallet.
4. Start the `compose.automation.yaml` overlay with persistent gateway state and signer journal storage.
5. Require authenticated `GET /v1/health/ready` success from both listeners before accepting withdrawals.

See [Wallet and authentication setup](../getting-started/wallet-and-auth.md) and [Docker operations](../operations/docker.md).

## 1. Create signed raw hex

Install the supported Node.js package:

```bash
npm install https://github.com/junocash-tools/juno-exchange-sdk/releases/download/v0.1.0/junocash-tools-exchange-sdk-0.1.0.tgz
```

Node.js 20 or later is required. The versioned GitHub Release archive is the supported public distribution. Configure the SDK for the same `mainnet`, `testnet`, or `regtest` network as the coordinator.

```js
import {CoordinatorClient} from '@junocash-tools/exchange-sdk';

const coordinator = new CoordinatorClient({
  baseUrl: process.env.JUNO_COORDINATOR_URL,
  authToken: process.env.JUNO_COORDINATOR_TOKEN,
  network: 'mainnet',
});

const signed = await coordinator.createRawTransaction({
  idempotencyKey: 'withdrawal-1842-attempt-1',
  walletId: 'hot',
  approvalReference: 'withdrawal:1842',
  toAddress: withdrawal.address,
  amountZat: withdrawal.amountZat,
  memoHex: withdrawal.memoHex,
});
```

`createRawTransaction` creates one attempt and polls until signed material is durable. An idempotent replay returns that material in `signed`, `broadcast`, `mined`, `orphaned`, or `final`. It rejects `expired_pending_reconciliation`, `released`, `failed_unsigned`, and `cancelled`, because those states are not safe instructions to submit old bytes. Its default wait is 10 minutes with one-second polling. A local timeout does **not** cancel the server attempt; save the attempt ID from the lower-level flow or retry the same creation key and payload.

After a gateway database reconstruction, the SDK reports `coordinator_recovery_sealed` and no attempt is created. Reconcile the pre-loss withdrawal ledger and selected notes, complete the [audited coordinator unseal](../operations/recovery.md#gatewaycoordinator-database-recovery), then retry the same key and request.

Start from the SDK's runnable [`create-raw-transaction.mjs`](https://github.com/junocash-tools/juno-exchange-sdk/blob/main/examples/create-raw-transaction.mjs) example. It emits a JSON record containing the wallet, approval reference, state, selected note IDs, and signed transaction material for your withdrawal store.

Use `walletId`, not `addressFrom`. A shielded transaction consumes notes controlled by the registered wallet UFVK. It does not debit a visible source address. The coordinator chooses inputs and a registered same-wallet change address.

Amounts are zatoshis. Pass a canonical decimal string or JavaScript `bigint`; the SDK rejects `number` to avoid rounding. A memo is optional lowercase hex encoding of at most 512 bytes.

Calling create is the exchange's authorization to sign. Perform withdrawal approval, sanctions/risk controls, balance reservation, destination validation, limits, and dual control **before** this call. Store a stable approval identifier in `approvalReference`.

## 2. Persist and verify the result

`signed.rawTxHex` is signed and ready for broadcast. Before broadcasting, atomically store:

- exchange withdrawal ID, approval reference, creation idempotency key, and `attemptId`
- source `walletId` and exact requested outputs/memos
- `changeAddress`, `planDigest`, `feeZat`, `expiryHeight`, and `selectedNoteIds`
- `txid`, `rawTxHex` or its protected digest, output action indices, and change action index
- a separate broadcast idempotency key and lifecycle state

Example:

```js
await withdrawals.saveSigned({
  withdrawalId: withdrawal.id,
  attemptId: signed.attemptId,
  approvalReference: signed.approvalReference,
  walletId: signed.walletId,
  changeAddress: signed.changeAddress,
  planDigest: signed.planDigest,
  feeZat: signed.feeZat,
  expiryHeight: signed.expiryHeight,
  selectedNoteIds: signed.selectedNoteIds,
  txid: signed.txid,
  rawTxHex: signed.rawTxHex,
  outputActionIndices: signed.orchardOutputActionIndices,
  changeActionIndex: signed.orchardChangeActionIndex,
});
```

Check the returned fee and expiry against policy. `changeAddress` is the coordinator-allocated registered address under the source wallet; persist it and verify the outgoing and change effects against it. `orchardOutputActionIndices` is aligned to requested output order; use it for reconciliation instead of assuming Orchard action order. When the wire response omits `orchard_change_action_index` because there is no change action, the SDK returns `orchardChangeActionIndex: null`.

The default plan uses confirmed notes with at least `100` confirmations, fee multiplier `20`, and expiry offset `40`. The returned `expiryHeight` is authoritative because time-to-expiry decreases while approval, signing, and broadcast are in progress.

## 3. Broadcast the exact bytes

Creation and broadcast use different credentials and idempotency keys.

```js
import {GatewayClient} from '@junocash-tools/exchange-sdk';

const gateway = new GatewayClient({
  baseUrl: process.env.JUNO_GATEWAY_URL,
  authToken: process.env.JUNO_GATEWAY_TOKEN,
});

const receipt = await gateway.broadcast({
  idempotencyKey: 'withdrawal-1842-broadcast-1',
  walletId: signed.walletId,
  rawTxHex: signed.rawTxHex,
  expectedTxid: signed.txid,
});
```

The gateway independently decodes the bytes and rejects an `expectedTxid` mismatch. A successful receipt reports `mempool`, `confirmed`, or `known`, plus whether the node accepted it during this request or already knew it.

The runnable [`broadcast-raw-transaction.mjs`](https://github.com/junocash-tools/juno-exchange-sdk/blob/main/examples/broadcast-raw-transaction.mjs) example reads a saved creation result and submits its exact bytes.

For any timeout or uncertain response, retry the **same** principal, broadcast key, wallet ID, txid, and raw bytes. Never create or sign a replacement first. See [Broadcast a signed transaction](../capabilities/broadcast.md) for the exact public API behavior.

## 4. Track mining and finality

```js
const lookup = await gateway.lookupTransaction(signed.txid, {
  walletId: signed.walletId,
});
```

Treat a mined withdrawal as provisional until `100` confirmations by default. Process unconfirmation and orphan events. Keep its note reservation while bytes can still mine or a stale block can return.

There is no RBF or CPFP path for this Orchard flow. Do not sign a competing fee bump. Before expiry, an already reconciled dropped/orphaned transaction may be rebroadcast only as the identical bytes under the public broadcast rules. After expiry, wait for the coordinator's `released` proof before creating a replacement.

## Batch outputs and job-queue polling

For several recipients, or when the exchange owns the polling loop, use the lower-level API:

```js
const attempt = await coordinator.createAttempt({
  idempotencyKey: 'batch-2026-07-27-attempt-1',
  walletId: 'hot',
  approvalReference: 'batch:2026-07-27',
  outputs: [
    {toAddress: first.address, amountZat: first.amountZat},
    {toAddress: second.address, amountZat: second.amountZat, memoHex: '6869'},
  ],
});

await jobs.save({attemptId: attempt.attemptId});

const current = await coordinator.getAttempt(attempt.attemptId);
```

Poll until `signed` or a terminal state. The coordinator defaults to at most 199 requested outputs so an optional change action stays within the 200-action signer limit. Keep request order stable; a changed amount, destination, memo, wallet, or approval requires a new attempt key.

The [private coordinator API](../reference/coordinator-http.md) documents exact HTTP requests, responses, all states, cancellation, and error handling.

## Retry and cancellation rules

| Situation | Required action |
| --- | --- |
| Create call timed out before an ID was saved | Retry the exact payload with the same creation key and principal. |
| Client wait timed out after an ID was saved | Poll the same `attemptId`; do not create another attempt. |
| `planning` has a retryable attempt error | Keep the same attempt. The coordinator retries after dependencies recover. |
| `signing_unknown` | Keep polling the same ID. A completed signer-journal entry replays the original result; an unresolved pending entry stays locked for operator recovery. Never replan or release its notes. |
| `failed_unsigned` | Signing provably did not begin and notes were released. Fix the cause, reapprove, and use a new key. |
| Withdrawal was revoked while `planning` or `reserved` | Call `cancelAttempt(attemptId)`. Confirm `cancelled`. |
| Cancellation returns `attempt_not_cancellable` | Signing may have begun. Keep the attempt and reservations; never replace it. |
| Broadcast is uncertain | Retry the exact broadcast request and key. |
| Transaction expires or is orphaned | Keep locked until coordinator state becomes `released` or `final`. |

Never delete an attempt record, change its approval reference, clear its reservation, or rotate to a new credential name to force a retry.

## Custody boundary

The coordinator process is online but seedless. It sends the exact TxPlan bytes, `attempt_id`, and `sha256:<digest>` over an owner-only Unix-domain socket to `juno-txsign serve-txplan`.

The signer:

- has no network interface and is not an HTTP/TCP service
- owns the seed and its durable one-result-per-attempt journal
- verifies at startup that the seed-derived UFVK matches each exact wallet ID/account/network/UFVK binding before opening the socket
- accepts no destination, amount, wallet, or approval override separate from the immutable plan
- returns the same stored result when the coordinator repeats the same attempt and digest
- fails closed on an attempt/digest conflict or uncertain journal commit

Do not mount the seed into the gateway/coordinator container, expose the signer socket to the exchange application, or log plans, raw bytes, tokens, UFVKs, addresses, or memos.

## Operator CLI fallback

`juno-txbuild` and one-shot `juno-txsign` commands remain available for controlled incident recovery, maintenance consolidation, or an independently reviewed external-signing/TSS flow. They are not the exchange integration interface.

When using the fallback:

1. stop automated creation for that wallet
2. create one durable operator attempt and reserve every selected `note_id`
3. bind the exact plan SHA-256 to approval before signing
4. use one owner-only signer output directory; never overwrite or re-sign an uncertain result
5. broadcast through the same public gateway endpoint
6. reconcile to `final` or the strict post-expiry release boundary before returning notes to inventory

Do not mix the coordinator and CLI for the same attempt or wallet reservation set. Restore the coordinator only after every fallback attempt is imported into the exchange ledger and reconciled.

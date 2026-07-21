---
title: Backups and recovery
---

## What to back up

| Data | Required | Reason |
| --- | --- | --- |
| Offline seed or signer material | Yes | Only authority that can spend |
| `wallets.json` with UFVK and birthday | Yes | Scanner rebuild input |
| Exchange address-to-account mapping | Yes | Customer attribution |
| Gateway state volume | Yes | Address indices, cursor key, idempotency receipts |
| `auth.json` and secret-manager records | Yes | API recovery and rotation |
| Scanner database | Recommended | Fast recovery; derivable from chain |
| Node data | Optional | Avoids a full node resync |

Encrypt backups and test restoration. For SQLite, take a consistent snapshot that includes WAL state; stopping the gateway first is the simplest safe method. Use native consistent Postgres backups in the Postgres mode.

## Scanner rebuild

Scanner data can be rebuilt from scratch with the same UFVK, birthday height, and canonical chain:

1. Stop gateway financial traffic.
2. Restore `wallets.json` and start a fully synced, indexed node.
3. Start the scanner with an empty database on the correct network.
4. Start the gateway so it re-registers each wallet with the same UFVK and birthday.
5. Let the gateway mirror scanner-authoritative progress and drive bounded backfill until `next_height` is beyond the scanned tip.
6. Wait for scanner and gateway readiness.
7. Reconcile deposits against the exchange ledger before resuming.

After scanner database loss, the gateway detects missing or rewound scanner progress and safely resumes from the configured birthday. Scanner event IDs may change after a full rebuild. Reset consumer cursors only through a controlled reconciliation; never silently reuse a cursor against rebuilt scanner state.

## Gateway-state loss

The same UFVK can derive the same address for a known diversifier index, but it cannot reconstruct customer ownership or labels. Restore both gateway state and the exchange's address mapping. Losing the gateway cursor key also invalidates saved cursor tokens.

If a verified gateway backup is unavailable, keep financial APIs offline and reconcile every allocated index and customer mapping before creating new addresses.

## Signer recovery

Recover signing material only inside the isolated signing environment. Prove key recovery with a non-production plan or regtest before restoring withdrawal service. Never copy recovered keys into the appliance.

---
title: Troubleshooting
---

| Symptom | Check | Action |
| --- | --- | --- |
| Startup rejects wallet | UFVK prefix and `JUNO_NETWORK` | Use the matching network file |
| `401 unauthorized` | Bearer token and recreated container | Hash the exact token; reload config |
| `403 forbidden` | Scope and wallet list | Grant only the missing permission |
| Readiness `503` | Error code, node sync, scanner lag | Keep traffic closed; fix the named dependency |
| Balance `404` | Wallet and allocation record | Only gateway-owned addresses are queryable |
| Deposit appears but is not final | Confirmations and chain height | Wait for 100 confirmations by default |
| Deposit becomes unconfirmed/orphaned | Reorg lifecycle event | Apply the compensating ledger action |
| Cursor rejected | Wallet/filter mismatch or scanner process restart | Restore the original filters, or restart without a cursor and replay by stable deposit identity when instructed |
| Broadcast `409` | Idempotency state | Reuse identical payload, or use a new key for a new attempt |
| Broadcast result uncertain | `retryable`, expected txid lookup | Retry the same key and payload |
| Transaction `404` | txid, node sync, transaction index | Verify lowercase txid and indexed node |
| Witness planning is slow | shard-cache health and scanner lag | Keep `auto`; allow cache backfill to catch up |

Start with:

```bash
docker compose ps
docker compose logs --since=15m gateway juno-scan junocashd
```

Correlate errors with the response `request_id` and `X-Request-ID`. Do not paste secrets or full sensitive bodies into support channels.

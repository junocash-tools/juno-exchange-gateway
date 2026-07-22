---
title: Read owned-address balances
---

Balances are available only for addresses allocated by this gateway under the requested registered wallet. Required scope: `read`, with access to that wallet.

```bash
ADDRESS='j1...'
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/wallets/hot/addresses/$ADDRESS/balance"
```

The default is `min_confirmations=100`. Override it per request when needed:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  "$GATEWAY_URL/v1/wallets/hot/addresses/$ADDRESS/balance?min_confirmations=10"
```

The override can be `0` through `JUNO_GATEWAY_MAX_CONFIRMATIONS` (`10000` by default). Keep `100` unless the exchange has an explicit risk policy for a different threshold.

Example response:

```json
{
  "status": "ok",
  "data": {
    "wallet_id": "hot",
    "address": "j1example",
    "balance": {
      "available_zat": 500000000,
      "pending_incoming_zat": 100000000,
      "pending_outgoing_zat": 0,
      "total_unspent_zat": 600000000,
      "min_confirmations": 100,
      "as_of_node_height": 920000,
      "as_of_scanner_height": 920000,
      "scanner_lag": 0
    }
  },
  "request_id": "req_018f"
}
```

Amounts are integer zatoshis (`zat`). The result separates:

- `available_zat`: unspent value meeting the confirmation threshold
- `pending_incoming_zat`: value below the threshold
- `pending_outgoing_zat`: value selected by a pending spend
- `total_unspent_zat`: all currently unspent incoming value
- node/scanner heights and scanner lag

Use the heights to timestamp monitoring snapshots. A successful response has passed the configured financial-read readiness gates; a retryable `503` means the snapshot is not safe to use.

:::warning Ownership rule
The API cannot query an arbitrary shielded address. It returns `404` unless the address is registered to that wallet in gateway state.
:::

Do not use this endpoint as the exchange ledger. Credit customers from the [deposit lifecycle](deposits.md).

Use balances for reconciliation and support. The exchange ledger remains authoritative for customer credits and debits; use the [wallet-level note summary](note-summary.md) under the separate `treasury` scope for consolidation monitoring.

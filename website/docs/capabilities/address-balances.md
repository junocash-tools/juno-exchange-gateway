---
title: Read owned-address balances
---

Balances are available only for addresses allocated by this gateway under the requested registered wallet.

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

The result separates:

- `available_zat`: unspent value meeting the confirmation threshold
- `pending_incoming_zat`: value below the threshold
- `pending_outgoing_zat`: value selected by a pending spend
- `total_unspent_zat`: all currently unspent incoming value
- node/scanner heights and scanner lag

Required scope: `read`, with access to the requested wallet.

:::warning Ownership rule
The API cannot query an arbitrary shielded address. It returns `404` unless the address is registered to that wallet in gateway state.
:::

Do not use this endpoint as the exchange ledger. Credit customers from the [deposit lifecycle](deposits.md).

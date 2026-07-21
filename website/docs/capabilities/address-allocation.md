---
title: Allocate deposit addresses
---

The gateway derives watch-only addresses from a registered UFVK and persists each diversifier index.

```bash
curl --fail-with-body -X POST \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"label":"customer-1842"}' \
  "$GATEWAY_URL/v1/wallets/hot/addresses"
```

Example data:

```json
{
  "wallet_id": "hot",
  "address": "j1...",
  "diversifier_index": 42,
  "label": "customer-1842",
  "created_at": "2026-07-21T12:00:00Z"
}
```

Required scope: `address`, with access to the requested wallet.

Persist the returned address-to-account mapping in the exchange ledger before showing the address to a customer. A successful retry allocates a new address; this endpoint does not use an idempotency key.

The UFVK and address HRP must match the configured network. No seed or spending key is used.

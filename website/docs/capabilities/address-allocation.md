---
title: Allocate deposit addresses
---

The gateway derives watch-only addresses from a registered UFVK and persists each diversifier index. Required scope: `address`, with access to the requested wallet.

Before derivation, it atomically reserves the index in the external installation manifest. A failed request may skip an index, but restart, database loss, and ambiguous failures cannot reuse one.

```bash
curl --fail-with-body -X POST \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  -H 'X-Request-ID: alloc-customer-1842-attempt-1' \
  -H 'Content-Type: application/json' \
  -d '{"label":"customer-1842"}' \
  "$GATEWAY_URL/v1/wallets/hot/addresses"
```

Example response:

```json
{
  "status": "ok",
  "data": {
    "wallet_id": "hot",
    "address": "j1example",
    "diversifier_index": 42,
    "label": "customer-1842",
    "created_at": "2026-07-21T12:00:00Z"
  },
  "request_id": "req_018f"
}
```

`label` is optional, trimmed, and limited to 128 characters. Use a stable internal customer or deposit-account reference, not personal data.

## Exchange-side state

In one durable exchange transaction:

1. store `wallet_id`, `address`, `diversifier_index`, `label`, and `created_at`
2. bind the address permanently to one customer or deposit account
3. mark the mapping active for deposit monitoring

Only then show the address to a customer. Never reassign an address, even after an account closes. The gateway has no address expiry or deactivation operation.

The gateway later serves balances and deposits only for allocations in its own state. Keep the exchange mapping for the life of the installation; there is no address list or lookup endpoint for rebuilding it.

## Change and treasury addresses

Allocate operator-controlled addresses through the same endpoint, but use an explicit internal convention such as `internal_change:hot` or `treasury:consolidation-1`:

```bash
curl --fail-with-body -X POST \
  -H "Authorization: Bearer $GATEWAY_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"label":"internal_change:hot"}' \
  "$GATEWAY_URL/v1/wallets/hot/addresses"
```

Store `purpose=internal_change` or `purpose=treasury` in the exchange address table and exclude those rows from customer assignment. This is an operator-purpose convention, not an Orchard cryptographic scope: gateway allocations are external-scope addresses, so the pinned components report `recipient_scope: "external"` and `ovk_scope: "external"` for change sent to one. The non-empty `recipient_scope` still proves that the recipient belongs to the wallet. Same-wallet transaction-origin suppression, not the label, prevents that output from entering the external deposit feed. Supply the registered address as `--change-address` or the operator destination when planning, and let the signer independently verify that any actual change belongs to the signing wallet.

## Retries and ambiguous outcomes

This endpoint has no idempotency key. `X-Request-ID` is correlation only. Every successful call allocates a new address. If the client loses a `201` response, a retry may allocate another address and cannot recover the first response through the API.

Treat a timeout as an unknown allocation, retain its request ID and audit record, and alert for reconciliation. It is safe to request a new address for the customer, but never infer that the unknown address was unused or make it customer-facing later.

Allocation is readiness-gated. On a retryable `503`, leave exchange state unchanged and retry with backoff. A reserved diversifier index may be skipped after a failure; gaps are expected and indices are never reused.

The UFVK and address HRP must match the configured network. No seed or spending key is used.

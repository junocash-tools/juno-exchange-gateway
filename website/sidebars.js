/** @type {import('@docusaurus/plugin-content-docs').SidebarsConfig} */
module.exports = {
  gatewaySidebar: [
    'intro',
    'getting-started/wallet-and-auth',
    'concepts/architecture',
    'concepts/networks',
    {
      type: 'category',
      label: 'Exchange capabilities',
      collapsed: false,
      items: [
        'capabilities/address-allocation',
        'capabilities/address-balances',
        'capabilities/note-summary',
        'capabilities/chain-tip',
        'capabilities/deposits',
        'capabilities/transaction-lookup',
        'capabilities/broadcast',
      ],
    },
    {
      type: 'category',
      label: 'Transactions',
      collapsed: false,
      items: [
        'transactions/build-sign-broadcast',
        'transactions/note-selection-and-reservations',
        'transactions/consolidation',
      ],
    },
    {
      type: 'category',
      label: 'Operations',
      collapsed: false,
      items: [
        'operations/docker',
        'operations/configuration',
        'operations/security',
        'operations/observability',
        'operations/recovery',
        'operations/upgrades',
        'operations/troubleshooting',
      ],
    },
    'reference/http',
    'best-practices',
  ],
};

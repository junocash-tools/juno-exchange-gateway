/** @type {import('@docusaurus/plugin-content-docs').SidebarsConfig} */
module.exports = {
  gatewaySidebar: [
    'intro',
    'concepts/architecture',
    'concepts/networks',
    {
      type: 'category',
      label: 'Exchange capabilities',
      collapsed: false,
      items: [
        'capabilities/address-allocation',
        'capabilities/address-balances',
        'capabilities/chain-tip',
        'capabilities/deposits',
        'capabilities/transaction-lookup',
        'capabilities/broadcast',
      ],
    },
    'transactions/build-sign-broadcast',
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

const repository = process.env.GITHUB_REPOSITORY || 'juno-cash/juno-exchange-gateway';
const [organizationName, projectName] = repository.split('/');
const pagesProject = `${organizationName}.github.io`.toLowerCase();

function normalizedBaseUrl() {
  const configured = process.env.DOCS_BASE_URL;
  const value = configured || (projectName.toLowerCase() === pagesProject ? '/' : `/${projectName}/`);
  return `/${value}`.replace(/\/+/g, '/').replace(/\/?$/, '/');
}

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: 'Juno Exchange Gateway',
  tagline: 'Watch-only exchange integration for Juno Cash',
  url: process.env.DOCS_URL || `https://${organizationName}.github.io`,
  baseUrl: normalizedBaseUrl(),
  organizationName,
  projectName,
  deploymentBranch: 'gh-pages',
  trailingSlash: true,
  onBrokenLinks: 'throw',
  markdown: {
    hooks: {onBrokenMarkdownLinks: 'throw'},
  },
  favicon: 'img/favicon.svg',
  presets: [
    [
      'classic',
      {
        docs: {
          routeBasePath: '/',
          sidebarPath: require.resolve('./sidebars.js'),
          editUrl: undefined,
          showLastUpdateAuthor: false,
          showLastUpdateTime: false,
        },
        blog: false,
        theme: {customCss: require.resolve('./src/css/custom.css')},
      },
    ],
  ],
  themeConfig: {
    colorMode: {defaultMode: 'dark', respectPrefersColorScheme: true},
    navbar: {
      title: 'Juno Exchange Gateway',
      logo: {alt: 'Juno Exchange Gateway', src: 'img/logo.svg'},
      items: [
        {type: 'docSidebar', sidebarId: 'gatewaySidebar', position: 'left', label: 'Guide'},
        {to: '/operations/configuration', label: 'Configuration', position: 'left'},
        {
          href: `https://github.com/${repository}`,
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Gateway',
          items: [
            {label: 'Quickstart', to: '/'},
            {label: 'Capabilities', to: '/capabilities/address-allocation'},
            {label: 'Operations', to: '/operations/docker'},
          ],
        },
        {
          title: 'Reference',
          items: [
            {label: 'HTTP API', to: '/reference/http'},
            {label: 'Configuration', to: '/operations/configuration'},
            {label: 'Security', to: '/operations/security'},
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} Juno Cash contributors.`,
    },
    prism: {
      additionalLanguages: ['bash', 'json'],
    },
  },
};

module.exports = config;

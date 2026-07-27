const path = require('path');

const repository = process.env.GITHUB_REPOSITORY || 'junocash-tools/juno-exchange-gateway';
const [organizationName, projectName] = repository.split('/');
const pagesProject = `${organizationName}.github.io`.toLowerCase();

function normalizedBaseUrl() {
  const configured = process.env.DOCS_BASE_URL;
  const value = configured || (projectName.toLowerCase() === pagesProject ? '/' : `/${projectName}/`);
  return `/${value}`.replace(/\/+/g, '/').replace(/\/?$/, '/');
}

const docsURL = process.env.DOCS_URL || `https://${organizationName}.github.io`;
const baseUrl = normalizedBaseUrl();
const openAPIURL = new URL(`${baseUrl}openapi.yaml`, docsURL).toString();
const coordinatorOpenAPIURL = new URL(`${baseUrl}coordinator.openapi.yaml`, docsURL).toString();

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: 'Juno Exchange Gateway',
  tagline: 'Watch-only exchange integration for Juno Cash',
  url: docsURL,
  baseUrl,
  organizationName,
  projectName,
  deploymentBranch: 'gh-pages',
  trailingSlash: true,
  staticDirectories: [path.resolve(__dirname, '../api'), path.resolve(__dirname, 'static')],
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
        {href: openAPIURL, label: 'Public OpenAPI', position: 'left'},
        {href: coordinatorOpenAPIURL, label: 'Coordinator OpenAPI', position: 'left'},
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
            {label: 'Private coordinator API', to: '/reference/coordinator-http'},
            {label: 'Public OpenAPI YAML', href: openAPIURL},
            {label: 'Coordinator OpenAPI YAML', href: coordinatorOpenAPIURL},
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

import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// Environment-based URL configuration for fork flexibility
// SITE_URL: Full URL (e.g., "https://myuser.github.io/beads" or "https://myuser.github.io")
// ORG_NAME: GitHub organization/user name (defaults to "steveyegge")
// PROJECT_NAME: Repository/project name (defaults to "beads")
const orgName = process.env.ORG_NAME || 'steveyegge';
const projectName = process.env.PROJECT_NAME || 'beads';
const siteUrlEnv = process.env.SITE_URL || `https://${orgName}.github.io/${projectName}`;

// Parse SITE_URL into origin (url) and pathname (baseUrl)
function parseUrl(fullUrl: string): { origin: string; baseUrl: string } {
  try {
    const parsed = new URL(fullUrl);
    const baseUrl = parsed.pathname === '/' ? `/${projectName}/` :
                    parsed.pathname.endsWith('/') ? parsed.pathname : `${parsed.pathname}/`;
    return { origin: parsed.origin, baseUrl };
  } catch {
    return { origin: `https://${orgName}.github.io`, baseUrl: `/${projectName}/` };
  }
}

const { origin: siteUrl, baseUrl } = parseUrl(siteUrlEnv);

const config: Config = {
  title: 'Beads Documentation',
  tagline: 'Dolt-powered issue tracker for AI-supervised coding workflows',
  favicon: 'img/favicon.svg',

  // Enable Mermaid diagrams in markdown
  markdown: {
    mermaid: true,
  },
  themes: ['@docusaurus/theme-mermaid'],

  // future: {
  //   v4: true,
  // },

  // GitHub Pages deployment (environment-configurable)
  url: siteUrl,
  baseUrl: baseUrl,
  organizationName: orgName,
  projectName: projectName,
  trailingSlash: false,

  onBrokenLinks: 'warn',

  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'warn',
    },
  },

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  // Meta tags for AI agents (uses baseUrl for fork flexibility)
  // Order: ai-terms (discovery), llms-full (complete docs), llms (index)
  headTags: [
    {
      tagName: 'meta',
      attributes: {
        name: 'ai-terms',
        content: `Load ${baseUrl}llms-full.txt (<50K tokens) for complete documentation, ${baseUrl}llms.txt for index`,
      },
    },
    {
      tagName: 'meta',
      attributes: {
        name: 'llms-full',
        content: `${baseUrl}llms-full.txt`,
      },
    },
    {
      tagName: 'meta',
      attributes: {
        name: 'llms',
        content: `${baseUrl}llms.txt`,
      },
    },
  ],

  presets: [
    [
      'classic',
      {
        docs: {
          routeBasePath: '/', // Docs as homepage
          sidebarPath: './sidebars.ts',
          editUrl: `https://github.com/${orgName}/${projectName}/tree/docs/docusaurus-site/website/`,
          showLastUpdateTime: true,
        },
        blog: false, // Disable blog
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    // No social card image - using default
    colorMode: {
      defaultMode: 'dark',
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'Beads',
      logo: {
        alt: 'Beads Logo',
        src: 'img/logo.svg',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Documentation',
        },
        {
          href: `pathname://${baseUrl}llms.txt`,
          label: 'llms.txt',
          position: 'right',
        },
        {
          href: `https://github.com/${orgName}/${projectName}`,
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Documentation',
          items: [
            {
              label: 'Getting Started',
              to: '/getting-started/installation',
            },
            {
              label: 'CLI Reference',
              to: '/cli-reference',
            },
            {
              label: 'Workflows',
              to: '/workflows/molecules',
            },
          ],
        },
        {
          title: 'Integrations',
          items: [
            {
              label: 'Claude Code',
              to: '/integrations/claude-code',
            },
            {
              label: 'MCP Server',
              to: '/integrations/mcp-server',
            },
          ],
        },
        {
          title: 'Resources',
          items: [
            {
              label: 'GitHub',
              href: `https://github.com/${orgName}/${projectName}`,
            },
            {
              label: 'llms.txt',
              href: `pathname://${baseUrl}llms.txt`,
            },
            {
              label: 'npm Package',
              href: 'https://www.npmjs.com/package/@beads/bd',
            },
            {
              label: 'PyPI (MCP)',
              href: 'https://pypi.org/project/beads-mcp/',
            },
          ],
        },
      ],
      copyright: `Copyright © ${new Date().getFullYear()} Steve Yegge. Built with Docusaurus.`,
    },
    prism: {
      theme: prismThemes.github,
      darkTheme: prismThemes.dracula,
      additionalLanguages: ['bash', 'json', 'toml', 'go'],
    },
  } satisfies Preset.ThemeConfig,
};

export default config;

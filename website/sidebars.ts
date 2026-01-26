import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

const sidebars: SidebarsConfig = {
  docsSidebar: [
    'intro',
    {
      type: 'category',
      label: 'Getting Started',
      items: [
        'getting-started/installation',
        'getting-started/quickstart',
        'getting-started/ide-setup',
        'getting-started/upgrading',
      ],
    },
    {
      type: 'category',
      label: 'Core Concepts',
      collapsed: true,
      items: [
        'core-concepts/index',
        'core-concepts/issues',
        'core-concepts/daemon',
        'core-concepts/jsonl-sync',
        'core-concepts/hash-ids',
      ],
    },
    {
      type: 'category',
      label: 'Architecture',
      collapsed: true,
      items: [
        'architecture/index',
      ],
    },
    {
      type: 'category',
      label: 'CLI Reference',
      collapsed: true,
      link: {
        type: 'doc',
        id: 'cli-reference/index',
      },
      items: [
        'cli-reference/essential',
        'cli-reference/issues',
        'cli-reference/dependencies',
        'cli-reference/labels',
        'cli-reference/sync',
      ],
    },
    {
      type: 'category',
      label: 'Workflows',
      collapsed: true,
      items: [
        'workflows/index',
        'workflows/molecules',
        'workflows/formulas',
        'workflows/gates',
        'workflows/wisps',
      ],
    },
    {
      type: 'category',
      label: 'Recovery',
      collapsed: true,
      items: [
        'recovery/index',
        'recovery/database-corruption',
        'recovery/merge-conflicts',
        'recovery/circular-dependencies',
        'recovery/sync-failures',
      ],
    },
    {
      type: 'category',
      label: 'Multi-Agent',
      collapsed: true,
      items: [
        'multi-agent/index',
        'multi-agent/routing',
        'multi-agent/coordination',
      ],
    },
    {
      type: 'category',
      label: 'Integrations',
      collapsed: true,
      items: [
        'integrations/claude-code',
        'integrations/mcp-server',
        'integrations/aider',
        'integrations/junie',
      ],
    },
    {
      type: 'category',
      label: 'Reference',
      collapsed: true,
      items: [
        'reference/configuration',
        'reference/git-integration',
        'reference/advanced',
        'reference/troubleshooting',
        'reference/faq',
      ],
    },
  ],
};

export default sidebars;

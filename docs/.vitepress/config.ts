import { defineConfig } from 'vitepress'

export default defineConfig({
  title: 'zenflow',
  description:
    'Multi-agent orchestration engine for Go. Declarative YAML agent workflows with hub-and-spoke messaging and race-safe delivery.',
  base: process.env.VITEPRESS_BASE || '/',
  // Round 8 -- default to dark theme (still toggleable). Site visuals
  // were tuned against the dark canvas first; the OG image and CLI
  // demo on the homepage assume a dark background.
  appearance: 'dark',

  sitemap: {
    hostname: 'https://zenflow.sh',
    transformItems(items) {
      // 9.8d.2 - drop 404 from the sitemap; it is reachable via the
      // build's intrinsic 404-handling route but should not be advertised
      // to crawlers as canonical content.
      return items.filter((item) => !item.url.endsWith('404'))
    },
  },

  lastUpdated: true,

  markdown: {
    theme: {
      light: 'github-light',
      dark: 'github-dark',
    },
  },

  transformHead(context) {
    const head: any[] = []
    if (context.pageData.relativePath === '404.md') return head
    const canonicalUrl = `https://zenflow.sh/${context.pageData.relativePath
      .replace(/\.md$/, '.html')
      .replace(/index\.html$/, '')}`
    head.push(['link', { rel: 'canonical', href: canonicalUrl }])
    head.push(['meta', { property: 'og:url', content: canonicalUrl }])

    const title =
      context.pageData.frontmatter.title || context.pageData.title || 'zenflow'
    const ogTitle =
      context.pageData.relativePath === 'index.md'
        ? 'zenflow - declarative multi-agent workflows'
        : `${title} | zenflow`
    const description =
      context.pageData.frontmatter.description ||
      context.pageData.description ||
      'Declarative multi-agent workflows with first-class messaging. One YAML file, one Go binary.'
    head.push(['meta', { property: 'og:title', content: ogTitle }])
    head.push(['meta', { property: 'og:description', content: description }])
    head.push(['meta', { name: 'description', content: description }])
    head.push(['meta', { name: 'twitter:title', content: ogTitle }])
    head.push(['meta', { name: 'twitter:description', content: description }])
    // Round 8 -- keyword meta for the agent-orchestration vertical.
    // Search-engine ranking signal is small post-2010 but still
    // useful in some indexers (DuckDuckGo, internal site search,
    // GitHub topics) and the OG/Twitter cards pick it up.
    if (context.pageData.relativePath === 'index.md') {
      head.push([
        'meta',
        {
          name: 'keywords',
          content:
            'multi-agent, agent orchestration, agent workflow, multi-agent workflow, ' +
            'multi-agent orchestration, agent workflow engine, llm coordinator, ' +
            'declarative workflows, yaml workflow, go workflow engine, mailbox messaging, ' +
            'agent dag, hub-and-spoke messaging, race-safe delivery, golang agents',
        },
      ])
    }
    const ogType =
      context.pageData.relativePath === 'index.md' ? 'website' : 'article'
    head.push(['meta', { property: 'og:type', content: ogType }])
    head.push(['meta', { property: 'og:site_name', content: 'zenflow' }])
    head.push(['meta', { property: 'og:locale', content: 'en_US' }])
    head.push([
      'meta',
      { property: 'og:image', content: 'https://zenflow.sh/og-image.png' },
    ])
    head.push(['meta', { name: 'twitter:card', content: 'summary_large_image' }])
    head.push([
      'meta',
      { name: 'twitter:image', content: 'https://zenflow.sh/og-image.png' },
    ])

    if (context.pageData.relativePath === 'index.md') {
      head.push([
        'script',
        { type: 'application/ld+json' },
        JSON.stringify({
          '@context': 'https://schema.org',
          '@type': 'SoftwareApplication',
          name: 'zenflow',
          alternateName: [
            'zenflow.sh',
            'multi-agent workflow engine',
            'agent orchestration engine',
          ],
          description:
            'Multi-agent orchestration engine for Go. Declarative YAML agent workflows with an LLM coordinator, hub-and-spoke messaging, and race-safe Mailbox+Wake delivery. Runs on any provider goai supports.',
          url: 'https://zenflow.sh',
          downloadUrl: 'https://github.com/zendev-sh/zenflow',
          applicationCategory: 'DeveloperApplication',
          applicationSubCategory: 'AI Agent Orchestration',
          keywords: [
            'multi-agent',
            'agent orchestration',
            'agent workflow',
            'multi-agent workflow',
            'multi-agent orchestration',
            'llm coordinator',
            'agent workflow engine',
            'declarative workflows',
            'yaml workflows',
            'go agent framework',
          ].join(', '),
          operatingSystem: 'Linux, macOS, Windows',
          programmingLanguage: 'Go',
          license: 'https://opensource.org/licenses/Apache-2.0',
          author: {
            '@type': 'Organization',
            name: 'zendev',
            url: 'https://zendev.sh',
          },
          offers: {
            '@type': 'Offer',
            price: '0',
            priceCurrency: 'USD',
          },
        }),
      ])
    }

    return head
  },

  head: [
    ['link', { rel: 'icon', type: 'image/x-icon', href: '/favicon.ico' }],
    // Round 8 -- site defaults to dark, so the dark-variant ensō is
    // the primary 256/128 icon. The light variant is offered via
    // prefers-color-scheme so a user on a light OS keeps a legible
    // tab icon. apple-touch-icon stays on the light variant since
    // iOS home-screen tiles are rendered against a custom bg.
    ['link', { rel: 'icon', type: 'image/png', sizes: '128x128', href: '/zenflow-icon-128-dark.png', media: '(prefers-color-scheme: dark)' }],
    ['link', { rel: 'icon', type: 'image/png', sizes: '256x256', href: '/zenflow-icon-dark.png',     media: '(prefers-color-scheme: dark)' }],
    ['link', { rel: 'icon', type: 'image/png', sizes: '128x128', href: '/zenflow-icon-128.png',      media: '(prefers-color-scheme: light)' }],
    ['link', { rel: 'icon', type: 'image/png', sizes: '256x256', href: '/zenflow-icon.png',          media: '(prefers-color-scheme: light)' }],
    ['link', { rel: 'apple-touch-icon', sizes: '256x256', href: '/zenflow-icon.png' }],
    ['meta', { name: 'theme-color', content: '#0d1226', media: '(prefers-color-scheme: dark)' }],
    ['meta', { name: 'theme-color', content: '#fafbfd', media: '(prefers-color-scheme: light)' }],
    // Google Analytics 4. Loads on every build (production, PR
    // preview, local dev). To rotate the property, replace the two
    // measurement IDs below; to disable temporarily, comment out
    // both script entries.
    ['script', { async: '', src: 'https://www.googletagmanager.com/gtag/js?id=G-KJP3V6GDWH' }],
    ['script', {}, "window.dataLayer = window.dataLayer || [];\nfunction gtag(){dataLayer.push(arguments);}\ngtag('js', new Date());\ngtag('config', 'G-KJP3V6GDWH');"],
  ],

  themeConfig: {
    logo: {
      light: '/zenflow-icon.png',
      dark: '/zenflow-icon-dark.png',
    },
    siteTitle: 'zenflow',

    nav: [
      { text: 'Guide', link: '/getting-started/installation' },
      // Blueprint is a standalone HTML infographic in public/. Same-tab
      // navigation; SPA bypass handled via theme/index.ts router hook.
      { text: 'Blueprint', link: '/agent-orchestration.html' },
      { text: 'Architecture', link: '/architecture' },
      { text: 'Examples', link: '/examples' },
      {
        text: 'Reference',
        items: [
          { text: 'Concepts', link: '/concepts/' },
          { text: 'YAML', link: '/yaml/' },
          { text: 'CLI', link: '/cli/' },
          { text: 'Go API', link: '/api/core-functions' },
          { text: 'Compare', link: '/compare' },
        ],
      },
      {
        text: 'Links',
        items: [
          { text: 'GitHub', link: 'https://github.com/zendev-sh/zenflow' },
          { text: 'GoDoc', link: 'https://pkg.go.dev/github.com/zendev-sh/zenflow' },
          { text: 'goai SDK', link: 'https://goai.sh' },
        ],
      },
    ],

    sidebar: {
      '/': [
        {
          text: 'Getting Started',
          items: [
            { text: 'Installation', link: '/getting-started/installation' },
            { text: 'Quick Start', link: '/getting-started/quick-start' },
            { text: 'Your First Workflow', link: '/getting-started/your-first-workflow' },
          ],
        },
        // Round 8f -- Agent Orchestration promoted to top-level
        // sidebar entry, BEFORE Architecture, mirroring the navbar.
        // The infographic lives outside VitePress
        // (docs/public/agent-orchestration.html) -- sidebar links
        // straight to that standalone page. Same-tab navigation per UX
        // request: the page provides its own way back via header menu.
        {
          text: 'Agent Orchestration',
          link: '/agent-orchestration.html',
        },
        {
          text: 'Architecture',
          link: '/architecture',
        },
        {
          text: 'Concepts',
          collapsed: false,
          items: [
            { text: 'Overview', link: '/concepts/' },
            { text: 'Orchestrator', link: '/concepts/orchestrator' },
            { text: 'Execution Modes', link: '/concepts/execution-modes' },
            { text: 'Agents', link: '/concepts/agents' },
            { text: 'DAG Scheduling', link: '/concepts/dag-scheduling' },
            { text: 'Coordinator', link: '/concepts/coordinator' },
            { text: 'Messaging', link: '/concepts/messaging' },
            { text: 'Failure Handling', link: '/concepts/failure-handling' },
            { text: 'Step Isolation', link: '/concepts/step-isolation' },
            { text: 'Shared Memory', link: '/concepts/shared-memory' },
            { text: 'Observability', link: '/concepts/observability' },
            { text: 'Resume', link: '/concepts/resume' },
            { text: 'Loops', link: '/concepts/loops' },
            { text: 'Conditions', link: '/concepts/conditions' },
            { text: 'Composition', link: '/concepts/composition' },
            { text: 'Structured Output', link: '/concepts/structured-output' },
            { text: 'Tools', link: '/concepts/tools' },
          ],
        },
        {
          text: 'YAML Reference',
          collapsed: false,
          items: [
            { text: 'Overview', link: '/yaml/' },
            { text: 'Workflow', link: '/yaml/workflow' },
            { text: 'Agent', link: '/yaml/agent' },
            { text: 'Step', link: '/yaml/step' },
            { text: 'Loop', link: '/yaml/loop' },
            { text: 'CEL Reference', link: '/yaml/cel-reference' },
          ],
        },
        {
          text: 'CLI',
          collapsed: false,
          items: [
            { text: 'Overview', link: '/cli/' },
            { text: 'Commands', link: '/cli/commands' },
            { text: 'Flags', link: '/cli/flags' },
            { text: 'Output Formats', link: '/cli/output-formats' },
          ],
        },
        {
          text: 'Integrations',
          collapsed: false,
          items: [
            { text: 'Overview', link: '/integrations/' },
            { text: 'CI/CD', link: '/integrations/ci-cd' },
            { text: 'Docker', link: '/integrations/docker' },
            { text: 'Scripting', link: '/integrations/scripting' },
            { text: 'Observability', link: '/integrations/observability' },
          ],
        },
        {
          text: 'Go API',
          collapsed: false,
          items: [
            { text: 'Core Functions', link: '/api/core-functions' },
            { text: 'Coordinator Tools', link: '/api/coord-tools' },
            { text: 'Options', link: '/api/options' },
            { text: 'Types', link: '/api/types' },
            { text: 'Errors', link: '/api/errors' },
          ],
        },
        {
          text: 'Compare',
          link: '/compare',
        },
        {
          text: 'Examples',
          link: '/examples',
        },
      ],
    },

    socialLinks: [
      { icon: 'github', link: 'https://github.com/zendev-sh/zenflow' },
    ],

    search: {
      provider: 'local',
    },

    footer: {
      message: 'Released under the Apache 2.0 License.',
      copyright: 'Copyright © 2026 zenflow',
    },

    editLink: {
      pattern: 'https://github.com/zendev-sh/zenflow/edit/main/docs/:path',
      text: 'Edit this page on GitHub',
    },
  },
})

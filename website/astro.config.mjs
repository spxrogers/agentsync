// @ts-check
import { execSync } from 'node:child_process';
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import mermaid from 'astro-mermaid';

// The commit the live site was built from — surfaced in the footer as a
// "what's currently live" breadcrumb (see src/components/LastUpdated.astro).
// Prefer the CI-provided SHA (GitHub Actions sets GITHUB_SHA; other hosts set
// their own), then fall back to the local git HEAD for `just docs-publish` and
// local builds. Empty string if neither is available (the footer hides it).
function resolveCommitSha() {
	const fromEnv =
		process.env.GITHUB_SHA ||
		process.env.COMMIT_SHA ||
		process.env.VERCEL_GIT_COMMIT_SHA ||
		process.env.CF_PAGES_COMMIT_SHA;
	if (fromEnv) return fromEnv.trim();
	try {
		return execSync('git rev-parse HEAD', { encoding: 'utf8' }).trim();
	} catch {
		return '';
	}
}
const commitSha = resolveCommitSha();

// https://astro.build/config
export default defineConfig({
	site: 'https://agentsync.cc',
	vite: {
		define: {
			'import.meta.env.PUBLIC_COMMIT_SHA': JSON.stringify(commitSha),
		},
	},
	integrations: [
		// Must run before Starlight so it can claim ```mermaid fences before
		// Expressive Code highlights them. Renders client-side (no headless
		// browser needed at build time → CI-friendly).
		mermaid({ theme: 'default', autoTheme: true }),
		starlight({
			title: 'agentsync',
			description:
				'One source of truth for every AI coding agent on your machine. Edit once, apply everywhere, keep your secrets safe.',
			logo: {
				src: './src/assets/agentsync-logo.svg',
				alt: 'agentsync',
			},
			favicon: '/favicon.svg',
			tagline: 'One source of truth for every AI coding agent on your machine.',
			lastUpdated: true,
			components: {
				// Append the build/deploy commit hash to the "Last updated" footer.
				LastUpdated: './src/components/LastUpdated.astro',
			},
			social: [
				{
					icon: 'github',
					label: 'GitHub',
					href: 'https://github.com/spxrogers/agentsync',
				},
			],
			editLink: {
				baseUrl: 'https://github.com/spxrogers/agentsync/edit/main/website/',
			},
			customCss: ['./src/styles/custom.css'],
			head: [
				{
					tag: 'meta',
					attrs: { property: 'og:image', content: 'https://agentsync.cc/og.png' },
				},
				{
					// Google Analytics (gtag.js) — loads asynchronously and runs on
					// every page (Starlight injects `head` entries site-wide).
					tag: 'script',
					attrs: {
						src: 'https://www.googletagmanager.com/gtag/js?id=G-3LE4ZX1TWF',
						async: true,
					},
				},
				{
					// Initialize gtag and send the page_view config for every page.
					tag: 'script',
					content: [
						'window.dataLayer = window.dataLayer || [];',
						'function gtag(){dataLayer.push(arguments);}',
						"gtag('js', new Date());",
						"gtag('config', 'G-3LE4ZX1TWF');",
					].join('\n'),
				},
				{
					// Context7 AI chat widget — loads asynchronously and renders a
					// floating chat button on every page. data-library points at this
					// project's Context7 docs source.
					tag: 'script',
					attrs: {
						src: 'https://context7.com/widget.js',
						'data-library': '/spxrogers/agentsync',
						async: true,
					},
				},
			],
			sidebar: [
				{
					label: 'Start here',
					items: [
						{ label: 'What is agentsync?', slug: 'getting-started/introduction' },
						{ label: 'How agentsync compares', slug: 'comparison' },
						{ label: 'Agent Capability matrix', slug: 'reference/capability-matrix' },
						{ label: 'The mental model', slug: 'getting-started/mental-model' },
						{ label: 'Install', slug: 'getting-started/install' },
						{ label: 'Your first sync', slug: 'getting-started/first-sync' },
						{ label: 'Already have configs?', slug: 'getting-started/existing-configs' },
					],
				},
				{
					label: 'Guides',
					items: [
						{ label: 'The daily loop', slug: 'guides/daily-loop' },
						{ label: 'MCP servers', slug: 'guides/mcp-servers' },
						{ label: 'Memory', slug: 'guides/memory' },
						{ label: 'Marketplaces & plugins', slug: 'guides/plugins' },
						{ label: 'Secrets', slug: 'guides/secrets' },
						{ label: 'Project-local config', slug: 'guides/projects' },
						{ label: 'Updating from the network', slug: 'guides/updating' },
						{ label: 'Cross-machine sync', slug: 'guides/cross-machine' },
					],
				},
				{
					label: 'Recipes',
					items: [
						{ label: 'Overview', slug: 'recipes' },
						{ label: 'Commit your config to dotfiles', slug: 'recipes/dotfiles' },
						{ label: 'Onboard a teammate', slug: 'recipes/team-onboarding' },
						{ label: 'Verify config in CI', slug: 'recipes/ci-verification' },
						{ label: 'Adopt a populated machine', slug: 'recipes/adopt-existing' },
					],
				},
				{
					label: 'Reference',
					items: [
						{ label: 'CLI commands', slug: 'reference/cli' },
						{ label: 'Configuration & layout', slug: 'reference/configuration' },
						{ label: 'Environment variables', slug: 'reference/environment' },
					],
				},
				{
					label: 'Concepts',
					items: [{ label: 'Concepts & glossary', slug: 'concepts' }],
				},
				{
					label: 'Under the hood',
					items: [
						{ label: 'Architecture', slug: 'internals/architecture' },
						{ label: 'Component map', slug: 'internals/components' },
						{ label: 'Security model', slug: 'internals/security' },
					],
				},
				{
					label: 'Help',
					items: [
						{ label: 'FAQ', slug: 'help/faq' },
						{ label: 'Troubleshooting', slug: 'help/troubleshooting' },
					],
				},
			],
		}),
	],
});

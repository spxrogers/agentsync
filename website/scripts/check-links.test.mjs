// Tests for scripts/check-links.mjs. Run with `node --test scripts/`.
//
// Two concerns:
//   1. The slugger produces the EXACT GitHub-style slugs the docs rely on
//      (github-slugger under the hood) — pinned as a regression guard,
//      including the two load-bearing slugs that motivated the checker.
//   2. The checker, run over a real on-disk fixture tree (written to a temp
//      dir per the "fidelity tests anchor to the artifact" principle), reports
//      exactly the broken targets and nothing else, and exits non-zero.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

import { slugifyHeading, checkTree } from './check-links.mjs';

const scriptPath = fileURLToPath(new URL('./check-links.mjs', import.meta.url));

// ---------------------------------------------------------------------------
// 1. Slugger fixture — pins the real slugs the docs depend on.
// ---------------------------------------------------------------------------

test('slugifyHeading matches the real GitHub-style slugs', () => {
	const cases = [
		// The two load-bearing slugs that motivated the checker. Note the em-dash
		// and the "8." both vanish while their surrounding spaces survive → `--`.
		['## 8. Secrets — how the leak is prevented', '8-secrets--how-the-leak-is-prevented'],
		['Drift — the 3-way classifier', 'drift--the-3-way-classifier'],
		// Ordinary cases: spaces, mixed case, backticks/code spans, punctuation.
		['Simple Heading', 'simple-heading'],
		['The apply / capture loop', 'the-apply--capture-loop'],
		['The `apply` command', 'the-apply-command'],
		['Hello, World!', 'hello-world'],
		['### PluginIngester (read-only)', 'pluginingester-read-only'],
		// A markdown link in a heading slugs to its label, not label+url.
		['See [the guide](/guides/rollback/)', 'see-the-guide'],
	];
	for (const [heading, expected] of cases) {
		assert.equal(slugifyHeading(heading), expected, `heading: ${heading}`);
	}
});

// ---------------------------------------------------------------------------
// 2. Checker behavior over a real on-disk fixture tree.
// ---------------------------------------------------------------------------

function writeFixtureTree() {
	const root = mkdtempSync(join(tmpdir(), 'check-links-'));

	const fm = (title) => `---\ntitle: ${JSON.stringify(title)}\n---\n\n`;

	// Root page (/). A valid site-absolute link + anchor.
	writeFileSync(
		join(root, 'index.md'),
		fm('Home') +
			'# Home\n\n## Overview\n\nJump to [page A](/page-a/#real-anchor).\n',
	);

	// A target page with a real heading (/page-a/, anchor #real-anchor).
	writeFileSync(
		join(root, 'page-a.md'),
		fm('Page A') + '## Real Anchor\n\nBody text.\n',
	);

	// All-good page: valid cross-page anchor, valid same-page anchor, valid
	// href route, a skipped external link, and a code-fenced link that MUST be
	// ignored (fence-skipping regression guard).
	writeFileSync(
		join(root, 'good.md'),
		fm('Good') +
			'## Good Section\n\n' +
			'Cross-page [ok](/page-a/#real-anchor) and same-page [self](#good-section).\n\n' +
			'An href route <a href="/page-a/">A</a> and an [external](https://example.com) link.\n\n' +
			'```bash\n' +
			'# not a real link, inside a fence:\n' +
			'see [fake](/nonexistent/#nope) here\n' +
			'```\n',
	);

	// The only page with breakages: one bad anchor (route resolves, anchor
	// does not) + one bad route. Nested in a subdir to exercise the walk.
	mkdirSync(join(root, 'sub'), { recursive: true });
	writeFileSync(
		join(root, 'sub', 'bad.md'),
		fm('Bad') +
			'## Bad Page\n\n' +
			'A [broken anchor](/page-a/#nope-not-real) and a [broken route](/does-not-exist/).\n',
	);

	return root;
}

test('checkTree reports exactly the broken targets, no false positives', () => {
	const root = writeFixtureTree();
	try {
		const { violations, stats } = checkTree(root);

		assert.equal(stats.pages, 4, 'walks all four fixture pages');
		assert.equal(violations.length, 2, 'exactly two breakages');

		const broken = violations
			.map((v) => `${v.kind}:${v.target}`)
			.sort();
		assert.deepEqual(broken, [
			'anchor:/page-a/#nope-not-real',
			'route:/does-not-exist/',
		]);

		// No false positives: every violation is in sub/bad.md.
		for (const v of violations) {
			assert.match(v.file, /bad\.md$/, `unexpected violation in ${v.file}`);
		}

		// The bad anchor carries closest-anchor hints (real-anchor is on page-a).
		const anchorViolation = violations.find((v) => v.kind === 'anchor');
		assert.ok(
			anchorViolation.hints.includes('real-anchor'),
			'anchor hint should suggest the real anchor',
		);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('CLI exits non-zero on breakage and names the bad targets', () => {
	const root = writeFixtureTree();
	try {
		const res = spawnSync(process.execPath, [scriptPath], {
			env: { ...process.env, CHECK_LINKS_ROOT: root },
			encoding: 'utf8',
		});
		assert.equal(res.status, 1, 'process exits non-zero on breakage');
		const out = res.stdout + res.stderr;
		assert.match(out, /\/page-a\/#nope-not-real/);
		assert.match(out, /\/does-not-exist\//);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

test('CLI exits zero on a clean tree', () => {
	const root = mkdtempSync(join(tmpdir(), 'check-links-clean-'));
	try {
		writeFileSync(
			join(root, 'index.md'),
			'---\ntitle: "Home"\n---\n\n## Overview\n\nAll [good](#overview).\n',
		);
		const res = spawnSync(process.execPath, [scriptPath], {
			env: { ...process.env, CHECK_LINKS_ROOT: root },
			encoding: 'utf8',
		});
		assert.equal(res.status, 0, 'process exits zero on a clean tree');
		assert.match(res.stdout, /0 broken/);
	} finally {
		rmSync(root, { recursive: true, force: true });
	}
});

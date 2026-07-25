// Build-time in-site link/anchor checker for the Starlight content tree.
//
// The mirror pipeline (scripts/sync-docs.mjs) copies the canonical docs/*.md
// contract pages into src/content/docs/ verbatim, rewriting link *paths* but
// never validating that a target page or `#anchor` actually exists. Authored
// .mdx pages are equally unchecked. So a broken in-site link or a dead heading
// anchor compiles and deploys silently.
//
// This script closes that gap. Run AFTER sync:docs (so it sees the generated +
// authored pages together), it walks every .md/.mdx under src/content/docs/,
// extracts every in-site link target (markdown `](/route…)` / `](#anchor)` and
// Starlight/HTML `href="…"`), and asserts:
//   * each site-absolute `/route` resolves to a real content file, and
//   * each `#anchor` matches a real heading slug on the TARGET page, using the
//     same GitHub-style slugger Starlight uses (github-slugger).
// External links (http(s)://, mailto:, tel:) and the GitHub blob/edit URLs the
// mirror emits for off-site repo docs are skipped. It collects ALL violations,
// prints a report (file, line, bad target, closest-anchor hints), and exits
// non-zero on any breakage; a clean run prints a one-line summary and exits 0.
// An ALLOWLIST entry that matched zero violations this run is STALE (the
// underlying link was fixed, or the page moved) and also fails the run with a
// remove-the-entry message, so dead exceptions cannot linger silently.
//
// Kept dependency-light and Astro-free (node: builtins + github-slugger) so it
// runs standalone in CI/local and under `node --test` without a full build.

import { readFileSync, readdirSync } from 'node:fs';
import { dirname, resolve, relative, join, sep } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import GithubSlugger from 'github-slugger';

const here = dirname(fileURLToPath(import.meta.url));
const defaultRoot = resolve(here, '..', 'src', 'content', 'docs');

// Known pre-existing breakages owned by OTHER issues. Each entry must carry a
// `// TODO(#NNN)` reference to the issue that fixes the underlying link; that
// issue deletes the entry when it lands. The checker treats an allowlisted
// violation as non-fatal (reported as a note, exit 0). Keep this EMPTY unless a
// genuine breakage this issue is not responsible for fixing survives on `main`.
// Staleness is ENFORCED: an entry that matches zero violations in a run fails
// the checker (see main), so forgetting to delete it when the underlying link
// is fixed shows up as a red run, not a silently dead exception.
const ALLOWLIST = [
	// docs/concepts.md links `[user guide](user-guide.md#command-reference)`.
	// sync-docs maps user-guide.md → /getting-started/introduction/, but that
	// authored page has no "Command reference" heading (the command reference
	// lives on /reference/cli/). A pre-existing concepts.md broken anchor —
	// concepts.md anchor hygiene is #145's domain; #145 merged fixing the
	// architecture anchor but not this one. Fixing it means editing the
	// canonical docs/concepts.md, which is out of scope for #170 (this issue
	// ships the guard, not the individual link fixes). Delete this entry when
	// the underlying docs/concepts.md link is corrected.
	{
		file: 'concepts/index.md',
		target: '/getting-started/introduction/#command-reference',
	}, // TODO(#145)
];

// ---------------------------------------------------------------------------
// Markdown parsing
// ---------------------------------------------------------------------------

const FENCE_RE = /^\s{0,3}(`{3,}|~{3,})/;
const ATX_RE = /^\s{0,3}(#{1,6})\s+(.*?)\s*$/;

// Reduce an ATX heading line to the plain text Starlight slugs: drop the
// leading `#`s and any trailing `#`s, unwrap markdown links/images to their
// label/alt text (so `[Foo](/x)` slugs as `foo`, not `foox`). Backticks, bold,
// and other punctuation are left for github-slugger, which strips them exactly
// as GitHub/Starlight do — critically WITHOUT collapsing the surrounding
// whitespace, so `8. Secrets — how …` keeps its double space (→ `--`).
export function headingText(raw) {
	return raw
		.replace(/^\s{0,3}#{1,6}\s+/, '')
		.replace(/\s+#*\s*$/, '')
		.replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
		.replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
		.trim();
}

// Slug a single heading with a fresh slugger (no dedupe state). Exposed for the
// slugger fixture test that pins the exact real slugs the docs rely on.
export function slugifyHeading(raw) {
	return new GithubSlugger().slug(headingText(raw));
}

// Parse a page into its ordered ATX heading texts and its in-site link
// candidates ({ line, target }). Skips YAML frontmatter, fenced code blocks,
// and inline code spans so example snippets never register as headings/links.
export function parsePage(content) {
	const lines = content.split(/\r?\n/);
	const headings = [];
	const links = [];
	let inFront = false;
	let inFence = false;
	let fenceChar = '';
	for (let i = 0; i < lines.length; i++) {
		const line = lines[i];
		const lineNo = i + 1;

		// Leading YAML frontmatter (only when it opens on the very first line).
		if (i === 0 && line.trim() === '---') {
			inFront = true;
			continue;
		}
		if (inFront) {
			if (line.trim() === '---') inFront = false;
			continue;
		}

		// Fenced code blocks (``` or ~~~). Closing fence must match the opener.
		const fence = line.match(FENCE_RE);
		if (fence) {
			const ch = fence[1][0];
			if (!inFence) {
				inFence = true;
				fenceChar = ch;
			} else if (ch === fenceChar) {
				inFence = false;
			}
			continue;
		}
		if (inFence) continue;

		// Headings.
		const heading = line.match(ATX_RE);
		if (heading) {
			headings.push(heading[2]);
			continue;
		}

		// Strip inline code spans before scanning for links so a documented
		// `](/x)` or `href="…"` inside backticks is not treated as a real link.
		const scan = line.replace(/`[^`]*`/g, '');

		const mdLink = /\]\(\s*([^)\s]+)(?:\s+(?:"[^"]*"|'[^']*'))?\s*\)/g;
		let m;
		while ((m = mdLink.exec(scan)) !== null) {
			links.push({ line: lineNo, target: m[1] });
		}

		const href = /href\s*=\s*(?:"([^"]*)"|'([^']*)')/g;
		while ((m = href.exec(scan)) !== null) {
			links.push({ line: lineNo, target: m[1] ?? m[2] ?? '' });
		}
	}
	return { headings, links };
}

// The set of heading-slug anchors a page exposes. A per-page slugger reproduces
// Starlight's duplicate-heading disambiguation (`foo`, `foo-1`, …).
export function pageAnchors(headings) {
	const slugger = new GithubSlugger();
	const set = new Set();
	for (const h of headings) set.add(slugger.slug(headingText(h)));
	return set;
}

// ---------------------------------------------------------------------------
// Route / file resolution (mirrors Starlight's default slug rules)
// ---------------------------------------------------------------------------

function walk(dir, acc = []) {
	for (const entry of readdirSync(dir, { withFileTypes: true })) {
		const full = join(dir, entry.name);
		if (entry.isDirectory()) walk(full, acc);
		else if (/\.mdx?$/.test(entry.name)) acc.push(full);
	}
	return acc;
}

// Map a content file to its site slug: path relative to the docs root, minus
// extension, lowercased, with `index` collapsing to its parent directory. The
// root `index.mdx` slugs to '' (the home route `/`).
export function fileToSlug(file, root) {
	let rel = relative(root, file).split(sep).join('/');
	rel = rel.replace(/\.mdx?$/, '');
	rel = rel.replace(/(^|\/)index$/, '');
	return rel.replace(/\/+$/, '').toLowerCase();
}

// Normalize a site-absolute route target to a comparable slug key.
function routeKey(path) {
	return path.replace(/^\/+/, '').replace(/\/+$/, '').toLowerCase();
}

// ---------------------------------------------------------------------------
// Checker
// ---------------------------------------------------------------------------

function levenshtein(a, b) {
	const m = a.length;
	const n = b.length;
	const dp = Array.from({ length: m + 1 }, (_, i) => i);
	for (let j = 1; j <= n; j++) {
		let prev = dp[0];
		dp[0] = j;
		for (let i = 1; i <= m; i++) {
			const tmp = dp[i];
			dp[i] = Math.min(
				dp[i] + 1,
				dp[i - 1] + 1,
				prev + (a[i - 1] === b[j - 1] ? 0 : 1),
			);
			prev = tmp;
		}
	}
	return dp[m];
}

function nearestAnchors(anchor, set) {
	const all = [...set];
	if (all.length === 0) return [];
	return all
		.map((a) => [a, levenshtein(anchor, a)])
		.sort((x, y) => x[1] - y[1])
		.slice(0, 3)
		.map(([a]) => a);
}

function isExternal(target) {
	return /^(?:https?:|mailto:|tel:)/i.test(target) || target.startsWith('//');
}

// Walk `root`, validate every in-site link, and return { violations, stats }.
// Pure (no process exit / logging) so tests can assert on the result directly.
export function checkTree(root) {
	const files = walk(root);
	const slugToFile = new Map();
	const validRoutes = new Set();
	for (const f of files) {
		const slug = fileToSlug(f, root);
		slugToFile.set(slug, f);
		validRoutes.add(slug);
	}

	const anchorCache = new Map();
	function anchorsFor(slug) {
		if (anchorCache.has(slug)) return anchorCache.get(slug);
		const file = slugToFile.get(slug);
		const { headings } = parsePage(readFileSync(file, 'utf8'));
		const set = pageAnchors(headings);
		anchorCache.set(slug, set);
		return set;
	}

	const violations = [];
	let linkCount = 0;

	for (const f of files) {
		const slug = fileToSlug(f, root);
		const { headings, links } = parsePage(readFileSync(f, 'utf8'));
		const selfAnchors = pageAnchors(headings);
		anchorCache.set(slug, selfAnchors);

		for (const { line, target } of links) {
			if (!target || isExternal(target)) continue;

			// Same-page anchor.
			if (target.startsWith('#')) {
				linkCount++;
				const anchor = decodeURIComponent(target.slice(1));
				if (!anchor) continue;
				if (!selfAnchors.has(anchor)) {
					violations.push({
						file: f,
						line,
						kind: 'anchor',
						target,
						hints: nearestAnchors(anchor, selfAnchors),
					});
				}
				continue;
			}

			// Site-absolute route (optionally with an anchor).
			if (target.startsWith('/')) {
				linkCount++;
				const hashIdx = target.indexOf('#');
				const path = hashIdx >= 0 ? target.slice(0, hashIdx) : target;
				const anchor =
					hashIdx >= 0 ? decodeURIComponent(target.slice(hashIdx + 1)) : '';
				const key = routeKey(path);
				if (!validRoutes.has(key)) {
					violations.push({ file: f, line, kind: 'route', target });
					continue;
				}
				if (anchor) {
					const set = anchorsFor(key);
					if (!set.has(anchor)) {
						violations.push({
							file: f,
							line,
							kind: 'anchor',
							target,
							hints: nearestAnchors(anchor, set),
						});
					}
				}
				continue;
			}

			// Anything else (relative paths, bare `foo.md`) is out of the in-site
			// route/anchor scope this checker owns — skip.
		}
	}

	return {
		violations,
		stats: { pages: files.length, links: linkCount, broken: violations.length },
	};
}

// The index of the allowlist entry shielding this violation, or -1. Indexes
// (not a boolean) so main can tell which entries matched — an entry that
// matched nothing is stale and fails the run.
function allowlistIndex(violation, root, allowlist) {
	const rel = relative(root, violation.file).split(sep).join('/');
	return allowlist.findIndex(
		(e) => e.file === rel && e.target === violation.target,
	);
}

// Test seam: CHECK_LINKS_ALLOWLIST (a JSON array of { file, target }) replaces
// the hardcoded ALLOWLIST, so the CLI tests can exercise the allowlisted and
// stale paths over fixture trees without the real entries bleeding into (or
// stale-failing) their runs. Production runs never set it.
function loadAllowlist() {
	const env = process.env.CHECK_LINKS_ALLOWLIST;
	return env === undefined ? ALLOWLIST : JSON.parse(env);
}

function reportPath(file) {
	const rel = relative(process.cwd(), file);
	return rel.startsWith('..') ? file : rel;
}

function main() {
	const root = process.env.CHECK_LINKS_ROOT
		? resolve(process.env.CHECK_LINKS_ROOT)
		: defaultRoot;

	const allowlist = loadAllowlist();
	const { violations, stats } = checkTree(root);
	const matched = new Set();
	const fatal = [];
	for (const v of violations) {
		const idx = allowlistIndex(v, root, allowlist);
		if (idx >= 0) matched.add(idx);
		else fatal.push(v);
	}
	const allowed = violations.length - fatal.length;
	// A stale entry shields nothing: its breakage was fixed (or the page moved),
	// so the exception must be deleted, not left to mask a future regression.
	const stale = allowlist.filter((_, i) => !matched.has(i));
	// A DUPLICATE allowlist entry can never match (allowlistIndex returns the
	// first index), so it would surface as a confusing "stale" forever — name
	// the real problem instead.
	const seenEntries = new Set();
	const dupes = [];
	for (const e of allowlist) {
		const k = `${e.file}\u0000${e.target}`;
		if (seenEntries.has(k)) dupes.push(e);
		seenEntries.add(k);
	}

	const cleanRun = fatal.length === 0 && stale.length === 0 && dupes.length === 0;
	if (cleanRun) {
		console.log(
			`check-links: ${stats.pages} pages, ${stats.links} in-site links, 0 broken`,
		);
		if (allowed > 0) {
			console.log(
				`  (${allowed} known pre-existing exception(s) allowlisted; see ALLOWLIST in scripts/check-links.mjs)`,
			);
		}
	} else if (fatal.length > 0) {
		console.error(
			`check-links: found ${fatal.length} broken in-site link(s) across ${stats.pages} pages:\n`,
		);
		for (const v of fatal) {
			const loc = `${reportPath(v.file)}:${v.line}`;
			if (v.kind === 'route') {
				console.error(`  ${loc}  broken route -> ${v.target}`);
			} else {
				console.error(`  ${loc}  broken anchor -> ${v.target}`);
				if (v.hints && v.hints.length > 0) {
					console.error(
						`      did you mean: ${v.hints.map((h) => `#${h}`).join('  ')} ?`,
					);
				}
			}
		}
		console.error(
			`\ncheck-links: ${stats.pages} pages, ${stats.links} in-site links, ${fatal.length} broken.`,
		);
	}

	if (dupes.length > 0) {
		console.error(
			`\ncheck-links: ${dupes.length} DUPLICATE ALLOWLIST entr${dupes.length === 1 ? 'y' : 'ies'} (only the first copy can ever match — delete the duplicate):`,
		);
		for (const e of dupes) {
			console.error(`  ${e.file}  ->  ${e.target}`);
		}
	}
	if (stale.length > 0) {
		console.error(
			`\ncheck-links: ${stale.length} stale ALLOWLIST entr${stale.length === 1 ? 'y' : 'ies'} matched zero violations this run:`,
		);
		for (const e of stale) {
			console.error(`  ${e.file}  ->  ${e.target}`);
		}
		console.error(
			'  the underlying link was fixed (or the page moved) — remove the stale entry from ALLOWLIST in scripts/check-links.mjs.',
		);
	}

	return cleanRun ? 0 : 1;
}

// Run as a CLI only when invoked directly (not when imported by tests).
if (
	process.argv[1] &&
	import.meta.url === pathToFileURL(process.argv[1]).href
) {
	process.exit(main());
}

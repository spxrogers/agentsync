#!/usr/bin/env node
// Build (and optionally publish) the agentsync npm packages from a GoReleaser
// release — the thing that makes `npx agentsync.cc` / `bunx agentsync.cc` work.
//
//   node npm/build.mjs --dist <dir> [--out <dir>] [--expect-version X.Y.Z]
//                      [--publish [--provenance] [--dry-run]]
//
// <dir> holds the release's archives (agentsync_<ver>_<os>_<arch>.tar.gz|.zip)
// and checksums.txt — either a local `goreleaser release --snapshot` dist/ or the
// assets downloaded from a GitHub Release (.github/workflows/npm-publish.yml).
// Every archive is sha256-checked against checksums.txt before its binary is
// packed, so npm ships byte-for-byte the binaries the GitHub Release ships.
//
// Layout (the esbuild/biome pattern — no postinstall, so bunx and
// --ignore-scripts work):
//   agentsync.cc                              launcher (npm/bin/agentsync.js)
//                                             + optionalDependencies
//   @spxrogers/agentsync.cc-<platform>-<arch> one prebuilt binary each, os/cpu-gated
// The platform packages live in the maintainer's npm scope so nobody else can
// publish under their names (squatting); users only ever type `agentsync.cc`.
//
// --publish publishes the platform packages first, then the launcher (so its
// optionalDependencies always resolve). A name@version already on the registry
// is skipped ONLY if its tarball is byte-identical to ours (same sha512
// integrity) — so re-running a half-finished publish is the recovery path,
// matching the rest of the release pipeline, while a version someone else put
// there (a squatted platform-package name) fails the publish instead of being
// silently pinned by our launcher.
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

export const MAIN_PACKAGE = 'agentsync.cc';
// npm scope of the platform packages: the publishing account's own scope, so the
// names can't be registered by anyone else.
export const PLATFORM_SCOPE = '@spxrogers';

// Every GoReleaser target (.goreleaser.yaml builds: goos × goarch), mapped to
// Node's process.platform / process.arch. A release missing any of these is
// refused: the launcher's optionalDependencies must never name a package that
// wasn't published.
export const TARGETS = [
	{ goos: 'linux', goarch: 'amd64', platform: 'linux', arch: 'x64' },
	{ goos: 'linux', goarch: 'arm64', platform: 'linux', arch: 'arm64' },
	{ goos: 'darwin', goarch: 'amd64', platform: 'darwin', arch: 'x64' },
	{ goos: 'darwin', goarch: 'arm64', platform: 'darwin', arch: 'arm64' },
	{ goos: 'windows', goarch: 'amd64', platform: 'win32', arch: 'x64' },
	{ goos: 'windows', goarch: 'arm64', platform: 'win32', arch: 'arm64' },
];

const SHARED = {
	license: 'MIT',
	homepage: 'https://agentsync.cc',
	repository: { type: 'git', url: 'git+https://github.com/spxrogers/agentsync.git' },
	bugs: { url: 'https://github.com/spxrogers/agentsync/issues' },
};

const SEMVER =
	/^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$/;

export function platformPackageName(t) {
	return `${PLATFORM_SCOPE}/${MAIN_PACKAGE}-${t.platform}-${t.arch}`;
}

export function archiveName(version, t) {
	return `agentsync_${version}_${t.goos}_${t.goarch}.${t.goos === 'windows' ? 'zip' : 'tar.gz'}`;
}

export function binaryName(t) {
	return t.goos === 'windows' ? 'agentsync.exe' : 'agentsync';
}

// compareVersions orders two semver strings by precedence (build metadata
// ignored; a prerelease sorts below its release; prerelease identifiers compare
// numerically when both are numeric, else lexically — semver.org §11).
export function compareVersions(a, b) {
	const parse = (v) => {
		const [core, pre] = v.split('+')[0].split(/-(.*)/s);
		return { core: core.split('.').map(Number), pre: pre === undefined ? [] : pre.split('.') };
	};
	const x = parse(a);
	const y = parse(b);
	for (let i = 0; i < 3; i++) if (x.core[i] !== y.core[i]) return x.core[i] < y.core[i] ? -1 : 1;
	if (!x.pre.length || !y.pre.length) return x.pre.length === y.pre.length ? 0 : x.pre.length ? -1 : 1;
	for (let i = 0; i < Math.max(x.pre.length, y.pre.length); i++) {
		const p = x.pre[i];
		const q = y.pre[i];
		if (p === undefined || q === undefined) return p === undefined ? -1 : 1;
		if (p === q) continue;
		const pn = /^\d+$/.test(p);
		const qn = /^\d+$/.test(q);
		if (pn && qn) return Number(p) < Number(q) ? -1 : 1;
		if (pn !== qn) return pn ? -1 : 1;
		return p < q ? -1 : 1;
	}
	return 0;
}

// distTag picks the dist-tag every package of this version is published under,
// given the launcher's current `latest` (null if it has none yet):
//   - prereleases (v1.2.3-rc.1, snapshots) go to `next`, never `latest`, so a
//     plain `npx agentsync.cc` doesn't pick one up;
//   - a stable version OLDER than the current `latest` (a backfill, or a patch
//     on an old line) goes to `backfill` — `npm publish --tag latest` would
//     otherwise move `latest` backwards and downgrade every `npx` user;
//   - anything else is the new `latest`.
// The registry points `latest` at a package's FIRST version whatever tag it was
// published with, so an rc as the very first publish lands on `latest`; a
// prerelease `latest` is therefore treated as no latest at all, and the first
// stable version of any number replaces it.
export function distTag(version, currentLatest) {
	const isPrerelease = (v) => v.split('+')[0].includes('-');
	if (isPrerelease(version)) return 'next';
	if (currentLatest && !isPrerelease(currentLatest) && compareVersions(version, currentLatest) < 0) return 'backfill';
	return 'latest';
}

export function platformPackageJSON(version, t) {
	return {
		name: platformPackageName(t),
		version,
		description: `The ${t.platform}-${t.arch} binary for agentsync (${MAIN_PACKAGE}). Install ${MAIN_PACKAGE}, not this.`,
		...SHARED,
		os: [t.platform],
		cpu: [t.arch],
		// Yarn PnP: keep the binary on disk so it can be exec'd.
		preferUnplugged: true,
		files: [`bin/${binaryName(t)}`],
	};
}

export function mainPackageJSON(version) {
	return {
		name: MAIN_PACKAGE,
		version,
		description: 'Centrally manage AI coding-agent configurations (Claude Code, Codex, Cursor, OpenCode, Gemini CLI, and more).',
		...SHARED,
		keywords: ['agentsync', 'ai', 'agents', 'claude', 'codex', 'cursor', 'opencode', 'gemini', 'mcp', 'dotfiles', 'cli'],
		bin: { agentsync: 'bin/agentsync.js' },
		files: ['bin/agentsync.js'],
		engines: { node: '>=18' },
		optionalDependencies: Object.fromEntries(TARGETS.map((t) => [platformPackageName(t), version])),
	};
}

// parseChecksums reads GoReleaser's checksums.txt (`<sha256>  <file>` lines).
export function parseChecksums(text) {
	const sums = new Map();
	for (const line of text.split(/\r?\n/)) {
		const m = line.match(/^([0-9a-f]{64})\s+\*?(\S+)$/);
		if (m) sums.set(m[2], m[1]);
	}
	return sums;
}

// inferVersion finds the single version the dist's archives were cut at.
export function inferVersion(files) {
	const versions = new Set();
	for (const f of files) {
		const m = f.match(/^agentsync_(.+)_(?:linux|darwin|windows)_[a-z0-9]+\.(?:tar\.gz|zip)$/);
		if (m) versions.add(m[1]);
	}
	if (versions.size !== 1) {
		throw new Error(`expected archives for exactly one version, found ${versions.size ? [...versions].join(', ') : 'none'}`);
	}
	return [...versions][0];
}

function extractBinary(archive, member, destFile) {
	const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-npm-'));
	try {
		if (archive.endsWith('.zip')) {
			try {
				execFileSync('unzip', ['-q', '-o', archive, member, '-d', tmp], { stdio: 'inherit' });
			} catch (err) {
				if (err.code !== 'ENOENT') throw err;
				// No unzip (macOS/Windows dev boxes): bsdtar reads zip too.
				execFileSync('tar', ['-xf', archive, '-C', tmp, member], { stdio: 'inherit' });
			}
		} else {
			execFileSync('tar', ['-xzf', archive, '-C', tmp, member], { stdio: 'inherit' });
		}
		// A release archive's `agentsync` must be a real, non-empty file: never
		// follow a symlink member (it would pack whatever file it points at on
		// this runner).
		const extracted = path.join(tmp, member);
		const st = fs.lstatSync(extracted);
		if (!st.isFile() || st.size === 0) {
			throw new Error(`${path.basename(archive)}: ${member} is not a regular non-empty file`);
		}
		fs.mkdirSync(path.dirname(destFile), { recursive: true });
		fs.copyFileSync(extracted, destFile);
		fs.chmodSync(destFile, 0o755);
	} finally {
		fs.rmSync(tmp, { recursive: true, force: true });
	}
}

// writeFile writes a package file with a fixed 0644 mode: npm packs the files'
// mode bits, so a umask-dependent mode would change the tarball's integrity and
// make an identical re-run look like a different build.
function writeFile(file, data) {
	fs.writeFileSync(file, data);
	fs.chmodSync(file, 0o644);
}

function writeJSON(file, value) {
	writeFile(file, `${JSON.stringify(value, null, 2)}\n`);
}

// realpathLoose resolves symlinks in the longest existing prefix of p (the rest
// may not exist yet), so two paths can be compared for containment.
function realpathLoose(p) {
	const tail = [];
	let head = path.resolve(p);
	while (!fs.existsSync(head)) {
		tail.unshift(path.basename(head));
		const up = path.dirname(head);
		if (up === head) break;
		head = up;
	}
	// .native also canonicalizes letter case on case-insensitive filesystems
	// (macOS), so `--out Release` can't slip past `--dist release`.
	return path.join(fs.existsSync(head) ? fs.realpathSync.native(head) : head, ...tail);
}

// contains reports whether `inner` is `outer` itself or somewhere beneath it.
export function contains(outer, inner) {
	const rel = path.relative(realpathLoose(outer), realpathLoose(inner));
	return rel !== '..' && !rel.startsWith(`..${path.sep}`) && !path.isAbsolute(rel);
}

// build assembles every package under outDir and returns their directories,
// platform packages first (publish order).
export function build({ dist, outDir, expectVersion }) {
	const here = path.dirname(fileURLToPath(import.meta.url));
	const repoRoot = path.resolve(here, '..');
	dist = path.resolve(dist);
	outDir = path.resolve(outDir);
	// outDir is wiped below; refuse anything that would take the release, the
	// checkout, the working directory, or the home directory with it.
	for (const [what, p] of [
		['--dist', dist],
		['the repository', repoRoot],
		['the working directory', process.cwd()],
		['the home directory', os.homedir()],
	]) {
		if (contains(outDir, p)) {
			throw new Error(`--out ${outDir} would delete ${what} (${p}); pick a dedicated output directory`);
		}
	}
	const files = fs.readdirSync(dist);
	const version = inferVersion(files);
	if (expectVersion && expectVersion !== version) {
		throw new Error(`--expect-version ${expectVersion} does not match the archives in ${dist} (${version})`);
	}
	if (!SEMVER.test(version)) throw new Error(`version ${version} is not valid semver`);

	const sumsFile = path.join(dist, 'checksums.txt');
	if (!fs.existsSync(sumsFile)) throw new Error(`${sumsFile} not found`);
	const sums = parseChecksums(fs.readFileSync(sumsFile, 'utf8'));

	fs.rmSync(outDir, { recursive: true, force: true });
	const license = fs.readFileSync(path.join(repoRoot, 'LICENSE'));
	const dirs = [];

	for (const t of TARGETS) {
		const archive = archiveName(version, t);
		const archivePath = path.join(dist, archive);
		if (!fs.existsSync(archivePath)) throw new Error(`missing release archive ${archive} in ${dist}`);
		const want = sums.get(archive);
		if (!want) throw new Error(`checksums.txt has no entry for ${archive}`);
		const got = createHash('sha256').update(fs.readFileSync(archivePath)).digest('hex');
		if (got !== want) throw new Error(`checksum mismatch for ${archive}: got ${got}, checksums.txt says ${want}`);

		const pkg = platformPackageJSON(version, t);
		const dir = path.join(outDir, pkg.name);
		extractBinary(archivePath, binaryName(t), path.join(dir, 'bin', binaryName(t)));
		writeJSON(path.join(dir, 'package.json'), pkg);
		writeFile(
			path.join(dir, 'README.md'),
			`# ${pkg.name}\n\nThe prebuilt \`${t.platform}-${t.arch}\` binary for [agentsync](https://agentsync.cc).\n` +
				`Don't install this directly — install [\`${MAIN_PACKAGE}\`](https://www.npmjs.com/package/${MAIN_PACKAGE}), ` +
				'which picks the right platform package for you.\n',
		);
		writeFile(path.join(dir, 'LICENSE'), license);
		dirs.push(dir);
	}

	const mainDir = path.join(outDir, MAIN_PACKAGE);
	fs.mkdirSync(path.join(mainDir, 'bin'), { recursive: true });
	fs.copyFileSync(path.join(here, 'bin', 'agentsync.js'), path.join(mainDir, 'bin', 'agentsync.js'));
	fs.chmodSync(path.join(mainDir, 'bin', 'agentsync.js'), 0o755);
	writeFile(path.join(mainDir, 'README.md'), fs.readFileSync(path.join(here, 'README.md')));
	writeFile(path.join(mainDir, 'LICENSE'), license);
	writeJSON(path.join(mainDir, 'package.json'), mainPackageJSON(version));
	dirs.push(mainDir);
	return { version, dirs };
}

// npmJSON runs `npm <args> --json` and returns the parsed stdout, or null when
// npm reports E404 (the package, or that version of it, isn't on the registry).
// Any other failure (auth, network) throws: never guess about the registry.
function npmJSON(npmBin, args) {
	try {
		const out = execFileSync(npmBin, [...args, '--json'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
		return out.trim() === '' ? null : JSON.parse(out);
	} catch (err) {
		if (/\bE404\b/.test(`${err.stderr}${err.stdout}`)) return null;
		throw new Error(`npm ${args.join(' ')} failed: ${err.stderr || err.message}`);
	}
}

// publish pushes the built packages in order (platform packages, then the
// launcher). npmBin is injectable for tests; production uses the npm on PATH.
export function publish({ version, dirs, provenance = false, dryRun = false, npmBin = 'npm', log = console.log }) {
	const tag = distTag(version, npmJSON(npmBin, ['view', MAIN_PACKAGE, 'dist-tags.latest']));
	log(`publishing ${MAIN_PACKAGE}@${version} under dist-tag ${tag}${dryRun ? ' (dry run)' : ''}`);
	for (const dir of dirs) {
		const { name } = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'));
		const published = npmJSON(npmBin, ['view', `${name}@${version}`, 'dist.integrity']);
		if (published !== null) {
			const [ours] = npmJSON(npmBin, ['pack', dir, '--dry-run']) ?? [];
			if (!ours || published !== ours.integrity) {
				let publisher = 'unknown';
				try {
					publisher = npmJSON(npmBin, ['view', `${name}@${version}`, '_npmUser']) ?? publisher;
				} catch {
					// best effort: it only enriches the message below
				}
				// Registry-supplied strings are JSON-quoted so they can't inject lines
				// (e.g. GitHub Actions `::` workflow commands) into the log.
				throw new Error(
					`${name}@${version} is already on the registry with DIFFERENT contents: ` +
						`registry ${JSON.stringify(published)} (published by ${JSON.stringify(publisher)}), ` +
						`ours ${JSON.stringify(ours?.integrity ?? null)}. Refusing to publish a launcher that ` +
						'would pin it. If that publisher is not us, the name was hijacked: stop and investigate. ' +
						'If it is us, this build differs from the one published: the npm/ tooling, LICENSE, or ' +
						'README changed since then (a backfill of a pre-npm tag uses the default branch), or the ' +
						'Node/npm version that packed it did (npm-publish.yml pins one). Recover by re-running the ' +
						'failed job or dispatching npm-publish for the tag; failing that, cut a patch release.',
				);
			}
			log(`skip ${name}@${version}: already published (identical tarball)`);
			continue;
		}
		const args = ['publish', dir, '--access', 'public', '--tag', tag];
		if (provenance) args.push('--provenance');
		if (dryRun) args.push('--dry-run');
		log(`npm ${args.join(' ')}`);
		execFileSync(npmBin, args, { stdio: 'inherit' });
	}
}

function main() {
	const { values } = parseArgs({
		options: {
			dist: { type: 'string' },
			out: { type: 'string' },
			'expect-version': { type: 'string' },
			publish: { type: 'boolean', default: false },
			provenance: { type: 'boolean', default: false },
			'dry-run': { type: 'boolean', default: false },
		},
	});
	const usage =
		'usage: node npm/build.mjs --dist <dir> [--out <dir>] [--expect-version X.Y.Z] [--publish [--provenance] [--dry-run]]';
	if (!values.dist || ((values.provenance || values['dry-run']) && !values.publish)) {
		console.error(usage);
		process.exit(2);
	}
	const outDir = path.resolve(values.out ?? path.join(values.dist, 'npm'));
	const { version, dirs } = build({ dist: values.dist, outDir, expectVersion: values['expect-version'] });
	console.log(`built ${dirs.length} packages for ${MAIN_PACKAGE}@${version} in ${outDir}`);
	if (values.publish) {
		publish({ version, dirs, provenance: values.provenance, dryRun: values['dry-run'] });
	}
}

// Run main() only when executed directly (not when imported by the tests).
// realpath both sides so a symlinked checkout or invocation path still matches.
const invoked = process.argv[1] && fs.existsSync(process.argv[1]) ? fs.realpathSync(process.argv[1]) : null;
if (invoked && invoked === fs.realpathSync(fileURLToPath(import.meta.url))) {
	try {
		main();
	} catch (err) {
		console.error(`npm/build.mjs: ${err.message}`);
		process.exit(1);
	}
}

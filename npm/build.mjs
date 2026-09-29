#!/usr/bin/env node
// Build (and optionally publish) the agentsync npm packages from a GoReleaser
// release — the thing that makes `npx agentsync.cc` / `bunx agentsync.cc` work.
//
//   node npm/build.mjs --dist <dir> [--out <dir>] [--version X.Y.Z]
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
//   agentsync.cc                  launcher (npm/bin/agentsync.js) + optionalDependencies
//   agentsync.cc-<platform>-<arch> one prebuilt binary each, gated by os/cpu
//
// --publish publishes the platform packages first, then the launcher (so its
// optionalDependencies always resolve), and skips any name@version already on
// the registry — so re-running a half-finished publish is the recovery path,
// matching the rest of the release pipeline.
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';

export const MAIN_PACKAGE = 'agentsync.cc';

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
	return `${MAIN_PACKAGE}-${t.platform}-${t.arch}`;
}

export function archiveName(version, t) {
	return `agentsync_${version}_${t.goos}_${t.goarch}.${t.goos === 'windows' ? 'zip' : 'tar.gz'}`;
}

export function binaryName(t) {
	return t.goos === 'windows' ? 'agentsync.exe' : 'agentsync';
}

// distTag keeps prereleases (v1.2.3-rc.1, snapshots) off `latest`, so a plain
// `npx agentsync.cc` never picks one up.
export function distTag(version) {
	return version.includes('-') ? 'next' : 'latest';
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
		fs.mkdirSync(path.dirname(destFile), { recursive: true });
		fs.copyFileSync(path.join(tmp, member), destFile);
		fs.chmodSync(destFile, 0o755);
	} finally {
		fs.rmSync(tmp, { recursive: true, force: true });
	}
}

function writeJSON(file, value) {
	fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`);
}

// build assembles every package under outDir and returns their directories,
// platform packages first (publish order).
export function build({ dist, outDir, version }) {
	const here = path.dirname(fileURLToPath(import.meta.url));
	const repoRoot = path.resolve(here, '..');
	const files = fs.readdirSync(dist);
	const inferred = inferVersion(files);
	if (version && version !== inferred) {
		throw new Error(`--version ${version} does not match the archives in ${dist} (${inferred})`);
	}
	version = inferred;
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
		fs.writeFileSync(
			path.join(dir, 'README.md'),
			`# ${pkg.name}\n\nThe prebuilt \`${t.platform}-${t.arch}\` binary for [agentsync](https://agentsync.cc).\n` +
				`Don't install this directly — install [\`${MAIN_PACKAGE}\`](https://www.npmjs.com/package/${MAIN_PACKAGE}), ` +
				'which picks the right platform package for you.\n',
		);
		fs.writeFileSync(path.join(dir, 'LICENSE'), license);
		dirs.push(dir);
	}

	const mainDir = path.join(outDir, MAIN_PACKAGE);
	fs.mkdirSync(path.join(mainDir, 'bin'), { recursive: true });
	fs.copyFileSync(path.join(here, 'bin', 'agentsync.js'), path.join(mainDir, 'bin', 'agentsync.js'));
	fs.chmodSync(path.join(mainDir, 'bin', 'agentsync.js'), 0o755);
	fs.copyFileSync(path.join(here, 'README.md'), path.join(mainDir, 'README.md'));
	fs.writeFileSync(path.join(mainDir, 'LICENSE'), license);
	writeJSON(path.join(mainDir, 'package.json'), mainPackageJSON(version));
	dirs.push(mainDir);
	return { version, dirs };
}

function alreadyPublished(name, version) {
	try {
		const out = execFileSync('npm', ['view', `${name}@${version}`, 'version'], {
			encoding: 'utf8',
			stdio: ['ignore', 'pipe', 'pipe'],
		});
		return out.trim() === version;
	} catch (err) {
		// E404 = the package (or this version) doesn't exist yet. Anything else
		// (auth, network) is a real failure: don't guess.
		if (/E404|404 Not Found/.test(`${err.stderr}`)) return false;
		throw new Error(`npm view ${name}@${version} failed: ${err.stderr || err.message}`);
	}
}

export function publish({ version, dirs, provenance, dryRun }) {
	const tag = distTag(version);
	for (const dir of dirs) {
		const { name } = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'));
		if (!dryRun && alreadyPublished(name, version)) {
			console.log(`skip ${name}@${version}: already published`);
			continue;
		}
		const args = ['publish', dir, '--access', 'public', '--tag', tag];
		if (provenance) args.push('--provenance');
		if (dryRun) args.push('--dry-run');
		console.log(`npm ${args.join(' ')}`);
		execFileSync('npm', args, { stdio: 'inherit' });
	}
}

function main() {
	const { values } = parseArgs({
		options: {
			dist: { type: 'string' },
			out: { type: 'string' },
			version: { type: 'string' },
			publish: { type: 'boolean', default: false },
			provenance: { type: 'boolean', default: false },
			'dry-run': { type: 'boolean', default: false },
		},
	});
	if (!values.dist) {
		console.error('usage: node npm/build.mjs --dist <dir> [--out <dir>] [--version X.Y.Z] [--publish [--provenance] [--dry-run]]');
		process.exit(2);
	}
	const outDir = path.resolve(values.out ?? path.join(values.dist, 'npm'));
	const { version, dirs } = build({ dist: path.resolve(values.dist), outDir, version: values.version });
	console.log(`built ${dirs.length} packages for ${MAIN_PACKAGE}@${version} (dist-tag ${distTag(version)}) in ${outDir}`);
	if (values.publish) {
		publish({ version, dirs, provenance: values.provenance, dryRun: values['dry-run'] });
	}
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
	try {
		main();
	} catch (err) {
		console.error(`npm/build.mjs: ${err.message}`);
		process.exit(1);
	}
}

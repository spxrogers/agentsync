// Tests for npm/build.mjs: package metadata, the launcher ↔ builder naming
// contract, the GoReleaser target parity guard, dist-tag selection, build() over
// fabricated release archives (checksum verification included), and publish()
// against a fake `npm` that records every call.
//
//   node --test npm/*.test.mjs
import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import { createRequire } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';

import {
	MAIN_PACKAGE,
	TARGETS,
	archiveName,
	binaryName,
	PLATFORM_SCOPE,
	build,
	compareVersions,
	contains,
	distTag,
	inferVersion,
	mainPackageJSON,
	parseChecksums,
	platformPackageJSON,
	platformPackageName,
	publish,
} from './build.mjs';

const require = createRequire(import.meta.url);
const here = path.dirname(fileURLToPath(import.meta.url));
const { platformPackage } = require('./bin/agentsync.js');

describe('targets', () => {
	// Adding a goos/goarch to .goreleaser.yaml without teaching npm about it (or
	// vice versa) must fail here, not at publish time.
	test('match every GoReleaser build target', () => {
		const yaml = fs.readFileSync(path.join(here, '..', '.goreleaser.yaml'), 'utf8');
		const list = (key) => {
			const m = yaml.match(new RegExp(`^\\s+${key}:\\s*\\[([^\\]]*)\\]`, 'm'));
			assert.ok(m, `.goreleaser.yaml has no inline ${key}: [...] list`);
			return m[1].split(',').map((s) => s.trim());
		};
		const want = list('goos')
			.flatMap((goos) => list('goarch').map((goarch) => `${goos}/${goarch}`))
			.sort();
		assert.deepEqual(TARGETS.map((t) => `${t.goos}/${t.goarch}`).sort(), want);
	});

	test('the launcher resolves exactly the package the builder publishes', () => {
		const main = mainPackageJSON('1.0.0');
		for (const t of TARGETS) {
			assert.equal(platformPackage(main, t.platform, t.arch), platformPackageName(t));
		}
	});

	test('platform packages live in the maintainer scope', () => {
		for (const t of TARGETS) {
			assert.ok(platformPackageName(t).startsWith(`${PLATFORM_SCOPE}/${MAIN_PACKAGE}-`), platformPackageName(t));
		}
	});
});

describe('package metadata', () => {
	test('main package pins every platform package to its own version', () => {
		const pkg = mainPackageJSON('0.16.0');
		assert.equal(pkg.name, MAIN_PACKAGE);
		assert.deepEqual(pkg.bin, { agentsync: 'bin/agentsync.js' });
		assert.equal(Object.keys(pkg.optionalDependencies).length, TARGETS.length);
		for (const v of Object.values(pkg.optionalDependencies)) assert.equal(v, '0.16.0');
		assert.equal(pkg.scripts, undefined, 'no install scripts: bunx and --ignore-scripts must work');
	});

	test('platform packages are gated by os/cpu and carry only the binary', () => {
		for (const t of TARGETS) {
			const pkg = platformPackageJSON('0.16.0', t);
			assert.deepEqual(pkg.os, [t.platform]);
			assert.deepEqual(pkg.cpu, [t.arch]);
			assert.deepEqual(pkg.files, [`bin/${binaryName(t)}`]);
			assert.equal(pkg.repository.url, 'git+https://github.com/spxrogers/agentsync.git', 'provenance needs the source repo');
		}
	});

	test('prereleases stay off latest', () => {
		assert.equal(distTag('1.2.3', null), 'latest');
		assert.equal(distTag('1.2.3-rc.1', null), 'next');
		assert.equal(distTag('0.16.1-snapshot-abc1234', '0.16.0'), 'next');
	});

	test('an older stable version never moves latest backwards', () => {
		assert.equal(distTag('0.14.0', '0.16.0'), 'backfill');
		assert.equal(distTag('0.15.9', '0.16.0'), 'backfill');
		assert.equal(distTag('0.16.0', '0.16.0'), 'latest', 're-running the current latest');
		assert.equal(distTag('0.16.1', '0.16.0'), 'latest');
		assert.equal(distTag('1.0.0', '1.0.0-rc.2'), 'latest', 'a release outranks its own rc');
	});

	test('a prerelease latest (the registry tags a first publish latest) never forces backfill', () => {
		assert.equal(distTag('0.9.0', '1.0.0-rc.1'), 'latest');
	});

	test('build metadata is not a prerelease', () => {
		assert.equal(distTag('1.0.0+build-1', null), 'latest');
		assert.equal(distTag('1.0.0-rc.1+build', null), 'next');
	});

	test('compareVersions follows semver precedence', () => {
		const ordered = ['0.9.0', '0.10.0', '1.0.0-alpha', '1.0.0-alpha.1', '1.0.0-alpha.beta', '1.0.0-beta.2', '1.0.0-beta.11', '1.0.0-rc.1', '1.0.0', '1.0.1', '2.0.0'];
		for (let i = 0; i < ordered.length; i++) {
			for (let j = 0; j < ordered.length; j++) {
				assert.equal(compareVersions(ordered[i], ordered[j]), Math.sign(i - j), `${ordered[i]} vs ${ordered[j]}`);
			}
		}
		assert.equal(compareVersions('1.0.0+build.5', '1.0.0'), 0, 'build metadata is ignored');
	});
});

describe('release parsing', () => {
	test('parseChecksums reads goreleaser lines', () => {
		const a = 'a'.repeat(64);
		const b = 'b'.repeat(64);
		const sums = parseChecksums(`${a}  agentsync_1.0.0_linux_amd64.tar.gz\n${b}  agentsync_linux_amd64.deb\n\n`);
		assert.equal(sums.get('agentsync_1.0.0_linux_amd64.tar.gz'), a);
		assert.equal(sums.get('agentsync_linux_amd64.deb'), b);
	});

	test('inferVersion needs exactly one version', () => {
		assert.equal(inferVersion(['agentsync_1.0.0_linux_amd64.tar.gz', 'agentsync_1.0.0_windows_arm64.zip', 'checksums.txt', 'agentsync_linux_amd64.deb']), '1.0.0');
		assert.equal(inferVersion(['agentsync_1.0.1-snapshot-abc_darwin_arm64.tar.gz']), '1.0.1-snapshot-abc');
		assert.throws(() => inferVersion(['checksums.txt']), /found none/);
		assert.throws(() => inferVersion(['agentsync_1.0.0_linux_amd64.tar.gz', 'agentsync_1.0.1_linux_amd64.tar.gz']), /exactly one version/);
	});
});

// In CI a missing tool must fail, not silently skip the build() suite.
const haveTools = (() => {
	try {
		execFileSync('zip', ['-v'], { stdio: 'ignore' });
		execFileSync('tar', ['--version'], { stdio: 'ignore' });
		return true;
	} catch {
		return false;
	}
})();

// fakeRelease writes a GoReleaser-shaped dist at <root>/dist: one archive per
// target (binary + the README goreleaser bundles) and checksums.txt over them.
// The dist is one level below a private root, so a test can aim --out at the
// root (a parent of --dist) without a regressed guard ever reaching os.tmpdir().
// Binaries are written 0644 so the builder's chmod is what makes them executable.
//   broken: { goos: 'symlink' | 'empty' } replaces that goos's binary member.
function fakeRelease(version, { broken = {} } = {}) {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-dist-'));
	const dist = path.join(root, 'dist');
	const stage = path.join(root, 'stage');
	fs.mkdirSync(dist);
	const lines = [];
	for (const t of TARGETS) {
		const dir = path.join(stage, `${t.goos}_${t.goarch}`);
		fs.mkdirSync(dir, { recursive: true });
		const bin = path.join(dir, binaryName(t));
		if (broken[t.goos] === 'symlink') fs.symlinkSync('/etc/hostname', bin);
		else fs.writeFileSync(bin, broken[t.goos] === 'empty' ? '' : `binary for ${t.goos}/${t.goarch}\n`, { mode: 0o644 });
		fs.writeFileSync(path.join(dir, 'README.md'), 'readme\n');
		const archive = path.join(dist, archiveName(version, t));
		if (t.goos === 'windows') execFileSync('zip', ['-q', '--symlinks', archive, binaryName(t), 'README.md'], { cwd: dir });
		else execFileSync('tar', ['-czf', archive, binaryName(t), 'README.md'], { cwd: dir });
		lines.push(`${createHash('sha256').update(fs.readFileSync(archive)).digest('hex')}  ${path.basename(archive)}`);
	}
	fs.writeFileSync(path.join(dist, 'checksums.txt'), `${lines.join('\n')}\n`);
	fs.rmSync(stage, { recursive: true, force: true });
	return { root, dist, out: path.join(root, 'npm') };
}

function withRelease(version, opts, fn) {
	const rel = fakeRelease(version, opts);
	try {
		fn(rel);
	} finally {
		fs.rmSync(rel.root, { recursive: true, force: true });
	}
}

test('zip and tar are available where CI runs the build() suite', { skip: !process.env.CI }, () => {
	assert.ok(haveTools, 'the build() suite below would be skipped');
});

describe('contains (the --out guard)', () => {
	test('is true for the path itself and anything beneath it', () => {
		assert.ok(contains('/a/b', '/a/b'));
		assert.ok(contains('/a', '/a/b/c'));
		assert.ok(contains('/a', '/a/..foo/dist'), 'a child whose name starts with ".." is still inside');
	});
	test('is false for siblings and parents', () => {
		assert.ok(!contains('/a/b', '/a/c'));
		assert.ok(!contains('/a/b', '/a'));
		assert.ok(!contains('/a/b', '/a/bc'));
	});
	test('sees through symlinks', () => {
		const root = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-contains-'));
		try {
			fs.mkdirSync(path.join(root, 'real', 'dist'), { recursive: true });
			fs.symlinkSync(path.join(root, 'real'), path.join(root, 'link'));
			assert.ok(contains(path.join(root, 'real'), path.join(root, 'link', 'dist')));
			assert.ok(contains(path.join(root, 'link'), path.join(root, 'real', 'dist')));
		} finally {
			fs.rmSync(root, { recursive: true, force: true });
		}
	});
});

describe('build', { skip: !haveTools && 'needs zip and tar' }, () => {
	test('assembles every package from verified archives', () => {
		withRelease('2.0.0', {}, ({ dist, out }) => {
			// A restrictive umask must not change what gets packed (0600 files would
			// change the tarball's integrity and break identical re-runs).
			const umask = process.umask(0o077);
			let built;
			try {
				built = build({ dist, outDir: out, expectVersion: '2.0.0' });
			} finally {
				process.umask(umask);
			}
			const { version, dirs } = built;
			assert.equal(version, '2.0.0');
			assert.equal(dirs.length, TARGETS.length + 1);
			assert.equal(path.basename(dirs.at(-1)), MAIN_PACKAGE, 'launcher publishes last');
			const license = fs.readFileSync(path.join(here, '..', 'LICENSE'));
			for (const t of TARGETS) {
				const pkgDir = path.join(out, platformPackageName(t));
				const bin = path.join(pkgDir, 'bin', binaryName(t));
				assert.equal(fs.readFileSync(bin, 'utf8'), `binary for ${t.goos}/${t.goarch}\n`);
				assert.equal(fs.statSync(bin).mode & 0o777, 0o755);
				assert.deepEqual(fs.readFileSync(path.join(pkgDir, 'LICENSE')), license);
				for (const f of ['LICENSE', 'README.md', 'package.json']) {
					assert.equal(fs.statSync(path.join(pkgDir, f)).mode & 0o777, 0o644, `${f}: umask must not leak into the tarball`);
				}
			}
			const mainDir = path.join(out, MAIN_PACKAGE);
			assert.deepEqual(JSON.parse(fs.readFileSync(path.join(mainDir, 'package.json'), 'utf8')), mainPackageJSON('2.0.0'));
			assert.deepEqual(fs.readFileSync(path.join(mainDir, 'LICENSE')), license);
			assert.deepEqual(fs.readFileSync(path.join(mainDir, 'README.md')), fs.readFileSync(path.join(here, 'README.md')));
			assert.deepEqual(fs.readFileSync(path.join(mainDir, 'bin', 'agentsync.js')), fs.readFileSync(path.join(here, 'bin', 'agentsync.js')));
			assert.equal(fs.statSync(path.join(mainDir, 'bin', 'agentsync.js')).mode & 0o777, 0o755);
		});
	});

	const refusals = [
		['a tampered archive', {}, ({ dist }) => fs.appendFileSync(path.join(dist, archiveName('2.0.0', TARGETS[0])), 'x'), /checksum mismatch/],
		['a release missing a target', {}, ({ dist }) => fs.rmSync(path.join(dist, archiveName('2.0.0', TARGETS[5]))), /missing release archive/],
		[
			'an archive checksums.txt does not list',
			{},
			({ dist }) => {
				const sums = path.join(dist, 'checksums.txt');
				fs.writeFileSync(sums, fs.readFileSync(sums, 'utf8').split('\n').filter((l) => !l.includes('darwin_arm64')).join('\n'));
			},
			/no entry for agentsync_2\.0\.0_darwin_arm64/,
		],
		['a symlinked binary member (tar)', { broken: { linux: 'symlink' } }, () => {}, /is not a regular non-empty file/],
		['a symlinked binary member (zip)', { broken: { windows: 'symlink' } }, () => {}, /is not a regular non-empty file/],
		['an empty binary member', { broken: { darwin: 'empty' } }, () => {}, /is not a regular non-empty file/],
	];
	for (const [what, opts, tamper, err] of refusals) {
		test(`refuses ${what}`, () => {
			withRelease('2.0.0', opts, (rel) => {
				tamper(rel);
				assert.throws(() => build({ dist: rel.dist, outDir: rel.out }), err);
			});
		});
	}

	test('refuses an --expect-version that disagrees with the archives', () => {
		withRelease('2.0.0', {}, ({ dist, out }) => {
			assert.throws(() => build({ dist, outDir: out, expectVersion: '2.0.1' }), /does not match/);
		});
	});

	test('refuses a version that is not semver', () => {
		withRelease('2.0', {}, ({ dist, out }) => {
			assert.throws(() => build({ dist, outDir: out }), /not valid semver/);
		});
	});

	test('refuses an --out that would delete the release', () => {
		withRelease('2.0.0', {}, ({ root, dist }) => {
			assert.throws(() => build({ dist, outDir: dist }), /would delete --dist/);
			assert.throws(() => build({ dist, outDir: root }), /would delete --dist/);
			assert.ok(fs.existsSync(path.join(dist, 'checksums.txt')), 'the release survived');
		});
	});
});

test('the CLI runs through a symlinked path and rejects --dry-run without --publish', () => {
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-cli-'));
	try {
		const link = path.join(dir, 'build.mjs');
		fs.symlinkSync(path.join(here, 'build.mjs'), link);
		const r = spawnSync(process.execPath, [link, '--dist', dir, '--dry-run'], { encoding: 'utf8' });
		assert.equal(r.status, 2, `stderr=${r.stderr}`);
		assert.match(r.stderr, /^usage: node npm\/build\.mjs/);
	} finally {
		fs.rmSync(dir, { recursive: true, force: true });
	}
});

// fakeNpm writes an executable standing in for `npm`: it answers `view`/`pack`
// from a registry state object and appends every invocation's argv to a log. It
// is strict about the flags publish() relies on (--json on reads, --dry-run on
// pack), so dropping one fails the test instead of silently hitting real npm.
//   state.latest       the launcher's dist-tags.latest (absent → E404)
//   state.published    { name: integrity } of name@version already on the registry
//   state.publisher    what `view name@version _npmUser` answers
//   state.ours         { name: integrity } `npm pack --dry-run` reports per package
//   state.viewError    stderr for a non-404 `npm view` failure (e.g. auth)
function fakeNpm(state) {
	const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-fakenpm-'));
	const log = path.join(dir, 'calls.jsonl');
	const bin = path.join(dir, 'npm');
	fs.writeFileSync(
		bin,
		`#!${process.execPath}
const fs = require('node:fs');
const path = require('node:path');
const state = ${JSON.stringify(state)};
const argv = process.argv.slice(2);
fs.appendFileSync(${JSON.stringify(log)}, JSON.stringify(argv) + '\\n');
const die = (msg) => { process.stderr.write(msg + '\\n'); process.exit(1); };
const e404 = () => die('npm error code E404');
if (argv[0] === 'view' || argv[0] === 'pack') {
	if (!argv.includes('--json')) die('fake npm: ' + argv[0] + ' called without --json');
}
if (argv[0] === 'view') {
	if (state.viewError) die(state.viewError);
	const [spec, field] = [argv[1], argv[2]];
	if (field === 'dist-tags.latest') return state.latest ? console.log(JSON.stringify(state.latest)) : e404();
	const name = spec.slice(0, spec.lastIndexOf('@'));
	if (!(name in (state.published || {}))) return e404();
	if (field === '_npmUser') return console.log(JSON.stringify(state.publisher || 'someone'));
	if (field === 'dist.integrity') return console.log(JSON.stringify(state.published[name]));
	die('fake npm: unexpected view field ' + field);
}
if (argv[0] === 'pack') {
	if (!argv.includes('--dry-run')) die('fake npm: pack without --dry-run would write a tarball');
	const { name } = JSON.parse(fs.readFileSync(path.join(argv[1], 'package.json'), 'utf8'));
	return console.log(JSON.stringify([{ integrity: (state.ours || {})[name] }]));
}
`,
		{ mode: 0o755 },
	);
	const calls = () =>
		fs.existsSync(log)
			? fs.readFileSync(log, 'utf8').trim().split('\n').map((l) => JSON.parse(l))
			: [];
	return { bin, calls, cleanup: () => fs.rmSync(dir, { recursive: true, force: true }) };
}

// packageDirs lays out the seven package dirs publish() walks (only package.json
// is read), in build()'s order: platform packages, then the launcher.
function packageDirs(version) {
	const root = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-pkgs-'));
	const pkgs = [...TARGETS.map((t) => platformPackageJSON(version, t)), mainPackageJSON(version)];
	const dirs = pkgs.map((pkg) => {
		const dir = path.join(root, pkg.name);
		fs.mkdirSync(dir, { recursive: true });
		fs.writeFileSync(path.join(dir, 'package.json'), JSON.stringify(pkg));
		return dir;
	});
	return { root, dirs, names: pkgs.map((p) => p.name) };
}

// sameIntegrity builds a state.ours map giving every package the same integrity.
const sameIntegrity = (names, integrity) => Object.fromEntries(names.map((n) => [n, integrity]));

describe('publish', { skip: process.platform === 'win32' && 'fake npm is a shebang script' }, () => {
	function withRegistry(state, version, fn) {
		const pkgs = packageDirs(version);
		const npm = fakeNpm(typeof state === 'function' ? state(pkgs.names) : state);
		const published = () =>
			npm
				.calls()
				.filter((c) => c[0] === 'publish')
				.map((c) => ({ name: path.relative(pkgs.root, c[1]).split(path.sep).join('/'), flags: c.slice(2) }));
		try {
			fn({ npm, pkgs, published, run: (opts = {}) => publish({ version, dirs: pkgs.dirs, npmBin: npm.bin, log: () => {}, ...opts }) });
		} finally {
			npm.cleanup();
			fs.rmSync(pkgs.root, { recursive: true, force: true });
		}
	}

	test('publishes every platform package, then the launcher, as the new latest', () => {
		withRegistry({ latest: '0.15.0' }, '0.16.0', ({ pkgs, published, run }) => {
			run({ provenance: true });
			assert.deepEqual(
				published().map((p) => p.name),
				pkgs.names,
			);
			assert.equal(pkgs.names.at(-1), MAIN_PACKAGE, 'launcher last, so its optionalDependencies resolve');
			for (const p of published()) assert.deepEqual(p.flags, ['--access', 'public', '--tag', 'latest', '--provenance']);
		});
	});

	test('passes --dry-run through and omits --provenance unless asked', () => {
		withRegistry({}, '0.16.0', ({ published, run }) => {
			run({ dryRun: true });
			assert.equal(published().length, TARGETS.length + 1);
			for (const p of published()) assert.deepEqual(p.flags, ['--access', 'public', '--tag', 'latest', '--dry-run']);
		});
	});

	test('publishes a prerelease under next and a backfill under backfill', () => {
		withRegistry({ latest: '0.16.0' }, '0.17.0-rc.1', ({ published, run }) => {
			run();
			for (const p of published()) assert.equal(p.flags[p.flags.indexOf('--tag') + 1], 'next');
		});
		withRegistry({ latest: '0.16.0' }, '0.14.0', ({ published, run }) => {
			run();
			assert.equal(published().length, TARGETS.length + 1);
			for (const p of published()) assert.equal(p.flags[p.flags.indexOf('--tag') + 1], 'backfill');
		});
	});

	test('skips a package already published with an identical tarball (re-run recovery)', () => {
		const version = '0.16.0';
		const done = platformPackageName(TARGETS[0]);
		const state = (names) => ({ latest: '0.15.0', ours: sameIntegrity(names, 'sha512-ours'), published: { [done]: 'sha512-ours' } });
		withRegistry(state, version, ({ pkgs, published, run }) => {
			run();
			assert.deepEqual(
				published().map((p) => p.name),
				pkgs.names.filter((n) => n !== done),
			);
		});
	});

	const squatted = platformPackageName(TARGETS[2]);
	const squatState = (names) => ({
		latest: '0.15.0',
		ours: sameIntegrity(names, 'sha512-ours'),
		published: { [squatted]: 'sha512-theirs' },
		publisher: 'mallory <m@example.com>',
	});

	test('refuses a package already published with DIFFERENT contents, before the launcher can pin it', () => {
		withRegistry(squatState, '0.16.0', ({ pkgs, published, run }) => {
			assert.throws(run, (err) => {
				assert.match(err.message, /already on the registry with DIFFERENT contents/);
				assert.match(err.message, /published by "mallory <m@example\.com>"/);
				assert.match(err.message, /"sha512-theirs".*"sha512-ours"/);
				return true;
			});
			assert.deepEqual(
				published().map((p) => p.name),
				pkgs.names.slice(0, 2),
				'stops at the squatted package; the launcher is never published',
			);
		});
	});

	test('compares each package against its OWN tarball', () => {
		// Every package but the squatted one matches: a publish() that packed the
		// wrong dir (e.g. always dirs[0]) would compare the wrong integrity.
		const state = (names) => ({ ...squatState(names), ours: { ...sameIntegrity(names, 'sha512-ours'), [squatted]: 'sha512-theirs' } });
		withRegistry(state, '0.16.0', ({ pkgs, published, run }) => {
			run();
			assert.deepEqual(
				published().map((p) => p.name),
				pkgs.names.filter((n) => n !== squatted),
			);
		});
	});

	test('a dry run performs the same registry checks', () => {
		withRegistry(squatState, '0.16.0', ({ npm, published, run }) => {
			assert.throws(() => run({ dryRun: true }), /DIFFERENT contents/);
			assert.ok(npm.calls().some((c) => c[0] === 'view' && c[1] === MAIN_PACKAGE), 'reads the launcher latest');
			assert.equal(published().length, 2);
		});
	});

	test('fails on a registry error that is not a 404, publishing nothing', () => {
		withRegistry({ viewError: 'npm error code E401\nnpm error Unable to authenticate' }, '0.16.0', ({ published, run }) => {
			assert.throws(run, /E401/);
			assert.equal(published().length, 0);
		});
	});
});

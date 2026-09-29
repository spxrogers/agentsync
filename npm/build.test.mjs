// Tests for npm/build.mjs: package metadata, the launcher ↔ builder naming
// contract, the GoReleaser target parity guard, dist-tag selection, build() over
// fabricated release archives (checksum verification included), and publish()
// against a fake `npm` that records every call.
//
//   node --test npm/*.test.mjs
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
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
	build,
	compareVersions,
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

// fakeRelease writes a GoReleaser-shaped dist: one archive per target (binary +
// the LICENSE/README goreleaser bundles) and checksums.txt over them. Binaries
// are written 0644 so the builder's chmod is what makes them executable.
// symlinkFor names a goos whose `agentsync` member is a symlink instead.
function fakeRelease(version, { symlinkFor } = {}) {
	const dist = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-dist-'));
	const stage = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-stage-'));
	const lines = [];
	for (const t of TARGETS) {
		const dir = path.join(stage, `${t.goos}_${t.goarch}`);
		fs.mkdirSync(dir);
		if (t.goos === symlinkFor) fs.symlinkSync('/etc/hostname', path.join(dir, binaryName(t)));
		else fs.writeFileSync(path.join(dir, binaryName(t)), `binary for ${t.goos}/${t.goarch}\n`, { mode: 0o644 });
		fs.writeFileSync(path.join(dir, 'README.md'), 'readme\n');
		const archive = path.join(dist, archiveName(version, t));
		if (t.goos === 'windows') execFileSync('zip', ['-q', '--symlinks', archive, binaryName(t), 'README.md'], { cwd: dir });
		else execFileSync('tar', ['-czf', archive, binaryName(t), 'README.md'], { cwd: dir });
		lines.push(`${createHash('sha256').update(fs.readFileSync(archive)).digest('hex')}  ${path.basename(archive)}`);
	}
	fs.writeFileSync(path.join(dist, 'checksums.txt'), `${lines.join('\n')}\n`);
	fs.rmSync(stage, { recursive: true, force: true });
	return dist;
}

test('zip and tar are available where CI runs the build() suite', { skip: !process.env.CI }, () => {
	assert.ok(haveTools, 'the build() suite below would be skipped');
});

describe('build', { skip: !haveTools && 'needs zip and tar' }, () => {
	test('assembles every package from verified archives', () => {
		const dist = fakeRelease('2.0.0');
		const out = path.join(dist, 'npm');
		try {
			const { version, dirs } = build({ dist, outDir: out, expectVersion: '2.0.0' });
			assert.equal(version, '2.0.0');
			assert.equal(dirs.length, TARGETS.length + 1);
			assert.equal(path.basename(dirs.at(-1)), MAIN_PACKAGE, 'launcher publishes last');
			for (const t of TARGETS) {
				const bin = path.join(out, platformPackageName(t), 'bin', binaryName(t));
				assert.equal(fs.readFileSync(bin, 'utf8'), `binary for ${t.goos}/${t.goarch}\n`);
				assert.equal(fs.statSync(bin).mode & 0o777, 0o755);
				assert.deepEqual(fs.readFileSync(path.join(out, platformPackageName(t), 'LICENSE')), fs.readFileSync(path.join(here, '..', 'LICENSE')));
			}
			assert.deepEqual(fs.readFileSync(path.join(out, MAIN_PACKAGE, 'LICENSE')), fs.readFileSync(path.join(here, '..', 'LICENSE')));
			assert.deepEqual(fs.readFileSync(path.join(out, MAIN_PACKAGE, 'README.md')), fs.readFileSync(path.join(here, 'README.md')));
			assert.equal(fs.statSync(path.join(out, MAIN_PACKAGE, 'bin', 'agentsync.js')).mode & 0o777, 0o755);
			const main = JSON.parse(fs.readFileSync(path.join(out, MAIN_PACKAGE, 'package.json'), 'utf8'));
			assert.deepEqual(main, mainPackageJSON('2.0.0'));
			assert.equal(
				fs.readFileSync(path.join(out, MAIN_PACKAGE, 'bin', 'agentsync.js'), 'utf8'),
				fs.readFileSync(path.join(here, 'bin', 'agentsync.js'), 'utf8'),
			);
		} finally {
			fs.rmSync(dist, { recursive: true, force: true });
		}
	});

	test('refuses a tampered archive', () => {
		const dist = fakeRelease('2.0.0');
		try {
			fs.appendFileSync(path.join(dist, archiveName('2.0.0', TARGETS[0])), 'x');
			assert.throws(() => build({ dist, outDir: path.join(dist, 'npm') }), /checksum mismatch/);
		} finally {
			fs.rmSync(dist, { recursive: true, force: true });
		}
	});

	test('refuses a release missing a target', () => {
		const dist = fakeRelease('2.0.0');
		try {
			fs.rmSync(path.join(dist, archiveName('2.0.0', TARGETS[5])));
			assert.throws(() => build({ dist, outDir: path.join(dist, 'npm') }), /missing release archive/);
		} finally {
			fs.rmSync(dist, { recursive: true, force: true });
		}
	});

	test('refuses an archive checksums.txt does not list', () => {
		const dist = fakeRelease('2.0.0');
		try {
			const sums = path.join(dist, 'checksums.txt');
			const keep = fs.readFileSync(sums, 'utf8').split('\n').filter((l) => !l.includes('darwin_arm64'));
			fs.writeFileSync(sums, keep.join('\n'));
			assert.throws(() => build({ dist, outDir: path.join(dist, 'npm') }), /no entry for agentsync_2\.0\.0_darwin_arm64/);
		} finally {
			fs.rmSync(dist, { recursive: true, force: true });
		}
	});

	test('refuses an --expect-version that disagrees with the archives', () => {
		const dist = fakeRelease('2.0.0');
		try {
			assert.throws(() => build({ dist, outDir: path.join(dist, 'npm'), expectVersion: '2.0.1' }), /does not match/);
		} finally {
			fs.rmSync(dist, { recursive: true, force: true });
		}
	});

	test('refuses a version that is not semver', () => {
		const dist = fakeRelease('2.0');
		try {
			assert.throws(() => build({ dist, outDir: path.join(dist, 'npm') }), /not valid semver/);
		} finally {
			fs.rmSync(dist, { recursive: true, force: true });
		}
	});

	for (const goos of ['linux', 'windows']) {
		test(`refuses a symlinked binary member (${goos})`, () => {
			const dist = fakeRelease('2.0.0', { symlinkFor: goos });
			try {
				assert.throws(() => build({ dist, outDir: path.join(dist, 'npm') }), /is not a regular non-empty file/);
			} finally {
				fs.rmSync(dist, { recursive: true, force: true });
			}
		});
	}

	test('refuses an --out that would delete the release', () => {
		const dist = fakeRelease('2.0.0');
		try {
			assert.throws(() => build({ dist, outDir: dist }), /would delete --dist/);
			assert.throws(() => build({ dist, outDir: path.dirname(dist) }), /would delete --dist/);
			assert.ok(fs.existsSync(path.join(dist, 'checksums.txt')), 'the release survived');
		} finally {
			fs.rmSync(dist, { recursive: true, force: true });
		}
	});
});

// fakeNpm writes an executable standing in for `npm`: it answers `view`/`pack`
// from a registry state object and appends every invocation's argv to a log.
//   state.latest       the launcher's dist-tags.latest (absent → E404)
//   state.published    { name: integrity } of name@version already on the registry
//   state.ours         integrity `npm pack --dry-run` reports for every dir
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
const e404 = () => { process.stderr.write('npm error code E404\\n'); process.exit(1); };
if (argv[0] === 'view') {
	if (state.viewError) { process.stderr.write(state.viewError); process.exit(1); }
	const [spec, field] = [argv[1], argv[2]];
	if (field === 'dist-tags.latest') return state.latest ? console.log(JSON.stringify(state.latest)) : e404();
	const name = spec.slice(0, spec.lastIndexOf('@'));
	return name in (state.published || {}) ? console.log(JSON.stringify(state.published[name])) : e404();
}
if (argv[0] === 'pack') return console.log(JSON.stringify([{ integrity: state.ours }]));
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
		fs.mkdirSync(dir);
		fs.writeFileSync(path.join(dir, 'package.json'), JSON.stringify(pkg));
		return dir;
	});
	return { root, dirs, names: pkgs.map((p) => p.name) };
}

describe('publish', { skip: process.platform === 'win32' && 'fake npm is a shebang script' }, () => {
	const publishCalls = (calls) => calls.filter((c) => c[0] === 'publish');

	function withRegistry(state, version, fn) {
		const npm = fakeNpm(state);
		const pkgs = packageDirs(version);
		try {
			fn({ npm, pkgs, run: (opts = {}) => publish({ version, dirs: pkgs.dirs, npmBin: npm.bin, log: () => {}, ...opts }) });
		} finally {
			npm.cleanup();
			fs.rmSync(pkgs.root, { recursive: true, force: true });
		}
	}

	test('publishes every platform package, then the launcher, as the new latest', () => {
		withRegistry({ latest: '0.15.0', ours: 'sha512-ours' }, '0.16.0', ({ npm, pkgs, run }) => {
			run({ provenance: true });
			const pubs = publishCalls(npm.calls());
			assert.deepEqual(
				pubs.map((c) => path.basename(c[1])),
				pkgs.names,
			);
			assert.equal(pkgs.names.at(-1), MAIN_PACKAGE, 'launcher last, so its optionalDependencies resolve');
			for (const c of pubs) assert.deepEqual(c.slice(2), ['--access', 'public', '--tag', 'latest', '--provenance']);
		});
	});

	test('passes --dry-run through and omits --provenance unless asked', () => {
		withRegistry({ ours: 'sha512-ours' }, '0.16.0', ({ npm, run }) => {
			run({ dryRun: true });
			for (const c of publishCalls(npm.calls())) assert.deepEqual(c.slice(2), ['--access', 'public', '--tag', 'latest', '--dry-run']);
		});
	});

	test('publishes a prerelease under next and a backfill under backfill', () => {
		withRegistry({ latest: '0.16.0', ours: 'x' }, '0.17.0-rc.1', ({ npm, run }) => {
			run();
			for (const c of publishCalls(npm.calls())) assert.equal(c[c.indexOf('--tag') + 1], 'next');
		});
		withRegistry({ latest: '0.16.0', ours: 'x' }, '0.14.0', ({ npm, run }) => {
			run();
			const pubs = publishCalls(npm.calls());
			assert.equal(pubs.length, TARGETS.length + 1);
			for (const c of pubs) assert.equal(c[c.indexOf('--tag') + 1], 'backfill');
		});
	});

	test('skips a package already published with an identical tarball (re-run recovery)', () => {
		const version = '0.16.0';
		const done = platformPackageName(TARGETS[0]);
		withRegistry({ latest: '0.15.0', ours: 'sha512-ours', published: { [done]: 'sha512-ours' } }, version, ({ npm, pkgs, run }) => {
			run();
			assert.deepEqual(
				publishCalls(npm.calls()).map((c) => path.basename(c[1])),
				pkgs.names.filter((n) => n !== done),
			);
		});
	});

	test('refuses a package already published with DIFFERENT contents, before the launcher can pin it', () => {
		const squatted = platformPackageName(TARGETS[2]);
		withRegistry({ latest: '0.15.0', ours: 'sha512-ours', published: { [squatted]: 'sha512-theirs' } }, '0.16.0', ({ npm, pkgs, run }) => {
			assert.throws(run, /already on the registry with DIFFERENT contents/);
			const published = publishCalls(npm.calls()).map((c) => path.basename(c[1]));
			assert.ok(!published.includes(MAIN_PACKAGE), 'launcher must not be published');
			assert.ok(!published.includes(squatted));
			assert.deepEqual(published, pkgs.names.slice(0, 2), 'stops at the squatted package');
		});
	});

	test('fails on a registry error that is not a 404, publishing nothing', () => {
		withRegistry({ viewError: 'npm error code E401\nnpm error Unable to authenticate\n' }, '0.16.0', ({ npm, run }) => {
			assert.throws(run, /E401/);
			assert.equal(publishCalls(npm.calls()).length, 0);
		});
	});
});

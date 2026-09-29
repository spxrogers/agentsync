// Tests for npm/build.mjs: package metadata, the launcher ↔ builder naming
// contract, the GoReleaser target parity guard, and build() over fabricated
// release archives (checksum verification included).
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
	distTag,
	inferVersion,
	mainPackageJSON,
	parseChecksums,
	platformPackageJSON,
	platformPackageName,
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
		assert.equal(distTag('1.2.3'), 'latest');
		assert.equal(distTag('1.2.3-rc.1'), 'next');
		assert.equal(distTag('0.16.1-snapshot-abc1234'), 'next');
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
// the LICENSE/README goreleaser bundles) and checksums.txt over them.
function fakeRelease(version) {
	const dist = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-dist-'));
	const stage = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-stage-'));
	const lines = [];
	for (const t of TARGETS) {
		const dir = path.join(stage, `${t.goos}_${t.goarch}`);
		fs.mkdirSync(dir);
		fs.writeFileSync(path.join(dir, binaryName(t)), `binary for ${t.goos}/${t.goarch}\n`, { mode: 0o755 });
		fs.writeFileSync(path.join(dir, 'README.md'), 'readme\n');
		const archive = path.join(dist, archiveName(version, t));
		if (t.goos === 'windows') execFileSync('zip', ['-q', archive, binaryName(t), 'README.md'], { cwd: dir });
		else execFileSync('tar', ['-czf', archive, binaryName(t), 'README.md'], { cwd: dir });
		lines.push(`${createHash('sha256').update(fs.readFileSync(archive)).digest('hex')}  ${path.basename(archive)}`);
	}
	fs.writeFileSync(path.join(dist, 'checksums.txt'), `${lines.join('\n')}\n`);
	fs.rmSync(stage, { recursive: true, force: true });
	return dist;
}

describe('build', { skip: !haveTools && 'needs zip and tar' }, () => {
	test('assembles every package from verified archives', () => {
		const dist = fakeRelease('2.0.0');
		const out = path.join(dist, 'npm');
		try {
			const { version, dirs } = build({ dist, outDir: out, version: '2.0.0' });
			assert.equal(version, '2.0.0');
			assert.equal(dirs.length, TARGETS.length + 1);
			assert.equal(path.basename(dirs.at(-1)), MAIN_PACKAGE, 'launcher publishes last');
			for (const t of TARGETS) {
				const bin = path.join(out, platformPackageName(t), 'bin', binaryName(t));
				assert.equal(fs.readFileSync(bin, 'utf8'), `binary for ${t.goos}/${t.goarch}\n`);
				assert.equal(fs.statSync(bin).mode & 0o777, 0o755);
				assert.ok(fs.existsSync(path.join(out, platformPackageName(t), 'LICENSE')));
			}
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

	test('refuses a --version that disagrees with the archives', () => {
		const dist = fakeRelease('2.0.0');
		try {
			assert.throws(() => build({ dist, outDir: path.join(dist, 'npm'), version: '2.0.1' }), /does not match/);
		} finally {
			fs.rmSync(dist, { recursive: true, force: true });
		}
	});
});

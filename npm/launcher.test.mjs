// Tests for the npm launcher (npm/bin/agentsync.js): run it the way npm does —
// from node_modules/agentsync.cc/bin — against a fake platform package whose
// "binary" is a shell script, and check argv/stdio/exit-status/signal fidelity.
//
//   node --test npm/
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import { createRequire } from 'node:module';
import os from 'node:os';
import path from 'node:path';
import { after, before, describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';

const require = createRequire(import.meta.url);
const here = path.dirname(fileURLToPath(import.meta.url));
const launcherSrc = path.join(here, 'bin', 'agentsync.js');
const { platformPackage } = require(launcherSrc);

const MAIN = 'agentsync.cc';
const VERSION = '1.2.3';
const posix = process.platform !== 'win32';

// The fake binary: a tiny command language so each test can pick a behavior.
const FAKE_BIN = `#!/bin/sh
case "$1" in
  echo) shift; for a in "$@"; do printf '%s\\n' "$a"; done ;;
  cat) cat ;;
  exit) exit "$2" ;;
  selfkill) kill -"$2" $$ ;;
  wait) trap "echo got-$2; kill \\$pid 2>/dev/null; exit 42" "$2"
        sleep 30 & pid=$!
        echo ready
        wait $pid ;;
esac
`;

// installFixture lays out node_modules the way npm would after installing the
// main package on this machine. withPlatform=false simulates --omit=optional.
function installFixture(root, { withPlatform }) {
	const mainDir = path.join(root, 'node_modules', MAIN);
	fs.mkdirSync(path.join(mainDir, 'bin'), { recursive: true });
	fs.copyFileSync(launcherSrc, path.join(mainDir, 'bin', 'agentsync.js'));
	const plat = `${MAIN}-${process.platform}-${process.arch}`;
	fs.writeFileSync(
		path.join(mainDir, 'package.json'),
		JSON.stringify({ name: MAIN, version: VERSION, optionalDependencies: { [plat]: VERSION } }),
	);
	if (withPlatform) {
		const platDir = path.join(root, 'node_modules', plat);
		fs.mkdirSync(path.join(platDir, 'bin'), { recursive: true });
		fs.writeFileSync(path.join(platDir, 'package.json'), JSON.stringify({ name: plat, version: VERSION }));
		fs.writeFileSync(path.join(platDir, 'bin', 'agentsync'), FAKE_BIN, { mode: 0o755 });
	}
	return path.join(mainDir, 'bin', 'agentsync.js');
}

describe('platformPackage', () => {
	const main = { name: MAIN, optionalDependencies: { [`${MAIN}-linux-x64`]: VERSION } };
	test('names a published platform', () => {
		assert.equal(platformPackage(main, 'linux', 'x64'), `${MAIN}-linux-x64`);
	});
	test('returns null for an unpublished platform', () => {
		assert.equal(platformPackage(main, 'linux', 'riscv64'), null);
		assert.equal(platformPackage(main, 'aix', 'ppc64'), null);
	});
});

describe('launcher', { skip: !posix && 'fake binary is a POSIX shell script' }, () => {
	let root;
	let launcher;
	before(() => {
		root = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-launcher-'));
		launcher = installFixture(root, { withPlatform: true });
	});
	after(() => fs.rmSync(root, { recursive: true, force: true }));

	const run = (args, opts = {}) =>
		spawnSync(process.execPath, [launcher, ...args], { encoding: 'utf8', timeout: 20000, ...opts });

	test('passes argv through verbatim', () => {
		const r = run(['echo', 'a b', '', '--flag=x y', '$HOME']);
		assert.equal(r.status, 0, r.stderr);
		assert.equal(r.stdout, 'a b\n\n--flag=x y\n$HOME\n');
	});

	test('inherits stdin', () => {
		const r = run(['cat'], { input: 'piped input\n' });
		assert.equal(r.status, 0, r.stderr);
		assert.equal(r.stdout, 'piped input\n');
	});

	for (const code of [0, 1, 3, 125]) {
		test(`mirrors exit status ${code}`, () => {
			assert.equal(run(['exit', String(code)]).status, code);
		});
	}

	for (const sig of ['TERM', 'INT', 'HUP']) {
		test(`dies of the same signal that killed the binary (SIG${sig})`, () => {
			const r = run(['selfkill', sig]);
			assert.equal(r.status, null);
			assert.equal(r.signal, `SIG${sig}`);
		});
	}

	test('exits 128+n for a signal Node cannot re-raise on itself (SIGPIPE)', () => {
		const r = run(['selfkill', 'PIPE']);
		assert.equal(r.signal, null);
		assert.equal(r.status, 128 + os.constants.signals.SIGPIPE);
	});

	for (const sig of ['TERM', 'INT', 'HUP', 'QUIT']) {
		test(`forwards a SIG${sig} sent to the launcher alone, then mirrors the exit`, async () => {
			const child = spawn(process.execPath, [launcher, 'wait', sig], { stdio: ['ignore', 'pipe', 'pipe'] });
			let out = '';
			const exited = new Promise((resolve) => child.on('exit', (code, signal) => resolve({ code, signal })));
			try {
				await new Promise((resolve, reject) => {
					const timer = setTimeout(() => reject(new Error(`binary never became ready; stdout=${out}`)), 15000);
					child.stdout.on('data', (d) => {
						out += d;
						if (out.includes('ready')) {
							clearTimeout(timer);
							resolve();
						}
					});
				});
			} catch (err) {
				child.kill('SIGKILL');
				throw err;
			}
			child.kill(`SIG${sig}`);
			const { code, signal } = await exited;
			assert.equal(signal, null, `launcher died of ${signal} instead of waiting for the binary; stdout=${out}`);
			assert.equal(code, 42, `stdout=${out}`);
			assert.match(out, new RegExp(`got-${sig}`));
		});
	}

	test('restores a lost exec bit once', () => {
		const bin = path.join(root, 'node_modules', `${MAIN}-${process.platform}-${process.arch}`, 'bin', 'agentsync');
		fs.chmodSync(bin, 0o644);
		const r = run(['echo', 'ok']);
		assert.equal(r.status, 0, r.stderr);
		assert.equal(r.stdout, 'ok\n');
		assert.equal(fs.statSync(bin).mode & 0o111, 0o111);
	});
});

describe('launcher without its platform package', () => {
	test('explains the missing optional dependency and exits 1', () => {
		const root = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-launcher-'));
		try {
			const launcher = installFixture(root, { withPlatform: false });
			const r = spawnSync(process.execPath, [launcher, '--version'], { encoding: 'utf8', timeout: 20000 });
			assert.equal(r.status, 1);
			assert.match(r.stderr, new RegExp(`${MAIN}-${process.platform}-${process.arch}@${VERSION} is not installed`));
			assert.match(r.stderr, /--omit=optional/);
		} finally {
			fs.rmSync(root, { recursive: true, force: true });
		}
	});
});

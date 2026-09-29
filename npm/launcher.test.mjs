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
  sleep) exec sleep 30 ;;
  wait) trap "echo got-$2; kill \\$pid 2>/dev/null; exit 42" "$2"
        sleep 30 & pid=$!
        echo ready
        wait $pid ;;
esac
`;

// installFixture lays out node_modules the way npm would after installing the
// main package on this machine. withPlatform=false simulates --omit=optional;
// supported=false makes the launcher list only a platform this machine isn't;
// platformVersion installs a platform package at a different version.
function installFixture(root, { withPlatform, supported = true, platformVersion = VERSION }) {
	const mainDir = path.join(root, 'node_modules', MAIN);
	fs.mkdirSync(path.join(mainDir, 'bin'), { recursive: true });
	fs.copyFileSync(launcherSrc, path.join(mainDir, 'bin', 'agentsync.js'));
	const plat = `@spxrogers/${MAIN}-${process.platform}-${process.arch}`;
	const listed = supported ? plat : `@spxrogers/${MAIN}-plan9-mips`;
	fs.writeFileSync(
		path.join(mainDir, 'package.json'),
		JSON.stringify({ name: MAIN, version: VERSION, optionalDependencies: { [listed]: VERSION } }),
	);
	if (withPlatform) {
		const platDir = path.join(root, 'node_modules', plat);
		fs.mkdirSync(path.join(platDir, 'bin'), { recursive: true });
		fs.writeFileSync(path.join(platDir, 'package.json'), JSON.stringify({ name: plat, version: platformVersion }));
		fs.writeFileSync(path.join(platDir, 'bin', 'agentsync'), FAKE_BIN, { mode: 0o755 });
	}
	return path.join(mainDir, 'bin', 'agentsync.js');
}

describe('platformPackage', () => {
	const main = {
		name: MAIN,
		optionalDependencies: { [`@spxrogers/${MAIN}-linux-x64`]: VERSION, [`${MAIN}-darwin-arm64`]: VERSION },
	};
	test('names a published platform, scoped or not', () => {
		assert.equal(platformPackage(main, 'linux', 'x64'), `@spxrogers/${MAIN}-linux-x64`);
		assert.equal(platformPackage(main, 'darwin', 'arm64'), `${MAIN}-darwin-arm64`);
	});
	test('never matches another package name that merely ends the same way', () => {
		const other = { name: MAIN, optionalDependencies: { [`not-${MAIN}-linux-x64`]: VERSION } };
		assert.equal(platformPackage(other, 'linux', 'x64'), null);
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

	test('exits 128+n for a signal Node handles itself (SIGUSR1)', () => {
		const r = run(['selfkill', 'USR1']);
		assert.equal(r.signal, null);
		assert.equal(r.status, 128 + os.constants.signals.SIGUSR1);
		assert.doesNotMatch(r.stderr, /Debugger listening/);
	});

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

	// preload arms a --require hook that wraps child_process.spawn: on the Nth
	// spawn it records the child's pid and immediately SIGTERMs the launcher —
	// the tightest window there is between starting the binary and the launcher
	// being ready to forward. Test-only; the launcher knows nothing of it.
	function preload(dir) {
		const file = path.join(dir, 'preload.cjs');
		fs.writeFileSync(
			file,
			`const cp = require('node:child_process');
const fs = require('node:fs');
const real = cp.spawn;
let n = 0;
cp.spawn = function (...args) {
	const child = real.apply(this, args);
	if (++n === Number(process.env.PRELOAD_SIGNAL_ON_SPAWN)) {
		fs.writeFileSync(process.env.PRELOAD_PIDFILE, String(child.pid ?? ''));
		process.kill(process.pid, 'SIGTERM');
	}
	return child;
};
`,
		);
		return file;
	}

	const alive = (pid) => {
		try {
			process.kill(pid, 0);
			return true;
		} catch {
			return false;
		}
	};

	test('a signal right after spawn() is forwarded, never orphaning the binary', async () => {
		const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-preload-'));
		const pidfile = path.join(dir, 'pid');
		let pid;
		try {
			const r = spawnSync(process.execPath, ['--require', preload(dir), launcher, 'sleep'], {
				encoding: 'utf8',
				timeout: 20000,
				env: { ...process.env, PRELOAD_SIGNAL_ON_SPAWN: '1', PRELOAD_PIDFILE: pidfile },
			});
			pid = Number(fs.readFileSync(pidfile, 'utf8'));
			assert.ok(pid > 0, 'the binary was spawned');
			assert.equal(r.signal, 'SIGTERM', 'the launcher mirrors the forwarded SIGTERM');
			for (let i = 0; i < 50 && alive(pid); i++) await new Promise((res) => setTimeout(res, 20));
			assert.ok(!alive(pid), 'the binary must not outlive the launcher');
		} finally {
			if (pid > 0 && alive(pid)) process.kill(pid, 'SIGKILL');
			fs.rmSync(dir, { recursive: true, force: true });
		}
	});

	test('a signal during the exec-bit retry reaches the retried binary', () => {
		const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-preload-'));
		const bin = path.join(root, 'node_modules', '@spxrogers', `${MAIN}-${process.platform}-${process.arch}`, 'bin', 'agentsync');
		try {
			fs.chmodSync(bin, 0o644); // first spawn fails with EACCES; the signal lands then
			const r = spawnSync(process.execPath, ['--require', preload(dir), launcher, 'sleep'], {
				encoding: 'utf8',
				timeout: 15000,
				env: { ...process.env, PRELOAD_SIGNAL_ON_SPAWN: '1', PRELOAD_PIDFILE: path.join(dir, 'pid') },
			});
			assert.equal(r.error, undefined, 'the retried binary ignored the signal and ran on');
			assert.equal(r.signal, 'SIGTERM', `stderr=${r.stderr}`);
		} finally {
			fs.chmodSync(bin, 0o755);
			fs.rmSync(dir, { recursive: true, force: true });
		}
	});

	test('restores a lost exec bit once', () => {
		const bin = path.join(root, 'node_modules', '@spxrogers', `${MAIN}-${process.platform}-${process.arch}`, 'bin', 'agentsync');
		fs.chmodSync(bin, 0o644);
		const r = run(['echo', 'ok']);
		assert.equal(r.status, 0, r.stderr);
		assert.equal(r.stdout, 'ok\n');
		assert.equal(fs.statSync(bin).mode & 0o111, 0o111);
	});
});

describe('launcher refusing to run', () => {
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

	test('refuses a platform package at a different version', () => {
		const root = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-launcher-'));
		try {
			const launcher = installFixture(root, { withPlatform: true, platformVersion: '9.9.9' });
			const r = spawnSync(process.execPath, [launcher, 'echo', 'ran'], { encoding: 'utf8', timeout: 20000 });
			assert.equal(r.status, 1);
			assert.equal(r.stdout, '', 'the mismatched binary must not run');
			assert.match(r.stderr, new RegExp(`found @spxrogers/${MAIN}-${process.platform}-${process.arch}@9\\.9\\.9, but this is ${MAIN}@${VERSION.replaceAll('.', '\\.')}`));
		} finally {
			fs.rmSync(root, { recursive: true, force: true });
		}
	});

	test('names the supported platforms when this one has no package', () => {
		const root = fs.mkdtempSync(path.join(os.tmpdir(), 'agentsync-launcher-'));
		try {
			const launcher = installFixture(root, { withPlatform: false, supported: false });
			const r = spawnSync(process.execPath, [launcher, '--version'], { encoding: 'utf8', timeout: 20000 });
			assert.equal(r.status, 1);
			assert.match(r.stderr, new RegExp(`no prebuilt binary for ${process.platform}-${process.arch}`));
			assert.match(r.stderr, /Supported: plan9-mips\./);
		} finally {
			fs.rmSync(root, { recursive: true, force: true });
		}
	});
});

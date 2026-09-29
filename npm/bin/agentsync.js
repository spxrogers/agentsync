#!/usr/bin/env node
// agentsync npm launcher — the `bin` of the `agentsync.cc` package.
//
// agentsync is a Go binary; npm/bunx can't run it directly. The main package
// carries only this launcher and lists one package per platform
// (`agentsync.cc-<platform>-<arch>`, each holding the prebuilt binary) as
// optionalDependencies with `os`/`cpu` fields, so the package manager installs
// exactly the one matching this machine. There is deliberately NO postinstall:
// `bunx` (and `npm --ignore-scripts`) skip install scripts, and this layout needs
// none. The launcher finds that platform package, then runs its binary with the
// same argv/stdio and exits with the binary's exit status (or re-raises the
// signal that killed it).
//
// Generated package layout and publishing live in npm/build.mjs. Keep this file
// dependency-free CommonJS: it runs under whatever Node (or Bun) the user has.
'use strict';

const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const INSTALL_DOCS = 'https://agentsync.cc/getting-started/install/';

// platformPackage returns the name of the platform package for this machine, or
// null when the main package ships no binary for it. `optionalDependencies` is
// the source of truth for what's supported: npm/build.mjs writes one entry per
// published platform, named `<main>-<platform>-<arch>` in Node's vocabulary.
function platformPackage(mainPkg, platform, arch) {
	const name = `${mainPkg.name}-${platform}-${arch}`;
	return Object.prototype.hasOwnProperty.call(mainPkg.optionalDependencies || {}, name) ? name : null;
}

// binaryPath resolves the binary inside the installed platform package.
// Throws (MODULE_NOT_FOUND) when the package manager didn't install it.
function binaryPath(pkgName, platform) {
	const pkgJSON = require.resolve(`${pkgName}/package.json`);
	return path.join(path.dirname(pkgJSON), 'bin', platform === 'win32' ? 'agentsync.exe' : 'agentsync');
}

function fail(lines) {
	process.stderr.write(`agentsync: ${lines.join('\n  ')}\n`);
	process.exit(1);
}

function main() {
	const mainPkg = require('../package.json');
	const { platform, arch } = process;
	const pkgName = platformPackage(mainPkg, platform, arch);
	if (!pkgName) {
		const supported = Object.keys(mainPkg.optionalDependencies || {})
			.map((n) => n.slice(mainPkg.name.length + 1))
			.join(', ');
		fail([
			`no prebuilt binary for ${platform}-${arch} in ${mainPkg.name}@${mainPkg.version}.`,
			`Supported: ${supported}.`,
			`Build from source instead: go install github.com/spxrogers/agentsync/cmd/agentsync@latest (${INSTALL_DOCS})`,
		]);
	}

	let bin;
	try {
		bin = binaryPath(pkgName, platform);
	} catch {
		fail([
			`the platform package ${pkgName}@${mainPkg.version} is not installed.`,
			'It is an optionalDependency of the package you ran, so it is skipped by',
			'`--omit=optional` / `--no-optional`, or when the lockfile was made on another',
			'platform. Reinstall without those, or install it directly:',
			`  npm install ${pkgName}@${mainPkg.version}`,
			`Other install options: ${INSTALL_DOCS}`,
		]);
	}
	run(bin, process.argv.slice(2), true);
}

// run execs the binary with inherited stdio and mirrors its exit. `retryChmod`
// covers a package manager that dropped the tarball's exec bit (EACCES): restore
// it once and retry.
function run(bin, args, retryChmod) {
	const child = spawn(bin, args, { stdio: 'inherit' });

	// While the binary runs, the launcher must neither die first nor swallow a
	// signal meant for it. A terminal Ctrl-C already reaches the child through the
	// foreground process group, but a signal sent to the launcher's pid alone
	// (`kill`, a CI runner cancelling the job, npm's own signal forwarding) would
	// otherwise kill only the launcher and orphan the binary. So forward on POSIX;
	// on Windows child.kill() is a hard TerminateProcess and the console already
	// delivers Ctrl-C to the child, so just stay alive.
	const signals = ['SIGINT', 'SIGTERM', 'SIGHUP', 'SIGQUIT'];
	const handlers = {};
	for (const sig of signals) {
		handlers[sig] = () => {
			if (process.platform !== 'win32' && child.exitCode === null && child.signalCode === null) {
				try {
					child.kill(sig);
				} catch {
					// already gone
				}
			}
		};
		try {
			process.on(sig, handlers[sig]);
		} catch {
			// signal unsupported on this platform (e.g. SIGQUIT on Windows)
		}
	}
	const detach = () => {
		for (const sig of signals) process.removeListener(sig, handlers[sig]);
	};

	child.on('error', (err) => {
		detach();
		if (err.code === 'EACCES' && retryChmod) {
			try {
				fs.chmodSync(bin, 0o755);
				run(bin, args, false);
				return;
			} catch {
				// fall through to the error below
			}
		}
		fail([`failed to run ${bin}: ${err.message}`]);
	});

	child.on('exit', (code, signal) => {
		detach();
		if (signal) {
			// Die of the same signal so the caller (a shell, `set -e`, a CI step) sees
			// what really happened — e.g. 130 for Ctrl-C — rather than a plain 1.
			process.kill(process.pid, signal);
			// Still alive: Node ignores this signal (SIGPIPE) or it can't be raised
			// here (Windows). Exit with the shell's encoding of it instead.
			process.exit(128 + (os.constants.signals[signal] || 0));
		}
		process.exit(code === null ? 1 : code);
	});
}

module.exports = { platformPackage, binaryPath };

if (require.main === module) {
	main();
}

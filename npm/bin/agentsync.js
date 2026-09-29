#!/usr/bin/env node
// agentsync npm launcher — the `bin` of the `agentsync.cc` package.
//
// agentsync is a Go binary; npm/bunx can't run it directly. The main package
// carries only this launcher and lists one package per platform
// (`@spxrogers/agentsync.cc-<platform>-<arch>`, each holding the prebuilt binary)
// as optionalDependencies with `os`/`cpu` fields, so the package manager installs
// exactly the one matching this machine. There is deliberately NO postinstall:
// `bunx` (and `npm --ignore-scripts`) skip install scripts, and this layout needs
// none. The launcher finds that platform package, then runs its binary with the
// same argv/stdio and exits with the binary's exit status, or re-raises the
// signal that killed it (exiting 128+n instead for SIGPIPE/SIGUSR1, which Node
// handles itself).
//
// Generated package layout and publishing live in npm/build.mjs. Keep this file
// dependency-free CommonJS: it runs under whatever Node (or Bun) the user has.
'use strict';

const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const INSTALL_DOCS = 'https://agentsync.cc/getting-started/install/';

// Signals forwarded to the binary while it runs (see run()).
const FORWARDED = ['SIGINT', 'SIGTERM', 'SIGHUP', 'SIGQUIT'];

// platformSuffix is the unscoped tail of a platform package name:
// `@spxrogers/agentsync.cc-linux-x64` → `agentsync.cc-linux-x64`.
function platformSuffix(name) {
	return name.slice(name.lastIndexOf('/') + 1);
}

// platformPackage returns the name of the platform package for this machine, or
// null when the main package ships no binary for it. `optionalDependencies` is
// the source of truth for what's supported: npm/build.mjs writes one entry per
// published platform, named `[<scope>/]<main>-<platform>-<arch>` in Node's
// vocabulary. Only names the launcher itself lists can ever be resolved.
function platformPackage(mainPkg, platform, arch) {
	const want = `${mainPkg.name}-${platform}-${arch}`;
	return Object.keys(mainPkg.optionalDependencies || {}).find((n) => platformSuffix(n) === want) || null;
}

// resolvePlatform finds the installed platform package: its binary and its
// version. Throws (MODULE_NOT_FOUND) when the package manager didn't install it.
function resolvePlatform(pkgName, platform) {
	const pkgJSON = require.resolve(`${pkgName}/package.json`);
	return {
		bin: path.join(path.dirname(pkgJSON), 'bin', platform === 'win32' ? 'agentsync.exe' : 'agentsync'),
		version: require(pkgJSON).version,
	};
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
			.map((n) => platformSuffix(n).slice(mainPkg.name.length + 1))
			.join(', ');
		fail([
			`no prebuilt binary for ${platform}-${arch} in ${mainPkg.name}@${mainPkg.version}.`,
			`Supported: ${supported}.`,
			`Build from source instead: go install github.com/spxrogers/agentsync/cmd/agentsync@latest (${INSTALL_DOCS})`,
		]);
	}

	let resolved;
	try {
		resolved = resolvePlatform(pkgName, platform);
	} catch {
		fail([
			`the platform package ${pkgName}@${mainPkg.version} is not installed.`,
			'It is an optionalDependency of the package you ran, so it is skipped by',
			'`--omit=optional` / `--no-optional`, or when the lockfile was made on another',
			'platform. Reinstall without those, or install it directly (add -g for a global install):',
			`  npm install ${pkgName}@${mainPkg.version}`,
			`Other install options: ${INSTALL_DOCS}`,
		]);
	}
	// require.resolve walks up node_modules, so with the optional dependency
	// omitted it can find ANOTHER install's copy (in any ancestor node_modules)
	// at a different version: refuse that. This checks the version only, not
	// where the copy lives — confining the lookup would break legitimate layouts
	// (hoisted npm installs, pnpm's store, Yarn PnP) — so like any Node package,
	// the launcher trusts whoever can write to its ancestor node_modules.
	if (resolved.version !== mainPkg.version) {
		fail([
			`found ${pkgName}@${resolved.version}, but the launcher is ${mainPkg.name}@${mainPkg.version}.`,
			'The platform package must be the same version (this one came from another install).',
			'Reinstall, or install it directly (add -g for a global install):',
			`  npm install ${pkgName}@${mainPkg.version}`,
			`Other install options: ${INSTALL_DOCS}`,
		]);
	}
	run(resolved.bin, process.argv.slice(2));
}

// Signals Node handles itself, which must never be re-raised on the launcher:
// Node ignores SIGPIPE, and SIGUSR1 starts its inspector (a debugger port).
const NOT_RERAISED = new Set(['SIGPIPE', 'SIGUSR1']);

// run execs the binary with inherited stdio and mirrors its exit. A package
// manager that dropped the tarball's exec bit (EACCES) gets it restored once,
// and the spawn retried.
function run(bin, args) {
	// While the binary runs, the launcher must neither die first nor swallow a
	// signal meant for it. A terminal Ctrl-C already reaches the child through the
	// foreground process group, but a signal sent to the launcher's pid alone
	// (`kill`, a CI runner cancelling the job, npm's own signal forwarding) would
	// otherwise kill only the launcher and orphan the binary. So forward on POSIX;
	// on Windows child.kill() is a hard TerminateProcess and the console already
	// delivers Ctrl-C to the child, so just stay alive.
	//
	// The handlers are installed BEFORE spawn(), once, for the whole run
	// (including the EACCES retry). Installing them is what arms the OS-level
	// handler: a signal that lands before it would take the default action and
	// kill the launcher. One that lands after is queued, and Node runs the
	// listener from the event loop, by which time `child` is the live process —
	// the retried one if the first spawn failed.
	let child = null;
	const forward = (sig) => {
		if (process.platform === 'win32') return;
		if (child && child.exitCode === null && child.signalCode === null) {
			try {
				child.kill(sig);
			} catch {
				// already gone
			}
		}
	};
	for (const sig of FORWARDED) {
		try {
			process.on(sig, forward);
		} catch {
			// signal unsupported on this platform (e.g. SIGQUIT on Windows)
		}
	}
	const detach = () => {
		for (const sig of FORWARDED) process.removeListener(sig, forward);
	};

	let retried = false;
	const start = () => {
		child = spawn(bin, args, { stdio: 'inherit' });
		child.on('error', (err) => {
			if (err.code === 'EACCES' && !retried) {
				retried = true;
				try {
					fs.chmodSync(bin, 0o755);
					start();
					return;
				} catch {
					// fall through to the error below
				}
			}
			detach();
			fail([`failed to run ${bin}: ${err.message}`]);
		});
		child.on('exit', (code, signal) => {
			detach();
			if (signal) {
				// Die of the same signal so the caller (a shell, `set -e`, a CI step)
				// sees what really happened — e.g. 130 for Ctrl-C — rather than a
				// plain 1.
				if (!NOT_RERAISED.has(signal)) process.kill(process.pid, signal);
				// Still alive (a signal Node handles itself, or one that can't be
				// raised here, e.g. on Windows): exit with the shell's encoding.
				process.exit(128 + (os.constants.signals[signal] || 0));
			}
			process.exit(code === null ? 1 : code);
		});
	};
	start();
}

module.exports = { platformPackage };

if (require.main === module) {
	main();
}

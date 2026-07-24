//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestE2E_VersionLdflagsInjection covers the one leg of the version-injection
// chain no in-process test can see: goreleaser's `-X main.version` /
// `-X main.commit` / `-X main.date` ldflags (.goreleaser.yaml builds.ldflags)
// landing on the package-main vars in cmd/agentsync/main.go and being copied
// into cli.Version/Commit/Date at startup. The in-process
// TestRoot_VersionInjection (internal/cli/root_test.go) assigns the cli vars
// directly, so renaming `var version` in main.go — which would make every
// release print "dev" — keeps it green; only building the real binary with the
// real flag spellings goes red. Asserts all three injected values reach BOTH
// version surfaces (`--version` and the `version` subcommand).
func TestE2E_VersionLdflagsInjection(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	const (
		wantVersion = "9.9.9-test"
		wantCommit  = "abc1234"
		wantDate    = "2026-01-02"
	)
	// The -X targets below are the load-bearing contract: they must keep naming
	// the actual vars in cmd/agentsync/main.go exactly as .goreleaser.yaml does.
	bin := buildBinaryWith(t, "-ldflags",
		"-X main.version="+wantVersion+
			" -X main.commit="+wantCommit+
			" -X main.date="+wantDate)

	// Run from a simulated $HOME like the lifecycle test's runner, so no-scope
	// auto-discovery can never trip over the repo's own .agentsync/ tree.
	home := t.TempDir()
	env := makeEnv(map[string]string{
		"AGENTSYNC_TARGET_ROOT": home,
		"HOME":                  home,
	})
	r := &runner{bin: bin, dir: home, env: env, t: t}

	for _, args := range [][]string{{"--version"}, {"version"}} {
		out := r.mustRun(args...)
		for _, want := range []string{wantVersion, wantCommit, wantDate} {
			if !strings.Contains(out, want) {
				t.Errorf("agentsync %s output missing injected %q; got:\n%s",
					strings.Join(args, " "), want, out)
			}
		}
	}
}

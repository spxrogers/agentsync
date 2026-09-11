package cli_test

import (
	"testing"
)

// TestRevert_TriangulationMessages pins every refusal `revert` produces while
// deciding which dirs to roll back, VERBATIM.
//
// The existing coverage asserted only that these combinations error. That is
// enough to catch a missing guard and nothing else: a refusal that fires for the
// wrong reason, or names the wrong flag, or is reworded into advice that no
// longer applies, passes an `err != nil` check silently. These messages are the
// entire user interface of a command that otherwise does nothing — the guards
// run before a printer is built, before the lock, before any repo is opened —
// so the message IS the behaviour.
//
// It became load-bearing when #235 lifted the body out of the command literal
// into revertRun: the triangulation rewrites `all` and `args` in place, and
// carrying that rewrite through an opts value instead of a closure is only
// obviously safe if the exact refusals are nailed down on both sides.
func TestRevert_TriangulationMessages(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	mustRun(t, env, "init")
	mustRun(t, env, "agent", "add", "claude")

	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "--agents with --all",
			args: []string{"revert", "--agents", "claude", "--all"},
			want: "--agents and --all both choose which dirs to revert; pass one",
		},
		{
			name: "--agents with a positional agent",
			args: []string{"revert", "--agents", "claude", "claude"},
			want: "--agents and a positional agent both choose which dirs to revert; pass one",
		},
		{
			name: "empty --agents",
			args: []string{"revert", "--agents", ""},
			want: `--agents cannot be empty; pass "*" for every managed dir or name one or more`,
		},
		{
			// --to is per-repo, so it cannot span the two agents --agents named.
			// The count comes from the parsed list, which is why it is pinned.
			name: "--to across several --agents",
			args: []string{"revert", "--agents", "claude,codex", "--to", "HEAD~1"},
			want: "--to names a checkpoint in one repo and can't apply across 2 agents; revert a single agent with --to",
		},
		{
			// `--agents "*"` is a spelling of --all, so it must reach --all's
			// wording here, not the per-agent one above. That routing is the
			// in-place rewrite of `all` this test exists to protect.
			name: "--to with --agents star",
			args: []string{"revert", "--agents", "*", "--to", "HEAD~1"},
			want: "--to names a checkpoint in one repo and can't apply across --all; revert a single agent with --to",
		},
		{
			name: "positional agent with --all",
			args: []string{"revert", "claude", "--all"},
			want: "--all reverts every managed dir; do not also name an agent",
		},
		{
			name: "--all with --to",
			args: []string{"revert", "--all", "--to", "HEAD~1"},
			want: "--to names a checkpoint in one repo and can't apply across --all; revert a single agent with --to",
		},
		{
			name: "neither an agent nor --all",
			args: []string{"revert"},
			want: "name an agent to revert (e.g. `agentsync revert claude`) or pass --all",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCLI(t, env, tc.args...)
			if err == nil {
				t.Fatalf("%v must be refused", tc.args)
			}
			if err.Error() != tc.want {
				t.Errorf("refusal text changed\n got: %s\nwant: %s", err.Error(), tc.want)
			}
		})
	}
}

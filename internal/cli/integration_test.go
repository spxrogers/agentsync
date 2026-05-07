package cli_test

import (
	"strings"
	"testing"
)

func TestIntegration_M0Lifecycle(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	type step struct {
		args      []string
		wantSubs  []string
		wantError bool
	}
	steps := []step{
		{args: []string{"init"}, wantSubs: []string{"initialized"}},
		{args: []string{"agent", "add", "claude"}, wantSubs: []string{"added agent: claude"}},
		{args: []string{"agent", "add", "opencode"}, wantSubs: []string{"added agent: opencode"}},
		{args: []string{"agent", "list"}, wantSubs: []string{"claude", "opencode"}},
		{args: []string{"verify"}, wantSubs: []string{"ok"}},
		{args: []string{"apply", "--dry-run"}, wantSubs: []string{"Plan", "claude", "opencode"}},
		{args: []string{"agent", "remove", "claude"}, wantSubs: []string{"removed agent: claude"}},
	}
	for _, s := range steps {
		out, err := runCLI(t, env, s.args...)
		if (err != nil) != s.wantError {
			t.Fatalf("%v: err=%v want-err=%v\n%s", s.args, err, s.wantError, out)
		}
		for _, sub := range s.wantSubs {
			if !strings.Contains(out, sub) {
				t.Fatalf("%v: output missing %q. Got:\n%s", s.args, sub, out)
			}
		}
	}
}

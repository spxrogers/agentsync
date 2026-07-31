package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/testenv"
	"github.com/spxrogers/agentsync/internal/ui"
)

// newPromptIO builds an importIO whose stdin carries the given typed lines and
// whose output is captured, so the prompter can be driven without a terminal.
// cmd.InOrStdin() is a strings.Reader here, so stdinIsTerminal reports false —
// tests that want the interactive branch call the prompter body directly, which
// is what these do; the TTY gate itself is covered by
// TestPromptNativeAgentsDeferral_NonInteractiveDefersWithoutAsking.
func newPromptIO(t *testing.T, typed string) (*importIO, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	p := &ui.Printer{Out: &out, Err: &out}
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(typed))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	return &importIO{p: p, out: &out, err: &out, cmd: cmd}, &out
}

// TestPromptNativeAgentsDeferral_NonInteractiveDefersWithoutAsking pins the
// fail-SAFE direction. Every other prompter in this package fails closed,
// because their unattended fallback is inaction. Here inaction is the damaging
// outcome — a scripted `agentsync import claude:plugin` that answered "no" by
// default would leave every component duplicated in Claude — so the unattended
// path takes the branch that changes nothing about the agent's own setup.
func TestPromptNativeAgentsDeferral_NonInteractiveDefersWithoutAsking(t *testing.T) {
	io, out := newPromptIO(t, "") // a strings.Reader is not a TTY
	got := promptNativeAgentsDeferral(io, "toolkit", []string{"claude"})
	if len(got) != 1 || got[0] != "claude" {
		t.Fatalf("a non-interactive import must record the deferral; got %v", got)
	}
	if !strings.Contains(out.String(), "native_agents") {
		t.Errorf("it must say what it did and where to change it; got:\n%s", out.String())
	}
	if strings.Contains(out.String(), "[Y]es") {
		t.Errorf("it must not print a prompt nobody can answer; got:\n%s", out.String())
	}
}

// TestPromptNativeAgentsDeferral_AnswerParsing drives the question loop
// directly — askDeferralAnswer is split from the TTY gate precisely so both
// branches are reachable without a terminal. A bare Enter and an EOF both take
// the safe default; only an explicit "no" opts into the duplicate.
func TestPromptNativeAgentsDeferral_AnswerParsing(t *testing.T) {
	cases := []struct {
		name  string
		typed string
		want  []string
	}{
		{name: "explicit yes", typed: "y\n", want: []string{"claude"}},
		{name: "yes spelled out", typed: "yes\n", want: []string{"claude"}},
		{name: "bare enter takes the safe default", typed: "\n", want: []string{"claude"}},
		{name: "explicit no opts into the duplicate", typed: "n\n", want: nil},
		{name: "no spelled out", typed: "no\n", want: nil},
		{name: "unrecognized then no", typed: "maybe\nn\n", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			io, _ := newPromptIO(t, tc.typed)
			got := askDeferralAnswer(io, "toolkit", []string{"claude"})
			if len(got) != len(tc.want) {
				t.Fatalf("typed %q: got %v, want %v", tc.typed, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("typed %q: got %v, want %v", tc.typed, got, tc.want)
				}
			}
		})
	}
}

// TestWarnDuplicateOptOut pins the wording of the opt-out warning. Declining the
// deferral produces a working setup ONLY if the user then turns the plugin off
// inside the agent, so the warning has to say that in as many words — naming the
// agent, and naming the consequence if they do not.
func TestWarnDuplicateOptOut(t *testing.T) {
	io, out := newPromptIO(t, "")
	warnDuplicateOptOut(io, "toolkit", []string{"claude"})
	got := out.String()
	for _, want := range []string{
		"DUPLICATE", // the consequence, stated plainly
		"twice",     // what that means concretely
		"claude",    // which harness
		"Disable or uninstall",
		"native_agents", // and the way back
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the opt-out warning should mention %q; got:\n%s", want, got)
		}
	}
}

// TestPluginNativeAgentsUnset gates whether import ASKS at all. Prompting for a
// plugin whose plugins/<id>.toml already carries `native_agents` would ask a
// question whose answer installPluginInto discards (it preserves the existing
// list, per #140) — so the reply must only be solicited when it can take effect.
//
// The three states are distinct on purpose: an ABSENT file is a first install
// (ask), a file with NO key is an upgrade from before the key existed (ask), and
// a file WITH a key — including a deliberately empty one, which reads as "defer
// to nobody" — is a decision already made (do not ask).
func TestPluginNativeAgentsUnset(t *testing.T) {
	testenv.RequireContainer(t)
	cases := []struct {
		name    string
		content string // "" means write no file at all
		want    bool
	}{
		{name: "no file — a first install", content: "", want: true},
		{
			name:    "file without the key — upgraded from before it existed",
			content: "[plugin]\nid = \"toolkit@mp\"\nagents = [\"*\"]\n",
			want:    true,
		},
		{
			name:    "file with a deferral already recorded",
			content: "[plugin]\nid = \"toolkit@mp\"\nnative_agents = [\"claude\"]\n",
			want:    false,
		},
		{
			name:    "explicitly empty list — a deliberate \"defer to nobody\"",
			content: "[plugin]\nid = \"toolkit@mp\"\nnative_agents = []\n",
			want:    false,
		},
		{
			name:    "unparseable file — installPluginInto refuses it, nothing to ask",
			content: "[plugin\nid = broken",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.content != "" {
				dir := filepath.Join(home, "plugins")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "toolkit.toml"), []byte(tc.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := pluginNativeAgentsUnset(home, "toolkit"); got != tc.want {
				t.Errorf("pluginNativeAgentsUnset = %v, want %v", got, tc.want)
			}
		})
	}
}

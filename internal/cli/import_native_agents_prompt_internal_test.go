package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
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
		// A line with no trailing newline reaches ReadString's err!=nil path with
		// a non-empty line, which is the ONLY route to the EOF guard — the "\n"
		// case above hits `case ""` first and never exercises it. Closed stdin
		// must land on the safe branch (defer), never fall through to the
		// duplicate.
		{name: "unrecognized then EOF takes the safe default", typed: "maybe", want: []string{"claude"}},
		{name: "bare EOF, no input at all", typed: "", want: []string{"claude"}},
		// Exhausting the attempt cap must also land safe rather than loop or
		// return the duplicate.
		{name: "cap exhausted by repeated garbage", typed: strings.Repeat("what\n", 8), want: []string{"claude"}},
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

// stubNativeAgentsPrompter replaces the deferral prompt for one test and returns
// a restore closure, mirroring stubPrompter in gitbackup_test.go. It also
// records whether the prompt was reached at all — several behaviors below are
// about NOT asking, and "returned the right value" cannot distinguish "asked and
// accepted" from "never asked".
func stubNativeAgentsPrompter(t *testing.T, reply []string, asked *bool) {
	t.Helper()
	prev := nativeAgentsPrompter
	nativeAgentsPrompter = func(*importIO, string, []string) []string {
		*asked = true
		return reply
	}
	t.Cleanup(func() { nativeAgentsPrompter = prev })
}

// writePluginTOMLFixture writes plugins/<id>.toml under home. Empty content
// writes no file at all (the "first install" state).
func writePluginTOMLFixture(t *testing.T, home, id, content string) {
	t.Helper()
	if content == "" {
		return
	}
	dir := filepath.Join(home, "plugins")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestResolveNativeAgents covers the whole decision `import` makes per plugin —
// including the branches that must NOT ask. Each case asserts BOTH the returned
// value and whether the prompt was reached; without the second, "did not ask"
// and "asked and got the same answer back" are indistinguishable, which is
// exactly the gap that let the prompt-gate behavior go unpinned.
func TestResolveNativeAgents(t *testing.T) {
	testenv.RequireContainer(t)
	candidates := []string{"claude"}
	cases := []struct {
		name         string
		existing     string // plugins/toolkit.toml content; "" = no file
		noCandidates bool   // pass an empty candidate list rather than the default
		dryRun       bool
		reply        []string // what the stubbed prompt answers
		wantAsked    bool
		want         []string
	}{
		{
			name:  "first install, accepted — records the deferral",
			reply: candidates, wantAsked: true, want: candidates,
		},
		{
			name:  "first install, declined — records nothing, so a later import re-asks",
			reply: nil, wantAsked: true, want: nil,
		},
		{
			name:     "already recorded — the answer would be discarded, so do not ask",
			existing: "[plugin]\nid = \"toolkit@mp\"\nnative_agents = [\"claude\"]\n",
			reply:    nil, wantAsked: false, want: candidates,
		},
		{
			name:     "explicitly empty — a decision already made; do not re-litigate it",
			existing: "[plugin]\nid = \"toolkit@mp\"\nnative_agents = []\n",
			reply:    nil, wantAsked: false, want: candidates,
		},
		{
			name:     "upgraded from before the key existed — ask",
			existing: "[plugin]\nid = \"toolkit@mp\"\nagents = [\"*\"]\n",
			reply:    candidates, wantAsked: true, want: candidates,
		},
		{
			name:   "dry run never asks — it has its own preview line",
			dryRun: true, reply: nil, wantAsked: false, want: candidates,
		},
		{
			name:         "no candidates — nothing to defer, nothing to ask",
			noCandidates: true,
			reply:        nil, wantAsked: false, want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writePluginTOMLFixture(t, home, "toolkit", tc.existing)
			io, _ := newPromptIO(t, "")
			io.dryRun = tc.dryRun
			var asked bool
			stubNativeAgentsPrompter(t, tc.reply, &asked)

			in := candidates
			if tc.noCandidates {
				in = nil
			}
			got := resolveNativeAgents(io, home, "toolkit", in)

			if asked != tc.wantAsked {
				t.Errorf("prompt asked = %v, want %v", asked, tc.wantAsked)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestResolveNativeAgents_DeclineWarns pins that declining is never silent. The
// decline branch returns nil, which on its own is indistinguishable from "there
// was nothing to defer" — the warning is what tells the user they now own a
// duplicate and must disable the plugin inside the agent themselves.
func TestResolveNativeAgents_DeclineWarns(t *testing.T) {
	testenv.RequireContainer(t)
	io, out := newPromptIO(t, "")
	var asked bool
	stubNativeAgentsPrompter(t, nil, &asked)

	if got := resolveNativeAgents(io, t.TempDir(), "toolkit", []string{"claude"}); got != nil {
		t.Fatalf("declining must record no deferral; got %v", got)
	}
	if !strings.Contains(out.String(), "DUPLICATE") || !strings.Contains(out.String(), "Disable or uninstall") {
		t.Errorf("declining must warn about the duplicate it creates; got:\n%s", out.String())
	}
}

// TestWarnDuplicateOptOut_RemedyIsValidTOML pins the remedy line for the
// MULTI-agent case. A naive `[%q]` over a comma-joined list rendered
// `native_agents = ["claude, cursor"]` — valid TOML syntax naming one agent that
// does not exist, so a user who pasted it would silently defer to nobody and
// keep the duplicate the warning is about.
func TestWarnDuplicateOptOut_RemedyIsValidTOML(t *testing.T) {
	testenv.RequireContainer(t)
	io, out := newPromptIO(t, "")
	warnDuplicateOptOut(io, "toolkit", []string{"claude", "cursor"})
	got := out.String()
	if !strings.Contains(got, `native_agents = ["claude", "cursor"]`) {
		t.Errorf("the remedy must be valid TOML naming both agents; got:\n%s", got)
	}
	if strings.Contains(got, `["claude, cursor"]`) {
		t.Errorf("the remedy collapsed both agents into one bogus name; got:\n%s", got)
	}
}

// TestPluginTOML_NativeAgentsRoundTrip is the durability pin for the three
// on-disk states of `native_agents`. The field is a POINTER precisely because
// `omitempty` drops an empty slice: before that fix, a user's explicit
// `native_agents = []` ("defer to nobody, stop asking") survived being READ —
// so the prompt correctly stayed quiet — and was then erased by the next write,
// after which the following import re-seeded the deferral, silently and, under
// --no-input, without asking.
//
// The oracle is a full write→read→write cycle, not a single decode: the bug was
// invisible to a decode-only test.
func TestPluginTOML_NativeAgentsRoundTrip(t *testing.T) {
	testenv.RequireContainer(t)
	empty := []string{}
	populated := []string{"claude"}
	cases := []struct {
		name    string
		in      *[]string
		wantKey string // substring the re-marshalled TOML must contain
		absent  bool   // or: the key must not appear at all
	}{
		{name: "absent stays absent", in: nil, absent: true},
		{name: "explicitly empty survives", in: &empty, wantKey: "native_agents = []"},
		{name: "populated survives", in: &populated, wantKey: "native_agents = ['claude']"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, err := toml.Marshal(pluginTOML{Plugin: pluginTOMLSpec{ID: "toolkit@mp", NativeAgents: tc.in}})
			if err != nil {
				t.Fatal(err)
			}
			var decoded pluginTOML
			if err := toml.Unmarshal(first, &decoded); err != nil {
				t.Fatal(err)
			}
			// The read side must preserve the absent/empty distinction, since it
			// is what pluginNativeAgentsUnset branches on.
			if (decoded.Plugin.NativeAgents == nil) != (tc.in == nil) {
				t.Fatalf("absent/present distinction lost on read: got nil=%v, want nil=%v",
					decoded.Plugin.NativeAgents == nil, tc.in == nil)
			}
			second, err := toml.Marshal(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if tc.absent {
				if strings.Contains(string(second), "native_agents") {
					t.Fatalf("no key was set, but one was written:\n%s", second)
				}
				return
			}
			if !strings.Contains(string(second), tc.wantKey) {
				t.Fatalf("rewrite lost the recorded decision; want %q, got:\n%s", tc.wantKey, second)
			}
		})
	}
}

// TestNativeAgentsSuffix_SanitizesAndStaysQuiet pins both halves of the item-line
// suffix. The values are read back from plugins/<id>.toml — a hand-editable file
// in a synced dotfiles repo — and item() sanitizes only the path it is given,
// appending this suffix verbatim, so an ESC pasted into that TOML would reach the
// terminal. The quiet branches matter too: a non-nil EMPTY list is the same
// OUTCOME as no deferral, and the item line reports outcomes.
func TestNativeAgentsSuffix_SanitizesAndStaysQuiet(t *testing.T) {
	testenv.RequireContainer(t)
	empty := []string{}
	hostile := []string{"claude\x1b[31m", "co\x1b]0;pwn\adex"}
	populated := []string{"claude"}

	if got := nativeAgentsSuffix(nil); got != "" {
		t.Errorf("no deferral must print nothing; got %q", got)
	}
	if got := nativeAgentsSuffix(&empty); got != "" {
		t.Errorf("an empty deferral defers to nobody — same outcome, same silence; got %q", got)
	}
	if got := nativeAgentsSuffix(&populated); !strings.Contains(got, "native_agents=[claude]") {
		t.Errorf("a real deferral must name the agents; got %q", got)
	}
	got := nativeAgentsSuffix(&hostile)
	if strings.ContainsAny(got, "\x1b\a") {
		t.Errorf("a control byte from a hand-edited TOML reached the suffix: %q", got)
	}
}

// TestKeptLifecycleSummary_Sanitizes covers the sibling display site. Both read
// the same user-editable file, and the pair is exactly where "one site
// remembered, the other forgot" happens.
func TestKeptLifecycleSummary_Sanitizes(t *testing.T) {
	testenv.RequireContainer(t)
	hostile := []string{"claude\x1b[31m"}
	got := keptLifecycleSummary(pluginTOMLSpec{
		Agents:       []string{"codex\x1b[32m"},
		NativeAgents: &hostile,
		Update:       "track",
	})
	if strings.ContainsAny(got, "\x1b\a") {
		t.Errorf("a control byte from a hand-edited TOML reached the install summary: %q", got)
	}
	if !strings.Contains(got, "native_agents=[") || !strings.Contains(got, "agents=[") {
		t.Errorf("both preserved lists should still be reported; got %q", got)
	}
}

// TestDryRunNativeAgentsNote covers the preview's three states. A dry run must
// never ASSERT a deferral, because the real run asks and the user may decline —
// advertising an outcome the real run will not produce is worse than saying
// nothing. Equally it must not stay silent when the real run will not ask at all.
func TestDryRunNativeAgentsNote(t *testing.T) {
	testenv.RequireContainer(t)
	cases := []struct {
		name       string
		existing   string
		candidates []string
		wantSubstr string // "" means the note must be empty
		wantAbsent string
	}{
		{
			name: "nothing to defer", candidates: nil, wantSubstr: "",
		},
		{
			name:       "no key yet — the real run will ask, so say that, do not assert it",
			candidates: []string{"claude"},
			wantSubstr: "will ask",
			wantAbsent: "native_agents=[claude]",
		},
		{
			name:       "already recorded — the real run preserves it, so preview the real value",
			existing:   "[plugin]\nid = \"toolkit@mp\"\nnative_agents = [\"claude\"]\n",
			candidates: []string{"claude"},
			wantSubstr: "native_agents=[claude]",
		},
		{
			name:       "recorded as empty — defers to nobody, so nothing to report",
			existing:   "[plugin]\nid = \"toolkit@mp\"\nnative_agents = []\n",
			candidates: []string{"claude"},
			wantSubstr: "",
		},
		{
			name:       "unreadable — the real run refuses, so the preview must not look clean",
			existing:   "[plugin\nid = broken",
			candidates: []string{"claude"},
			wantSubstr: "will skip this plugin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writePluginTOMLFixture(t, home, "toolkit", tc.existing)
			got := dryRunNativeAgentsNote(home, "toolkit", tc.candidates)
			if tc.wantSubstr == "" {
				if got != "" {
					t.Fatalf("expected no note, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSubstr) {
				t.Fatalf("note = %q, want it to contain %q", got, tc.wantSubstr)
			}
			if tc.wantAbsent != "" && strings.Contains(got, tc.wantAbsent) {
				t.Errorf("the preview asserted an outcome the prompt may decline: %q", got)
			}
		})
	}
}

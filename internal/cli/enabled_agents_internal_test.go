package cli

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestEnabledAgentNames pins the pair's contract: enabled agents only, SORTED,
// with a membership map that agrees with the slice, and both non-nil when
// nothing is enabled.
//
// The sort is the load-bearing part. The eight map walks this replaced were
// unordered, and two of them (explain, plugin explain) re-sorted afterwards
// precisely because the order reaches the user — render.Plan visits agents in
// this order and `--json` emits rows in it. Centralizing the sort is what makes
// the other six deterministic too.
func TestEnabledAgentNames(t *testing.T) {
	cases := []struct {
		name    string
		cfg     source.Config
		want    []string
		wantMap map[string]bool
	}{
		{
			name:    "empty config",
			cfg:     source.Config{},
			want:    []string{},
			wantMap: map[string]bool{},
		},
		{
			name: "all disabled",
			cfg: source.Config{Agents: map[string]source.Agent{
				"claude": {Enabled: false},
				"codex":  {Enabled: false},
			}},
			want:    []string{},
			wantMap: map[string]bool{},
		},
		{
			name: "only the enabled ones, sorted",
			cfg: source.Config{Agents: map[string]source.Agent{
				"zed":      {Enabled: true},
				"claude":   {Enabled: true},
				"opencode": {Enabled: false},
				"codex":    {Enabled: true},
			}},
			want:    []string{"claude", "codex", "zed"},
			wantMap: map[string]bool{"claude": true, "codex": true, "zed": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Repeat: Go randomizes map iteration, so a single pass can agree
			// with the sorted expectation by luck. Ten passes cannot.
			for i := 0; i < 10; i++ {
				got, enabled := enabledAgentNames(tc.cfg)
				if got == nil {
					t.Fatal("names must be non-nil even when nothing is enabled")
				}
				if enabled == nil {
					t.Fatal("the membership map must be non-nil even when nothing is enabled")
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("pass %d: names = %v, want %v", i, got, tc.want)
				}
				if !reflect.DeepEqual(enabled, tc.wantMap) {
					t.Fatalf("pass %d: enabled = %v, want %v", i, enabled, tc.wantMap)
				}
				// The two returns must describe the same set: selectAgents
				// validates an --agents allowlist against the MAP and returns the
				// SLICE when the flag is absent, so a disagreement would let an
				// agent be selectable but never rendered (or vice versa).
				if len(got) != len(enabled) {
					t.Fatalf("pass %d: slice/map disagree: %v vs %v", i, got, enabled)
				}
				for _, n := range got {
					if !enabled[n] {
						t.Fatalf("pass %d: %q is in the slice but not the map", i, n)
					}
				}
			}
		})
	}
}

// enabledAgentLoopSites reports the files whose source contains the open-coded
// enabled-agent walk this helper replaced: a `range` over an agents map whose
// body tests `.Enabled`. Factored out of the repo walk so the guard can be
// exercised against a synthetic source (see the negative control).
func enabledAgentLoopSites(src string) bool {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "range") || !strings.Contains(line, ".Agents") {
			continue
		}
		// The `if ag.Enabled {` test is the next line in every copy this
		// replaced; allow a couple of lines of slack for a reformat.
		for j := i + 1; j < len(lines) && j <= i+3; j++ {
			if strings.Contains(lines[j], ".Enabled") {
				return true
			}
		}
	}
	return false
}

// TestEnabledAgentExtractionIsInOnePlace pins that "which agents are enabled"
// has ONE definition.
//
// Eight copies of this walk existed before #235, in apply, status, diff,
// reconcile, explain, plugin (upgrade --lossless), plugin explain and plugin
// poll. Four built the `enabled` membership map and four did not; two sorted
// the result and six did not. Nothing forced them to agree, so "enabled
// agents" quietly meant three different things depending on which command you
// ran.
//
// DELIBERATELY NOT MATCHED: check.go walks `c.Config.Agents` to validate EVERY
// declared agent name, enabled or not — a different question with a different
// answer, and its loop body does not test .Enabled, so the guard does not see
// it. internal/project/project.go likewise merges the whole [agents] table.
//
// LIMIT: the pattern is line-shaped. A walk written with a helper variable, or
// with the .Enabled test more than three lines below the range, slips past. It
// catches the copy-paste that actually happened eight times, not every spelling.
func TestEnabledAgentExtractionIsInOnePlace(t *testing.T) {
	repoRoot := repoRootFromCaller(t)

	allowed := map[string]string{
		"internal/cli/agents_flag.go": "enabledAgentNames — the one enabled-agent extraction",
	}

	var unexpected []string
	seen := map[string]bool{}
	if err := walkRepoGoFiles(repoRoot, func(rel, src string) {
		if !enabledAgentLoopSites(src) {
			return
		}
		if _, ok := allowed[rel]; ok {
			seen[rel] = true
			return
		}
		unexpected = append(unexpected, rel)
	}); err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		t.Errorf("an open-coded enabled-agent walk outside internal/cli/agents_flag.go:\n  %s\n\n"+
			"Call enabledAgentNames(cfg) instead — it returns the exact ([]string, map[string]bool) pair selectAgents takes.",
			strings.Join(unexpected, "\n  "))
	}
	for rel, reason := range allowed {
		if !seen[rel] {
			t.Errorf("%s is allowlisted (%s) but no longer contains the walk — drop it from the allowlist", rel, reason)
		}
	}

	// NEGATIVE CONTROL — a guard that has stopped biting must fail HERE rather
	// than pass silently.
	t.Run("negative control", func(t *testing.T) {
		reintroduced := "func run() {\n\tvar agents []string\n\tfor name, ag := range c.Config.Agents {\n" +
			"\t\tif ag.Enabled {\n\t\t\tagents = append(agents, name)\n\t\t}\n\t}\n}\n"
		if !enabledAgentLoopSites(reintroduced) {
			t.Fatal("the guard must flag a reintroduced enabled-agent walk")
		}
		// check.go's shape: a walk over the same map that never tests .Enabled.
		validateAll := "\tfor name := range c.Config.Agents {\n\t\tif err := validateAgent(name); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n"
		if enabledAgentLoopSites(validateAll) {
			t.Fatal("validating every declared agent is a different question; the guard must not flag it")
		}
		// Neither token alone is the walk.
		if enabledAgentLoopSites("\tfor _, x := range xs {\n\t\tif x.Enabled {\n\t\t}\n\t}\n") {
			t.Fatal("a .Enabled test over some other collection is not an agents walk")
		}
	})
}

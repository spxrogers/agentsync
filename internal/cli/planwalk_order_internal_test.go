package cli

import (
	"sort"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// TestMergedKeyOrderIsDeterministic pins #229 axis 13 for every surface that
// lists merged keys: five servers under one key-merge op, inserted in an order
// that is NOT sorted, listed by each builder 50 times, must come back in
// exactly one order, and that order ascending. render.CollectPointers ranges a
// Go map — measured five distinct orderings over 200 calls before the shared
// walk — so 50 identical unsorted runs would have probability ~120^-49.
func TestMergedKeyOrderIsDeterministic(t *testing.T) {
	ids := []string{"zulu", "mike", "alpha", "tango", "bravo"}
	tests := []struct {
		name string
		// pointers lists the merged-key pointers the builder reports, in the
		// builder's own order, for the fixture at userHome.
		pointers func(t *testing.T, userHome, path string, plan render.RenderPlan, s *state.Targets) []string
	}{
		{
			// N-6: status's --json payload and dashboard rows.
			name: "status",
			pointers: func(t *testing.T, userHome, _ string, plan render.RenderPlan, s *state.Targets) []string {
				t.Helper()
				var out []string
				for _, it := range buildStatusModel(plan, []string{"claude"}, s, userHome, adapter.ScopeUser, "").Agents[0].Items {
					out = append(out, it.Pointer)
				}
				return out
			},
		},
		{
			// N-7: diff's hunks (every key differs, so all five print).
			name: "diff",
			pointers: func(t *testing.T, _, _ string, plan render.RenderPlan, _ *state.Targets) []string {
				t.Helper()
				hunks, _ := collectDiffHunks(plan, []string{"claude"}, "", nil)
				var out []string
				for _, h := range hunks {
					out = append(out, h.Pointer)
				}
				return out
			},
		},
		{
			// N-8: reconcile's prompt queue.
			name: "reconcile",
			pointers: func(t *testing.T, userHome, _ string, plan render.RenderPlan, s *state.Targets) []string {
				t.Helper()
				var out []string
				for _, it := range collectItems(plan, registryFactory(), s, adapter.ScopeUser, "", userHome, nil) {
					out = append(out, it.ptr)
				}
				return out
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			userHome := t.TempDir()
			d := dest(userHome, "settings.json")
			// Every key differs on all three sides (source, state, disk), so
			// a builder that lists only differing keys (diff) still reports
			// all five.
			disk := `{"mcpServers":{`
			for i, id := range ids {
				if i > 0 {
					disk += ","
				}
				disk += `"` + id + `":{"command":"` + id + `-disk"}`
			}
			mustWrite(t, d, disk+"}}")
			s := state.New()
			for _, id := range ids {
				s.Keys[ptrKey(userHome, "claude", d, "/mcpServers/"+id)] = state.KeyEntry{SHA256: hv(id + "-old")}
			}
			plan := planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, mcpJSON(ids...))}})

			distinct := map[string]bool{}
			var first []string
			for i := 0; i < 50; i++ {
				got := tc.pointers(t, userHome, d, plan, s)
				if len(got) != len(ids) {
					t.Fatalf("run %d: %d pointers, want %d: %v", i, len(got), len(ids), got)
				}
				key := ""
				for _, p := range got {
					key += p + "\x00"
				}
				distinct[key] = true
				if first == nil {
					first = got
				}
			}
			if len(distinct) != 1 {
				t.Errorf("%d distinct pointer orderings over 50 runs; want exactly 1", len(distinct))
			}
			if !sort.StringsAreSorted(first) {
				t.Errorf("pointers are not ascending: %v", first)
			}
		})
	}
}

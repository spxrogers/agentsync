package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// This file is the characterization harness for #229: it pins, fixture by
// fixture, what the four plan→drift walks — buildStatusModel (S),
// collectDiffHunks (D), collectItems ++ collectOrphanFileItems (R) and
// buildExplainModel (E) — answer TODAY, before they are unified behind one
// shared walk. Every golden below was written from the drift classifier's
// truth table and each surface's documented rules, then confirmed against the
// unmodified code; the harness is an oracle for the OLD behaviour, so a
// refactor that needs to edit a golden here is not a refactor.
//
// What is compared IN ORDER: agent order, op order across paths, status's
// whole-file-before-key partition, reconcile's all-items-before-all-orphans
// composition. The ONLY thing the harness is blind to is pointer order within
// one (agent, path) run — see normalizeRuns — because render.CollectPointers
// ranges a Go map and today's output there is genuinely nondeterministic.

// sRow is one status item with its agent, the S projection's row currency.
type sRow struct {
	agent, path, ptr, cls string
}

// sAgentProj is one statusAgent: the agent name and its ordered rows. The
// skeleton is part of the contract — status appends an agent that has a plan
// result even when it has zero items ("(no tracked items)").
type sAgentProj struct {
	agent string
	rows  []sRow
}

type sProj struct {
	agents  []sAgentProj
	summary map[string]int
}

type dProj struct {
	hunks         []diffHunk
	filterMatched bool
}

// rRow projects one reconcileItem: everything the reconcile loop reads off it
// except scope/projectRoot (fixture inputs) and pluginOwner (no plugins here).
type rRow struct {
	agent, path, ptr, cls string
	hsrc, happlied, hdest string
	orphan, hasText       bool
	srcText, dstText      string
}

type eRow struct {
	agent, ptr, ownership, drift string
}

type eProj struct {
	rows        []eRow
	unmanaged   bool
	pathManaged bool
}

// normalizeRuns stable-sorts, by pointer, each maximal run of adjacent rows that
// share (agent, path) and carry a pointer. That is the ONLY thing today's code
// leaves nondeterministic (render.CollectPointers ranges a Go map), so it is the
// only thing the harness is allowed to be blind to. Everything else — agent
// order, op order across paths, the whole-file-before-key partition, orphan
// placement — is compared IN ORDER. key returns (agent, path, ptr) for a row.
func normalizeRuns[T any](rows []T, key func(T) (agent, path, ptr string)) []T {
	if rows == nil {
		return nil
	}
	out := append([]T(nil), rows...)
	for i := 0; i < len(out); {
		a, p, ptr := key(out[i])
		if ptr == "" {
			i++
			continue
		}
		j := i + 1
		for j < len(out) {
			a2, p2, ptr2 := key(out[j])
			if a2 != a || p2 != p || ptr2 == "" {
				break
			}
			j++
		}
		run := out[i:j]
		sort.SliceStable(run, func(x, y int) bool {
			_, _, px := key(run[x])
			_, _, py := key(run[y])
			return px < py
		})
		i = j
	}
	return out
}

// planFixture is one characterized scenario. setup builds the on-disk tree,
// the plan and the state under userHome (already symlink-resolved) and returns
// them; the four wants are the goldens.
type planFixture struct {
	name string
	// scope/projectRoot default to user scope; T-21 overrides.
	scope       adapter.Scope
	projectRoot func(userHome string) string
	// diffFilter, when set, is collectDiffHunks's <path> argument.
	diffFilter func(userHome string) string
	// target/pointer feed buildExplainModel.
	target  func(userHome string) string
	pointer string
	setup   func(t *testing.T, userHome string) (render.RenderPlan, *state.Targets)
	wantS   func(userHome string) sProj
	wantD   func(userHome string) dProj
	wantR   func(userHome string) []rRow
	wantE   func(userHome string) eProj
	// extra runs fixture-specific assertions on top of the four projections.
	extra func(t *testing.T, userHome string, s sProj, d dProj, r []rRow, e eProj)
}

// hf is the whole-file hash of a literal content string, the value state
// records and hashFile answers for it.
func hf(s string) string { return hashContent([]byte(s)) }

// mcpVal is the decoded value of one fixture MCP server: every fixture server
// is {"command": <cmd>}, so its key hash and pretty form are functions of cmd.
func mcpVal(cmd string) map[string]any { return map[string]any{"command": cmd} }

// hv is hashAnyValue over a fixture server value, what state records for a key.
func hv(cmd string) string { return hashAnyValue(mcpVal(cmd)) }

// pretty is marshalPretty's rendering of a fixture server value — the literal
// text diff and reconcile show for it.
func pretty(cmd string) string { return "{\n  \"command\": \"" + cmd + "\"\n}" }

// mcpJSON renders a {"mcpServers": {id: {"command": id}}} document for the ids.
func mcpJSON(ids ...string) string {
	out := `{"mcpServers":{`
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf(`%q:{"command":%q}`, id, id)
	}
	return out + "}}"
}

func fileKey(userHome, agent, path string) state.Key {
	return stateFileKey(userHome, agent, adapter.ScopeUser, "", path)
}

func ptrKey(userHome, agent, path, ptr string) state.Key {
	return stateKeyKey(userHome, agent, adapter.ScopeUser, "", path, ptr)
}

func fileOp(path, content string) adapter.FileOp {
	return adapter.FileOp{Action: "write", Path: path, Content: []byte(content), SourceID: "memory/AGENTS.md"}
}

func keyOp(path, content string) adapter.FileOp {
	return adapter.FileOp{
		Action: "write", Path: path, Content: []byte(content),
		MergeStrategy: "merge-json-keys", SourceID: "mcp/* (multiple)",
	}
}

func planFor(agents map[string][]adapter.FileOp) render.RenderPlan {
	plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{}}
	for name, ops := range agents {
		plan.PerAgent[name] = render.AgentResult{Ops: ops}
	}
	return plan
}

func dest(userHome, name string) string { return filepath.Join(userHome, "dest", name) }

func planFixtures() []planFixture {
	// Single-agent, single whole-file fixtures share one shape: the op renders
	// SOURCE to dest/file.md; disk and state vary per drift class.
	wholeFile := func(name, onDisk, applied, rendered, cls, ownership string, dHunk bool) planFixture {
		return planFixture{
			name:   name,
			target: func(h string) string { return dest(h, "file.md") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				d := dest(h, "file.md")
				if onDisk != "" {
					mustWrite(t, d, onDisk)
				}
				s := state.New()
				if applied != "" {
					s.Files[fileKey(h, "claude", d)] = state.FileEntry{SHA256: hf(applied), SourceID: "memory/AGENTS.md"}
				}
				return planFor(map[string][]adapter.FileOp{"claude": {fileOp(d, rendered)}}), s
			},
			wantS: func(h string) sProj {
				return sProj{
					agents:  []sAgentProj{{agent: "claude", rows: []sRow{{"claude", dest(h, "file.md"), "", cls}}}},
					summary: map[string]int{cls: 1},
				}
			},
			wantD: func(h string) dProj {
				if !dHunk {
					return dProj{filterMatched: true}
				}
				return dProj{hunks: []diffHunk{{Path: dest(h, "file.md"), Source: rendered, Dest: onDisk}}, filterMatched: true}
			},
			wantR: func(h string) []rRow {
				happlied := ""
				if applied != "" {
					happlied = hf(applied)
				}
				hdest := ""
				if onDisk != "" {
					hdest = hf(onDisk)
				}
				return []rRow{{
					agent: "claude", path: dest(h, "file.md"), cls: cls,
					hsrc: hf(rendered), happlied: happlied, hdest: hdest,
					hasText: true, srcText: rendered, dstText: onDisk,
				}}
			},
			wantE: func(string) eProj {
				return eProj{rows: []eRow{{"claude", "", ownership, cls}}, pathManaged: true}
			},
		}
	}

	// Single-agent, single key-merge fixtures: claude key-merges one server
	// "gh" into dest/settings.json; disk and state vary per class.
	keyMerge := func(name, onDisk string, seedApplied bool, cls, ownership string, dHunk bool, dstText string, foreign []eRow) planFixture {
		return planFixture{
			name:   name,
			target: func(h string) string { return dest(h, "settings.json") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				d := dest(h, "settings.json")
				if onDisk != "" {
					mustWrite(t, d, onDisk)
				}
				s := state.New()
				if seedApplied {
					s.Keys[ptrKey(h, "claude", d, "/mcpServers/gh")] = state.KeyEntry{SHA256: hv("gh"), SourceID: "mcp/* (multiple)"}
				}
				return planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, mcpJSON("gh"))}}), s
			},
			wantS: func(h string) sProj {
				return sProj{
					agents:  []sAgentProj{{agent: "claude", rows: []sRow{{"claude", dest(h, "settings.json"), "/mcpServers/gh", cls}}}},
					summary: map[string]int{cls: 1},
				}
			},
			wantD: func(h string) dProj {
				if !dHunk {
					return dProj{filterMatched: true}
				}
				return dProj{hunks: []diffHunk{{Path: dest(h, "settings.json"), Pointer: "/mcpServers/gh", Source: pretty("gh"), Dest: dstText}}, filterMatched: true}
			},
			wantR: func(h string) []rRow {
				happlied := ""
				if seedApplied {
					happlied = hv("gh")
				}
				hdest := ""
				if dstText != "<absent>" {
					hdest = hv("gh")
				}
				return []rRow{{
					agent: "claude", path: dest(h, "settings.json"), ptr: "/mcpServers/gh", cls: cls,
					hsrc: hv("gh"), happlied: happlied, hdest: hdest,
					hasText: true, srcText: pretty("gh"), dstText: dstText,
				}}
			},
			wantE: func(string) eProj {
				rows := append([]eRow{{"claude", "/mcpServers/gh", ownership, cls}}, foreign...)
				return eProj{rows: rows, pathManaged: true}
			},
		}
	}

	return []planFixture{
		// T-01..T-07: the seven non-orphan classes for a whole file, straight
		// from drift.Classify's table (hsrc, happlied, hdest).
		wholeFile("whole-file/clean", "SOURCE", "SOURCE", "SOURCE", "clean", "managed", false),
		wholeFile("whole-file/pending", "APPLIED", "APPLIED", "SOURCE2", "pending", "managed", true),
		wholeFile("whole-file/drift", "EDITED", "SOURCE", "SOURCE", "drift", "managed", true),
		wholeFile("whole-file/conflict", "EDITED", "APPLIED", "SOURCE", "conflict", "managed", true),
		wholeFile("whole-file/converged", "SOURCE", "APPLIED", "SOURCE", "converged", "managed", false),
		// T-06: absent dest, no state → new; diff shows the whole source against "".
		wholeFile("whole-file/new", "", "", "SOURCE", "new", "untracked", true),
		// T-07: a pre-existing native file, never applied → foreign-collision.
		wholeFile("whole-file/foreign-collision", "FOREIGN", "", "SOURCE", "foreign-collision", "untracked", true),

		// T-08: an orphan (state-owned, no op renders it) beside a kept file.
		// status lists it after the agent's rendered items; reconcile lists it
		// in the orphan half with NO text; diff and explain never see it.
		orphanFixture("whole-file/orphan", "APPLIED", "orphan"),
		orphanFixture("whole-file/orphan-drifted", "EDITED", "orphan-drifted"),

		// T-09: content clean, permission bits drifted. Three answers today:
		// status folds RECORDED-mode drift into `drift`; diff emits a "mode"
		// hunk against op.Mode; reconcile and explain ignore mode entirely.
		// recordedMode (0755) != op.Mode (0700) != on disk (0644), so a walk
		// that swapped the recorded mode for op.Mode would still be caught.
		{
			name:   "whole-file/mode-drift-only",
			target: func(h string) string { return dest(h, "run.sh") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				d := dest(h, "run.sh")
				mustWrite(t, d, "SOURCE")
				if err := os.Chmod(d, 0o644); err != nil {
					t.Fatal(err)
				}
				s := state.New()
				s.Files[fileKey(h, "claude", d)] = state.FileEntry{SHA256: hf("SOURCE"), Mode: 0o755, SourceID: "skills/x/run.sh"}
				op := fileOp(d, "SOURCE")
				op.Mode = 0o700
				op.SourceID = "skills/x/run.sh"
				return planFor(map[string][]adapter.FileOp{"claude": {op}}), s
			},
			wantS: func(h string) sProj {
				return sProj{
					agents:  []sAgentProj{{agent: "claude", rows: []sRow{{"claude", dest(h, "run.sh"), "", "drift"}}}},
					summary: map[string]int{"drift": 1},
				}
			},
			wantD: func(h string) dProj {
				return dProj{hunks: []diffHunk{{Path: dest(h, "run.sh"), Pointer: "mode", Source: "mode 0700", Dest: "mode 0644"}}, filterMatched: true}
			},
			wantR: func(h string) []rRow {
				return []rRow{{
					agent: "claude", path: dest(h, "run.sh"), cls: "clean",
					hsrc: hf("SOURCE"), happlied: hf("SOURCE"), hdest: hf("SOURCE"),
					hasText: true, srcText: "SOURCE", dstText: "SOURCE",
				}}
			},
			wantE: func(string) eProj {
				return eProj{rows: []eRow{{"claude", "", "managed", "clean"}}, pathManaged: true}
			},
		},

		// T-10: the destination is a symlink to an identical file. The HASH side
		// (status/reconcile/explain) answers the symlink sentinel → drift; the
		// TEXT side (diff, reconcile's dstText) reads THROUGH the link → equal.
		// That split is #229 axis 9 and is preserved as-is. explain's target is
		// the link's TARGET: explain resolves op paths through symlinks.
		{
			name:   "whole-file/dest-is-symlink",
			target: func(h string) string { return dest(h, "real.md") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				real := dest(h, "real.md")
				mustWrite(t, real, "SOURCE")
				link := dest(h, "link.md")
				if err := os.Symlink(real, link); err != nil {
					t.Fatal(err)
				}
				s := state.New()
				s.Files[fileKey(h, "claude", link)] = state.FileEntry{SHA256: hf("SOURCE"), SourceID: "memory/AGENTS.md"}
				return planFor(map[string][]adapter.FileOp{"claude": {fileOp(link, "SOURCE")}}), s
			},
			wantS: func(h string) sProj {
				return sProj{
					agents:  []sAgentProj{{agent: "claude", rows: []sRow{{"claude", dest(h, "link.md"), "", "drift"}}}},
					summary: map[string]int{"drift": 1},
				}
			},
			wantD: func(string) dProj { return dProj{filterMatched: true} },
			wantR: func(h string) []rRow {
				return []rRow{{
					agent: "claude", path: dest(h, "link.md"), cls: "drift",
					hsrc: hf("SOURCE"), happlied: hf("SOURCE"), hdest: "symlink-not-regular-file",
					hasText: true, srcText: "SOURCE", dstText: "SOURCE",
				}}
			},
			wantE: func(string) eProj {
				return eProj{rows: []eRow{{"claude", "", "managed", "drift"}}, pathManaged: true}
			},
			extra: func(t *testing.T, _ string, s sProj, d dProj, r []rRow, _ eProj) {
				t.Helper()
				// D2's split, asserted by name so the intent survives a golden edit.
				if r[0].hdest != "symlink-not-regular-file" || r[0].cls != "drift" || s.agents[0].rows[0].cls != "drift" {
					t.Errorf("hash side must answer the symlink sentinel and classify drift: %+v", r[0])
				}
				if r[0].srcText != r[0].dstText || len(d.hunks) != 0 {
					t.Errorf("text side must read through the link and produce no hunk: r=%+v d=%+v", r[0], d)
				}
			},
		},

		// T-11: claude renders the SAME path twice (deduped per agent, once), and
		// opencode renders it too (NOT deduped across agents). Disk drifted so
		// every surface yields something to count.
		{
			name:   "whole-file/two-agents-same-path",
			target: func(h string) string { return dest(h, "AGENTS.md") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				d := dest(h, "AGENTS.md")
				mustWrite(t, d, "EDITED")
				s := state.New()
				for _, a := range []string{"claude", "opencode"} {
					s.Files[fileKey(h, a, d)] = state.FileEntry{SHA256: hf("SHARED"), SourceID: "memory/AGENTS.md"}
				}
				return planFor(map[string][]adapter.FileOp{
					"claude":   {fileOp(d, "SHARED"), fileOp(d, "SHARED")},
					"opencode": {fileOp(d, "SHARED")},
				}), s
			},
			wantS: func(h string) sProj {
				return sProj{
					agents: []sAgentProj{
						{agent: "claude", rows: []sRow{{"claude", dest(h, "AGENTS.md"), "", "drift"}}},
						{agent: "opencode", rows: []sRow{{"opencode", dest(h, "AGENTS.md"), "", "drift"}}},
					},
					summary: map[string]int{"drift": 2},
				}
			},
			wantD: func(h string) dProj {
				return dProj{hunks: []diffHunk{
					{Path: dest(h, "AGENTS.md"), Source: "SHARED", Dest: "EDITED"},
					{Path: dest(h, "AGENTS.md"), Source: "SHARED", Dest: "EDITED"},
				}, filterMatched: true}
			},
			wantR: func(h string) []rRow {
				row := func(a string) rRow {
					return rRow{
						agent: a, path: dest(h, "AGENTS.md"), cls: "drift",
						hsrc: hf("SHARED"), happlied: hf("SHARED"), hdest: hf("EDITED"),
						hasText: true, srcText: "SHARED", dstText: "EDITED",
					}
				}
				return []rRow{row("claude"), row("opencode")}
			},
			wantE: func(string) eProj {
				return eProj{rows: []eRow{{"claude", "", "managed", "drift"}, {"opencode", "", "managed", "drift"}}, pathManaged: true}
			},
		},

		// T-12: two key-merge ops to ONE path (claude's /mcpServers and
		// /lspServers both land in settings.json). Never deduped by path: both
		// sections' pointers must appear. The lsp key is drifted on disk.
		{
			name:   "key-merge/two-ops-one-path",
			target: func(h string) string { return dest(h, "settings.json") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				d := dest(h, "settings.json")
				mustWrite(t, d, `{"mcpServers":{"gh":{"command":"gh"}},"lspServers":{"gopls":{"command":"gopls2"}}}`)
				s := state.New()
				s.Keys[ptrKey(h, "claude", d, "/mcpServers/gh")] = state.KeyEntry{SHA256: hv("gh"), SourceID: "mcp/* (multiple)"}
				s.Keys[ptrKey(h, "claude", d, "/lspServers/gopls")] = state.KeyEntry{SHA256: hv("gopls"), SourceID: "lsp/* (multiple)"}
				lsp := keyOp(d, `{"lspServers":{"gopls":{"command":"gopls"}}}`)
				lsp.SourceID = "lsp/* (multiple)"
				return planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, mcpJSON("gh")), lsp}}), s
			},
			wantS: func(h string) sProj {
				return sProj{
					agents: []sAgentProj{{agent: "claude", rows: []sRow{
						{"claude", dest(h, "settings.json"), "/lspServers/gopls", "drift"},
						{"claude", dest(h, "settings.json"), "/mcpServers/gh", "clean"},
					}}},
					summary: map[string]int{"clean": 1, "drift": 1},
				}
			},
			wantD: func(h string) dProj {
				return dProj{hunks: []diffHunk{
					{Path: dest(h, "settings.json"), Pointer: "/lspServers/gopls", Source: pretty("gopls"), Dest: pretty("gopls2")},
				}, filterMatched: true}
			},
			wantR: func(h string) []rRow {
				return []rRow{
					{
						agent: "claude", path: dest(h, "settings.json"), ptr: "/lspServers/gopls", cls: "drift",
						hsrc: hv("gopls"), happlied: hv("gopls"), hdest: hv("gopls2"),
						hasText: true, srcText: pretty("gopls"), dstText: pretty("gopls2"),
					},
					{
						agent: "claude", path: dest(h, "settings.json"), ptr: "/mcpServers/gh", cls: "clean",
						hsrc: hv("gh"), happlied: hv("gh"), hdest: hv("gh"),
						hasText: true, srcText: pretty("gh"), dstText: pretty("gh"),
					},
				}
			},
			wantE: func(string) eProj {
				return eProj{rows: []eRow{
					{"claude", "/lspServers/gopls", "managed", "drift"},
					{"claude", "/mcpServers/gh", "managed", "clean"},
				}, pathManaged: true}
			},
			extra: func(t *testing.T, h string, s sProj, _ dProj, r []rRow, _ eProj) {
				t.Helper()
				// Axis 11: the multiset of pointers at the shared path carries BOTH
				// sections. normalizeRuns merges the two ops into one run, so this
				// is asserted explicitly rather than by order.
				want := map[string]int{"/lspServers/gopls": 1, "/mcpServers/gh": 1}
				gotR := map[string]int{}
				for _, it := range r {
					if it.path == dest(h, "settings.json") {
						gotR[it.ptr]++
					}
				}
				gotS := map[string]int{}
				for _, it := range s.agents[0].rows {
					gotS[it.ptr]++
				}
				if !reflect.DeepEqual(gotR, want) || !reflect.DeepEqual(gotS, want) {
					t.Errorf("pointer multiset at the shared path: R=%v S=%v want %v", gotR, gotS, want)
				}
			},
		},

		// T-13: five servers under one op, inserted in non-sorted order; "tango"
		// is absent from disk. Rows are compared in sorted-pointer order (the
		// run normalization), classes per pointer.
		{
			name:   "key-merge/many-pointers",
			target: func(h string) string { return dest(h, "settings.json") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				d := dest(h, "settings.json")
				mustWrite(t, d, mcpJSON("zulu", "mike", "alpha", "bravo"))
				s := state.New()
				for _, id := range []string{"zulu", "mike", "alpha", "tango", "bravo"} {
					s.Keys[ptrKey(h, "claude", d, "/mcpServers/"+id)] = state.KeyEntry{SHA256: hv(id), SourceID: "mcp/* (multiple)"}
				}
				return planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, mcpJSON("zulu", "mike", "alpha", "tango", "bravo"))}}), s
			},
			wantS: func(h string) sProj {
				d := dest(h, "settings.json")
				return sProj{
					agents: []sAgentProj{{agent: "claude", rows: []sRow{
						{"claude", d, "/mcpServers/alpha", "clean"},
						{"claude", d, "/mcpServers/bravo", "clean"},
						{"claude", d, "/mcpServers/mike", "clean"},
						{"claude", d, "/mcpServers/tango", "drift"},
						{"claude", d, "/mcpServers/zulu", "clean"},
					}}},
					summary: map[string]int{"clean": 4, "drift": 1},
				}
			},
			wantD: func(h string) dProj {
				return dProj{hunks: []diffHunk{
					{Path: dest(h, "settings.json"), Pointer: "/mcpServers/tango", Source: pretty("tango"), Dest: "<absent>"},
				}, filterMatched: true}
			},
			wantR: func(h string) []rRow {
				d := dest(h, "settings.json")
				clean := func(id string) rRow {
					return rRow{
						agent: "claude", path: d, ptr: "/mcpServers/" + id, cls: "clean",
						hsrc: hv(id), happlied: hv(id), hdest: hv(id),
						hasText: true, srcText: pretty(id), dstText: pretty(id),
					}
				}
				return []rRow{
					clean("alpha"), clean("bravo"), clean("mike"),
					{
						agent: "claude", path: d, ptr: "/mcpServers/tango", cls: "drift",
						hsrc: hv("tango"), happlied: hv("tango"), hdest: "",
						hasText: true, srcText: pretty("tango"), dstText: "<absent>",
					},
					clean("zulu"),
				}
			},
			wantE: func(string) eProj {
				return eProj{rows: []eRow{
					{"claude", "/mcpServers/alpha", "managed", "clean"},
					{"claude", "/mcpServers/bravo", "managed", "clean"},
					{"claude", "/mcpServers/mike", "managed", "clean"},
					{"claude", "/mcpServers/tango", "managed", "drift"},
					{"claude", "/mcpServers/zulu", "managed", "clean"},
				}, pathManaged: true}
			},
		},

		// T-14: the merged destination does not exist yet → new per key.
		keyMerge("key-merge/dest-missing", "", false, "new", "untracked", true, "<absent>", nil),
		// T-15: a hand-commented JSONC destination with trailing commas decodes
		// (hujson), so the owned key is clean; its foreign sibling is reported by
		// explain as first-class.
		keyMerge("key-merge/dest-is-JSONC-with-comments",
			"// managed by agentsync\n{\n  \"mcpServers\": {\n    \"gh\": {\"command\": \"gh\"},\n  },\n  \"other\": {\"x\": 1},\n}\n",
			true, "clean", "managed", false, pretty("gh"), []eRow{{"claude", "/other/x", "foreign", ""}}),
		// T-16: an unparseable destination decodes to an EMPTY document, so the
		// owned key classifies against an absent value (drift, not conflict, not
		// a file-level item), and explain reports no foreign keys.
		keyMerge("key-merge/dest-unparseable", "{not json", true, "drift", "managed", true, "<absent>", nil),

		// T-17: op.Content that is not JSON yields no pointers → zero items, but
		// the agent still has a status entry and the path still counts as
		// managed for diff's filter and explain.
		{
			name:       "key-merge/op-content-not-json",
			diffFilter: func(h string) string { return dest(h, "settings.json") },
			target:     func(h string) string { return dest(h, "settings.json") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				d := dest(h, "settings.json")
				mustWrite(t, d, mcpJSON("gh"))
				return planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, "not json")}}), state.New()
			},
			wantS: func(string) sProj {
				return sProj{agents: []sAgentProj{{agent: "claude"}}, summary: map[string]int{}}
			},
			wantD: func(string) dProj { return dProj{filterMatched: true} },
			wantR: func(string) []rRow { return nil },
			wantE: func(string) eProj { return eProj{unmanaged: true, pathManaged: true} },
		},

		// T-18: a server id containing '/' and '~' is RFC-6901-escaped in the
		// pointer and decoded again on lookup, so it classifies clean rather
		// than as phantom drift. explain is narrowed to that pointer.
		{
			name:    "key-merge/pointer-id-with-slash-and-tilde",
			target:  func(h string) string { return dest(h, "settings.json") },
			pointer: "/mcpServers/a~1b~0c",
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				d := dest(h, "settings.json")
				mustWrite(t, d, mcpJSON("a/b~c"))
				s := state.New()
				s.Keys[ptrKey(h, "claude", d, "/mcpServers/a~1b~0c")] = state.KeyEntry{SHA256: hv("a/b~c"), SourceID: "mcp/* (multiple)"}
				return planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, mcpJSON("a/b~c"))}}), s
			},
			wantS: func(h string) sProj {
				return sProj{
					agents:  []sAgentProj{{agent: "claude", rows: []sRow{{"claude", dest(h, "settings.json"), "/mcpServers/a~1b~0c", "clean"}}}},
					summary: map[string]int{"clean": 1},
				}
			},
			wantD: func(string) dProj { return dProj{filterMatched: true} },
			wantR: func(h string) []rRow {
				return []rRow{{
					agent: "claude", path: dest(h, "settings.json"), ptr: "/mcpServers/a~1b~0c", cls: "clean",
					hsrc: hv("a/b~c"), happlied: hv("a/b~c"), hdest: hv("a/b~c"),
					hasText: true, srcText: pretty("a/b~c"), dstText: pretty("a/b~c"),
				}}
			},
			wantE: func(string) eProj {
				return eProj{rows: []eRow{{"claude", "/mcpServers/a~1b~0c", "managed", "clean"}}, pathManaged: true}
			},
		},

		// T-19: claude owns P in state but no longer renders it; opencode still
		// does. status shows claude's ownership view (P is an orphan for claude);
		// reconcile EXCLUDES the orphan because another agent renders the path.
		{
			name:   "orphan/shared-dest-other-agent-renders",
			target: func(h string) string { return dest(h, "AGENTS.md") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				p := dest(h, "AGENTS.md")
				k := dest(h, "CLAUDE.md")
				mustWrite(t, p, "SHARED")
				mustWrite(t, k, "KEPT")
				s := state.New()
				s.Files[fileKey(h, "claude", p)] = state.FileEntry{SHA256: hf("SHARED"), SourceID: "memory/AGENTS.md"}
				s.Files[fileKey(h, "claude", k)] = state.FileEntry{SHA256: hf("KEPT"), SourceID: "memory/AGENTS.md"}
				s.Files[fileKey(h, "opencode", p)] = state.FileEntry{SHA256: hf("SHARED"), SourceID: "memory/AGENTS.md"}
				return planFor(map[string][]adapter.FileOp{
					"claude":   {fileOp(k, "KEPT")},
					"opencode": {fileOp(p, "SHARED")},
				}), s
			},
			wantS: func(h string) sProj {
				return sProj{
					agents: []sAgentProj{
						{agent: "claude", rows: []sRow{
							{"claude", dest(h, "CLAUDE.md"), "", "clean"},
							{"claude", dest(h, "AGENTS.md"), "", "orphan"},
						}},
						{agent: "opencode", rows: []sRow{{"opencode", dest(h, "AGENTS.md"), "", "clean"}}},
					},
					summary: map[string]int{"clean": 2, "orphan": 1},
				}
			},
			wantD: func(string) dProj { return dProj{filterMatched: true} },
			wantR: func(h string) []rRow {
				return []rRow{
					{
						agent: "claude", path: dest(h, "CLAUDE.md"), cls: "clean",
						hsrc: hf("KEPT"), happlied: hf("KEPT"), hdest: hf("KEPT"),
						hasText: true, srcText: "KEPT", dstText: "KEPT",
					},
					{
						agent: "opencode", path: dest(h, "AGENTS.md"), cls: "clean",
						hsrc: hf("SHARED"), happlied: hf("SHARED"), hdest: hf("SHARED"),
						hasText: true, srcText: "SHARED", dstText: "SHARED",
					},
				}
			},
			wantE: func(string) eProj {
				return eProj{rows: []eRow{{"opencode", "", "managed", "clean"}}, pathManaged: true}
			},
		},

		// T-20: an empty plan (the one fixture allowed to project nothing at
		// all), and an agent with a plan result but no ops (a status skeleton
		// entry with zero items).
		{
			name:   "plan/empty",
			target: func(h string) string { return dest(h, "file.md") },
			setup: func(_ *testing.T, _ string) (render.RenderPlan, *state.Targets) {
				return render.RenderPlan{PerAgent: map[string]render.AgentResult{}}, state.New()
			},
			wantS: func(string) sProj { return sProj{summary: map[string]int{}} },
			wantD: func(string) dProj { return dProj{filterMatched: true} },
			wantR: func(string) []rRow { return nil },
			wantE: func(string) eProj { return eProj{unmanaged: true} },
		},
		{
			name:   "plan/agent-with-no-ops",
			target: func(h string) string { return dest(h, "file.md") },
			setup: func(_ *testing.T, _ string) (render.RenderPlan, *state.Targets) {
				return planFor(map[string][]adapter.FileOp{"claude": nil}), state.New()
			},
			wantS: func(string) sProj {
				return sProj{agents: []sAgentProj{{agent: "claude"}}, summary: map[string]int{}}
			},
			wantD: func(string) dProj { return dProj{filterMatched: true} },
			wantR: func(string) []rRow { return nil },
			wantE: func(string) eProj { return eProj{unmanaged: true} },
		},

		// T-21: project scope. State keys are scoped, so a user-scope entry for
		// a different path is neither the project file's record nor an orphan
		// of the project walk.
		{
			name:        "scope/project",
			scope:       adapter.ScopeProject,
			projectRoot: func(h string) string { return filepath.Join(h, "proj") },
			target:      func(h string) string { return filepath.Join(h, "proj", "CLAUDE.md") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				root := filepath.Join(h, "proj")
				d := filepath.Join(root, "CLAUDE.md")
				mustWrite(t, d, "SOURCE")
				s := state.New()
				s.Files[stateFileKey(h, "claude", adapter.ScopeProject, root, d)] = state.FileEntry{SHA256: hf("SOURCE"), SourceID: "memory/AGENTS.md"}
				// A USER-scope entry for another path: out of this walk's tree.
				s.Files[fileKey(h, "claude", dest(h, "user-only.md"))] = state.FileEntry{SHA256: hf("USER"), SourceID: "memory/AGENTS.md"}
				return planFor(map[string][]adapter.FileOp{"claude": {fileOp(d, "SOURCE")}}), s
			},
			wantS: func(h string) sProj {
				return sProj{
					agents:  []sAgentProj{{agent: "claude", rows: []sRow{{"claude", filepath.Join(h, "proj", "CLAUDE.md"), "", "clean"}}}},
					summary: map[string]int{"clean": 1},
				}
			},
			wantD: func(string) dProj { return dProj{filterMatched: true} },
			wantR: func(h string) []rRow {
				return []rRow{{
					agent: "claude", path: filepath.Join(h, "proj", "CLAUDE.md"), cls: "clean",
					hsrc: hf("SOURCE"), happlied: hf("SOURCE"), hdest: hf("SOURCE"),
					hasText: true, srcText: "SOURCE", dstText: "SOURCE",
				}}
			},
			wantE: func(string) eProj {
				return eProj{rows: []eRow{{"claude", "", "managed", "clean"}}, pathManaged: true}
			},
		},

		// T-22: a key-merge op BEFORE a whole-file op in plan order, both
		// drifted. status re-partitions whole-file rows ahead of key rows; diff
		// and reconcile keep plan order.
		{
			name:   "order/key-merge-op-before-whole-file",
			target: func(h string) string { return dest(h, "MEM.md") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				sj := dest(h, "settings.json")
				mem := dest(h, "MEM.md")
				mustWrite(t, sj, `{"mcpServers":{"x":{"command":"npm"}}}`)
				mustWrite(t, mem, "EDITED")
				s := state.New()
				s.Keys[ptrKey(h, "claude", sj, "/mcpServers/x")] = state.KeyEntry{SHA256: hv("npx"), SourceID: "mcp/* (multiple)"}
				s.Files[fileKey(h, "claude", mem)] = state.FileEntry{SHA256: hf("SOURCE"), SourceID: "memory/AGENTS.md"}
				return planFor(map[string][]adapter.FileOp{"claude": {
					keyOp(sj, `{"mcpServers":{"x":{"command":"npx"}}}`),
					fileOp(mem, "SOURCE"),
				}}), s
			},
			wantS: func(h string) sProj {
				return sProj{
					agents: []sAgentProj{{agent: "claude", rows: []sRow{
						{"claude", dest(h, "MEM.md"), "", "drift"},
						{"claude", dest(h, "settings.json"), "/mcpServers/x", "drift"},
					}}},
					summary: map[string]int{"drift": 2},
				}
			},
			wantD: func(h string) dProj {
				return dProj{hunks: []diffHunk{
					{Path: dest(h, "settings.json"), Pointer: "/mcpServers/x", Source: pretty("npx"), Dest: pretty("npm")},
					{Path: dest(h, "MEM.md"), Source: "SOURCE", Dest: "EDITED"},
				}, filterMatched: true}
			},
			wantR: func(h string) []rRow {
				return []rRow{
					{
						agent: "claude", path: dest(h, "settings.json"), ptr: "/mcpServers/x", cls: "drift",
						hsrc: hv("npx"), happlied: hv("npx"), hdest: hv("npm"),
						hasText: true, srcText: pretty("npx"), dstText: pretty("npm"),
					},
					{
						agent: "claude", path: dest(h, "MEM.md"), cls: "drift",
						hsrc: hf("SOURCE"), happlied: hf("SOURCE"), hdest: hf("EDITED"),
						hasText: true, srcText: "SOURCE", dstText: "EDITED",
					},
				}
			},
			wantE: func(string) eProj {
				return eProj{rows: []eRow{{"claude", "", "managed", "drift"}}, pathManaged: true}
			},
		},

		// T-23: claude has a plan result with no ops and a state-owned orphan P;
		// opencode renders Q. reconcile lists ALL rendered items before ALL
		// orphans (Q then P) even though claude sorts first; status keeps per
		// agent grouping. P matches no op, so diff's filter misses and explain
		// calls it unmanaged.
		{
			name:       "order/two-agents-orphan-then-ops",
			diffFilter: func(h string) string { return dest(h, "P.md") },
			target:     func(h string) string { return dest(h, "P.md") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				p := dest(h, "P.md")
				q := dest(h, "Q.md")
				mustWrite(t, p, "APPLIED")
				mustWrite(t, q, "SOURCE")
				s := state.New()
				s.Files[fileKey(h, "claude", p)] = state.FileEntry{SHA256: hf("APPLIED"), SourceID: "memory/AGENTS.md"}
				s.Files[fileKey(h, "opencode", q)] = state.FileEntry{SHA256: hf("SOURCE"), SourceID: "memory/AGENTS.md"}
				return planFor(map[string][]adapter.FileOp{
					"claude":   nil,
					"opencode": {fileOp(q, "SOURCE")},
				}), s
			},
			wantS: func(h string) sProj {
				return sProj{
					agents: []sAgentProj{
						{agent: "claude", rows: []sRow{{"claude", dest(h, "P.md"), "", "orphan"}}},
						{agent: "opencode", rows: []sRow{{"opencode", dest(h, "Q.md"), "", "clean"}}},
					},
					summary: map[string]int{"clean": 1, "orphan": 1},
				}
			},
			wantD: func(string) dProj { return dProj{filterMatched: false} },
			wantR: func(h string) []rRow {
				return []rRow{
					{
						agent: "opencode", path: dest(h, "Q.md"), cls: "clean",
						hsrc: hf("SOURCE"), happlied: hf("SOURCE"), hdest: hf("SOURCE"),
						hasText: true, srcText: "SOURCE", dstText: "SOURCE",
					},
					{
						agent: "claude", path: dest(h, "P.md"), cls: "orphan",
						hsrc: "", happlied: hf("APPLIED"), hdest: hf("APPLIED"),
						orphan: true,
					},
				}
			},
			wantE: func(string) eProj { return eProj{unmanaged: true, pathManaged: false} },
		},

		// T-24: a key-merge op whose Content is "{}" — the shape render.Plan
		// synthesizes to clean up an emptied section — against a destination
		// that still carries foreign keys there. It yields ZERO items, yet the
		// path matched an op: diff's filterMatched and explain's pathManaged are
		// side effects of the match, not of the item count (#229 amendment A3).
		{
			name:       "key-merge/emptied-section",
			diffFilter: func(h string) string { return dest(h, "settings.json") },
			target:     func(h string) string { return dest(h, "settings.json") },
			setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
				t.Helper()
				d := dest(h, "settings.json")
				mustWrite(t, d, `{"mcpServers":{"foreign":{"command":"x"}}}`)
				return planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, "{}")}}), state.New()
			},
			wantS: func(string) sProj {
				return sProj{agents: []sAgentProj{{agent: "claude"}}, summary: map[string]int{}}
			},
			wantD: func(string) dProj { return dProj{filterMatched: true} },
			wantR: func(string) []rRow { return nil },
			// Zero owners: an owner is appended only when it has items, and
			// foreign rows attach only to owners — so none are reported.
			wantE: func(string) eProj { return eProj{unmanaged: true, pathManaged: true} },
		},
	}
}

// orphanFixture is T-08's shape (from iss155_internal_test.go): claude renders
// kept.md (clean) and still owns orphan.md in state, which no op renders; the
// orphan's on-disk content decides orphan vs orphan-drifted.
func orphanFixture(name, onDisk, cls string) planFixture {
	return planFixture{
		name:   name,
		target: func(h string) string { return dest(h, "orphan.md") },
		setup: func(t *testing.T, h string) (render.RenderPlan, *state.Targets) {
			t.Helper()
			orphan := dest(h, "orphan.md")
			kept := dest(h, "kept.md")
			mustWrite(t, orphan, onDisk)
			mustWrite(t, kept, "KEPT")
			s := state.New()
			s.Files[fileKey(h, "claude", orphan)] = state.FileEntry{SHA256: hf("APPLIED"), SourceID: "memory/AGENTS.md"}
			s.Files[fileKey(h, "claude", kept)] = state.FileEntry{SHA256: hf("KEPT"), SourceID: "memory/AGENTS.md"}
			return planFor(map[string][]adapter.FileOp{"claude": {fileOp(kept, "KEPT")}}), s
		},
		wantS: func(h string) sProj {
			return sProj{
				agents: []sAgentProj{{agent: "claude", rows: []sRow{
					{"claude", dest(h, "kept.md"), "", "clean"},
					{"claude", dest(h, "orphan.md"), "", cls},
				}}},
				summary: map[string]int{"clean": 1, cls: 1},
			}
		},
		wantD: func(string) dProj { return dProj{filterMatched: true} },
		wantR: func(h string) []rRow {
			return []rRow{
				{
					agent: "claude", path: dest(h, "kept.md"), cls: "clean",
					hsrc: hf("KEPT"), happlied: hf("KEPT"), hdest: hf("KEPT"),
					hasText: true, srcText: "KEPT", dstText: "KEPT",
				},
				{
					agent: "claude", path: dest(h, "orphan.md"), cls: cls,
					hsrc: "", happlied: hf("APPLIED"), hdest: hf(onDisk),
					orphan: true,
				},
			}
		},
		// explain never sees an orphan: no op renders the path.
		wantE: func(string) eProj { return eProj{unmanaged: true, pathManaged: false} },
	}
}

// ---- projections --------------------------------------------------------------

func projectS(m statusModel) sProj {
	out := sProj{summary: m.Summary}
	for _, ag := range m.Agents {
		a := sAgentProj{agent: ag.Agent}
		for _, it := range ag.Items {
			a.rows = append(a.rows, sRow{ag.Agent, it.Path, it.Pointer, it.Class})
		}
		a.rows = normalizeRuns(a.rows, func(r sRow) (string, string, string) { return r.agent, r.path, r.ptr })
		out.agents = append(out.agents, a)
	}
	return out
}

func projectD(hunks []diffHunk, matched bool) dProj {
	// A "mode" hunk is a whole-file row wearing a label, not a pointer; keep it
	// out of the run sort so its placement stays asserted in order.
	hunks = normalizeRuns(hunks, func(h diffHunk) (string, string, string) {
		if h.Pointer == "mode" {
			return "", h.Path, ""
		}
		return "", h.Path, h.Pointer
	})
	return dProj{hunks: hunks, filterMatched: matched}
}

func projectR(items []reconcileItem) []rRow {
	var out []rRow
	for _, it := range items {
		out = append(out, rRow{
			agent: it.agentName, path: it.op.Path, ptr: it.ptr, cls: it.cls.String(),
			hsrc: it.hsrc, happlied: it.happlied, hdest: it.hdest,
			orphan: it.orphan, hasText: it.hasText, srcText: it.srcText, dstText: it.dstText,
		})
	}
	return normalizeRuns(out, func(r rRow) (string, string, string) { return r.agent, r.path, r.ptr })
}

func projectE(m explainModel) eProj {
	var rows []eRow
	for _, o := range m.Owners {
		for _, it := range o.Items {
			rows = append(rows, eRow{o.Agent, it.Pointer, it.Ownership, it.Drift})
		}
	}
	rows = normalizeRuns(rows, func(r eRow) (string, string, string) { return r.agent, m.Path, r.ptr })
	return eProj{rows: rows, unmanaged: m.Unmanaged, pathManaged: m.pathManaged}
}

// TestPlanWalkCharacterization runs every fixture through the four builders
// and compares each projection to its golden. The four calls below mirror the
// production call sites verbatim: reg.Names() as the agent order everywhere,
// and reconcile's `collectItems(...) ++ collectOrphanFileItems(...)`
// composition (reconcile.go, reconcileRun).
func TestPlanWalkCharacterization(t *testing.T) {
	reg := registryFactory()
	for _, tc := range planFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			// explain resolves op paths through symlinks before matching, so the
			// fixture root must already be its own resolved form.
			userHome, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			sc, root := adapter.ScopeUser, ""
			if tc.projectRoot != nil {
				sc, root = tc.scope, tc.projectRoot(userHome)
			}
			plan, s := tc.setup(t, userHome)

			gotS := projectS(buildStatusModel(plan, reg.Names(), s, userHome, sc, root))

			filter := ""
			if tc.diffFilter != nil {
				filter = tc.diffFilter(userHome)
			}
			gotD := projectD(collectDiffHunks(plan, reg.Names(), filter, nil))

			items := collectItems(plan, reg, s, sc, root, userHome, nil)
			items = append(items, collectOrphanFileItems(plan, reg, s, sc, root, userHome)...)
			gotR := projectR(items)

			gotE := projectE(buildExplainModel(explainInputs{
				fs:            afero.NewMemMapFs(),
				target:        tc.target(userHome),
				pointer:       tc.pointer,
				plan:          plan,
				agents:        reg.Names(),
				state:         s,
				userHome:      userHome,
				agentsyncHome: filepath.Join(userHome, ".agentsync"),
				srcHome:       filepath.Join(userHome, ".agentsync"),
				scope:         sc,
				projectRoot:   root,
			}))

			// Vacuity guard: a fixture that projects nothing on S, D and R
			// characterizes nothing. Only the empty plan may.
			if tc.name != "plan/empty" && len(gotS.agents) == 0 && len(gotD.hunks) == 0 && len(gotR) == 0 {
				t.Fatalf("fixture projects nothing on S, D and R — it pins no behaviour")
			}

			if want := tc.wantS(userHome); !reflect.DeepEqual(gotS, want) {
				t.Errorf("S (status) projection mismatch\n got: %+v\nwant: %+v", gotS, want)
			}
			if want := tc.wantD(userHome); !reflect.DeepEqual(gotD, want) {
				t.Errorf("D (diff) projection mismatch\n got: %+v\nwant: %+v", gotD, want)
			}
			if want := tc.wantR(userHome); !reflect.DeepEqual(gotR, want) {
				t.Errorf("R (reconcile) projection mismatch\n got: %+v\nwant: %+v", gotR, want)
			}
			if want := tc.wantE(userHome); !reflect.DeepEqual(gotE, want) {
				t.Errorf("E (explain) projection mismatch\n got: %+v\nwant: %+v", gotE, want)
			}
			if tc.extra != nil {
				tc.extra(t, userHome, gotS, gotD, gotR, gotE)
			}
		})
	}
}

package cli

import (
	"encoding"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// walkUser runs walkPlanItems at user scope with the fixture defaults the
// characterization harness uses (userHome as the state base, no project).
func walkUser(userHome string, plan render.RenderPlan, s *state.Targets, agents []string, opts func(*planWalk)) []planItem {
	w := planWalk{plan: plan, agents: agents, state: s, userHome: userHome, scope: adapter.ScopeUser}
	if opts != nil {
		opts(&w)
	}
	return walkPlanItems(w)
}

// itemKeys projects the walk output to "agent path#ptr" (with "!" for an
// orphan) so an ORDER assertion reads as one slice comparison.
func itemKeys(items []planItem) []string {
	var out []string
	for _, it := range items {
		k := it.agent + " " + it.op.Path
		if it.ptr != "" {
			k += "#" + it.ptr
		}
		if it.orphan {
			k += "!"
		}
		out = append(out, k)
	}
	return out
}

// TestWalkPlanItems pins the walk's own contract (#229 N-12): agent and op
// order, sorted pointers, per-agent whole-file dedupe vs never-dedupe for
// key-merge ops, orphan placement and filtering, the matchOp side-effect
// contract, the Action filter, the withText fields and the hash/text split.
func TestWalkPlanItems(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, h string)
	}{
		{
			name: "agent-and-op-order",
			run: func(t *testing.T, h string) {
				a, b, c := dest(h, "a.md"), dest(h, "b.md"), dest(h, "c.md")
				plan := planFor(map[string][]adapter.FileOp{
					"claude":   {fileOp(a, "A"), fileOp(b, "B")},
					"opencode": {fileOp(c, "C")},
					"codex":    {fileOp(a, "A")},
				})
				// agents order is the caller's, NOT sorted, and an agent the
				// plan has no result for is skipped.
				got := itemKeys(walkUser(h, plan, state.New(), []string{"opencode", "claude", "cursor"}, nil))
				want := []string{"opencode " + c, "claude " + a, "claude " + b}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("order: got %v want %v", got, want)
				}
			},
		},
		{
			name: "pointers-sorted",
			run: func(t *testing.T, h string) {
				d := dest(h, "settings.json")
				ids := []string{"zulu", "mike", "alpha", "tango", "bravo"}
				plan := planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, mcpJSON(ids...))}})
				// CollectPointers ranges a map, so one lucky run proves nothing;
				// 50 unsorted runs agreeing has probability ~120^-49.
				for i := 0; i < 50; i++ {
					items := walkUser(h, plan, state.New(), []string{"claude"}, nil)
					if len(items) != len(ids) {
						t.Fatalf("run %d: got %d items want %d", i, len(items), len(ids))
					}
					var ptrs []string
					for _, it := range items {
						ptrs = append(ptrs, it.ptr)
					}
					if !sort.StringsAreSorted(ptrs) {
						t.Fatalf("run %d: pointers not sorted: %v", i, ptrs)
					}
				}
			},
		},
		{
			name: "whole-file-deduped-per-agent",
			run: func(t *testing.T, h string) {
				d := dest(h, "AGENTS.md")
				plan := planFor(map[string][]adapter.FileOp{
					"claude":   {fileOp(d, "X"), fileOp(d, "X")},
					"opencode": {fileOp(d, "X")},
				})
				got := itemKeys(walkUser(h, plan, state.New(), []string{"claude", "opencode"}, nil))
				want := []string{"claude " + d, "opencode " + d}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("dedupe: got %v want %v", got, want)
				}
			},
		},
		{
			name: "key-merge-never-deduped",
			run: func(t *testing.T, h string) {
				d := dest(h, "settings.json")
				plan := planFor(map[string][]adapter.FileOp{"claude": {
					keyOp(d, mcpJSON("gh")),
					keyOp(d, `{"lspServers":{"gopls":{"command":"gopls"}}}`),
				}})
				got := itemKeys(walkUser(h, plan, state.New(), []string{"claude"}, nil))
				want := []string{"claude " + d + "#/mcpServers/gh", "claude " + d + "#/lspServers/gopls"}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("both sections must be walked: got %v want %v", got, want)
				}
			},
		},
		{
			name: "orphans-follow-their-agent",
			run: func(t *testing.T, h string) {
				a, p, b := dest(h, "a.md"), dest(h, "p.md"), dest(h, "b.md")
				mustWrite(t, p, "P")
				s := state.New()
				s.Files[fileKey(h, "claude", p)] = state.FileEntry{SHA256: hf("P"), SourceID: "skills/x/SKILL.md"}
				plan := planFor(map[string][]adapter.FileOp{
					"claude":   {fileOp(a, "A")},
					"opencode": {fileOp(b, "B")},
				})
				items := walkUser(h, plan, s, []string{"claude", "opencode"}, func(w *planWalk) { w.includeOrphans = true })
				got := itemKeys(items)
				want := []string{"claude " + a, "claude " + p + "!", "opencode " + b}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("orphan placement: got %v want %v", got, want)
				}
				o := items[1]
				if o.op.Action != "delete" || o.op.SourceID != "skills/x/SKILL.md" || o.op.Mode != 0 ||
					o.cls != drift.Orphan || o.hsrc != "" || o.happlied != hf("P") || o.hdest != hf("P") {
					t.Errorf("orphan item: %+v", o)
				}
				// Without includeOrphans the orphan is not yielded at all.
				if got := itemKeys(walkUser(h, plan, s, []string{"claude", "opencode"}, nil)); len(got) != 2 {
					t.Errorf("includeOrphans=false must yield ops only: %v", got)
				}
			},
		},
		{
			name: "orphans-are-not-cross-agent-filtered",
			run: func(t *testing.T, h string) {
				p := dest(h, "AGENTS.md")
				mustWrite(t, p, "SHARED")
				s := state.New()
				s.Files[fileKey(h, "claude", p)] = state.FileEntry{SHA256: hf("SHARED")}
				plan := planFor(map[string][]adapter.FileOp{
					"claude":   nil,
					"opencode": {fileOp(p, "SHARED")},
				})
				got := itemKeys(walkUser(h, plan, s, []string{"claude", "opencode"}, func(w *planWalk) { w.includeOrphans = true }))
				// opencode still renders p; the walk yields claude's orphan
				// anyway — that exclusion is reconcile's, not the walk's.
				want := []string{"claude " + p + "!", "opencode " + p}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("orphan set must be per agent and unfiltered: got %v want %v", got, want)
				}
			},
		},
		{
			name: "orphan-ignores-mode",
			run: func(t *testing.T, h string) {
				p := dest(h, "orphan.sh")
				mustWrite(t, p, "APPLIED")
				if err := os.Chmod(p, 0o644); err != nil {
					t.Fatal(err)
				}
				s := state.New()
				s.Files[fileKey(h, "claude", p)] = state.FileEntry{SHA256: hf("APPLIED"), Mode: 0o755}
				plan := planFor(map[string][]adapter.FileOp{"claude": nil})
				items := walkUser(h, plan, s, []string{"claude"}, func(w *planWalk) { w.includeOrphans = true })
				if len(items) != 1 || !items[0].orphan {
					t.Fatalf("want one orphan item, got %+v", items)
				}
				// The mode facts ARE populated (recorded 0755, disk 0644 → the
				// predicate would say drift)…
				if !items[0].recordedModeDrifted() {
					t.Fatalf("fixture: orphan mode facts not populated: %+v", items[0])
				}
				// …and the status projection must not consult them: an orphan's
				// class is the classifier's, untouched by the chmod.
				model := buildStatusModel(plan, []string{"claude"}, s, h, adapter.ScopeUser, "")
				if got := model.Summary; got["orphan"] != 1 || got["drift"] != 0 {
					t.Errorf("status must leave an orphan's class alone under a chmod: summary=%v", got)
				}
			},
		},
		{
			name: "matchOp-filters-and-is-called-once-per-op",
			run: func(t *testing.T, h string) {
				a, b, p := dest(h, "a.md"), dest(h, "b.md"), dest(h, "p.md")
				mustWrite(t, p, "P")
				s := state.New()
				s.Files[fileKey(h, "claude", p)] = state.FileEntry{SHA256: hf("P")}
				del := fileOp(b, "B")
				del.Action = "delete"
				plan := planFor(map[string][]adapter.FileOp{
					"claude":   {fileOp(a, "A"), fileOp(a, "A"), fileOp(b, "B"), del, keyOp(b, "{}")},
					"opencode": {fileOp(b, "B")},
				})
				var calls []string
				items := walkUser(h, plan, s, []string{"claude", "opencode"}, func(w *planWalk) {
					w.includeOrphans = true
					w.matchOp = func(agent string, op adapter.FileOp) bool {
						calls = append(calls, agent+" "+op.Path)
						return op.Path == a
					}
				})
				// Once per op that survives the Action filter, in walk order,
				// with the agent name; the "delete" op never reaches it, and the
				// duplicate whole-file op at `a` is offered BEFORE the per-agent
				// path dedupe drops it.
				wantCalls := []string{"claude " + a, "claude " + a, "claude " + b, "claude " + b, "opencode " + b}
				if !reflect.DeepEqual(calls, wantCalls) {
					t.Errorf("matchOp calls: got %v want %v", calls, wantCalls)
				}
				// Only the accepted op yields; the orphan is NOT subject to
				// matchOp and is still yielded.
				got := itemKeys(items)
				want := []string{"claude " + a, "claude " + p + "!"}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("filtered items: got %v want %v", got, want)
				}
			},
		},
		{
			name: "action-not-write-is-skipped",
			run: func(t *testing.T, h string) {
				a, b := dest(h, "a.md"), dest(h, "b.md")
				del := fileOp(b, "B")
				del.Action = "delete"
				empty := fileOp(a, "A")
				empty.Action = ""
				plan := planFor(map[string][]adapter.FileOp{"claude": {del, empty}})
				got := itemKeys(walkUser(h, plan, state.New(), []string{"claude"}, nil))
				// "" and "write" are the accepted spellings; anything else is
				// skipped (matches explain and render.OrphanFiles).
				if want := []string{"claude " + a}; !reflect.DeepEqual(got, want) {
					t.Errorf("got %v want %v", got, want)
				}
			},
		},
		{
			name: "withText-off-leaves-text-empty",
			run: func(t *testing.T, h string) {
				f, k := dest(h, "f.md"), dest(h, "settings.json")
				mustWrite(t, f, "F")
				mustWrite(t, k, mcpJSON("gh"))
				plan := planFor(map[string][]adapter.FileOp{"claude": {fileOp(f, "F"), keyOp(k, mcpJSON("gh"))}})
				for _, it := range walkUser(h, plan, state.New(), []string{"claude"}, nil) {
					if it.srcText != "" || it.dstText != "" {
						t.Errorf("withText=false must leave text empty: %+v", it)
					}
				}
			},
		},
		{
			name: "withText-on-whole-file-is-raw-content-and-guarded-read",
			run: func(t *testing.T, h string) {
				f, absent, dir := dest(h, "f.md"), dest(h, "absent.md"), dest(h, "dir.md")
				mustWrite(t, f, "ON DISK")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				plan := planFor(map[string][]adapter.FileOp{"claude": {
					fileOp(f, "RENDERED"), fileOp(absent, "RENDERED"), fileOp(dir, "RENDERED"),
				}})
				items := walkUser(h, plan, state.New(), []string{"claude"}, func(w *planWalk) { w.withText = true })
				if len(items) != 3 {
					t.Fatalf("got %d items", len(items))
				}
				for _, it := range items {
					if it.srcText != "RENDERED" {
						t.Errorf("srcText is the raw op content: %q", it.srcText)
					}
				}
				if items[0].dstText != "ON DISK" || items[0].hdest != hf("ON DISK") {
					t.Errorf("regular dest: %+v", items[0])
				}
				if items[1].dstText != "" || items[1].hdest != "" {
					t.Errorf("absent dest reads as empty text and absent hash: %+v", items[1])
				}
				// A wrong-shaped destination is refused before the open: the
				// hash side answers the shape sentinel, the text side "".
				if items[2].dstText != "" || items[2].hdest != "not-a-regular-file" {
					t.Errorf("directory dest: %+v", items[2])
				}
			},
		},
		{
			name: "withText-on-key-is-marshalPretty-with-absent-sentinel",
			run: func(t *testing.T, h string) {
				d := dest(h, "settings.json")
				mustWrite(t, d, mcpJSON("gh"))
				plan := planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, mcpJSON("gh", "missing"))}})
				items := walkUser(h, plan, state.New(), []string{"claude"}, func(w *planWalk) { w.withText = true })
				if len(items) != 2 {
					t.Fatalf("got %d items", len(items))
				}
				if items[0].ptr != "/mcpServers/gh" || items[0].srcText != pretty("gh") || items[0].dstText != pretty("gh") {
					t.Errorf("present key: %+v", items[0])
				}
				if items[1].ptr != "/mcpServers/missing" || items[1].srcText != pretty("missing") || items[1].dstText != "<absent>" {
					t.Errorf("absent key: %+v", items[1])
				}
			},
		},
		{
			name: "symlink-hash-text-split",
			run: func(t *testing.T, h string) {
				real, link := dest(h, "real.md"), dest(h, "link.md")
				mustWrite(t, real, "SOURCE")
				if err := os.Symlink(real, link); err != nil {
					t.Fatal(err)
				}
				s := state.New()
				s.Files[fileKey(h, "claude", link)] = state.FileEntry{SHA256: hf("SOURCE")}
				plan := planFor(map[string][]adapter.FileOp{"claude": {fileOp(link, "SOURCE")}})
				items := walkUser(h, plan, s, []string{"claude"}, func(w *planWalk) { w.withText = true })
				if len(items) != 1 {
					t.Fatalf("got %d items", len(items))
				}
				it := items[0]
				// D2: the hash side answers hashFile's symlink sentinel (→ drift);
				// the text side reads THROUGH the link (→ identical text).
				if it.hdest != "symlink-not-regular-file" || it.cls != drift.Drift {
					t.Errorf("hash side: hdest=%q cls=%v", it.hdest, it.cls)
				}
				if it.srcText != "SOURCE" || it.dstText != "SOURCE" {
					t.Errorf("text side: src=%q dst=%q", it.srcText, it.dstText)
				}
				if it.destRegular {
					t.Errorf("a symlinked destination is not a regular file for the mode predicates")
				}
			},
		},
		{
			name: "op-content-not-json-yields-no-items",
			run: func(t *testing.T, h string) {
				d := dest(h, "settings.json")
				mustWrite(t, d, mcpJSON("gh"))
				plan := planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, "not json")}})
				if got := walkUser(h, plan, state.New(), []string{"claude"}, nil); len(got) != 0 {
					t.Errorf("malformed op.Content contributes no items, got %+v", got)
				}
			},
		},
		{
			name: "nil-decoded-dest-classifies-per-key",
			run: func(t *testing.T, h string) {
				d := dest(h, "settings.json")
				s := state.New()
				s.Keys[ptrKey(h, "claude", d, "/mcpServers/gh")] = state.KeyEntry{SHA256: hv("gh")}
				plan := planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, mcpJSON("gh", "new"))}})
				items := walkUser(h, plan, s, []string{"claude"}, func(w *planWalk) {
					w.readDestConfig = func(string, string) map[string]any { return nil }
				})
				if len(items) != 2 {
					t.Fatalf("got %d items", len(items))
				}
				// An empty decoded document is "absent" per key, never one
				// file-level item: a recorded key is drift, an unrecorded one new.
				if items[0].ptr != "/mcpServers/gh" || items[0].cls != drift.Drift || items[0].hdest != "" {
					t.Errorf("recorded key: %+v", items[0])
				}
				if items[1].ptr != "/mcpServers/new" || items[1].cls != drift.New {
					t.Errorf("unrecorded key: %+v", items[1])
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			tc.run(t, h)
		})
	}
}

// TestWalkPlanItems_ReadsKeyMergeDestOncePerOp pins #229 axis 5: a key-merge
// destination is decoded ONCE per op, so every pointer of one op classifies
// against the same snapshot and a file with N servers is not read N times.
// The readDestConfig seam exists for exactly this count.
func TestWalkPlanItems_ReadsKeyMergeDestOncePerOp(t *testing.T) {
	h := t.TempDir()
	d := dest(h, "settings.json")
	mustWrite(t, d, mcpJSON("a", "b", "c", "d"))
	plan := planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, mcpJSON("a", "b", "c", "d"))}})
	reads := 0
	items := walkUser(h, plan, state.New(), []string{"claude"}, func(w *planWalk) {
		w.readDestConfig = func(strategy, path string) map[string]any {
			reads++
			return readDestFile(strategy, path)
		}
	})
	if len(items) != 4 {
		t.Fatalf("fixture must yield four pointers, got %d", len(items))
	}
	if reads != 1 {
		t.Errorf("key-merge destination read %d times for one op; want exactly 1", reads)
	}
}

// TestPlanItemIsNotASerializationSurface pins the walk's secrecy guard (#229
// N-13): a planItem carries resolved cleartext in op.Content, and the ONLY
// thing that keeps it out of a --json payload is that encoding/json ignores
// unexported fields. Exporting a field, or tagging one `json:`, is how that
// property dies — so either fails here. (staticcheck's SA9005 independently
// rejects a json.Marshal of a struct with no exported fields, which is why this
// test asserts the shape of the type rather than marshalling one.)
func TestPlanItemIsNotASerializationSurface(t *testing.T) {
	typ := reflect.TypeOf(planItem{})
	// Proof of life: the reflection must actually be walking the struct.
	if typ.NumField() < 10 {
		t.Fatalf("planItem has only %d fields; the guard is not looking at the type it claims to", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.IsExported() {
			t.Errorf("planItem.%s is exported — a planItem must never be marshalled; keep every field unexported", f.Name)
		}
		if f.Anonymous {
			t.Errorf("planItem embeds %s — an embedded type's exported fields are promoted into a marshalled form", f.Name)
		}
		if tag, ok := f.Tag.Lookup("json"); ok {
			t.Errorf("planItem.%s carries a json tag %q — a planItem is not a serialization surface", f.Name, tag)
		}
	}
	// encoding/json also honours encoding.TextMarshaler, which would marshal
	// the whole item as one string — same egress, different door.
	for _, iface := range []reflect.Type{
		reflect.TypeOf((*json.Marshaler)(nil)).Elem(),
		reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem(),
	} {
		if typ.Implements(iface) || reflect.PointerTo(typ).Implements(iface) {
			t.Errorf("planItem implements %s — a planItem must never be marshalled", iface)
		}
	}
}

// TestDestModePerm pins the (perm, regular) contract: `chmod 000` is a regular
// file with perm 0, distinguishable from "not there" — the two the mode
// predicates must not conflate.
func TestDestModePerm(t *testing.T) {
	h := t.TempDir()
	reg, zero, link, dir := filepath.Join(h, "reg"), filepath.Join(h, "zero"), filepath.Join(h, "link"), filepath.Join(h, "dir")
	mustWrite(t, reg, "x")
	mustWrite(t, zero, "x")
	if err := os.Chmod(reg, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(zero, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(reg, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path  string
		wantPerm    uint32
		wantRegular bool
	}{
		{name: "regular", path: reg, wantPerm: 0o644, wantRegular: true},
		{name: "chmod-000-is-still-regular", path: zero, wantPerm: 0, wantRegular: true},
		{name: "absent", path: filepath.Join(h, "nope"), wantPerm: 0, wantRegular: false},
		{name: "symlink-is-the-link-not-the-target", path: link, wantPerm: 0, wantRegular: false},
		{name: "directory", path: dir, wantPerm: 0, wantRegular: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			perm, regular := destModePerm(tc.path)
			if perm != tc.wantPerm || regular != tc.wantRegular {
				t.Errorf("destModePerm = (%04o, %v); want (%04o, %v)", perm, regular, tc.wantPerm, tc.wantRegular)
			}
		})
	}
}

// TestPathFilterFlagsSurviveAZeroItemOp pins #229 amendment A3: diff's
// filterMatched and explain's pathManaged are set when the path MATCHES an op,
// not when the op yields an item. The op here is the exact shape render.Plan
// synthesizes to clean up an emptied key-merge section — Content "{}" — which
// yields zero pointers. Deriving either flag from the item count would turn
// today's "no diff" into "path … is not managed by agentsync", and explain's
// "managed path, no owners" into "unmanaged path".
func TestPathFilterFlagsSurviveAZeroItemOp(t *testing.T) {
	userHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := dest(userHome, ".claude.json")
	mustWrite(t, d, `{"mcpServers":{"foreign":{"command":"x"}}}`)
	plan := planFor(map[string][]adapter.FileOp{"claude": {keyOp(d, "{}")}})
	names := []string{"claude"}

	hunks, matched := collectDiffHunks(plan, names, d, nil)
	if len(hunks) != 0 || !matched {
		t.Errorf("collectDiffHunks: got %d hunks, filterMatched=%v; want 0 hunks and filterMatched=true", len(hunks), matched)
	}

	model := buildExplainModel(explainInputs{
		fs:          afero.NewMemMapFs(),
		target:      d,
		plan:        plan,
		agents:      names,
		state:       state.New(),
		userHome:    userHome,
		srcHome:     filepath.Join(userHome, ".agentsync"),
		scope:       adapter.ScopeUser,
		projectRoot: "",
	})
	if !model.Unmanaged || !model.pathManaged {
		t.Errorf("buildExplainModel: Unmanaged=%v pathManaged=%v; want Unmanaged=true (no owners) and pathManaged=true (the path matched an op)",
			model.Unmanaged, model.pathManaged)
	}
}

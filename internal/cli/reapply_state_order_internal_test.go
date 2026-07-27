package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReapplyLoadsStateAfterSourceReload pins an ORDERING contract that has no
// behavioral test, and says plainly why.
//
// `plugin upgrade <id>` runs under the global lock and finishes by re-applying.
// The re-apply reloads the canonical source, and that reload can run a pending
// subagent migration, which rewrites this tree's recorded source_id values in
// .state/targets.json. Reading targets.json BEFORE that reload and saving the
// copy afterwards therefore writes stale ids over the rewrite.
//
// WHY THIS IS A SOURCE GUARD AND NOT A BEHAVIORAL TEST: the same re-apply then
// calls PruneStaleState + RecordOpsState over the fresh plan, which re-records
// every op it renders — so for a subagent the agent still renders, the stale
// write is immediately healed and the end state is indistinguishable. I wrote
// the obvious end-to-end test first; it passed with the bug deliberately
// reintroduced, which is precisely why the bug shipped unnoticed and precisely
// why asserting the observable proves nothing here. What is actually true is
// structural: do not read state across a call that can rewrite it.
//
// If this ever gains a genuine observable (a component the re-apply does not
// re-record), replace this with a test of that.
func TestReapplyLoadsStateAfterSourceReload(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "update.go"))
	if err != nil {
		t.Fatal(err)
	}

	body := funcBody(string(src), "func reapplyAfterPluginChange(")
	if body == "" {
		t.Fatal("reapplyAfterPluginChange not found in update.go — update this guard")
	}

	reload := strings.Index(body, "loadProjectedForScope(")
	load := strings.Index(body, "state.Load(")
	switch {
	case reload < 0:
		t.Fatal("reapplyAfterPluginChange no longer calls loadProjectedForScope — update this guard")
	case load < 0:
		t.Fatal("reapplyAfterPluginChange no longer calls state.Load — if state is passed in again, " +
			"it is read before the reload that can rewrite it; see this test's doc comment")
	case load < reload:
		t.Fatal("reapplyAfterPluginChange reads state.Load BEFORE loadProjectedForScope. That reload " +
			"can run the pending subagent migration, which rewrites recorded source_ids — so the copy " +
			"read here is stale and saving it reverts the rewrite. Load state AFTER the reload.")
	}

	// The signature must not accept state either: a caller-supplied
	// *state.Targets is read before this function runs, which has the same
	// defect and is how the bug originally shipped.
	sig := funcSignature(string(src), "func reapplyAfterPluginChange(")
	if strings.Contains(sig, "*state.Targets") {
		t.Errorf("reapplyAfterPluginChange takes a *state.Targets again: %s\n"+
			"A caller reads it before the source reload that can rewrite it. Load it inside, after the reload.", sig)
	}
}

// funcBody returns the source text of the function whose declaration starts
// with decl, from the opening brace to the first line that is exactly "}".
func funcBody(src, decl string) string {
	i := strings.Index(src, decl)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	j := strings.Index(rest, "\n}\n")
	if j < 0 {
		return rest
	}
	return rest[:j]
}

// funcSignature returns the declaration line of the function starting with decl.
func funcSignature(src, decl string) string {
	i := strings.Index(src, decl)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n"); j >= 0 {
		return rest[:j]
	}
	return rest
}

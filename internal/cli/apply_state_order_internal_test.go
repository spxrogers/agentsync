package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestApplyPipelineLoadsStateAfterSourceReload pins an ORDERING contract that
// has no behavioral test, and says plainly why.
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
// Since #231 the re-apply IS the apply pipeline (runApplyPipeline, apply.go),
// so the guard has two halves. Half A holds the original contract against the
// surviving implementation: runApplyPipeline must call loadProjectedForScope
// and then state.Load, in that order, and must not accept a *state.Targets.
// Half B is what makes "routed through the pipeline" a guarded property rather
// than a fact about today's tree: reapplyAfterPluginChange must delegate to
// runApplyPipeline and must NOT load state, plan, apply or save on its own — a
// future hand-rolled re-apply (the exact regression #231 closed) fails here by
// name, on both counts.
//
// If this ever gains a genuine observable (a component the re-apply does not
// re-record), replace half A with a test of that.
func TestApplyPipelineLoadsStateAfterSourceReload(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)

	// Half A — the ordering contract, on the pipeline itself.
	applySrc, err := os.ReadFile(filepath.Join(dir, "apply.go"))
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(string(applySrc), "func runApplyPipeline(")
	if body == "" {
		t.Fatal("runApplyPipeline not found in apply.go — update this guard")
	}

	reload := strings.Index(body, "loadProjectedForScope(")
	load := strings.Index(body, "state.Load(")
	switch {
	case reload < 0:
		t.Fatal("runApplyPipeline no longer calls loadProjectedForScope — update this guard")
	case load < 0:
		t.Fatal("runApplyPipeline no longer calls state.Load — if state is passed in again, " +
			"it is read before the reload that can rewrite it; see this test's doc comment")
	case load < reload:
		t.Fatal("runApplyPipeline reads state.Load BEFORE loadProjectedForScope. That reload " +
			"can run the pending subagent migration, which rewrites recorded source_ids — so the copy " +
			"read here is stale and saving it reverts the rewrite. Load state AFTER the reload.")
	}

	// The signature must not accept state either: a caller-supplied
	// *state.Targets is read before this function runs, which has the same
	// defect and is how the bug originally shipped.
	sig := funcSignature(string(applySrc), "func runApplyPipeline(")
	if strings.Contains(sig, "*state.Targets") {
		t.Errorf("runApplyPipeline takes a *state.Targets again: %s\n"+
			"A caller reads it before the source reload that can rewrite it. Load it inside, after the reload.", sig)
	}

	// Half B — the plugin re-apply must BE the pipeline, not a copy of it. The
	// positive check and the four negatives are Errorf, not Fatal, so a
	// hand-rolled re-apply reports every way it diverged in one run.
	pollSrc, err := os.ReadFile(filepath.Join(dir, "plugin_poll.go"))
	if err != nil {
		t.Fatal(err)
	}
	reapply := funcBody(string(pollSrc), "func reapplyAfterPluginChange(")
	if reapply == "" {
		t.Fatal("reapplyAfterPluginChange not found in plugin_poll.go — update this guard")
	}
	if !strings.Contains(reapply, "runApplyPipeline(") {
		t.Errorf("reapplyAfterPluginChange no longer calls runApplyPipeline. The plugin re-apply must be " +
			"the apply pipeline itself — a second transcription of it is the divergence #231 closed (that " +
			"copy had already lost the git baseline/checkpoint, the removal-aware headline, backup pruning " +
			"and the translation report).")
	}
	for _, own := range []string{"state.Load(", "render.Plan(", "render.Apply(", "state.Save("} {
		if strings.Contains(reapply, own) {
			t.Errorf("reapplyAfterPluginChange calls %s itself. Everything between the source reload and "+
				"the report belongs to runApplyPipeline; a hand-rolled re-apply is the regression #231 closed.", own)
		}
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

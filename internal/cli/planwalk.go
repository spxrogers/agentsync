package cli

import (
	"encoding/json"
	"os"
	"sort"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// planItem is one classified destination the walk yields: a whole destination
// file, one RFC-6901 pointer inside a key-merged file, or a whole-file orphan
// (a destination this agent still owns in state but no longer renders).
//
// SECURITY. Every field is unexported, deliberately. op.Content holds RESOLVED
// CLEARTEXT whenever the caller built its plan from secrets.SubstituteCanonical
// — `status` and `explain` both do. A planItem must therefore never be
// marshalled, logged or persisted. encoding/json ignores unexported fields,
// which makes "planItem is not a serialization surface" a property of the type
// rather than a rule a reviewer has to remember;
// TestPlanItemIsNotASerializationSurface fails if a field is ever exported or
// given a `json:` tag. Callers project a planItem into their own statusItem /
// diffHunk / reconcileItem / explainItem and mask before display
// (secrets.MaskResolved).
type planItem struct {
	agent string

	// op is the plan op that produced the item. For an ORPHAN it is SYNTHESIZED
	// from state — adapter.FileOp{Action: "delete", Path, SourceID} — with Mode
	// left 0 because the orphan removal path never reads it.
	op adapter.FileOp

	// ptr is the RFC-6901 pointer for a key item; "" for a whole-file item.
	ptr string

	// orphan marks a whole-file destination owned in state that this agent no
	// longer renders. The set is PER AGENT and UNFILTERED: a path another
	// enabled agent still renders IS yielded (status's ownership view).
	// reconcile applies its own cross-agent exclusion and path dedupe on top —
	// see collectReconcileItems.
	orphan bool

	// cls is the CONTENT-only classification. It deliberately does NOT fold in
	// permission drift; classWithModeDrift is the folded reading status and
	// explain report, and opModeDrifted the predicate behind it.
	cls drift.Class

	// The triple cls was computed from. hdest is "" for absent-or-unreadable,
	// and one of three opaque sentinels for a refused symlink, an unresolvable
	// one, or a refused shape — see hashFile, whose semantics this reproduces.
	hsrc, happlied, hdest string

	// Whole-file mode facts, from destModePerm: destRegular is false for an
	// absent, refused-symlink or non-regular destination, which is what keeps
	// `chmod 000` distinguishable from "absent" (destPerm 0, regular true vs
	// destPerm 0, regular false). The intended mode is op.Mode; the mode state
	// RECORDED at the last apply is deliberately not carried, because no
	// surface asks it (#229 axis 14).
	destPerm    uint32
	destRegular bool

	// srcText/dstText are populated only when planWalk.withText, and never for
	// an orphan. For a key item they are marshalPretty of the pointer's value
	// on each side ("<absent>" when missing); for a whole-file item they are
	// the raw op content and readDestText's guarded destination read ("" on a
	// refused symlink, a refused shape, or any read error), which applies the
	// same symlink policy as the hash half above, so the two sides cannot
	// disagree about whether a link is looked through (#229 axis 9). Text is
	// RAW: callers mask (secrets.MaskResolved) at their own existing call sites.
	srcText, dstText string
}

// opModeDrifted is THE mode question, the one every surface that reports
// permission drift asks: do the destination's permission bits differ from the
// mode the next apply would WRITE (op.Mode — render.Writer.Write chmods to
// it)? An unspecified op.Mode (0) is never drift, and an absent /
// refused-symlink / non-regular destination is left to the content
// classifier. diff's modeHunk and classWithModeDrift below are both gated on
// exactly this, so they cannot disagree (#229 axis 14).
func (i planItem) opModeDrifted() bool {
	if i.op.Mode == 0 || !i.destRegular {
		return false
	}
	return os.FileMode(i.destPerm).Perm() != os.FileMode(i.op.Mode).Perm()
}

// classWithModeDrift is the class status and explain report: the content class,
// upgraded from clean or converged (content in sync either way) to drift when
// only the permission bits differ from what the
// next apply would WRITE (op.Mode — render.Writer.Write chmods to it). A merged key
// has no mode, and an orphan's synthesized op carries Mode 0, so both fall through.
func (i planItem) classWithModeDrift() drift.Class {
	if i.ptr == "" && (i.cls == drift.Clean || i.cls == drift.Converged) && i.opModeDrifted() {
		return drift.Drift
	}
	return i.cls
}

// destSymlinkRefused reports whether a whole-file destination is a symlink the
// read side did not look through (destReadPath: the switch unset, or the link
// unresolvable once opted in). It is a DERIVATION from hdest, not a field:
// diff keys its symlink hunk on the very hash status, reconcile and explain
// classified from, so the four surfaces cannot disagree about it (#229 axis 9).
func (i planItem) destSymlinkRefused() bool {
	return i.ptr == "" && (i.hdest == symlinkRefusedSentinel || i.hdest == symlinkUnresolvableSentinel)
}

// destShapeRefused is its sibling for a whole-file destination readDestBytes
// refused: present and not a regular file, or unstattable (hashFile's one
// token for both).
func (i planItem) destShapeRefused() bool { return i.ptr == "" && i.hdest == shapeSentinel }

// destModePerm answers the permission bits of the REGULAR file at path.
// regular is false — and perm 0 — for an absent, refused-symlink
// (destReadPath: a link this configuration does not look through) or
// non-regular destination, so a caller can tell `chmod 000` (0, true) from
// "not there" (0, false). With AGENTSYNC_ALLOW_SYMLINK_DEST=1 a link is
// resolved and the perm is the TARGET's — the bits apply's mode fix chmods
// through the link.
func destModePerm(path string) (perm uint32, regular bool) {
	path, why := destReadPath(path)
	if why != symlinkNone {
		return 0, false
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return 0, false
	}
	return uint32(fi.Mode().Perm()), true
}

// planWalk is the input to walkPlanItems.
type planWalk struct {
	plan render.RenderPlan
	// agents is the iteration order (production callers pass reg.Names()).
	agents []string
	// state is REQUIRED, non-nil. status, reconcile and explain pass the
	// loaded state; diff passes an empty state.New() because it never reads the
	// applied side. The walk itself must not fall back to one.
	state *state.Targets
	// userHome is the user's $HOME, the HomeRelative base for state keys.
	userHome    string
	scope       adapter.Scope
	projectRoot string

	// matchOp, when non-nil, narrows the walk to the ops it accepts. It is
	// called EXACTLY ONCE per op that survives the Action filter, in walk
	// order, and side effects are an intended use: `diff` sets filterMatched
	// and `explain` sets pathManaged/keyMergers inside it, because both must
	// record "the path matched an op" for an op that yields ZERO items (a
	// synthesized orphan-cleanup op has Content "{}" → no pointers). Deriving
	// those flags from len(items) is a regression — see #229 amendment A3.
	// matchOp is NOT applied to orphan items: a filtered walk that also sets
	// includeOrphans yields every orphan, not the matching ones.
	matchOp func(agent string, op adapter.FileOp) bool

	// includeOrphans appends each agent's render.OrphanFiles items AFTER that
	// agent's op items, unfiltered (see planItem.orphan).
	includeOrphans bool

	// withText populates srcText/dstText. It governs THOSE FIELDS ONLY:
	// op.Content still carries resolved cleartext for status and explain
	// whether or not this is set, so it is a cost control, not a secrecy
	// control (#229 amendment A5). The secrecy guard is the unexported-field
	// rule on planItem.
	withText bool

	// readDestConfig is a TEST SEAM: nil means readDestFile. It exists so a
	// test can count key-merge destination reads and prove they are once-per-op
	// rather than once-per-pointer (#229 axis 5). Production callers leave it
	// nil.
	readDestConfig func(strategy, path string) map[string]any
}

// walkPlanItems classifies every destination a rendered plan touches, for one
// scope, against state and the current on-disk contents. It is the single walk
// behind `status`, `diff`, `reconcile` and `explain`; before #229 each of those
// held its own copy and the four disagreed.
//
// ORDER is part of the contract. For each name in w.agents that w.plan has a
// result for, in w.agents order: the agent's ops in plan order; within one
// key-merge op, pointers SORTED ascending; then, when w.includeOrphans, that
// agent's render.OrphanFiles (already path-sorted). Whole-file ops are deduped
// by path PER AGENT. Key-merge ops are NEVER deduped by path — one agent emits
// several of them to one file (codex /mcp_servers + /hooks → config.toml;
// claude /hooks + /lspServers → settings.json), each owning a distinct section,
// and deduping dropped the second section's items.
//
// ERRORS DO NOT TRAVEL. Every read, stat and parse failure becomes item state —
// an empty or sentinel hdest, an empty decoded map, an empty dstText — exactly
// as all four copies did. A malformed op.Content is swallowed the same way
// (json.Unmarshal's error is discarded, ours stays nil, the op contributes no
// items). Introducing an error return would be a behavior change with no
// oracle.
//
// Every destination read goes through hashFile / readDestFile / readDestText,
// each of which reads through the readDestBytes gate, so a FIFO, device,
// socket or directory at a destination can never block a read-only command
// (internal/cli/destread.go, #240). The whole-file reads also share
// destReadPath, the symlink policy (#229 axis 9).
func walkPlanItems(w planWalk) []planItem {
	readDest := w.readDestConfig
	if readDest == nil {
		readDest = readDestFile
	}
	var out []planItem
	for _, name := range w.agents {
		res, ok := w.plan.PerAgent[name]
		if !ok {
			continue
		}
		seenPath := map[string]bool{}
		for _, op := range res.Ops {
			if op.Action != "" && op.Action != "write" {
				continue
			}
			if w.matchOp != nil && !w.matchOp(name, op) {
				continue
			}
			if render.IsKeyMerge(op.MergeStrategy) {
				var ours map[string]any
				_ = json.Unmarshal(op.Content, &ours)
				// ONCE per op, not per pointer: every pointer of one op
				// classifies against the same destination snapshot (#229 axis 5).
				final := readDest(op.MergeStrategy, op.Path)
				ptrs := render.CollectPointers(ours, "")
				sort.Strings(ptrs) // CollectPointers ranges a map (#229 axis 13)
				for _, ptr := range ptrs {
					srcV := getPointerValue(ours, ptr)
					dstV := getPointerValue(final, ptr)
					it := planItem{
						agent:    name,
						op:       op,
						ptr:      ptr,
						hsrc:     hashAnyValue(srcV),
						happlied: w.state.Keys[stateKeyKey(w.userHome, name, w.scope, w.projectRoot, op.Path, ptr)].SHA256,
						hdest:    hashAnyValue(dstV),
					}
					it.cls = drift.Classify(it.hsrc, it.happlied, it.hdest)
					if w.withText {
						it.srcText, it.dstText = marshalPretty(srcV), marshalPretty(dstV)
					}
					out = append(out, it)
				}
				continue
			}
			if seenPath[op.Path] {
				continue
			}
			seenPath[op.Path] = true
			entry := w.state.Files[stateFileKey(w.userHome, name, w.scope, w.projectRoot, op.Path)]
			perm, reg := destModePerm(op.Path)
			it := planItem{
				agent:       name,
				op:          op,
				hsrc:        hashContent(op.Content),
				happlied:    entry.SHA256,
				hdest:       hashFile(op.Path),
				destPerm:    perm,
				destRegular: reg,
			}
			it.cls = drift.Classify(it.hsrc, it.happlied, it.hdest)
			if w.withText {
				it.srcText, it.dstText = string(op.Content), readDestText(op.Path)
			}
			out = append(out, it)
		}
		if !w.includeOrphans {
			continue
		}
		for _, orphan := range render.OrphanFiles(w.state, w.userHome, name, w.scope, w.projectRoot, res.Ops) {
			entry := w.state.Files[stateFileKey(w.userHome, name, w.scope, w.projectRoot, orphan)]
			perm, reg := destModePerm(orphan)
			it := planItem{
				agent:  name,
				orphan: true,
				// SourceID matters: the reclaimable-KIND check behind reconcile's
				// prompt wording is SourceID-keyed and silently degrades to
				// "unknown kind" without it.
				op:          adapter.FileOp{Action: "delete", Path: orphan, SourceID: entry.SourceID},
				happlied:    entry.SHA256,
				hdest:       hashFile(orphan),
				destPerm:    perm,
				destRegular: reg,
			}
			it.cls = drift.Classify("", it.happlied, it.hdest)
			out = append(out, it)
		}
	}
	return out
}

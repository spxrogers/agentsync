package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/state"
)

// PruneStaleState removes Files / Keys entries owned by agent+scope+project
// whose path or pointer is no longer produced by the current set of ops.
// This must be called BEFORE RecordOpsState so the freshly-applied entries
// don't get pruned by their own absence in the previous run's state.
//
// Without this, removing an MCP server / skill / hook from
// ~/.agentsync/ leaves its state entry behind forever; it shows up as
// `Orphan` in `status` and `targets.json` grows unbounded over time.
//
// The userHome argument (the user's $HOME, paths.HomeDir) is used to
// normalize op.Path to its HOME-relative form so state-key lookups match
// keys written by RecordOpsState. It must NOT be the agentsync home — dest
// files live under $HOME, not under ~/.agentsync.
func PruneStaleState(s *state.Targets, userHome, agent string, scope adapter.Scope, project string, ops []adapter.FileOp) {
	if s == nil {
		return
	}
	prefix := fmt.Sprintf("%s:%s:%s:", agent, scope.String(), paths.HomeRelative(userHome, project))

	// Build the set of paths and per-path pointer sets that this agent's
	// current plan still produces. Paths are normalized to HOME-relative
	// so they compare equal to what's stored in state.
	currentFiles := map[string]struct{}{}           // portable path → present
	currentKeys := map[string]map[string]struct{}{} // portable path → set of pointers
	for _, op := range ops {
		if op.Action != "" && op.Action != "write" {
			continue
		}
		portable := paths.HomeRelative(userHome, op.Path)
		switch op.MergeStrategy {
		case "merge-json-keys", "merge-jsonc-keys", "merge-toml-keys":
			ptrs, ok := currentKeys[portable]
			if !ok {
				ptrs = map[string]struct{}{}
				currentKeys[portable] = ptrs
			}
			// op.Content is always JSON (the pointer-merge currency), even when
			// the destination file is TOML — so it is parsed as JSON here.
			var ours map[string]any
			if err := json.Unmarshal(op.Content, &ours); err == nil {
				for _, p := range CollectPointers(ours, "") {
					ptrs[p] = struct{}{}
				}
			}
		default:
			currentFiles[portable] = struct{}{}
		}
	}

	for key := range s.Files {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		path := strings.TrimPrefix(key, prefix)
		if _, ok := currentFiles[path]; !ok {
			delete(s.Files, key)
		}
	}
	for key := range s.Keys {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rest := strings.TrimPrefix(key, prefix)
		// rest = "<path>:<pointer>", and BOTH path and pointer can contain
		// ':' (e.g. a Windows "C:"-drive dest path), so the split point is
		// ambiguous. Test every currentKeys path whose "path:" prefixes rest
		// and keep the key if ANY of them owns the remaining pointer. We must
		// NOT stop at the first prefix match: when one path is a colon-
		// delimited string-prefix of another, the first candidate (map order
		// is random) may be the wrong one, and breaking there would prune a
		// live key.
		matched := false
		for path, ptrs := range currentKeys {
			if !strings.HasPrefix(rest, path+":") {
				continue
			}
			ptr := strings.TrimPrefix(rest, path+":")
			if _, ok := ptrs[ptr]; ok {
				matched = true
				break
			}
		}
		if !matched {
			delete(s.Keys, key)
		}
	}
}

// OrphanFiles returns the absolute dest paths that agent+scope+project still
// OWNS in state as whole-file (replace-strategy) entries but the current plan's
// ops no longer render — i.e. the source component was removed since the last
// apply. The same detection PruneStaleState uses, surfaced so diagnostics
// (status/diff/reconcile) can report a dest the next apply would prune instead
// of falsely reporting "clean". Returns absolute paths, sorted.
func OrphanFiles(s *state.Targets, userHome, agent string, scope adapter.Scope, project string, ops []adapter.FileOp) []string {
	if s == nil {
		return nil
	}
	prefix := fmt.Sprintf("%s:%s:%s:", agent, scope.String(), paths.HomeRelative(userHome, project))
	current := map[string]struct{}{}
	for _, op := range ops {
		if op.Action != "" && op.Action != "write" {
			continue
		}
		if IsKeyMerge(op.MergeStrategy) {
			continue
		}
		current[paths.HomeRelative(userHome, op.Path)] = struct{}{}
	}
	var out []string
	for key := range s.Files {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		path := strings.TrimPrefix(key, prefix)
		if _, ok := current[path]; !ok {
			out = append(out, paths.FromHomeRelative(userHome, path))
		}
	}
	sort.Strings(out)
	return out
}

// skillOrphanDeletes returns delete FileOps for skill files this agent owns in
// state — entries whose SourceID is under "skills/" — that the current plan no
// longer renders. It is how `apply` converges a destination when a whole skill,
// or a single bundled file within one, is removed from the canonical source:
// the leftover dest file is removed instead of lingering forever.
//
// Scoped deliberately to skills (the Agent Skills spec treats a skill as a whole
// directory, so removal must reclaim the whole tree). Other replace-strategy
// components (subagents/commands) keep their established reconcile-driven
// cleanup. Each op carries the state SourceID + Mode so the writer can back up a
// drifted dest before deleting and bound empty-directory pruning to the skills
// root. Sorted deepest-path-first so a directory empties out before it is pruned.
func skillOrphanDeletes(s *state.Targets, userHome, agent string, scope adapter.Scope, project string, ops []adapter.FileOp) []adapter.FileOp {
	if s == nil {
		return nil
	}
	prefix := fmt.Sprintf("%s:%s:%s:", agent, scope.String(), paths.HomeRelative(userHome, project))
	rendered := map[string]struct{}{}
	for _, op := range ops {
		if op.Action != "" && op.Action != "write" {
			continue
		}
		if IsKeyMerge(op.MergeStrategy) {
			continue
		}
		rendered[paths.HomeRelative(userHome, op.Path)] = struct{}{}
	}
	var out []adapter.FileOp
	for key, entry := range s.Files {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if !strings.HasPrefix(entry.SourceID, "skills/") {
			continue
		}
		path := strings.TrimPrefix(key, prefix)
		if _, ok := rendered[path]; ok {
			continue
		}
		out = append(out, adapter.FileOp{
			Action:   "delete",
			Path:     paths.FromHomeRelative(userHome, path),
			SourceID: entry.SourceID,
			Mode:     entry.Mode,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path > out[j].Path })
	return out
}

// RecordOpsState updates s with hashes for files and keys produced by ops.
// Caller is expected to call this AFTER a successful Apply.
//
// Both op.Path and project are normalized to HOME-relative form via
// paths.HomeRelative (against userHome, the user's $HOME) before being
// embedded in the state-map keys, so the resulting targets.json is portable
// across machines whose $HOME differs (e.g. /Users/alice/ vs /home/alice/
// after a chezmoi sync). userHome must NOT be the agentsync home: dest files
// live under $HOME, not under ~/.agentsync, so using the agentsync home as
// the base leaves every key machine-absolute and unportable.
func RecordOpsState(s *state.Targets, userHome, agent string, scope adapter.Scope, project string, ops []adapter.FileOp) error {
	now := time.Now().UTC()
	portableProject := paths.HomeRelative(userHome, project)
	for _, op := range ops {
		if op.Action != "" && op.Action != "write" {
			continue
		}
		portablePath := paths.HomeRelative(userHome, op.Path)
		switch op.MergeStrategy {
		case "merge-json-keys", "merge-jsonc-keys", "merge-toml-keys":
			// Re-read final on-disk content and record per pointer. The
			// destination is decoded per strategy (TOML for merge-toml-keys),
			// while op.Content is always JSON.
			data, err := os.ReadFile(op.Path)
			if err != nil {
				return fmt.Errorf("read post-apply %s: %w", op.Path, err)
			}
			final, err := decodeDestObject(op.MergeStrategy, data)
			if err != nil {
				return fmt.Errorf("parse post-apply %s: %w", op.Path, err)
			}
			var ours map[string]any
			if err := json.Unmarshal(op.Content, &ours); err != nil {
				return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
			}
			for _, ptr := range CollectPointers(ours, "") {
				v, present := getPointerOK(final, ptr)
				if !present {
					// The pointer is not in the post-apply file. In a normal
					// (full-success) apply every pointer in `ours` lands on
					// disk, so absence means this op's merge never happened —
					// the case where the apply-error rescue records several
					// same-path ops but only some were written. Recording an
					// absent pointer as owned (with a hash of null) would
					// suppress its foreign-collision backup on a later apply
					// and silently overwrite a value the user added in between.
					// Skip it; only record what actually landed.
					continue
				}
				hash := hashAny(v)
				key := fmt.Sprintf("%s:%s:%s:%s:%s", agent, scope.String(), portableProject, portablePath, ptr)
				s.Keys[key] = state.KeyEntry{
					SHA256:    hash,
					AppliedAt: now,
					SourceID:  op.SourceID,
				}
			}
		default:
			data, err := os.ReadFile(op.Path)
			if err != nil {
				return fmt.Errorf("read post-apply %s: %w", op.Path, err)
			}
			sum := sha256.Sum256(data)
			key := fmt.Sprintf("%s:%s:%s:%s", agent, scope.String(), portableProject, portablePath)
			s.Files[key] = state.FileEntry{
				SHA256:    hex.EncodeToString(sum[:]),
				Mode:      op.Mode,
				AppliedAt: now,
				SourceID:  op.SourceID,
			}
		}
	}
	return nil
}

// CollectPointers walks m and returns JSON pointers for every leaf-or-object
// at the second level (e.g. /mcpServers/github → stop). agentsync owns at
// the second-level granularity; deeper edits fall under that key's value hash.
// The prefix argument is used for recursive calls; callers should pass "".
func CollectPointers(m map[string]any, prefix string) []string {
	var out []string
	for k, v := range m {
		ptr := prefix + "/" + escapeJSONPointer(k)
		switch vv := v.(type) {
		case map[string]any:
			// Drill one level: each child key becomes a pointer.
			for kk := range vv {
				out = append(out, ptr+"/"+escapeJSONPointer(kk))
			}
		default:
			out = append(out, ptr)
		}
	}
	return out
}

func escapeJSONPointer(s string) string {
	s = replaceAll(s, "~", "~0")
	s = replaceAll(s, "/", "~1")
	return s
}

func replaceAll(s, from, to string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if i+len(from) <= len(s) && s[i:i+len(from)] == from {
			out = append(out, to...)
			i += len(from)
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}

func getPointer(m map[string]any, ptr string) any {
	v, _ := getPointerOK(m, ptr)
	return v
}

// getPointerOK is getPointer with an explicit presence signal so callers can
// distinguish "pointer absent from the document" from "pointer present with a
// null value" — getPointer returns nil for both. RecordOpsState relies on this
// to avoid recording a pointer that never landed on disk.
func getPointerOK(m map[string]any, ptr string) (any, bool) {
	parts := splitPtr(ptr)
	var cur any = m
	for _, p := range parts {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, present := mm[p]
		if !present {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

func splitPtr(ptr string) []string {
	if len(ptr) > 0 && ptr[0] == '/' {
		ptr = ptr[1:]
	}
	if ptr == "" {
		return nil
	}
	parts := []string{}
	cur := []byte{}
	for i := 0; i < len(ptr); i++ {
		if ptr[i] == '/' {
			parts = append(parts, string(cur))
			cur = cur[:0]
			continue
		}
		cur = append(cur, ptr[i])
	}
	parts = append(parts, string(cur))
	for i, p := range parts {
		p = replaceAll(p, "~1", "/")
		p = replaceAll(p, "~0", "~")
		parts[i] = p
	}
	return parts
}

func hashAny(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

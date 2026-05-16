package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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
// The home argument is used to normalize op.Path to its HOME-relative
// form so state-key lookups match keys written by RecordOpsState (which
// stores HOME-relative paths for cross-machine portability).
func PruneStaleState(s *state.Targets, home, agent string, scope adapter.Scope, project string, ops []adapter.FileOp) {
	if s == nil {
		return
	}
	prefix := fmt.Sprintf("%s:%s:%s:", agent, scope.String(), paths.HomeRelative(home, project))

	// Build the set of paths and per-path pointer sets that this agent's
	// current plan still produces. Paths are normalized to HOME-relative
	// so they compare equal to what's stored in state.
	currentFiles := map[string]struct{}{}           // portable path → present
	currentKeys := map[string]map[string]struct{}{} // portable path → set of pointers
	for _, op := range ops {
		if op.Action != "" && op.Action != "write" {
			continue
		}
		portable := paths.HomeRelative(home, op.Path)
		switch op.MergeStrategy {
		case "merge-json-keys", "merge-jsonc-keys":
			ptrs, ok := currentKeys[portable]
			if !ok {
				ptrs = map[string]struct{}{}
				currentKeys[portable] = ptrs
			}
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
		// rest = "<path>:<pointer>"; find the LAST ':' isn't safe because
		// pointers can contain ':'. Use strings.Index since we know the
		// path part can also contain ':'. Match against currentKeys instead:
		// look for any path in currentKeys that is a prefix of rest with a
		// trailing ':<ptr>'.
		matched := false
		for path, ptrs := range currentKeys {
			if !strings.HasPrefix(rest, path+":") {
				continue
			}
			ptr := strings.TrimPrefix(rest, path+":")
			if _, ok := ptrs[ptr]; ok {
				matched = true
			}
			break
		}
		if !matched {
			delete(s.Keys, key)
		}
	}
}

// RecordOpsState updates s with hashes for files and keys produced by ops.
// Caller is expected to call this AFTER a successful Apply.
//
// Both op.Path and project are normalized to HOME-relative form via
// paths.HomeRelative before being embedded in the state-map keys, so the
// resulting targets.json is portable across machines whose $HOME differs
// (e.g. /Users/alice/ vs /home/alice/ after a chezmoi sync). Without
// this, every key prefix would shift on the new machine and every
// native file would reclassify as ForeignCollision.
func RecordOpsState(s *state.Targets, home, agent string, scope adapter.Scope, project string, ops []adapter.FileOp) error {
	now := time.Now().UTC()
	portableProject := paths.HomeRelative(home, project)
	for _, op := range ops {
		if op.Action != "" && op.Action != "write" {
			continue
		}
		portablePath := paths.HomeRelative(home, op.Path)
		switch op.MergeStrategy {
		case "merge-json-keys", "merge-jsonc-keys":
			// Re-read final on-disk content and record per pointer.
			data, err := os.ReadFile(op.Path)
			if err != nil {
				return fmt.Errorf("read post-apply %s: %w", op.Path, err)
			}
			var final map[string]any
			if err := json.Unmarshal(data, &final); err != nil {
				return fmt.Errorf("parse post-apply %s: %w", op.Path, err)
			}
			var ours map[string]any
			if err := json.Unmarshal(op.Content, &ours); err != nil {
				return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
			}
			for _, ptr := range CollectPointers(ours, "") {
				v := getPointer(final, ptr)
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
	parts := splitPtr(ptr)
	var cur any = m
	for _, p := range parts {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
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

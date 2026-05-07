package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/state"
)

// RecordOpsState updates s with hashes for files and keys produced by ops.
// Caller is expected to call this AFTER a successful Apply.
func RecordOpsState(s *state.Targets, agent string, scope adapter.Scope, project string, ops []adapter.FileOp) error {
	now := time.Now().UTC()
	for _, op := range ops {
		if op.Action != "" && op.Action != "write" {
			continue
		}
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
				key := fmt.Sprintf("%s:%s:%s:%s:%s", agent, scope.String(), project, op.Path, ptr)
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
			key := fmt.Sprintf("%s:%s:%s:%s", agent, scope.String(), project, op.Path)
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

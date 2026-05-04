// Package jsonkeys implements per-key JSON pointer merge used by adapters that
// need to own a subset of keys inside a shared JSON (or JSONC) config file.
package jsonkeys

import (
	"strings"
)

// MergeKeys merges ours into existing, removing ownedPointers that are no
// longer in ours. Returns the merged map plus diagnostic lists.
//
// kept: pointers from ownedPointers that are still present in ours.
// removed: pointers from ownedPointers that are absent from ours and were
// deleted from existing.
//
// JSON pointer syntax: leading "/", "/" separated path segments. RFC 6901
// escapes ("~0" for "~", "~1" for "/") are supported.
func MergeKeys(existing, ours map[string]any, ownedPointers []string) (map[string]any, []string, []string) {
	merged := deepCopyMap(existing)
	if merged == nil {
		merged = map[string]any{}
	}

	// Step 1: overlay ours onto merged
	for k, v := range ours {
		switch ev := merged[k].(type) {
		case map[string]any:
			if vv, ok := v.(map[string]any); ok {
				merged[k] = mergeMaps(ev, vv)
				continue
			}
		}
		merged[k] = v
	}

	// Step 2: walk ownedPointers; if a pointer is no longer present in `ours`,
	// delete it from merged. If still present, mark kept.
	var kept, removed []string
	for _, p := range ownedPointers {
		if pointerExists(ours, p) {
			kept = append(kept, p)
			continue
		}
		if pointerExists(merged, p) {
			deletePointer(merged, p)
			removed = append(removed, p)
		}
	}
	return merged, kept, removed
}

func mergeMaps(a, b map[string]any) map[string]any {
	out := deepCopyMap(a)
	for k, v := range b {
		switch existing := out[k].(type) {
		case map[string]any:
			if vv, ok := v.(map[string]any); ok {
				out[k] = mergeMaps(existing, vv)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func deepCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if mm, ok := v.(map[string]any); ok {
			out[k] = deepCopyMap(mm)
		} else {
			out[k] = v
		}
	}
	return out
}

func pointerExists(m map[string]any, ptr string) bool {
	parts := splitPointer(ptr)
	var cur any = m
	for _, p := range parts {
		mp, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = mp[p]
		if !ok {
			return false
		}
	}
	return true
}

func deletePointer(m map[string]any, ptr string) {
	parts := splitPointer(ptr)
	if len(parts) == 0 {
		return
	}
	cur := m
	for i, p := range parts {
		if i == len(parts)-1 {
			delete(cur, p)
			return
		}
		next, ok := cur[p].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
}

func splitPointer(ptr string) []string {
	ptr = strings.TrimPrefix(ptr, "/")
	if ptr == "" {
		return nil
	}
	raw := strings.Split(ptr, "/")
	out := make([]string, len(raw))
	for i, s := range raw {
		s = strings.ReplaceAll(s, "~1", "/")
		s = strings.ReplaceAll(s, "~0", "~")
		out[i] = s
	}
	return out
}

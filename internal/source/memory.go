package source

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var memoryImportRe = regexp.MustCompile(`(?m)^@import\s+\./fragments/(\S+)\s*$`)

// MemoryHasFragments reports whether the canonical memory at home is composed of
// fragment files (memory/fragments/*). Write-back of memory (import / reconcile)
// is UNSAFE when it is: the destination CLAUDE.md/AGENTS.md is the fully
// EXPANDED memory, so persisting it back into memory/AGENTS.md would inline
// every `@import` and strand the fragment files. Ingest cannot de-resolve the
// expansion (which inlined text came from which fragment is unrecoverable), so
// the only safe action is to refuse/skip the write-back — callers consult this.
func MemoryHasFragments(home string) bool {
	dir := filepath.Join(home, "memory", "fragments")
	has := false
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if info.Mode().IsRegular() {
			has = true
		}
		return nil
	})
	return has
}

// maxMemoryImportDepth bounds recursive fragment expansion as a belt against
// pathological nesting (cycles are already broken by the visiting set).
const maxMemoryImportDepth = 16

// ExpandMemoryImports replaces `@import ./fragments/<name>` directives in body
// with the named fragment's content, RECURSIVELY — a fragment may itself
// `@import` another. A single non-recursive pass (the previous behavior) left
// nested directives as literal `@import` lines in the rendered CLAUDE.md /
// AGENTS.md. Cycles are broken (a fragment already being expanded is left as a
// literal directive), recursion is depth-bounded, and unknown fragments are
// left as literal directives so the user notices.
func ExpandMemoryImports(body string, fragments map[string]string) string {
	return expandMemoryImports(body, fragments, map[string]bool{}, 0)
}

func expandMemoryImports(body string, fragments map[string]string, visiting map[string]bool, depth int) string {
	if depth >= maxMemoryImportDepth {
		return body
	}
	return memoryImportRe.ReplaceAllStringFunc(body, func(line string) string {
		m := memoryImportRe.FindStringSubmatch(line)
		if len(m) < 2 {
			return line
		}
		name := m[1]
		frag, ok := fragments[name]
		if !ok || visiting[name] {
			return line
		}
		visiting[name] = true
		expanded := expandMemoryImports(frag, fragments, visiting, depth+1)
		delete(visiting, name)
		return strings.TrimRight(expanded, "\n")
	})
}

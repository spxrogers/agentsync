package source

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var memoryImportRe = regexp.MustCompile(`(?m)^@import\s+\./fragments/(\S+)\s*$`)

// Fragment boundary markers. ExpandMemoryImports wraps each inlined fragment in
// these so the expansion is REVERSIBLE: capture (import/reconcile) parses them
// back into memory/AGENTS.md (`@import` directives) plus the fragment files,
// giving fragmented memory a bidirectional round-trip instead of the apply-only
// guard. They are HTML comments (the only "ignorable" Markdown syntax) carrying
// the fragment's relative path, so an agent reading the rendered memory treats
// them as metadata, not instructions, and no absolute path leaks into the prompt.
const fragmentMarkerToken = "agentsync:fragment"

var (
	fragStartRe = regexp.MustCompile(`^<!-- agentsync:fragment (\S+) -->$`)
	fragEndRe   = regexp.MustCompile(`^<!-- /agentsync:fragment (\S+) -->$`)
)

func fragmentStartMarker(name string) string { return "<!-- agentsync:fragment " + name + " -->" }
func fragmentEndMarker(name string) string   { return "<!-- /agentsync:fragment " + name + " -->" }

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
//
// Each inlined fragment is wrapped in boundary markers (see fragmentMarkerToken)
// so CollapseMemoryMarkers can reverse the expansion. The one exception is a
// MARKER COLLISION: if the body or any fragment already contains the marker
// token, markers are omitted entirely (plain expansion) so they can't corrupt
// the reverse parse — the write-back guard then keeps that memory apply-only.
func ExpandMemoryImports(body string, fragments map[string]string) string {
	markers := !markerCollision(body, fragments)
	return expandMemoryImports(body, fragments, map[string]bool{}, 0, markers)
}

func markerCollision(body string, fragments map[string]string) bool {
	if strings.Contains(body, fragmentMarkerToken) {
		return true
	}
	for _, v := range fragments {
		if strings.Contains(v, fragmentMarkerToken) {
			return true
		}
	}
	return false
}

func expandMemoryImports(body string, fragments map[string]string, visiting map[string]bool, depth int, markers bool) string {
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
		expanded := strings.TrimRight(expandMemoryImports(frag, fragments, visiting, depth+1, markers), "\n")
		delete(visiting, name)
		if !markers {
			return expanded
		}
		return fragmentStartMarker(name) + "\n" + expanded + "\n" + fragmentEndMarker(name)
	})
}

// CollapseMemoryMarkers reverses ExpandMemoryImports' marker emission: it parses
// a rendered memory file back into memory/AGENTS.md (with `@import` directives
// restored where each fragment block was) plus the fragment files. It is how
// import/reconcile capture a native memory edit back into the fragment structure
// instead of flattening it.
//
// Returns (mem, true, nil) when balanced markers were found and collapsed;
// (zero, false, nil) when the input carries no markers (caller takes the plain
// path); (zero, true, err) when markers are present but malformed, unbalanced,
// reference a traversing path, or the same fragment appears twice with differing
// content — the caller must then refuse rather than guess.
func CollapseMemoryMarkers(dest string) (Memory, bool, error) {
	if !strings.Contains(dest, fragmentMarkerToken) {
		return Memory{}, false, nil
	}
	type frame struct {
		name  string
		lines []string
	}
	var bodyLines []string
	var stack []*frame
	frags := map[string]string{}
	emit := func(s string) {
		if len(stack) == 0 {
			bodyLines = append(bodyLines, s)
			return
		}
		top := stack[len(stack)-1]
		top.lines = append(top.lines, s)
	}
	for _, ln := range strings.Split(dest, "\n") {
		if m := fragStartRe.FindStringSubmatch(ln); m != nil {
			name := m[1]
			if err := validateFragmentName(name); err != nil {
				return Memory{}, true, err
			}
			emit("@import ./fragments/" + name) // restore the directive in the parent
			stack = append(stack, &frame{name: name})
			continue
		}
		if m := fragEndRe.FindStringSubmatch(ln); m != nil {
			name := m[1]
			if len(stack) == 0 || stack[len(stack)-1].name != name {
				return Memory{}, true, fmt.Errorf("unbalanced memory fragment marker for %q", name)
			}
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			content := strings.TrimRight(strings.Join(top.lines, "\n"), "\n") + "\n"
			if prev, dup := frags[name]; dup && prev != content {
				return Memory{}, true, fmt.Errorf("memory fragment %q appears multiple times with differing content; resolve it by hand", name)
			}
			frags[name] = content
			continue
		}
		emit(ln)
	}
	if len(stack) != 0 {
		return Memory{}, true, fmt.Errorf("unterminated memory fragment marker for %q", stack[len(stack)-1].name)
	}
	// Normalize the reconstructed AGENTS.md to a single trailing newline (the
	// markdown convention loadMemory/WriteMemory round-trip on): expansion's
	// `\s*$` consumes the newline after a trailing `@import`, so it can't be
	// recovered positionally.
	body := strings.Join(bodyLines, "\n")
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return Memory{Body: body, Fragments: frags}, true, nil
}

// validateFragmentName rejects a fragment path that would escape memory/fragments/
// on a reverse write — a marker in a hand-edited dest is untrusted input, so a
// "../" segment must never become an arbitrary-file-write primitive.
func validateFragmentName(name string) error {
	if name == "" {
		return fmt.Errorf("empty fragment name")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("fragment name %q escapes memory/fragments", name)
	}
	return nil
}

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

// Managed-banner markers. Like the fragment markers above, these are reversible
// HTML-comment delimiters: RenderManagedMemory injects the agentsync "managed
// file" notice inside them on render, and StripManagedBanner removes the whole
// block on capture (import/reconcile) so it NEVER lands in the canonical memory
// source. The banner body between the markers is human-readable Markdown (a
// blockquote) so an agent reading the rendered CLAUDE.md/AGENTS.md sees the
// notice; the markers themselves are HTML comments the agent ignores.
//
// The token "agentsync:managed" is RESERVED: canonical memory (the AGENTS.md body
// or any fragment) must not contain it, and checkReservedMarkers (called from
// loadMemory and WriteMemory) rejects a canonical that does — with a message
// telling the user to remove/rename the offending content — rather than letting it
// collide with the banner's markers. StripManagedBanner additionally anchors on
// the banner's lead line (managedBannerLead), not the bare markers, so even a
// hand-edited native file that happens to carry a user-authored
// `<!-- agentsync:managed -->` block has its content preserved (the write-back
// then surfaces the reserved-token error) — it is never silently deleted.
const managedMarkerToken = "agentsync:managed"

const (
	managedStartMarker = "<!-- agentsync:managed -->"
	managedEndMarker   = "<!-- /agentsync:managed -->"
	// managedBannerLead is the fixed opening of the banner body (everything before
	// the per-file name). StripManagedBanner anchors on it so it removes ONLY
	// agentsync's own banner; managedBanner builds from the same constant so the
	// two halves can never drift.
	managedBannerLead = "> **Managed by [agentsync](https://agentsync.cc)"
)

// managedBannerRe matches a whole injected banner block — the markers, the banner
// body between them, and the blank line that separates it from the memory content.
// It anchors on managedBannerLead immediately after the start marker, so it only
// matches agentsync's own banner, never an arbitrary user-authored
// `<!-- agentsync:managed -->` block. `(?s)` lets `.` span lines; `.*?` is
// non-greedy so the block is bounded minimally; the `\r?\n` / `(?:\r?\n){0,2}`
// tolerate CRLF line endings and consume the trailing separator the renderer adds
// (and a hand-removed blank line). ReplaceAll makes StripManagedBanner idempotent.
var managedBannerRe = regexp.MustCompile(
	`(?s)` + regexp.QuoteMeta(managedStartMarker) + `\r?\n` + regexp.QuoteMeta(managedBannerLead) +
		`.*?` + regexp.QuoteMeta(managedEndMarker) + `(?:\r?\n){0,2}`,
)

// checkReservedMarkers rejects canonical memory that contains the reserved
// managed-banner token. The token is owned by the managed-file banner
// (RenderManagedMemory); a body or fragment carrying it would collide with the
// banner's reversible markers, so agentsync errors and asks the user to fix it
// rather than silently degrading. Called from loadMemory (authoring path) and
// WriteMemory (capture path) so the violation surfaces at both boundaries.
func checkReservedMarkers(body string, fragments map[string]string) error {
	if strings.Contains(body, managedMarkerToken) {
		return fmt.Errorf("memory/AGENTS.md contains the reserved marker %q, which is owned by agentsync's managed-file banner; remove or rephrase that text", managedMarkerToken)
	}
	for name, content := range fragments {
		if strings.Contains(content, managedMarkerToken) {
			return fmt.Errorf("memory fragment %q contains the reserved marker %q, which is owned by agentsync's managed-file banner; remove or rephrase that text (or rename the fragment if the marker is in its name)", name, managedMarkerToken)
		}
	}
	return nil
}

// MemoryBannerEnabled reports whether the managed-file banner should be rendered
// into memory files for this config. It is ON by default; only an explicit
// `[memory] banner = false` in agentsync.toml disables it.
func (cfg Config) MemoryBannerEnabled() bool {
	return cfg.Memory.Banner == nil || *cfg.Memory.Banner
}

// managedBanner is the agentsync "managed file" notice prepended to a rendered
// memory file, naming destFile (e.g. "CLAUDE.md"). It is STATIC apart from the
// filename so it hashes identically on every render — drift compares the whole
// rendered file, so a banner that varied (a version, a timestamp) would either
// thrash the classifier or, worse, feed mutable data into hashed content.
func managedBanner(destFile string) string {
	return managedStartMarker + "\n" +
		managedBannerLead + " — do not edit `" + destFile + "` directly.**\n" +
		"> To change it, edit `.agentsync/memory/AGENTS.md` (or the relevant\n" +
		"> `.agentsync/memory/fragments/*.md` fragment) and run `agentsync apply`.\n" +
		"> Direct edits here are reported as drift and overwritten on the next apply.\n" +
		managedEndMarker + "\n"
}

// RenderManagedMemory expands fragment imports (see ExpandMemoryImports) and,
// when banner is true, PREPENDS the agentsync managed-file notice naming
// destFile. The banner is wrapped in reversible markers (see managedMarkerToken)
// so StripManagedBanner removes it on capture — it is therefore never part of
// the canonical memory source and never compounds across applies. Every adapter
// renders memory through this one helper so the notice is byte-identical across
// agents.
//
// Returns plain expansion (no banner) when banner is false, destFile is empty,
// or the memory already contains the reserved managed marker token (a belt-and-
// suspenders guard; checkReservedMarkers already rejects such a canonical at load
// time). Callers guard `body == ""` before calling, so the banner is only ever
// attached to a non-empty memory file.
func RenderManagedMemory(body string, fragments map[string]string, destFile string, banner bool) string {
	expanded := ExpandMemoryImports(body, fragments)
	if !banner || destFile == "" || strings.Contains(expanded, managedMarkerToken) {
		return expanded
	}
	return managedBanner(destFile) + "\n" + expanded
}

// StripManagedBanner removes the managed-file banner block(s) RenderManagedMemory
// injects, returning the memory content without it. It is the inverse half that
// keeps the banner out of the canonical source: capture (import/reconcile) calls
// it on a rendered memory file BEFORE CollapseMemoryMarkers, so neither the
// banner nor the markers reach memory/AGENTS.md. It matches only agentsync's own
// banner (anchored on managedBannerLead), so a user-authored
// `<!-- agentsync:managed -->` block is left untouched — never silently deleted.
// It is idempotent and a no-op on content that carries no banner.
func StripManagedBanner(s string) string {
	if !strings.Contains(s, managedBannerLead) {
		return s
	}
	return managedBannerRe.ReplaceAllString(s, "")
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

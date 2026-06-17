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
// The namespace "agentsync:managed" is RESERVED. Each managed marker carries its
// own identifier AFTER the namespace (here "memory-banner"), so if agentsync adds
// more managed markers in future, each parses unambiguously and bidirectionally —
// mirroring the per-name fragment markers above. Canonical memory (the AGENTS.md
// body or any fragment) must not contain the namespace, and checkReservedMarkers
// (called from loadMemory and WriteMemory) rejects a canonical that does — with a
// message telling the user to remove/rename the offending content — rather than
// letting it collide with the banner's markers. StripManagedBanner matches
// agentsync's FULL rendered banner (derived from managedBannerFmt below), not the
// bare markers, so a hand-edited native file carrying a user-authored
// `<!-- agentsync:managed … -->` block — even one that begins like the banner — is
// never stripped: it is left for checkReservedMarkers to reject loudly, never
// silently deleted.
const managedMarkerNamespace = "agentsync:managed"

const (
	managedStartMarker = "<!-- " + managedMarkerNamespace + " memory-banner -->"
	managedEndMarker   = "<!-- /" + managedMarkerNamespace + " memory-banner -->"
	// managedBannerFmt is the banner with a single %s for the destination file
	// name. managedBanner renders it and managedBannerRe is DERIVED from it (see
	// compileManagedBannerRe), so the rendered banner and the pattern that strips
	// it are guaranteed to match the same text — they can never drift.
	managedBannerFmt = managedStartMarker + "\n" +
		"> **Managed by [agentsync](https://agentsync.cc) — do not edit `%s` directly.**\n" +
		"> To change it, edit `.agentsync/memory/AGENTS.md` (or the relevant\n" +
		"> `.agentsync/memory/fragments/*.md` fragment) and run `agentsync apply`.\n" +
		"> Direct edits here are reported as drift and overwritten on the next apply.\n" +
		managedEndMarker + "\n"
)

// managedBannerRe matches agentsync's full rendered banner: every static line
// verbatim, the per-file name (%s) as a non-newline wildcard, CRLF-tolerant, plus
// the blank-line separator RenderManagedMemory appends. It is derived from
// managedBannerFmt so it tracks the banner text automatically. Matching the WHOLE
// banner (not just the markers or its lead line) is what makes StripManagedBanner
// safe: a block that differs from the banner in any STATIC line is not matched, so
// a user-authored marker block is preserved rather than silently deleted. (The one
// non-static span is the filename wildcard, so a block byte-identical to the banner
// except for the file it names is still stripped — but such a block can never reach
// the canonical source: checkReservedMarkers rejects the reserved namespace at both
// load and capture, and only the boilerplate notice itself is matched, never the
// user prose around it.)
var managedBannerRe = compileManagedBannerRe()

func compileManagedBannerRe() *regexp.Regexp {
	body := strings.TrimSuffix(managedBannerFmt, "\n") // drop the template's own trailing newline
	pre, post, _ := strings.Cut(body, "%s")
	// Quote each literal half and make every embedded newline CRLF-tolerant; the
	// %s (filename) becomes a non-newline run; consume the trailing separator.
	quote := func(s string) string { return strings.ReplaceAll(regexp.QuoteMeta(s), "\n", `\r?\n`) }
	return regexp.MustCompile(quote(pre) + `[^\r\n]*` + quote(post) + `(?:\r?\n){0,2}`)
}

// checkReservedMarkers rejects canonical memory that contains the reserved
// managed-banner token. The token is owned by the managed-file banner
// (RenderManagedMemory); a body or fragment carrying it would collide with the
// banner's reversible markers, so agentsync errors and asks the user to fix it
// rather than silently degrading. Called from loadMemory (authoring path) and
// WriteMemory (capture path) so the violation surfaces at both boundaries.
func checkReservedMarkers(body string, fragments map[string]string) error {
	if strings.Contains(body, managedMarkerNamespace) {
		return fmt.Errorf("memory/AGENTS.md contains the reserved marker %q, which is owned by agentsync's managed-file banner; remove or rephrase that text", managedMarkerNamespace)
	}
	for name, content := range fragments {
		if strings.Contains(content, managedMarkerNamespace) {
			return fmt.Errorf("memory fragment %q contains the reserved marker %q, which is owned by agentsync's managed-file banner; remove or rephrase that text (or rename the fragment if the marker is in its name)", name, managedMarkerNamespace)
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
	return fmt.Sprintf(managedBannerFmt, destFile)
}

// RenderManagedMemory expands fragment imports (see ExpandMemoryImports) and,
// when banner is true, PREPENDS the agentsync managed-file notice naming
// destFile. The banner is wrapped in reversible markers (see managedMarkerNamespace)
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
	if !banner || destFile == "" || strings.Contains(expanded, managedMarkerNamespace) {
		return expanded
	}
	return managedBanner(destFile) + "\n" + expanded
}

// StripManagedBanner removes the managed-file banner block(s) RenderManagedMemory
// injects, returning the memory content without it. It is the inverse half that
// keeps the banner out of the canonical source: capture (import/reconcile) calls
// it on a rendered memory file BEFORE CollapseMemoryMarkers, so neither the
// banner nor the markers reach memory/AGENTS.md. It matches only agentsync's full
// rendered banner (managedBannerRe), so a user-authored `<!-- agentsync:managed -->`
// block — even one that opens like the banner — is left untouched and never
// silently deleted (checkReservedMarkers rejects it loudly instead). It is
// idempotent and a no-op on content that carries no banner.
func StripManagedBanner(s string) string {
	if !strings.Contains(s, managedStartMarker) {
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

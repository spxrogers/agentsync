package source_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestExpandMemoryImports_Recursive is the regression for the single-pass
// expander: a fragment that itself @imports another left the nested directive
// as a literal line in the rendered memory file.
func TestExpandMemoryImports_Recursive(t *testing.T) {
	body := "Top\n@import ./fragments/a.md\n"
	frags := map[string]string{
		"a.md": "Content A\n@import ./fragments/b.md",
		"b.md": "Content B",
	}
	got := source.ExpandMemoryImports(body, frags)
	if !strings.Contains(got, "Content A") || !strings.Contains(got, "Content B") {
		t.Fatalf("nested fragment not expanded: %q", got)
	}
	if strings.Contains(got, "@import") {
		t.Fatalf("literal @import directive leaked into output: %q", got)
	}
}

// TestExpandMemoryImports_Cycle ensures a fragment cycle terminates and does
// not stack-overflow; the cyclic directive is left literal.
func TestExpandMemoryImports_Cycle(t *testing.T) {
	body := "@import ./fragments/a.md\n"
	frags := map[string]string{
		"a.md": "A\n@import ./fragments/b.md",
		"b.md": "B\n@import ./fragments/a.md",
	}
	got := source.ExpandMemoryImports(body, frags) // must not hang/overflow
	if !strings.Contains(got, "A") || !strings.Contains(got, "B") {
		t.Fatalf("cycle expansion dropped content: %q", got)
	}
}

// TestExpandMemoryImports_UnknownFragmentLeftLiteral keeps a directive for a
// missing fragment so the user notices.
func TestExpandMemoryImports_UnknownFragmentLeftLiteral(t *testing.T) {
	got := source.ExpandMemoryImports("@import ./fragments/missing.md\n", map[string]string{})
	if !strings.Contains(got, "@import ./fragments/missing.md") {
		t.Fatalf("unknown fragment directive should be preserved: %q", got)
	}
}

// TestWriteMemory_RefusesWhenFragmentsExist guards the silent flatten-and-orphan
// hazard: when canonical memory is composed of fragments, the value handed to
// WriteMemory is the fully expanded memory (ingest can't de-resolve it), so
// overwriting AGENTS.md would inline every @import and strand the fragment
// files. WriteMemory must refuse and leave the source untouched.
func TestWriteMemory_RefusesWhenFragmentsExist(t *testing.T) {
	home := t.TempDir()
	memDir := filepath.Join(home, "memory")
	fragDir := filepath.Join(memDir, "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "# Memory\n@import ./fragments/style.md\n"
	if err := os.WriteFile(filepath.Join(memDir, "AGENTS.md"), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "style.md"), []byte("Be concise.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !source.MemoryHasFragments(home) {
		t.Fatal("MemoryHasFragments should be true")
	}

	err := source.WriteMemory(home, source.Memory{Body: "# Memory\nBe concise.\n"})
	if err == nil {
		t.Fatal("WriteMemory must refuse to overwrite fragment-composed memory")
	}
	// Source must be untouched: AGENTS.md still has the @import, fragment intact.
	got, _ := os.ReadFile(filepath.Join(memDir, "AGENTS.md"))
	if string(got) != orig {
		t.Fatalf("AGENTS.md was modified despite refusal: %q", got)
	}
	if _, err := os.Stat(filepath.Join(fragDir, "style.md")); err != nil {
		t.Fatalf("fragment was orphaned/removed: %v", err)
	}
}

// TestWriteMemory_WritesWhenNoFragments confirms the guard does not block the
// normal (no-fragments) write.
func TestWriteMemory_WritesWhenNoFragments(t *testing.T) {
	home := t.TempDir()
	if source.MemoryHasFragments(home) {
		t.Fatal("MemoryHasFragments should be false on an empty home")
	}
	if err := source.WriteMemory(home, source.Memory{Body: "# Memory\n"}); err != nil {
		t.Fatalf("WriteMemory should succeed without fragments: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(home, "memory", "AGENTS.md"))
	if string(got) != "# Memory\n" {
		t.Fatalf("memory not written: %q", got)
	}
}

// TestMemoryMarkers_RoundTrip proves expansion is reversible: a fragmented
// memory expands with boundary markers, and CollapseMemoryMarkers reconstructs
// AGENTS.md (with @import restored) and the fragment content byte-for-byte.
func TestMemoryMarkers_RoundTrip(t *testing.T) {
	body := "# Memory\n\n@import ./fragments/style.md\n"
	frags := map[string]string{"style.md": "Be concise.\n"}

	expanded := source.ExpandMemoryImports(body, frags)
	if !strings.Contains(expanded, "<!-- agentsync:fragment style.md -->") ||
		!strings.Contains(expanded, "<!-- /agentsync:fragment style.md -->") {
		t.Fatalf("markers not emitted: %q", expanded)
	}
	if strings.Contains(expanded, "@import") {
		t.Fatalf("literal @import leaked into rendered output: %q", expanded)
	}

	mem, had, err := source.CollapseMemoryMarkers(expanded)
	if err != nil || !had {
		t.Fatalf("collapse: had=%v err=%v", had, err)
	}
	if mem.Body != body {
		t.Fatalf("body round-trip: got %q want %q", mem.Body, body)
	}
	if mem.Fragments["style.md"] != "Be concise.\n" {
		t.Fatalf("fragment round-trip: got %q", mem.Fragments["style.md"])
	}
}

// TestMemoryMarkers_Nested covers a fragment that itself @imports another: the
// inner block is restored as an @import inside the outer fragment, not inlined.
func TestMemoryMarkers_Nested(t *testing.T) {
	body := "# M\n@import ./fragments/outer.md\n"
	frags := map[string]string{
		"outer.md": "Outer top\n@import ./fragments/inner.md\nOuter bottom\n",
		"inner.md": "Inner\n",
	}
	mem, had, err := source.CollapseMemoryMarkers(source.ExpandMemoryImports(body, frags))
	if err != nil || !had {
		t.Fatalf("collapse: had=%v err=%v", had, err)
	}
	if mem.Body != body {
		t.Fatalf("body: got %q", mem.Body)
	}
	if mem.Fragments["outer.md"] != frags["outer.md"] {
		t.Fatalf("outer fragment: got %q want %q", mem.Fragments["outer.md"], frags["outer.md"])
	}
	if mem.Fragments["inner.md"] != frags["inner.md"] {
		t.Fatalf("inner fragment: got %q", mem.Fragments["inner.md"])
	}
}

// TestCollapseMemoryMarkers_Errors covers the refuse-not-guess cases.
func TestCollapseMemoryMarkers_Errors(t *testing.T) {
	cases := map[string]string{
		"unbalanced": "# M\n<!-- agentsync:fragment a.md -->\nx\n",
		"mismatched": "<!-- agentsync:fragment a.md -->\nx\n<!-- /agentsync:fragment b.md -->\n",
		"traversal":  "<!-- agentsync:fragment ../evil -->\nx\n<!-- /agentsync:fragment ../evil -->\n",
		"ambiguous":  "<!-- agentsync:fragment a.md -->\nx\n<!-- /agentsync:fragment a.md -->\n<!-- agentsync:fragment a.md -->\ny\n<!-- /agentsync:fragment a.md -->\n",
	}
	for name, dest := range cases {
		_, had, err := source.CollapseMemoryMarkers(dest)
		if !had || err == nil {
			t.Fatalf("%s: expected (had=true, err!=nil), got had=%v err=%v", name, had, err)
		}
	}
}

// TestCollapseMemoryMarkers_NoMarkers returns had=false so callers take the
// plain (or guard) path.
func TestCollapseMemoryMarkers_NoMarkers(t *testing.T) {
	_, had, err := source.CollapseMemoryMarkers("# Memory\nplain body\n")
	if had || err != nil {
		t.Fatalf("expected had=false err=nil, got had=%v err=%v", had, err)
	}
}

// boolPtr is a local helper for the *bool banner setting.
func boolPtr(b bool) *bool { return &b }

// TestMemoryBannerEnabled: unset (nil) defaults ON; explicit true/false honoured.
func TestMemoryBannerEnabled(t *testing.T) {
	cases := []struct {
		name string
		cfg  source.Config
		want bool
	}{
		{"unset defaults on", source.Config{}, true},
		{"explicit true", source.Config{Memory: source.MemoryConfig{Banner: boolPtr(true)}}, true},
		{"explicit false", source.Config{Memory: source.MemoryConfig{Banner: boolPtr(false)}}, false},
	}
	for _, tc := range cases {
		if got := tc.cfg.MemoryBannerEnabled(); got != tc.want {
			t.Errorf("%s: MemoryBannerEnabled() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRenderManagedMemory_PrependsBanner: the banner is emitted, names the
// destination file, sits BEFORE the memory body, and the body survives intact.
func TestRenderManagedMemory_PrependsBanner(t *testing.T) {
	body := "# My memory\n\nBe concise.\n"
	got := source.RenderManagedMemory(body, nil, "CLAUDE.md", true)

	for _, want := range []string{
		"<!-- agentsync:managed memory-banner -->",
		"<!-- /agentsync:managed memory-banner -->",
		"do not edit `CLAUDE.md` directly",
		".agentsync/memory/AGENTS.md",
		"agentsync apply",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("banner missing %q in:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, "<!-- agentsync:managed memory-banner -->") {
		t.Fatalf("banner must be prepended; got prefix %q", got[:min(48, len(got))])
	}
	if !strings.Contains(got, "Be concise.") {
		t.Fatalf("memory body dropped: %s", got)
	}
	if strings.Index(got, "<!-- /agentsync:managed memory-banner -->") > strings.Index(got, "Be concise.") {
		t.Fatalf("banner must precede the body: %s", got)
	}
}

// TestRenderManagedMemory_SuppressedWhenDisabledOrEmpty: no banner when the
// setting is off, the destination name is empty, or the memory already contains
// the reserved managed token (collision guard).
func TestRenderManagedMemory_SuppressedWhenDisabledOrEmpty(t *testing.T) {
	body := "# Memory\nplain\n"
	cases := []struct {
		name     string
		body     string
		destFile string
		banner   bool
	}{
		{"disabled", body, "CLAUDE.md", false},
		{"empty dest name", body, "", true},
		{"collision", "# Memory\nsee <!-- agentsync:managed --> note\n", "CLAUDE.md", true},
	}
	for _, tc := range cases {
		got := source.RenderManagedMemory(tc.body, nil, tc.destFile, tc.banner)
		if strings.Contains(got, "Managed by [agentsync]") {
			t.Errorf("%s: banner should be suppressed, got:\n%s", tc.name, got)
		}
	}
}

// TestManagedBanner_RoundTripPlain proves a banner-prefixed plain memory file
// strips back to EXACTLY the expanded body (byte-for-byte) — the property that
// keeps the notice out of the canonical source.
func TestManagedBanner_RoundTripPlain(t *testing.T) {
	body := "# Memory\n\nLine one.\nLine two.\n"
	rendered := source.RenderManagedMemory(body, nil, "AGENTS.md", true)
	if !strings.Contains(rendered, "agentsync:managed") {
		t.Fatalf("expected a banner: %s", rendered)
	}
	got := source.StripManagedBanner(rendered)
	if got != body {
		t.Fatalf("strip did not recover body byte-for-byte:\n got %q\nwant %q", got, body)
	}
}

// TestStripManagedBanner_NoopAndIdempotent: stripping content with no banner is
// a no-op, and stripping is idempotent.
func TestStripManagedBanner_NoopAndIdempotent(t *testing.T) {
	plain := "# Memory\nno banner here\n"
	if got := source.StripManagedBanner(plain); got != plain {
		t.Fatalf("no-op strip changed content: %q", got)
	}
	rendered := source.RenderManagedMemory("# M\nbody\n", nil, "CLAUDE.md", true)
	once := source.StripManagedBanner(rendered)
	twice := source.StripManagedBanner(once)
	if once != twice {
		t.Fatalf("strip not idempotent:\n once %q\ntwice %q", once, twice)
	}
}

// TestManagedBanner_ArtifactRoundTrip is the fidelity guard (CLAUDE.md: anchor
// to the on-disk artifact, not the parsed model). It starts from a spec-complete
// memory tree on disk (AGENTS.md with an @import + a real fragment file), renders
// it to a destination WITH the banner enabled, captures it back the way
// import/reconcile do (StripManagedBanner → CollapseMemoryMarkers → WriteMemory),
// and asserts the rebuilt canonical files are byte-identical to the originals —
// and that the banner leaked into neither.
func TestManagedBanner_ArtifactRoundTrip(t *testing.T) {
	home := t.TempDir()
	memDir := filepath.Join(home, "memory")
	fragDir := filepath.Join(memDir, "fragments")
	if err := os.MkdirAll(fragDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Import sits last (a blank line immediately after an @import is consumed by
	// expansion's `\s*$` — a documented fragment limitation, see memory.go — and
	// is orthogonal to the banner under test here).
	bodyOnDisk := "# Memory\n\nProject conventions.\n\n@import ./fragments/style.md\n"
	fragOnDisk := "Be concise.\n"
	if err := os.WriteFile(filepath.Join(memDir, "AGENTS.md"), []byte(bodyOnDisk), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragDir, "style.md"), []byte(fragOnDisk), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// Render to a destination file with the banner on (the default).
	rendered := source.RenderManagedMemory(c.Memory.Body, c.Memory.Fragments, "CLAUDE.md", true)
	if !strings.Contains(rendered, "agentsync:managed") || !strings.Contains(rendered, "agentsync:fragment style.md") {
		t.Fatalf("rendered file missing banner and/or fragment markers:\n%s", rendered)
	}

	// Capture it back exactly as import/reconcile do.
	stripped := source.StripManagedBanner(rendered)
	if strings.Contains(stripped, "agentsync:managed") {
		t.Fatalf("banner survived strip:\n%s", stripped)
	}
	mem, had, err := source.CollapseMemoryMarkers(stripped)
	if err != nil || !had {
		t.Fatalf("collapse: had=%v err=%v", had, err)
	}

	out := t.TempDir()
	if err := source.WriteMemory(out, mem); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	gotBody, _ := os.ReadFile(filepath.Join(out, "memory", "AGENTS.md"))
	gotFrag, _ := os.ReadFile(filepath.Join(out, "memory", "fragments", "style.md"))
	if string(gotBody) != bodyOnDisk {
		t.Fatalf("AGENTS.md round-trip drift:\n got %q\nwant %q", gotBody, bodyOnDisk)
	}
	if string(gotFrag) != fragOnDisk {
		t.Fatalf("fragment round-trip drift:\n got %q\nwant %q", gotFrag, fragOnDisk)
	}
	// The banner must have leaked into NEITHER canonical file.
	for name, data := range map[string][]byte{"AGENTS.md": gotBody, "style.md": gotFrag} {
		if strings.Contains(string(data), "agentsync:managed") || strings.Contains(string(data), "Managed by [agentsync]") {
			t.Fatalf("managed banner leaked into canonical %s:\n%s", name, data)
		}
	}
}

// TestStripManagedBanner_PreservesUserAuthoredBlock is the regression for the
// strip/render ownership asymmetry (PR #91 review): StripManagedBanner must
// remove ONLY agentsync's own full banner, never an arbitrary user-authored
// block in the reserved namespace — deleting the latter would be a silent data
// loss on capture. Because the strip matches the WHOLE banner, even a block that
// reuses the exact managed markers but differs in body is preserved (and then
// rejected loudly by checkReservedMarkers, not deleted).
func TestStripManagedBanner_PreservesUserAuthoredBlock(t *testing.T) {
	cases := map[string]string{
		// Bare markers (old namespace style), arbitrary body.
		"bare markers": "# Memory\n\n<!-- agentsync:managed -->\nmy own notes here\n<!-- /agentsync:managed -->\n\nrest\n",
		// The EXACT managed markers agentsync renders, but a body that is NOT the banner.
		"exact markers": "# Memory\n\n<!-- agentsync:managed memory-banner -->\nmy own notes here\n<!-- /agentsync:managed memory-banner -->\n\nrest\n",
		// Body that even opens like the banner's first line but then diverges.
		"banner-like opener": "<!-- agentsync:managed memory-banner -->\n> **Managed by [agentsync](https://agentsync.cc) but I rewrote the rest**\n<!-- /agentsync:managed memory-banner -->\nkeep me\n",
	}
	for name, user := range cases {
		if got := source.StripManagedBanner(user); got != user {
			t.Fatalf("%s: user-authored block must be preserved verbatim:\n got %q\nwant %q", name, got, user)
		}
	}
	// agentsync's own banner IS stripped (and only it).
	withBanner := source.RenderManagedMemory("# Memory\nbody\n", nil, "CLAUDE.md", true)
	if !strings.Contains(withBanner, "agentsync:managed memory-banner") {
		t.Fatalf("expected a banner to strip: %q", withBanner)
	}
	if got := source.StripManagedBanner(withBanner); got != "# Memory\nbody\n" {
		t.Fatalf("agentsync banner not stripped to clean body: %q", got)
	}
}

// TestStripManagedBanner_CRLF: the banner strips cleanly even with CRLF line
// endings (a Windows-authored or editor-rewritten native file).
func TestStripManagedBanner_CRLF(t *testing.T) {
	lf := source.RenderManagedMemory("# Body\n", nil, "CLAUDE.md", true)
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	if got := source.StripManagedBanner(crlf); got != "# Body\r\n" {
		t.Fatalf("CRLF strip left residue: %q", got)
	}
}

// TestStripManagedBanner_CrossFilename locks the filename-wildcard contract: a
// banner strips back to the clean body regardless of which file it names, so
// capture works for every agent's destination (CLAUDE.md, GEMINI.md, the rule
// files, …) without the stripper knowing the name.
func TestStripManagedBanner_CrossFilename(t *testing.T) {
	for _, name := range []string{"CLAUDE.md", "AGENTS.md", "GEMINI.md", "agentsync.md", "global_rules.md"} {
		rendered := source.RenderManagedMemory("# Body\n\nkeep me\n", nil, name, true)
		if got := source.StripManagedBanner(rendered); got != "# Body\n\nkeep me\n" {
			t.Fatalf("%s: strip did not recover body: %q", name, got)
		}
	}
}

// TestRenderManagedMemory_Deterministic underpins the "banner never manufactures
// drift" claim: the rendered bytes are identical across calls (no timestamp or
// other nondeterminism), so the re-render / last-applied / on-disk hashes the
// drift classifier compares stay equal for an untouched file.
func TestRenderManagedMemory_Deterministic(t *testing.T) {
	body := "# Memory\n\n@import ./fragments/x.md\n"
	frags := map[string]string{"x.md": "be terse\n"}
	a := source.RenderManagedMemory(body, frags, "CLAUDE.md", true)
	b := source.RenderManagedMemory(body, frags, "CLAUDE.md", true)
	if a != b {
		t.Fatalf("render not deterministic:\n a=%q\n b=%q", a, b)
	}
}

// TestLoad_RejectsReservedMarker enforces the reserved-token contract (PR #91
// review): canonical memory — the AGENTS.md body OR a fragment — must not contain
// the managed-banner marker, or it would collide with the banner's reversible
// markers. Load surfaces it as a clear error instead of silently degrading.
func TestLoad_RejectsReservedMarker(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		fragment string // written to fragments/bad.md when non-empty
	}{
		{"in body", "# M\nsee <!-- agentsync:managed --> here\n", ""},
		{"in fragment", "# M\n", "oops <!-- agentsync:managed -->\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			fragDir := filepath.Join(home, "memory", "fragments")
			if err := os.MkdirAll(fragDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "memory", "AGENTS.md"), []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.fragment != "" {
				if err := os.WriteFile(filepath.Join(fragDir, "bad.md"), []byte(tc.fragment), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := source.Load(afero.NewOsFs(), home); err == nil || !strings.Contains(err.Error(), "reserved marker") {
				t.Fatalf("Load should reject the reserved marker; got err=%v", err)
			}
		})
	}
}

// TestWriteMemory_RejectsReservedMarker: the capture funnel (import/reconcile →
// WriteMemory) refuses to persist the reserved managed-banner marker into the
// canonical source, so a hand-edited native file carrying it errors loudly rather
// than corrupting memory/.
func TestWriteMemory_RejectsReservedMarker(t *testing.T) {
	home := t.TempDir()
	if err := source.WriteMemory(home, source.Memory{Body: "x <!-- agentsync:managed --> y\n"}); err == nil ||
		!strings.Contains(err.Error(), "reserved marker") {
		t.Fatalf("WriteMemory should reject the reserved marker; got err=%v", err)
	}
}

// TestExpandMemoryImports_MarkerCollision: a fragment whose content already
// contains the marker token disables markers entirely (plain expansion), so a
// reverse parse can't be corrupted.
func TestExpandMemoryImports_MarkerCollision(t *testing.T) {
	body := "@import ./fragments/a.md\n"
	frags := map[string]string{"a.md": "see <!-- agentsync:fragment x -->\n"}
	expanded := source.ExpandMemoryImports(body, frags)
	if strings.Contains(expanded, "<!-- agentsync:fragment a.md -->") {
		t.Fatalf("markers must be suppressed on collision: %q", expanded)
	}
	_, had, _ := source.CollapseMemoryMarkers(expanded)
	_ = had // content token may still trip detection; the write-back guard covers safety
}

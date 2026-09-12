package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestKeyItemKind pins the ONE derivation of a key-merge item's component kind.
// It comes from the op's SourceID, never from the pointer's root key — root keys
// are per-agent data that grow with every agent added and COLLIDE across agents
// (Claude, Cursor, Gemini, Windsurf, Roo, Cline and eleven breadth-tier specs
// all use "mcpServers"; OpenCode and Crush both use "mcp"), so no root-keyed
// classification can be right for every agent.
func TestKeyItemKind(t *testing.T) {
	tests := []struct {
		name     string
		sourceID string
		want     string
	}{
		{"key-merge mcp section", "mcp/* (multiple)", "mcp"},
		{"whole-file mcp (continue)", "mcp/github.toml", "mcp"},
		{"key-merge hooks section", "hooks/* (multiple)", "hooks"},
		{"whole-file hooks", "hooks/PreToolUse.toml", "hooks"},
		{"lsp, unreachable today but symmetric", "lsp/* (multiple)", "lsp"},
		{"no provenance", "", ""},
		{"a non-key component", "skills/demo/SKILL.md", ""},
		{"a bare prefix without the separator is NOT mcp", "mcpServers", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := keyItemKind(tc.sourceID); got != tc.want {
				t.Errorf("keyItemKind(%q) = %q, want %q", tc.sourceID, got, tc.want)
			}
		})
	}
}

// TestKeyItemPointerParts pins the pointer split, and specifically the RFC 6901
// decode of the second segment. render.CollectPointers builds every pointer
// with jsonkeys.EscapeToken, so the segment arrives encoded; using it raw was a
// live bug — an MCP server id containing "~" reached write-back as "til~0de",
// missed the destination map, and was reported as a destination-side DELETION.
//
// The root key is returned verbatim: it is one pointer segment produced by the
// adapter's own render and is only ever used to index the decoded destination
// object, never as a path. Crush's "mcp", Zed's "context_servers", Copilot's
// "servers" and Amp's flat dotted "amp.mcpServers" all pass through unchanged.
func TestKeyItemPointerParts(t *testing.T) {
	tests := []struct {
		name     string
		ptr      string
		wantRoot string
		wantID   string
		wantOK   bool
	}{
		{"claude", "/mcpServers/github", "mcpServers", "github", true},
		{"opencode", "/mcp/github", "mcp", "github", true},
		{"codex", "/mcp_servers/github", "mcp_servers", "github", true},
		{"zed", "/context_servers/github", "context_servers", "github", true},
		{"copilot", "/servers/github", "servers", "github", true},
		{"amp flat dotted root", "/amp.mcpServers/github", "amp.mcpServers", "github", true},
		{"deeper pointer keeps the id", "/mcpServers/github/env/TOKEN", "mcpServers", "github", true},
		{"tilde is decoded", "/mcpServers/til~0de", "mcpServers", "til~de", true},
		{"slash is decoded", "/mcpServers/a~1b", "mcpServers", "a/b", true},
		{"tilde-one is decoded in the right order", "/mcpServers/x~01", "mcpServers", "x~1", true},
		{"root key is decoded too", "/a~1b/github", "a/b", "github", true},
		{"hook event segment", "/hooks/BeforeTool", "hooks", "BeforeTool", true},
		{"root only", "/mcpServers", "", "", false},
		{"empty id", "/mcpServers/", "", "", false},
		{"empty pointer", "", "", "", false},
		{"unrooted pointer is tolerated", "mcpServers/github", "mcpServers", "github", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root, id, ok := keyItemPointerParts(tc.ptr)
			if root != tc.wantRoot || id != tc.wantID || ok != tc.wantOK {
				t.Errorf("keyItemPointerParts(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.ptr, root, id, ok, tc.wantRoot, tc.wantID, tc.wantOK)
			}
		})
	}
}

// TestPointerSourceFile_KindFromSourceID pins the pointer→canonical-source
// inversion now that the kind is derived. Before this, a second root allowlist
// ("mcpServers"/"mcp"/"mcp_servers", "lspServers"/"lsp") answered "" for every
// breadth-tier root nobody had added: `explain <path>#/context_servers/<id>`
// (Zed), `#/servers/<id>` (Copilot) and `#/amp.mcpServers/<id>` (Amp) reported
// "assembled from several canonical sources" instead of naming mcp/<id>.toml,
// and reconcile's multi-agent fan-out guard could not see those items at all.
func TestPointerSourceFile_KindFromSourceID(t *testing.T) {
	reg := registryFactory()
	const home = "/src"
	events := []string{"PreToolUse"}

	tests := []struct {
		name     string
		agent    string
		sourceID string
		ptr      string
		want     string
	}{
		{"claude mcp", "claude", "mcp/* (multiple)", "/mcpServers/github", filepath.Join(home, "mcp", "github.toml")},
		{"opencode mcp", "opencode", "mcp/* (multiple)", "/mcp/github", filepath.Join(home, "mcp", "github.toml")},
		{"codex mcp", "codex", "mcp/* (multiple)", "/mcp_servers/github", filepath.Join(home, "mcp", "github.toml")},
		{"zed mcp (was unresolvable)", "zed", "mcp/* (multiple)", "/context_servers/github", filepath.Join(home, "mcp", "github.toml")},
		{"copilot mcp (was unresolvable)", "copilot", "mcp/* (multiple)", "/servers/github", filepath.Join(home, "mcp", "github.toml")},
		{"amp mcp (was unresolvable)", "amp", "mcp/* (multiple)", "/amp.mcpServers/github", filepath.Join(home, "mcp", "github.toml")},
		{"crush mcp is crush, not opencode", "crush", "mcp/* (multiple)", "/mcp/github", filepath.Join(home, "mcp", "github.toml")},
		{"tilde id is decoded into the filename", "claude", "mcp/* (multiple)", "/mcpServers/til~0de", filepath.Join(home, "mcp", "til~de.toml")},
		{"lsp, unreachable today", "claude", "lsp/* (multiple)", "/lspServers/gopls", filepath.Join(home, "lsp", "gopls.toml")},
		{"claude hook passes through", "claude", "hooks/* (multiple)", "/hooks/PreToolUse", filepath.Join(home, "hooks", "PreToolUse.toml")},
		{"gemini hook is inverted", "gemini", "hooks/* (multiple)", "/hooks/BeforeTool", filepath.Join(home, "hooks", "PreToolUse.toml")},
		{"a hook with no canonical equivalent", "gemini", "hooks/* (multiple)", "/hooks/BeforeModel", ""},
		{"a root key alone names no entry", "claude", "mcp/* (multiple)", "/mcpServers", ""},
		{"an op with no per-entry provenance", "claude", "", "/mcpServers/github", ""},
		{"a non-key component SourceID", "claude", "skills/demo/SKILL.md", "/mcpServers/github", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pointerSourceFile(reg, home, tc.agent, tc.sourceID, tc.ptr, events)
			if got != tc.want {
				t.Errorf("pointerSourceFile(%s, %q, %q) = %q, want %q", tc.agent, tc.sourceID, tc.ptr, got, tc.want)
			}
		})
	}
}

// TestComponentFromPointer_KindFromSourceID pins `explain`'s (kind, name) for a
// key-merge pointer, now that it shares keyItemKind / keyItemPointerParts with
// reconcile. It was the fourth copy of the pointer-root allowlist: a breadth-tier
// root nobody had listed answered ("", "") — so `explain <path>#<ptr>` printed no
// component line for zed/copilot/amp items and matched none of their skips,
// plugin origins or secret references — and a `~`-bearing id was named under its
// escaped spelling.
func TestComponentFromPointer_KindFromSourceID(t *testing.T) {
	reg := registryFactory()
	events := []string{"PreToolUse"}

	tests := []struct {
		name     string
		agent    string
		sourceID string
		ptr      string
		wantKind string
		wantName string
	}{
		{"claude mcp", "claude", "mcp/* (multiple)", "/mcpServers/github", "mcp", "github"},
		{"opencode mcp", "opencode", "mcp/* (multiple)", "/mcp/github", "mcp", "github"},
		{"codex mcp", "codex", "mcp/* (multiple)", "/mcp_servers/github", "mcp", "github"},
		{"zed mcp (was unresolvable)", "zed", "mcp/* (multiple)", "/context_servers/github", "mcp", "github"},
		{"copilot mcp (was unresolvable)", "copilot", "mcp/* (multiple)", "/servers/github", "mcp", "github"},
		{"amp mcp (was unresolvable)", "amp", "mcp/* (multiple)", "/amp.mcpServers/github", "mcp", "github"},
		{"crush mcp is crush, not opencode", "crush", "mcp/* (multiple)", "/mcp/github", "mcp", "github"},
		{"tilde id is decoded into the name", "claude", "mcp/* (multiple)", "/mcpServers/til~0de", "mcp", "til~de"},
		{"lsp, unreachable today", "claude", "lsp/* (multiple)", "/lspServers/gopls", "lsp", "gopls"},
		{"claude hook passes through", "claude", "hooks/* (multiple)", "/hooks/PreToolUse", "hook", "PreToolUse"},
		{"gemini hook is inverted", "gemini", "hooks/* (multiple)", "/hooks/BeforeTool", "hook", "PreToolUse"},
		{"a hook with no canonical equivalent keeps the kind", "gemini", "hooks/* (multiple)", "/hooks/BeforeModel", "hook", ""},
		{"a root key alone names no entry", "claude", "mcp/* (multiple)", "/mcpServers", "", ""},
		{"an op with no per-entry provenance", "claude", "", "/mcpServers/github", "", ""},
		{"a non-key component SourceID", "claude", "skills/demo/SKILL.md", "/mcpServers/github", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			kind, name := componentFromPointer(reg, tc.agent, tc.sourceID, tc.ptr, events)
			if kind != tc.wantKind || name != tc.wantName {
				t.Errorf("componentFromPointer(%s, %q, %q) = (%q, %q), want (%q, %q)",
					tc.agent, tc.sourceID, tc.ptr, kind, name, tc.wantKind, tc.wantName)
			}
		})
	}
}

// noInverseAdapter is an adapter that is REGISTERED but declares no
// MCPSpecIngester: the shape the registry-wide guard makes unrepresentable for
// the real adapters, driven here by hand so the refusal it exists to guard
// (writeBackKeyItem's "declares no native MCP translation") has a test that
// fails when the refusal is removed. The embedded Adapter is nil — only Name()
// is called on the way to the refusal.
type noInverseAdapter struct {
	adapter.Adapter
	name string
}

func (a noInverseAdapter) Name() string { return a.name }

// TestWriteBackKeyItem_Refusals pins every refusal a key-item write-back can
// raise before any translation happens, and that the id surfaced in the
// entry refusal is sanitized (the id comes from a native file, so a control
// byte in it must not reach the terminal raw — issue #93/#171). These used to
// fail no test: replacing any of them with `return nil` was green across the
// package, which would have turned a plausible hand-edit into a silent
// "write-back:" lie.
func TestWriteBackKeyItem_Refusals(t *testing.T) {
	testenv.RequireContainer(t)
	reg := registryFactory()
	if err := reg.Register(noInverseAdapter{name: "noinverse"}); err != nil {
		t.Fatal(err)
	}
	// cmd is set so a refusal that regressed into a fall-through reports as the
	// assertion below, not as a nil-deref panic at capture.Capture's Warn.
	s := &reconcileSession{reg: reg, home: t.TempDir(), cmd: &cobra.Command{}}
	item := func(agent, strategy, dest, ptr string) reconcileItem {
		return reconcileItem{
			agentName: agent,
			op:        adapter.FileOp{Path: dest, MergeStrategy: strategy, SourceID: "mcp/* (multiple)"},
			ptr:       ptr,
		}
	}
	tests := []struct {
		name     string
		agent    string
		strategy string
		dest     string // the destination's bytes; "" with missing=true means no file at all
		missing  bool
		ptr      string
		want     []string // substrings the error must carry
		reject   []string // substrings it must NOT carry
	}{
		{
			name: "root key absent", agent: "claude", strategy: "merge-json-keys", dest: `{}`, ptr: "/mcpServers/github",
			want: []string{"mcpServers", "absent"},
		},
		{
			name: "root key is an array", agent: "claude", strategy: "merge-json-keys", dest: `{"mcpServers": []}`, ptr: "/mcpServers/github",
			want: []string{"mcpServers", "not an object"},
		},
		{
			name: "root key is a scalar", agent: "claude", strategy: "merge-json-keys", dest: `{"mcpServers": 1}`, ptr: "/mcpServers/github",
			want: []string{"mcpServers", "not an object"},
		},
		{
			name: "entry is a scalar", agent: "claude", strategy: "merge-json-keys", dest: `{"mcpServers": {"github": 5}}`, ptr: "/mcpServers/github",
			want: []string{"claude", "github", "not an object"},
		},
		{
			name: "entry is an array", agent: "claude", strategy: "merge-json-keys", dest: `{"mcpServers": {"github": []}}`, ptr: "/mcpServers/github",
			want: []string{"github", "not an object"},
		},
		{
			// The `>`-clobber shape. Whichever refusal names it, it must be refused:
			// a zero-byte destination holds no server to write back.
			name: "zero-byte file", agent: "claude", strategy: "merge-json-keys", dest: "", ptr: "/mcpServers/github",
			want: []string{"mcpServers"},
		},
		{
			// A destination the user broke between the drift walk and the [w]
			// keystroke: readDestFile would swallow the parse error and report a
			// missing root key; the by-hand read names the real cause.
			name: "truncated JSON", agent: "claude", strategy: "merge-json-keys", dest: `{ "mcpServers": `, ptr: "/mcpServers/github",
			want: []string{"does not parse", "merge-json-keys"}, reject: []string{"absent from the destination"},
		},
		{
			// TOML decodes through a different arm than JSON/JSONC, and codex's
			// config.toml is a live write-back destination.
			name: "truncated TOML", agent: "codex", strategy: "merge-toml-keys", dest: "[mcp_servers.github\n", ptr: "/mcp_servers/github",
			want: []string{"does not parse", "merge-toml-keys"}, reject: []string{"absent from the destination"},
		},
		{
			name: "missing file", agent: "claude", strategy: "merge-json-keys", missing: true, ptr: "/mcpServers/github",
			want: []string{"read destination", "[o]verride"},
		},
		{
			// Reachable only by hand: the registry-wide guard makes a REAL
			// adapter that renders MCP key items without the inverse
			// unrepresentable. The refusal must still refuse — a nil here would
			// call a nil interface, and a guess at a dialect would be worse.
			name: "agent declares no inverse", agent: "noinverse", strategy: "merge-json-keys",
			dest: `{"mcpServers": {"github": {"command": "npx"}}}`, ptr: "/mcpServers/github",
			want: []string{"noinverse", "declares no native MCP translation"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "dest")
			if !tc.missing {
				if err := os.WriteFile(dest, []byte(tc.dest), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := s.writeBackKeyItem(item(tc.agent, tc.strategy, dest, tc.ptr))
			if err == nil {
				t.Fatal("writeBackKeyItem = nil, want a refusal: a nil here prints \"write-back:\" for an edit that was not persisted")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err = %q, want it to mention %q", err, w)
				}
			}
			for _, r := range tc.reject {
				if strings.Contains(err.Error(), r) {
					t.Errorf("err = %q names the wrong cause (%q)", err, r)
				}
			}
			if tc.missing {
				// os.ReadFile's error carries the path too; the refusal must not
				// print it twice ("read destination X: open X: …").
				if n := strings.Count(err.Error(), dest); n != 1 {
					t.Errorf("err = %q names the path %d times, want once", err, n)
				}
			}
		})
	}

	t.Run("a control byte in the id is sanitized on the way out", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "claude.json")
		// The id holds an ESC byte (JSON-escaped so the file parses, as a native
		// file would spell it); the entry is a scalar so the refusal that
		// interpolates the id is the one that fires.
		if err := os.WriteFile(dest, []byte(`{"mcpServers": {"bad\u001b[31mid": 5}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		err := s.writeBackKeyItem(item("claude", "merge-json-keys", dest, "/mcpServers/bad\x1b[31mid"))
		if err == nil {
			t.Fatal("want a refusal")
		}
		if !strings.Contains(err.Error(), "not an object") {
			t.Fatalf("err = %q, want the entry refusal (the one that names the id)", err)
		}
		if strings.Contains(err.Error(), "\x1b") {
			t.Fatalf("err = %q carries the raw ESC byte from the native id", err)
		}
	})
}

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
// MCPSpecIngester: the shape the registry-wide guard turns into a failing test
// for the real adapters, driven here by hand so the refusal it exists to guard
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
	item := func(agent, strategy, sourceID, dest, ptr string) reconcileItem {
		return reconcileItem{
			agentName: agent,
			op:        adapter.FileOp{Path: dest, MergeStrategy: strategy, SourceID: sourceID},
			ptr:       ptr,
		}
	}
	tests := []struct {
		name     string
		agent    string
		strategy string
		sourceID string // "" means the ordinary "mcp/* (multiple)"
		dest     string // the destination's bytes; "" with missing=true means no file at all
		missing  bool
		dir      bool // the destination path is a directory
		ptr      string
		want     []string // substrings the error must carry
		reject   []string // substrings it must NOT carry
	}{
		{
			// The kind gate: a key item whose SourceID is not an MCP section is
			// refused before anything is read. TestReconcile_HookWriteBackIsRefused
			// pins it end to end; this row pins it beside its siblings.
			name: "component kind is not mcp", agent: "claude", strategy: "merge-json-keys",
			sourceID: "hooks/* (multiple)", dest: `{"hooks": {"PreToolUse": []}}`, ptr: "/hooks/PreToolUse",
			want: []string{"not implemented in v1", "MCP-server items"},
		},
		{
			// Defensive in production (the render never emits a one-segment
			// pointer for an object root), reachable here: the refusal must
			// still refuse rather than index a nil map.
			name: "pointer names no server", agent: "claude", strategy: "merge-json-keys",
			dest: `{"mcpServers": {"github": {"command": "npx"}}}`, ptr: "/mcpServers",
			want: []string{"names no MCP server"},
		},
		{
			// The non-regular arm of the read refusal: no [o]verride offered,
			// because the re-render's convergence read would hang on it (#241).
			name: "destination is a directory", agent: "claude", strategy: "merge-json-keys",
			dir: true, ptr: "/mcpServers/github",
			want:   []string{"read destination", "not a regular file", "remove or replace", "[i]gnore"},
			reject: []string{"[o]verride"},
		},
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
			// Reachable only by hand: for a REAL adapter that renders MCP key
			// items without the inverse, the registry-wide guard is a failing
			// test. The refusal must still refuse — a nil here would call a nil
			// interface, and a guess at a dialect would be worse.
			name: "agent declares no inverse", agent: "noinverse", strategy: "merge-json-keys",
			dest: `{"mcpServers": {"github": {"command": "npx"}}}`, ptr: "/mcpServers/github",
			want: []string{"noinverse", "declares no native MCP translation"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "dest")
			switch {
			case tc.dir:
				if err := os.Mkdir(dest, 0o755); err != nil {
					t.Fatal(err)
				}
			case !tc.missing:
				if err := os.WriteFile(dest, []byte(tc.dest), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			sourceID := tc.sourceID
			if sourceID == "" {
				sourceID = "mcp/* (multiple)"
			}
			err := s.writeBackKeyItem(item(tc.agent, tc.strategy, sourceID, dest, tc.ptr))
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
			if tc.missing || tc.dir {
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
		err := s.writeBackKeyItem(item("claude", "merge-json-keys", "mcp/* (multiple)", dest, "/mcpServers/bad\x1b[31mid"))
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

// TestWriteBackFileItem_ReadRefusalNamesThePathOnce pins the whole-file arm of
// destReadRefusal the way the key-item table pins its own: os.ReadFile's error
// already carries the path, so the message must not print it twice.
func TestWriteBackFileItem_ReadRefusalNamesThePathOnce(t *testing.T) {
	testenv.RequireContainer(t)
	dest := filepath.Join(t.TempDir(), "gone.md")
	err := writeBackFileItem(t.TempDir(), reconcileItem{op: adapter.FileOp{Path: dest, SourceID: "demo"}})
	if err == nil {
		t.Fatal("writeBackFileItem = nil for a missing destination, want the read refusal")
	}
	if !strings.Contains(err.Error(), "[o]verride") {
		t.Fatalf("err = %q, want the absent-destination remedy", err)
	}
	if n := strings.Count(err.Error(), dest); n != 1 {
		t.Fatalf("err = %q names the path %d times, want once", err, n)
	}
}

// TestWriteBackFileItem_Refusals pins the whole-file arm's own refusals the way
// TestWriteBackKeyItem_Refusals pins the key item's: each row is one
// `return fmt.Errorf(...)` in writeBackFileItem that must fail a test when it
// becomes `return nil`. The two SourceID refusals had no such test. The
// secret-bearing-kind refusal is new: Continue's per-server MCP file — the one
// whole-file render of a kind walkSecretFields visits — used to be copied
// VERBATIM into ~/.agentsync/mcp/<id>.toml: the agent's YAML into a canonical
// TOML file, with the secrets the render had resolved in cleartext, outside
// capture.Capture. A refusal that still wrote would be the same bug with a
// message, so every row also asserts the canonical tree stayed empty.
func TestWriteBackFileItem_Refusals(t *testing.T) {
	testenv.RequireContainer(t)
	tests := []struct {
		name     string
		agent    string
		sourceID string
		want     []string
	}{
		{
			name: "no SourceID", agent: "claude", sourceID: "",
			want: []string{"requires a single source-of-record", "[o]verride"},
		},
		{
			name: "multiple source fragments", agent: "claude", sourceID: "skills/* (multiple)",
			want: []string{"concatenation of multiple source fragments", "strand the others"},
		},
		{
			name: "mcp whole-file render (continue)", agent: "continue", sourceID: filepath.Join("mcp", "gh.toml"),
			want: []string{
				"refused", "continue's whole-file render of the mcp component \"gh\"", "verbatim", "cleartext",
				"`agentsync import continue:mcp:gh`", "[o]verride",
			},
		},
		{
			name: "lsp whole-file render", agent: "x", sourceID: "lsp/gopls.toml",
			want: []string{"refused", "lsp component \"gopls\"", "`agentsync import x:lsp:gopls`"},
		},
		{
			name: "hooks whole-file render", agent: "x", sourceID: "hooks/PreToolUse.toml",
			want: []string{"refused", "hooks component \"PreToolUse\"", "`agentsync import x:hook:PreToolUse`"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dest := filepath.Join(t.TempDir(), "dest.yaml")
			if err := os.WriteFile(dest, []byte("mcpServers:\n- name: gh\n  command: npm\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			err := writeBackFileItem(home, reconcileItem{agentName: tc.agent, op: adapter.FileOp{Path: dest, SourceID: tc.sourceID}})
			if err == nil {
				t.Fatal("writeBackFileItem = nil, want a refusal")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err = %q\n  missing %q", err, w)
				}
			}
			entries, rerr := os.ReadDir(home)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if len(entries) != 0 {
				t.Errorf("a refusal still wrote into the canonical tree: %v", entries)
			}
		})
	}
	// Positive control: a text component — whose canonical form IS the rendered
	// text — still writes back verbatim, so the kind gate is not over-broad.
	t.Run("text component still writes back verbatim", func(t *testing.T) {
		home := t.TempDir()
		dest := filepath.Join(t.TempDir(), "hello.md")
		const body = "# hello\n"
		if err := os.WriteFile(dest, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		it := reconcileItem{agentName: "claude", op: adapter.FileOp{Path: dest, SourceID: filepath.Join("commands", "hello.md")}}
		if err := writeBackFileItem(home, it); err != nil {
			t.Fatalf("writeBackFileItem = %v for a command, want the verbatim write", err)
		}
		got, err := os.ReadFile(filepath.Join(home, "commands", "hello.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body {
			t.Fatalf("canonical commands/hello.md = %q, want %q", got, body)
		}
	})
}

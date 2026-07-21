package source_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestWriteSkill_PrunesRemovedBundledFiles is the artifact-anchored regression
// for issue #162 item A: WriteSkill must be idempotent w.r.t. deletions. It
// writes a skill with two bundled files to a real temp dir, reloads it, drops
// one bundled file from the model, re-writes, and asserts the orphan file AND
// its now-empty subdir are gone on disk while the surviving files (SKILL.md +
// references/r.md, including the +x bit on the survivor) are byte-identical.
func TestWriteSkill_PrunesRemovedBundledFiles(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	sk := source.Skill{
		Name:        "demo",
		Frontmatter: map[string]any{"name": "demo"},
		Body:        "skill body\n",
		Files: []source.SkillFile{
			{Path: "scripts/a.sh", Content: []byte("#!/bin/sh\necho a\n"), Mode: 0o755},
			{Path: "references/r.md", Content: []byte("ref content\n"), Mode: 0o644},
		},
	}
	if err := source.WriteSkill(home, sk); err != nil {
		t.Fatalf("WriteSkill: %v", err)
	}
	dir := filepath.Join(home, "skills", "demo")

	// Reload via the loader so the assertion oracle is the on-disk tree.
	loaded, err := source.Load(afero.NewOsFs(), home)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Skills) != 1 || len(loaded.Skills[0].Files) != 2 {
		t.Fatalf("expected 1 skill with 2 bundled files, got %+v", loaded.Skills)
	}
	// The executable bit must survive the FIRST write.
	if fi, serr := os.Stat(filepath.Join(dir, "scripts", "a.sh")); serr != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("scripts/a.sh: err=%v perm=%v (want 0755)", serr, fi.Mode().Perm())
	}

	// Drop scripts/a.sh from the model and re-write.
	pruned := loaded.Skills[0]
	var keep []source.SkillFile
	for _, f := range pruned.Files {
		if f.Path != "scripts/a.sh" {
			keep = append(keep, f)
		}
	}
	pruned.Files = keep
	if err := source.WriteSkill(home, pruned); err != nil {
		t.Fatalf("WriteSkill (re-write with deletion): %v", err)
	}

	// The orphan file must be gone and its now-empty scripts/ dir pruned.
	if _, serr := os.Stat(filepath.Join(dir, "scripts", "a.sh")); !os.IsNotExist(serr) {
		t.Fatalf("orphan scripts/a.sh survived: err=%v", serr)
	}
	if _, serr := os.Stat(filepath.Join(dir, "scripts")); !os.IsNotExist(serr) {
		t.Fatalf("emptied scripts/ dir not pruned: err=%v", serr)
	}
	// Survivors are byte-identical.
	if b, rerr := os.ReadFile(filepath.Join(dir, "references", "r.md")); rerr != nil || string(b) != "ref content\n" {
		t.Fatalf("references/r.md lost/changed: %q err=%v", b, rerr)
	}
	if _, serr := os.Stat(filepath.Join(dir, "SKILL.md")); serr != nil {
		t.Fatalf("SKILL.md lost: %v", serr)
	}
	// The skill dir itself must remain (prune never removes the root).
	if _, serr := os.Stat(dir); serr != nil {
		t.Fatalf("skill dir removed by prune: %v", serr)
	}
}

// TestWriteReadMCP_ExtraRoundTrip is artifact-anchored (item B): it starts from
// an on-disk mcp/<id>.toml whose [server.extra] table holds unmodeled native
// keys, then ReadMCP → WriteMCP → ReadMCP and asserts the Extra passthrough
// survives byte-for-byte (value-identically). Table-drives stdio/http/sse.
func TestWriteReadMCP_ExtraRoundTrip(t *testing.T) {
	testenv.RequireContainer(t)
	cases := []struct {
		name      string
		toml      string
		wantExtra map[string]any
	}{
		{
			name:      "stdio",
			toml:      "[server]\ntype = \"stdio\"\ncommand = \"npx\"\n\n[server.extra]\ntimeout = 30\ncwd = \"/work\"\n",
			wantExtra: map[string]any{"timeout": int64(30), "cwd": "/work"},
		},
		{
			name:      "http",
			toml:      "[server]\ntype = \"http\"\nurl = \"https://mcp.example.com\"\n\n[server.extra]\ndisabled = true\n",
			wantExtra: map[string]any{"disabled": true},
		},
		{
			name:      "sse",
			toml:      "[server]\ntype = \"sse\"\nurl = \"https://sse.example.com\"\n\n[server.extra]\nlabel = \"prod\"\n",
			wantExtra: map[string]any{"label": "prod"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.MkdirAll(filepath.Join(home, "mcp"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "mcp", "srv.toml"), []byte(tc.toml), 0o644); err != nil {
				t.Fatal(err)
			}
			m, ok, err := source.ReadMCP(home, "srv")
			if err != nil || !ok {
				t.Fatalf("ReadMCP: ok=%v err=%v", ok, err)
			}
			if !reflect.DeepEqual(m.Server.Extra, tc.wantExtra) {
				t.Fatalf("Extra after first read = %#v, want %#v", m.Server.Extra, tc.wantExtra)
			}
			if err := source.WriteMCP(home, "srv", m); err != nil {
				t.Fatalf("WriteMCP: %v", err)
			}
			// The re-written .toml must still carry the extra table.
			raw, err := os.ReadFile(filepath.Join(home, "mcp", "srv.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "[server.extra]") {
				t.Fatalf("re-written mcp/srv.toml dropped the [server.extra] table:\n%s", raw)
			}
			m2, ok, err := source.ReadMCP(home, "srv")
			if err != nil || !ok {
				t.Fatalf("ReadMCP (round-trip): ok=%v err=%v", ok, err)
			}
			if !reflect.DeepEqual(m2.Server.Extra, tc.wantExtra) {
				t.Fatalf("Extra after round-trip = %#v, want %#v", m2.Server.Extra, tc.wantExtra)
			}
		})
	}
}

// TestWriteReadLSP_ExtraRoundTrip mirrors the MCP test for LSP (item B): the
// LSPServerSpec.Extra passthrough must round-trip through WriteLSP/ReadLSP.
func TestWriteReadLSP_ExtraRoundTrip(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "lsp"), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := "[server]\ncommand = \"gopls\"\n\n[server.extra]\ninit_timeout = 5\nroot_marker = \"go.mod\"\n"
	if err := os.WriteFile(filepath.Join(home, "lsp", "go.toml"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	wantExtra := map[string]any{"init_timeout": int64(5), "root_marker": "go.mod"}

	ls, ok, err := source.ReadLSP(home, "go")
	if err != nil || !ok {
		t.Fatalf("ReadLSP: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(ls.Spec.Extra, wantExtra) {
		t.Fatalf("LSP Extra after read = %#v, want %#v", ls.Spec.Extra, wantExtra)
	}
	if err := source.WriteLSP(home, ls); err != nil {
		t.Fatalf("WriteLSP: %v", err)
	}
	ls2, ok, err := source.ReadLSP(home, "go")
	if err != nil || !ok {
		t.Fatalf("ReadLSP (round-trip): ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(ls2.Spec.Extra, wantExtra) {
		t.Fatalf("LSP Extra after round-trip = %#v, want %#v", ls2.Spec.Extra, wantExtra)
	}
}

// TestLoadComponents_RejectsUnknownKey is the strict-decode regression for item
// C: a typo'd key in any per-component TOML (mcp/plugins/marketplaces/hooks/lsp)
// must be SURFACED as a load error, never silently dropped — while the MCP/LSP
// [server.extra] passthrough and the deliberate marketplace fetch-cache keys
// (head_sha/name) still load cleanly.
func TestLoadComponents_RejectsUnknownKey(t *testing.T) {
	testenv.RequireContainer(t)
	cases := []struct {
		name     string
		rel      string
		content  string
		wantErr  string // substring the load error must contain; "" = expect success
		wantMCP  bool   // on success, assert MCP Extra preserved
		wantLSPx bool   // on success, assert LSP Extra preserved
	}{
		{"mcp typo", "mcp/x.toml", "[server]\ntype = \"stdio\"\ncomand = \"npx\"\n", "comand", false, false},
		{"mcp extra preserved", "mcp/y.toml", "[server]\ntype = \"stdio\"\ncommand = \"npx\"\n[server.extra]\ntimeout = 30\n", "", true, false},
		{"plugin typo", "plugins/p.toml", "[plugin]\nid = \"a@b\"\nupdat = \"track\"\n", "updat", false, false},
		{"marketplace typo", "marketplaces/m.toml", "[marketplace]\nurl = \"https://x\"\ndeafult_update_mode = \"track\"\n", "deafult_update_mode", false, false},
		{"marketplace cache keys tolerated", "marketplaces/n.toml", "[marketplace]\nurl = \"https://x\"\nhead_sha = \"abc123\"\nname = \"n\"\n", "", false, false},
		{"hook typo", "hooks/PreToolUse.toml", "[[hook]]\ntype = \"command\"\ncomand = \"echo\"\n", "comand", false, false},
		{"lsp typo", "lsp/l.toml", "[server]\ncomand = \"gopls\"\n", "comand", false, false},
		{"lsp extra preserved", "lsp/e.toml", "[server]\ncommand = \"gopls\"\n[server.extra]\nfoo = \"bar\"\n", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			home := "/home/.agentsync"
			if err := afero.WriteFile(fs, filepath.Join(home, tc.rel), []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			c, err := source.Load(fs, home)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected a strict error mentioning %q, got nil (silent drop)", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention the typo'd key %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected clean load, got: %v", err)
			}
			if tc.wantMCP {
				if len(c.MCPServers) != 1 || c.MCPServers[0].Server.Extra["timeout"] != int64(30) {
					t.Fatalf("MCP Extra not preserved: %+v", c.MCPServers)
				}
			}
			if tc.wantLSPx {
				if len(c.LSPServers) != 1 || c.LSPServers[0].Spec.Extra["foo"] != "bar" {
					t.Fatalf("LSP Extra not preserved: %+v", c.LSPServers)
				}
			}
		})
	}
}

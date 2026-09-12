package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// writeCanonicalMCP seeds one canonical mcp/<id>.toml under home.
func writeCanonicalMCP(t *testing.T, home, id, body string) string {
	t.Helper()
	p := filepath.Join(home, "mcp", id+".toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestReconcile_Writeback_MCPDialects is the regression for reconcile's
// key-level write-back selecting the native→canonical MCP translation by the
// JSON POINTER'S ROOT KEY instead of by the rendering adapter.
//
// Root keys are per-agent data and they collide, so the old allowlist
// ("mcpServers" → Claude's 1:1 JSON shape, "mcp" → OpenCode, "mcp_servers" →
// Codex, anything else refused) was wrong in three distinct ways at once. Each
// row below is one of them, driven end to end — apply, hand-edit the native MCP
// entry, `reconcile --auto-writeback`, then assert the canonical file gained
// EXACTLY the edit:
//
//	crush     root "mcp"              collides with OpenCode → its entry was
//	                                  translated with OpenCode's dialect
//	                                  (array `command`, `environment`), which
//	                                  demoted `args`/`env` into [server.extra]
//	gemini    root "mcpServers"       collides with Claude → `httpUrl` was not
//	                                  inverted, so the URL left the model
//	                                  entirely and landed in [server.extra]
//	windsurf  root "mcpServers"       same, for `serverUrl`
//	amp       root "amp.mcpServers"   matched nothing → refused outright
//	                                  ("not implemented in v1"), exit 1
//	zed       root "context_servers"  same
//
// The generic-tier rows (crush, amp, zed) are the ones no allowlist could ever
// have covered: generic.Specs() grows with every agent added.
func TestReconcile_Writeback_MCPDialects(t *testing.T) {
	const remoteSrc = `[server]
type = "http"
url = "https://api.example.com/mcp"
agents = ["%s"]
enabled = true
[server.headers]
Authorization = "Bearer tok"
`
	const stdioSrc = `[server]
type = "stdio"
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]
agents = ["%s"]
enabled = true
[server.env]
GITHUB_TOKEN = "tok"
`
	cases := []struct {
		name   string
		agent  string
		native string // path relative to AGENTSYNC_TARGET_ROOT
		src    string // canonical mcp/github.toml body (%s = agent name)
		old    string // substring to replace in the native file
		new    string
		want   []string // substrings the canonical file must contain afterwards
		reject []string // substrings it must NOT contain
	}{
		{
			name: "crush root mcp is not opencode", agent: "crush",
			native: ".config/crush/crush.json", src: stdioSrc,
			old: `"npx"`, new: `"npm"`,
			want: []string{"command = 'npm'", "args = ['-y', '@modelcontextprotocol/server-github']", "[server.env]", "GITHUB_TOKEN = 'tok'"},
			// OpenCode's inverse reads `environment`, not `env`, and a string
			// `command` as the whole argv — so args/env fell into Extra.
			reject: []string{"[server.extra]"},
		},
		{
			name: "gemini httpUrl is inverted", agent: "gemini",
			native: ".gemini/settings.json", src: remoteSrc,
			old: "api.example.com", new: "api.edited.com",
			want:   []string{"type = 'http'\n", "url = 'https://api.edited.com/mcp'"},
			reject: []string{"[server.extra]", "httpUrl"},
		},
		{
			name: "windsurf serverUrl is inverted", agent: "windsurf",
			native: ".codeium/windsurf/mcp_config.json", src: remoteSrc,
			old: "api.example.com", new: "api.edited.com",
			want:   []string{"type = 'http'\n", "url = 'https://api.edited.com/mcp'"},
			reject: []string{"[server.extra]", "serverUrl"},
		},
		{
			name: "amp flat namespaced root", agent: "amp",
			native: ".config/amp/settings.json", src: remoteSrc,
			old: "api.example.com", new: "api.edited.com",
			want:   []string{"type = 'http'\n", "url = 'https://api.edited.com/mcp'"},
			reject: []string{"[server.extra]"},
		},
		{
			name: "zed context_servers root", agent: "zed",
			native: ".config/zed/settings.json", src: remoteSrc,
			old: "api.example.com", new: "api.edited.com",
			want:   []string{"type = 'http'\n", "url = 'https://api.edited.com/mcp'"},
			reject: []string{"[server.extra]"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
			if _, err := runCLI(t, env, "init"); err != nil {
				t.Fatal(err)
			}
			if out, err := runCLI(t, env, "agent", "add", tc.agent); err != nil {
				t.Fatalf("agent add %s: %v\n%s", tc.agent, err, out)
			}
			home := filepath.Join(tmp, ".agentsync")
			srcFile := writeCanonicalMCP(t, home, "github", strings.ReplaceAll(tc.src, "%s", tc.agent))
			if out, err := runCLI(t, env, "apply", "--scope", "user"); err != nil {
				t.Fatalf("apply: %v\n%s", err, out)
			}

			dest := filepath.Join(tmp, tc.native)
			body, err := os.ReadFile(dest)
			if err != nil {
				t.Fatalf("read rendered dest %s: %v", tc.native, err)
			}
			if !strings.Contains(string(body), tc.old) {
				t.Fatalf("rendered %s does not contain %q; the fixture no longer drives this dialect:\n%s",
					tc.native, tc.old, body)
			}
			edited := strings.Replace(string(body), tc.old, tc.new, 1)
			if err := os.WriteFile(dest, []byte(edited), 0o644); err != nil {
				t.Fatal(err)
			}

			out, err := runCLI(t, env, "reconcile", "--scope", "user", "--auto-writeback")
			if err != nil {
				t.Fatalf("reconcile --auto-writeback: %v\n%s", err, out)
			}
			if !strings.Contains(out, "write-back:") {
				t.Fatalf("reconcile did not report a write-back:\n%s", out)
			}
			got, err := os.ReadFile(srcFile)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !strings.Contains(string(got), w) {
					t.Errorf("canonical mcp/github.toml is missing %q:\n%s", w, got)
				}
			}
			for _, r := range tc.reject {
				if strings.Contains(string(got), r) {
					t.Errorf("canonical mcp/github.toml still carries %q — a modeled field was "+
						"demoted to passthrough, i.e. the wrong dialect was applied:\n%s", r, got)
				}
			}
			// Source-only fields the destination never carries must survive.
			for _, keep := range []string{"agents", "enabled"} {
				if !strings.Contains(string(got), keep) {
					t.Errorf("write-back dropped the source-only %s field:\n%s", keep, got)
				}
			}
		})
	}
}

// TestReconcile_Writeback_TildeServerID is the regression for a pointer segment
// being used RAW where RFC 6901 says it is escaped. An MCP server id containing
// "~" renders as the pointer segment "~0" (jsonkeys.EscapeToken), so looking the
// id up in the destination with the raw segment missed it — and a miss is the
// tombstone signal. Reconcile therefore reported
//
//	write-back: removed source mcp/til~0de.toml (destination dropped …)
//
// for a server that had merely been EDITED: the user's edit was discarded, the
// canonical file (spelled til~de.toml, so the unlink also missed) was left
// stale, and the next apply overwrote the destination back.
func TestReconcile_Writeback_TildeServerID(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, ".agentsync")
	srcFile := writeCanonicalMCP(t, home, "til~de", "[server]\ntype = \"stdio\"\ncommand = \"npx\"\nagents = [\"claude\"]\n")
	if out, err := runCLI(t, env, "apply", "--scope", "user"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	dest := filepath.Join(tmp, ".claude.json")
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte(strings.Replace(string(body), `"npx"`, `"npm"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "reconcile", "--scope", "user", "--auto-writeback")
	if err != nil {
		t.Fatalf("reconcile --auto-writeback: %v\n%s", err, out)
	}
	if strings.Contains(out, "removed source") {
		t.Fatalf("reconcile reported a destination-side DELETION for an edited server:\n%s", out)
	}
	got, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatalf("canonical mcp/til~de.toml: %v", err)
	}
	if !strings.Contains(string(got), "npm") {
		t.Fatalf("write-back did not capture the edit for a ~-bearing server id:\n%s", got)
	}
}

// TestReconcile_Writeback_SecretBearingDialect pins the SECRET half of the
// dialect fix. Routing a crush entry through OpenCode's inverse moved the
// server's `env` map into the `Extra` passthrough — which
// secrets.ReReferenceCanonical does not visit — so capture.Capture's
// fail-closed leak backstop (scanExtraResidual) correctly REFUSED the whole
// write-back of any secret-bearing crush server, and the user could never
// persist the edit at all.
//
// With the dialect taken from the rendering adapter the env map stays a modeled
// field, re-reference restores ${secret:…}, and the write succeeds. The
// assertion that matters either way: the resolved value never reaches
// ~/.agentsync.
func TestReconcile_Writeback_SecretBearingDialect(t *testing.T) {
	const sentinel = "ghp_SENTINEL_VALUE_DO_NOT_PERSIST"
	tmp := t.TempDir()
	t.Setenv("GH_TOKEN", sentinel)
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, ".agentsync")
	cfg := filepath.Join(home, "agentsync.toml")
	existing, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// backend = "env" resolves ${secret:K} from the environment, so the backstop
	// has a live secret value to detect without an age vault in the fixture.
	if err := os.WriteFile(cfg, append(existing, []byte("\n[secrets]\nbackend = \"env\"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, env, "agent", "add", "crush"); err != nil {
		t.Fatalf("agent add crush: %v\n%s", err, out)
	}
	srcFile := writeCanonicalMCP(t, home, "github", `[server]
type = "stdio"
command = "npx"
args = ["-y", "srv"]
agents = ["crush"]
[server.env]
GITHUB_TOKEN = "${secret:GH_TOKEN}"
`)
	if out, err := runCLI(t, env, "apply", "--scope", "user"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	dest := filepath.Join(tmp, ".config", "crush", "crush.json")
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), sentinel) {
		t.Fatalf("apply did not resolve the secret into the crush destination:\n%s", body)
	}
	if err := os.WriteFile(dest, []byte(strings.Replace(string(body), `"npx"`, `"npm"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "reconcile", "--scope", "user", "--auto-writeback")
	if err != nil {
		t.Fatalf("reconcile --auto-writeback refused a secret-bearing crush write-back: %v\n%s", err, out)
	}
	got, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "${secret:GH_TOKEN}") {
		t.Errorf("write-back did not restore the ${secret:…} reference:\n%s", got)
	}
	if !strings.Contains(string(got), "command = 'npm'") {
		t.Errorf("write-back did not capture the dest edit:\n%s", got)
	}
	// The load-bearing assertion: no resolved cleartext anywhere under
	// ~/.agentsync, whatever else happened.
	if err := filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		if strings.Contains(string(data), sentinel) {
			rel, _ := filepath.Rel(home, p)
			t.Errorf("resolved secret persisted into the canonical source at %s", rel)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestReconcile_Writeback_DifferentDialectsAgree pins the fourth symptom of the
// root-key allowlist: one canonical server fanned out to two agents with
// DIFFERENT dialects, both edited identically, used to trip the multi-agent
// fan-out guard — not because the edits differed, but because the
// mistranslated side (Gemini read as Claude: `httpUrl` dropped, `type` empty)
// produced a different canonical value from the correctly-translated one, so
// reconcile printed a spurious `conflict:` and exited non-zero. The existing
// identical-write-back test pairs claude with opencode over a STDIO server,
// which both dialects already read correctly; this one pairs claude with gemini
// over a REMOTE server, which only the rendering adapter's own inverse reads
// right.
func TestReconcile_Writeback_DifferentDialectsAgree(t *testing.T) {
	testenv.RequireContainer(t)
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	for _, a := range [][]string{{"init"}, {"agent", "add", "claude"}, {"agent", "add", "gemini"}} {
		if out, err := runCLI(t, env, a...); err != nil {
			t.Fatalf("%v: %v\n%s", a, err, out)
		}
	}
	home := filepath.Join(tmp, ".agentsync")
	srcFile := writeCanonicalMCP(t, home, "shared", "[server]\ntype = \"http\"\nurl = \"https://orig.example.com/mcp\"\nagents = [\"*\"]\n")
	if out, err := runCLI(t, env, "apply", "--scope", "user"); err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	for _, native := range []string{".claude.json", filepath.Join(".gemini", "settings.json")} {
		dest := filepath.Join(tmp, native)
		body, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("read rendered dest %s: %v", native, err)
		}
		if !strings.Contains(string(body), "orig.example.com") {
			t.Fatalf("rendered %s does not carry the server url:\n%s", native, body)
		}
		edited := strings.Replace(string(body), "orig.example.com", "same.example.com", 1)
		if err := os.WriteFile(dest, []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runCLI(t, env, "reconcile", "--scope", "user", "--auto-writeback")
	if err != nil {
		t.Fatalf("identical edits behind two dialects must not conflict: %v\n%s", err, out)
	}
	if strings.Contains(out, "conflict:") {
		t.Fatalf("reconcile reported a conflict between two agents that hold the SAME edit:\n%s", out)
	}
	got, err := os.ReadFile(srcFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"type = 'http'\n", "url = 'https://same.example.com/mcp'"} {
		if !strings.Contains(string(got), w) {
			t.Errorf("canonical mcp/shared.toml is missing %q:\n%s", w, got)
		}
	}
}

package capture_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/capture"
	"github.com/spxrogers/agentsync/internal/source"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCapture_ReReferencesAndPreserves drives Capture directly (no CLI) to prove
// the boundary both re-references a resolved secret AND preserves the
// source-only fields (agents + enabled) the rendered destination never carries.
func TestCapture_ReReferencesAndPreserves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOK", "live-secret-value")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	// Existing source: templated secret + source-only fields.
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"), ""+
		"[server]\n"+
		"type = \"stdio\"\n"+
		"command = \"npx\"\n"+
		"agents = [\"claude\"]\n"+
		"enabled = false\n"+
		"[server.env]\n"+
		"GH = \"${secret:TOK}\"\n")

	// Ingested-from-dest: secret resolved to cleartext, source-only fields absent.
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID: "srv",
		Server: source.MCPServerSpec{
			Type:    "stdio",
			Command: "npx",
			Env:     map[string]string{"GH": "live-secret-value"},
		},
	}}}

	res, err := capture.Capture(home, ingested, capture.Opts{})
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(res.Written) != 1 || res.Written[0] != "mcp/srv.toml" {
		t.Fatalf("unexpected Result.Written: %v", res.Written)
	}

	got, ok, err := source.ReadMCP(home, "srv")
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	if got.Server.Env["GH"] != "${secret:TOK}" {
		t.Errorf("secret not re-referenced: GH=%q", got.Server.Env["GH"])
	}
	if len(got.Server.Agents) != 1 || got.Server.Agents[0] != "claude" {
		t.Errorf("source-only agents not preserved: %v", got.Server.Agents)
	}
	if got.Server.Enabled == nil || *got.Server.Enabled {
		t.Errorf("source-only enabled not preserved: %v", got.Server.Enabled)
	}
}

// TestCapture_NoLeakOnStructuralEdit drives the full dest->source funnel for the
// exact scenario that leaked: a user adds a flag to an MCP server's args in the
// native UI (shifting the secret to a new index), then write-back is captured.
// The positional re-reference misses the shifted index; the value-based fallback
// must restore the ${secret:…} placeholder so the resolved credential is NEVER
// persisted into ~/.agentsync.
func TestCapture_NoLeakOnStructuralEdit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOK", "ghp_live_credential_value")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"), ""+
		"[server]\n"+
		"type = \"stdio\"\n"+
		"command = \"npx\"\n"+
		"args = [\"--token\", \"${secret:TOK}\"]\n")

	// Native edit prepended "--verbose"; the secret resolved to cleartext sits
	// at the new index 2.
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID: "srv",
		Server: source.MCPServerSpec{
			Type:    "stdio",
			Command: "npx",
			Args:    []string{"--verbose", "--token", "ghp_live_credential_value"},
		},
	}}}

	if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
		t.Fatalf("capture: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "mcp", "srv.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ghp_live_credential_value") {
		t.Fatalf("LEAK: resolved secret persisted into source:\n%s", raw)
	}
	got, _, _ := source.ReadMCP(home, "srv")
	if len(got.Server.Args) != 3 || got.Server.Args[2] != "${secret:TOK}" {
		t.Fatalf("shifted secret not re-referenced: %v", got.Server.Args)
	}
}

// TestCapture_RefusesMovedSecretIntoLiteralField is the fail-closed backstop
// for a secret MOVED onto a field whose source counterpart is a literal: source
// command="run-server" (literal) + a secret in env; the user inlines the token
// onto the command in the dest. Re-reference can't restore it (no templated
// counterpart there), so capture must REFUSE rather than persist cleartext.
func TestCapture_RefusesMovedSecretIntoLiteralField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOK", "ghp_live_credential_value")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"), ""+
		"[server]\ntype = \"stdio\"\ncommand = \"run-server\"\n[server.env]\nAUTH = \"${secret:TOK}\"\n")

	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "srv",
		Server: source.MCPServerSpec{Type: "stdio", Command: "run-server --token=ghp_live_credential_value"},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err == nil {
		t.Fatal("capture must REFUSE a secret moved into a literal-counterpart field, got nil")
	}
	// And it must NOT have written the cleartext.
	if raw, _ := os.ReadFile(filepath.Join(home, "mcp", "srv.toml")); strings.Contains(string(raw), "ghp_live_credential_value") {
		t.Fatalf("LEAK: cleartext persisted despite refusal:\n%s", raw)
	}
}

// TestCapture_RefusesShortSecretMovedIntoLiteralField is the analog of
// TestCapture_RefusesMovedSecretIntoLiteralField for a SHORT (1–3 char) secret.
// The re-reference value-based fallback intentionally skips values shorter than
// minReReferenceLen (=4), but the fail-closed backstop must NOT inherit that
// floor: a short credential moved onto a field whose source counterpart is a
// literal (so re-reference leaves it as cleartext) must still be refused rather
// than persisted into the committed canonical source. This fails on main (the
// backstop reused sourceSecretValues, which dropped the short value) and passes
// after decoupling the backstop onto backstopSecretValues.
func TestCapture_RefusesShortSecretMovedIntoLiteralField(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "1char", value: "a"},
		{name: "2char", value: "ab"},
		{name: "3char", value: "abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("TOK", tc.value)
			writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
			writeFile(t, filepath.Join(home, "mcp", "srv.toml"), ""+
				"[server]\ntype = \"stdio\"\ncommand = \"run-server\"\n[server.env]\nAUTH = \"${secret:TOK}\"\n")

			ingested := &source.Canonical{MCPServers: []source.MCPServer{{
				ID:     "srv",
				Server: source.MCPServerSpec{Type: "stdio", Command: "run-server --token=" + tc.value},
			}}}
			if _, err := capture.Capture(home, ingested, capture.Opts{}); err == nil {
				t.Fatal("capture must REFUSE a short secret moved into a literal-counterpart field, got nil")
			}
			if raw, _ := os.ReadFile(filepath.Join(home, "mcp", "srv.toml")); strings.Contains(string(raw), "--token="+tc.value) {
				t.Fatalf("LEAK: short cleartext persisted despite refusal:\n%s", raw)
			}
		})
	}
}

// TestCapture_EmptySecretNotFalseRefused proves the backstop does not
// false-refuse when a ${secret:K} resolves to the EMPTY string: an empty value
// has nothing to leak, and strings.Contains(field, "") is always true, so an
// unguarded detection set would refuse every benign write-back. A benign,
// unchanged write must succeed.
func TestCapture_EmptySecretNotFalseRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K", "")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"), ""+
		"[server]\ntype = \"stdio\"\ncommand = \"npx\"\n[server.env]\nAUTH = \"${secret:K}\"\n")

	// Unchanged write-back: apply resolved ${secret:K} to "" in the dest env.
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID: "srv",
		Server: source.MCPServerSpec{
			Type: "stdio", Command: "npx",
			Env: map[string]string{"AUTH": ""},
		},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
		t.Fatalf("empty-resolving secret must not be false-refused: %v", err)
	}
}

// TestCapture_RefusesRotatedSecret is the fail-closed backstop for a ROTATED
// secret: the source field is ${secret:}-templated, but the user changed the
// dest value to a NEW token the vault doesn't know. Re-reference can't match it,
// so capture must REFUSE rather than persist the new cleartext.
func TestCapture_RefusesRotatedSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOK", "ghp_old_value_xyz")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"), ""+
		"[server]\ntype = \"stdio\"\ncommand = \"npx\"\n[server.env]\nAUTH = \"${secret:TOK}\"\n")

	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID: "srv",
		Server: source.MCPServerSpec{
			Type: "stdio", Command: "npx",
			Env: map[string]string{"AUTH": "ghp_ROTATED_new_value"},
		},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err == nil {
		t.Fatal("capture must REFUSE a rotated (vault-unknown) secret value, got nil")
	}
	if raw, _ := os.ReadFile(filepath.Join(home, "mcp", "srv.toml")); strings.Contains(string(raw), "ghp_ROTATED_new_value") {
		t.Fatalf("LEAK: rotated cleartext persisted despite refusal:\n%s", raw)
	}
}

// TestCapture_AllowsLegitWriteBacks proves the backstop does NOT false-refuse:
// an unchanged secret (re-referenced cleanly) and a non-secret-part edit of a
// templated field (secret re-referenced, edit captured) both write successfully.
func TestCapture_AllowsLegitWriteBacks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOK", "ghp_live_value_abc")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"), ""+
		"[server]\ntype = \"stdio\"\ncommand = \"serve --port=8080 --tok=${secret:TOK}\"\n")

	// User edited the non-secret port; the secret resolved value is unchanged.
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "srv",
		Server: source.MCPServerSpec{Type: "stdio", Command: "serve --port=9090 --tok=ghp_live_value_abc"},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
		t.Fatalf("legit non-secret-part edit must write back, got refusal: %v", err)
	}
	got, _, _ := source.ReadMCP(home, "srv")
	if got.Server.Command != "serve --port=9090 --tok=${secret:TOK}" {
		t.Fatalf("expected port edit captured + secret re-referenced, got: %q", got.Server.Command)
	}
}

// TestCapture_SingleItemNotRefusedByUnrelatedSecret is the regression for a
// false-refusal: a single-item write-back (import claude:mcp:alpha) must NOT be
// blocked because a DIFFERENT source server (beta) has a ${secret:} the leak
// scan thinks is "missing" — only the items being written are relevant.
func TestCapture_SingleItemNotRefusedByUnrelatedSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AT", "alpha_secret_value")
	t.Setenv("BT", "beta_secret_value")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	writeFile(t, filepath.Join(home, "mcp", "alpha.toml"),
		"[server]\ntype=\"stdio\"\ncommand=\"a\"\n[server.env]\nK = \"${secret:AT}\"\n")
	writeFile(t, filepath.Join(home, "mcp", "beta.toml"),
		"[server]\ntype=\"stdio\"\ncommand=\"b\"\n[server.env]\nK = \"${secret:BT}\"\n")
	// Capture only alpha (unchanged secret value as apply wrote it).
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "alpha",
		Server: source.MCPServerSpec{Type: "stdio", Command: "a", Env: map[string]string{"K": "alpha_secret_value"}},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
		t.Fatalf("single-item write-back must not be refused by an unrelated server's secret: %v", err)
	}
	got, _, _ := source.ReadMCP(home, "alpha")
	if got.Server.Env["K"] != "${secret:AT}" {
		t.Fatalf("alpha secret not re-referenced: %q", got.Server.Env["K"])
	}
}

// TestCapture_RemovingSecretFieldAllowed is the regression for a false-refusal:
// removing a secret-bearing field entirely (the server is now public) is safe —
// there is no cleartext to persist — so capture must allow it, not refuse.
func TestCapture_RemovingSecretFieldAllowed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOK", "live_secret_value")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"),
		"[server]\ntype=\"stdio\"\ncommand=\"npx\"\n[server.env]\nAUTH = \"${secret:TOK}\"\n")
	// Dest had the env removed (server no longer needs auth).
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "srv",
		Server: source.MCPServerSpec{Type: "stdio", Command: "npx"},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
		t.Fatalf("removing a secret field (no residual cleartext) must be allowed: %v", err)
	}
}

// TestCapture_TrimmedEmbeddedSecretAllowed is the regression for a false-refusal:
// trimming a secret OUT of a still-present field (the field keeps non-secret
// text) leaves no cleartext, so capture must allow it — the SLOT prong must
// distinguish a trim (slot+context removed) from a rotation (slot holds a new
// value).
func TestCapture_TrimmedEmbeddedSecretAllowed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("T", "real_token_value")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"),
		"[server]\ntype=\"stdio\"\ncommand=\"serve --tok=${secret:T}\"\n")
	// User dropped the --tok flag in the dest; nothing cleartext remains.
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "srv",
		Server: source.MCPServerSpec{Type: "stdio", Command: "serve"},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
		t.Fatalf("trimming a secret out of a field (no cleartext) must be allowed: %v", err)
	}
}

// TestCapture_UnchangedLiteralCollidingWithSecretAllowed is the regression for a
// false-refusal: an UNCHANGED literal in one server that coincidentally contains
// another server's secret value must not be refused (it was already in source;
// re-reference deliberately leaves such literals byte-for-byte).
func TestCapture_UnchangedLiteralCollidingWithSecretAllowed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("T", "tok12345abc")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	writeFile(t, filepath.Join(home, "mcp", "a.toml"),
		"[server]\ntype=\"stdio\"\ncommand=\"a\"\n[server.env]\nK = \"${secret:T}\"\n")
	writeFile(t, filepath.Join(home, "mcp", "b.toml"),
		"[server]\ntype=\"stdio\"\ncommand=\"run --note=tok12345abc-extra\"\n")
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "b",
		Server: source.MCPServerSpec{Type: "stdio", Command: "run --note=tok12345abc-extra"},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
		t.Fatalf("unchanged literal that merely contains another secret's value must be allowed: %v", err)
	}
	if got, _, _ := source.ReadMCP(home, "b"); got.Server.Command != "run --note=tok12345abc-extra" {
		t.Fatalf("literal must be left byte-for-byte, got: %q", got.Server.Command)
	}
}

// TestCapture_RefusesEmbeddedRotation locks that the shape-aware SLOT still
// catches a genuine rotation of an EMBEDDED secret to a vault-unknown value
// (the leak it must keep refusing while allowing trims above).
func TestCapture_RefusesEmbeddedRotation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("T", "old_token_value")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"),
		"[server]\ntype=\"stdio\"\ncommand=\"serve --tok=${secret:T}\"\n")
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "srv",
		Server: source.MCPServerSpec{Type: "stdio", Command: "serve --tok=ROTATED_UNVAULTED_TOKEN"},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err == nil {
		t.Fatal("rotating an embedded secret to a new value must be refused")
	}
	if raw, _ := os.ReadFile(filepath.Join(home, "mcp", "srv.toml")); strings.Contains(string(raw), "ROTATED_UNVAULTED_TOKEN") {
		t.Fatalf("rotated cleartext must not be persisted:\n%s", raw)
	}
}

// TestCapture_RejectsTraversalID is the regression for an arbitrary-file-write
// primitive via import/capture: an ingested component id/event (taken from a
// foreign / synced / project-supplied native config) was joined straight into a
// source path with no validation, so `import claude:mcp:'../../../x'` wrote a
// .toml OUTSIDE ~/.agentsync. The source write boundary must reject traversal.
func TestCapture_RejectsTraversalID(t *testing.T) {
	home := t.TempDir()
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "../../../../escape",
		Server: source.MCPServerSpec{Type: "stdio", Command: "x"},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err == nil {
		t.Fatal("capture must reject a traversal component id, got nil error")
	}
}

// TestCapture_FailsClosedOnLoadError proves the secret boundary fails CLOSED
// when the current source cannot be loaded. A malformed file anywhere in the
// tree makes source.Load error; the previous code then skipped both
// re-referencing AND the warning and wrote the ingested values verbatim —
// silently persisting a resolved cleartext secret into ~/.agentsync. Capture
// must instead refuse to write and surface the load error.
func TestCapture_FailsClosedOnLoadError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOK", "live-secret-value")
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
	// Existing source: the server templated with the secret.
	writeFile(t, filepath.Join(home, "mcp", "srv.toml"), ""+
		"[server]\n"+
		"type = \"stdio\"\n"+
		"command = \"npx\"\n"+
		"[server.env]\n"+
		"GH = \"${secret:TOK}\"\n")
	// An unrelated malformed file makes source.Load return an error.
	writeFile(t, filepath.Join(home, "mcp", "broken.toml"), "[server\ncommand = \"x\"\n")

	// Ingested-from-dest: apply resolved ${secret:TOK} to its cleartext.
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID: "srv",
		Server: source.MCPServerSpec{
			Type:    "stdio",
			Command: "npx",
			Env:     map[string]string{"GH": "live-secret-value"},
		},
	}}}

	if _, err := capture.Capture(home, ingested, capture.Opts{}); err == nil {
		t.Fatal("expected Capture to fail closed when source cannot be loaded, got nil error")
	}
	// The original templated source must be intact — no cleartext persisted.
	got, ok, rerr := source.ReadMCP(home, "srv")
	if rerr != nil {
		t.Fatalf("read back: %v", rerr)
	}
	if ok && got.Server.Env["GH"] == "live-secret-value" {
		t.Fatalf("LEAK: resolved cleartext secret persisted into source: GH=%q", got.Server.Env["GH"])
	}
}

// TestCapture_FirstImportNoSource exercises the path where no canonical source
// exists yet (first import): there is nothing to re-reference against, so the
// ingested values are written verbatim and Capture must not error.
func TestCapture_FirstImportNoSource(t *testing.T) {
	home := t.TempDir()
	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID:     "srv",
		Server: source.MCPServerSpec{Type: "stdio", Command: "npx"},
	}}}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
		t.Fatalf("capture (first import): %v", err)
	}
	got, ok, err := source.ReadMCP(home, "srv")
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	if got.Server.Command != "npx" {
		t.Errorf("first-import write-as-is failed: command=%q", got.Server.Command)
	}
}

// TestCapture_RefusesOnLockedBackend pins the fail-closed-under-indeterminacy
// behavior (issue #163): when the secrets backend can't resolve a ${secret:…} the
// source references (vault locked / absent), the backstop's value prong is BLIND —
// so Capture refuses the whole write-back rather than degrade to a warning, and
// leaves the canonical source file untouched.
func TestCapture_RefusesOnLockedBackend(t *testing.T) {
	home := t.TempDir()
	// age backend whose identity + vault do NOT exist -> Resolve fails (locked).
	writeFile(t, filepath.Join(home, "agentsync.toml"), ""+
		"[secrets]\n"+
		"backend = \"age\"\n"+
		"recipient = \"age1qqqq\"\n"+
		"identity_file = \""+filepath.Join(home, "missing.key")+"\"\n"+
		"file = \"secrets/secrets.age\"\n")
	// Source references a ${secret:…} the locked backend can't resolve.
	srcPath := filepath.Join(home, "mcp", "srv.toml")
	writeFile(t, srcPath, ""+
		"[server]\n"+
		"type = \"stdio\"\n"+
		"command = \"npx\"\n"+
		"[server.env]\n"+
		"TOK = \"${secret:github.token}\"\n")
	before, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}

	ingested := &source.Canonical{MCPServers: []source.MCPServer{{
		ID: "srv",
		Server: source.MCPServerSpec{
			Type:    "stdio",
			Command: "npx",
			Env:     map[string]string{"TOK": "some-resolved-cleartext"},
		},
	}}}

	res, err := capture.Capture(home, ingested, capture.Opts{})
	if err == nil {
		t.Fatal("Capture must refuse when the backend can't resolve a ${secret:…} the source references")
	}
	if !strings.Contains(err.Error(), "could not resolve") {
		t.Fatalf("refusal error should name the unresolvable secret; got: %v", err)
	}
	if len(res.Written) != 0 {
		t.Fatalf("nothing should be written on refusal; wrote %v", res.Written)
	}
	// The canonical source file must be byte-for-byte unchanged.
	after, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("source mcp file modified despite refusal:\nbefore=%q\nafter=%q", before, after)
	}
}

// TestCapture_PreservesIngestedEnabled is the #152 regression guard: Codex reads
// native `enabled` into the modeled MCPServerSpec.Enabled, so a captured explicit
// &true/&false (a native enable/disable) MUST survive write-back of an
// already-managed server. The "enabled is source-only" preservation may only fall
// back to the source value when the ingest carried NONE (nil) — otherwise a native
// disable is silently dropped and the next apply re-enables the server (violating
// CLAUDE.md's "never drop it silently"). Agents targeting stays unconditionally
// source-preserved (no native dest ever carries it).
func TestCapture_PreservesIngestedEnabled(t *testing.T) {
	t.Run("ingested-false-survives-over-source-nil", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
		// Already-managed server: no explicit enabled (nil = default-on), plus a
		// source-only agents allowlist.
		writeFile(t, filepath.Join(home, "mcp", "srv.toml"),
			"[server]\ntype = \"stdio\"\ncommand = \"npx\"\nagents = [\"codex\"]\n")

		no := false
		ingested := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Type: "stdio", Command: "npx", Enabled: &no},
		}}}
		if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
			t.Fatalf("capture: %v", err)
		}
		got, ok, err := source.ReadMCP(home, "srv")
		if err != nil || !ok {
			t.Fatalf("read back: err=%v ok=%v", err, ok)
		}
		if got.Server.Enabled == nil || *got.Server.Enabled {
			t.Fatalf("captured native enabled=false was dropped; got Enabled=%v", got.Server.Enabled)
		}
		if len(got.Server.Agents) != 1 || got.Server.Agents[0] != "codex" {
			t.Fatalf("source-only agents allowlist not preserved; got %v", got.Server.Agents)
		}
	})

	t.Run("source-enabled-preserved-when-ingest-carries-none", func(t *testing.T) {
		home := t.TempDir()
		writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")
		// Source has an explicit enabled=true; the ingest (an adapter whose dest
		// does not carry enabled) reports nil, so the source value must survive.
		writeFile(t, filepath.Join(home, "mcp", "srv.toml"),
			"[server]\ntype = \"stdio\"\ncommand = \"npx\"\nenabled = true\n")

		ingested := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Type: "stdio", Command: "npx"}, // Enabled: nil
		}}}
		if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
			t.Fatalf("capture: %v", err)
		}
		got, _, _ := source.ReadMCP(home, "srv")
		if got.Server.Enabled == nil || !*got.Server.Enabled {
			t.Fatalf("source-only enabled=true was NOT preserved when ingest carried none; got %v", got.Server.Enabled)
		}
	})
}

// TestCapture_NormalizesExtraNumbersToTOML proves the funnel converts
// UseNumber-decoded passthrough values before persisting: a json.Number left in
// an Extra map marshals through go-toml as a TOML *string* (`timeout = '30'`),
// silently flipping the value's native type on the next render. The oracle is
// the on-disk TOML artifact (raw bytes), not the parsed model — a model
// round-trip cannot see this bug.
func TestCapture_NormalizesExtraNumbersToTOML(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "agentsync.toml"), "[secrets]\nbackend = \"env\"\n")

	ingested := &source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "srv",
			Server: source.MCPServerSpec{
				Type:    "stdio",
				Command: "npx",
				Extra: map[string]any{
					"timeout": json.Number("30"),
					// 2^53+1: the value UseNumber exists to keep exact.
					"snowflake": json.Number("9007199254740993"),
					"ratio":     json.Number("0.5"),
					"nested":    map[string]any{"retries": json.Number("4")},
				},
			},
		}},
		LSPServers: []source.LSPServer{{
			ID: "lsp",
			Spec: source.LSPServerSpec{
				Command: "gopls",
				Extra:   map[string]any{"port": json.Number("9000")},
			},
		}},
	}
	if _, err := capture.Capture(home, ingested, capture.Opts{}); err != nil {
		t.Fatalf("capture: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "mcp", "srv.toml"))
	if err != nil {
		t.Fatal(err)
	}
	toml := string(raw)
	for _, want := range []string{"timeout = 30", "snowflake = 9007199254740993", "ratio = 0.5", "retries = 4"} {
		if !strings.Contains(toml, want) {
			t.Errorf("mcp/srv.toml missing bare TOML value %q; got:\n%s", want, toml)
		}
	}
	for _, leak := range []string{"'30'", "\"30\"", "'9007199254740993'", "\"9007199254740993\""} {
		if strings.Contains(toml, leak) {
			t.Errorf("mcp/srv.toml persisted a numeric Extra value as a TOML string %q:\n%s", leak, toml)
		}
	}
	rawLSP, err := os.ReadFile(filepath.Join(home, "lsp", "lsp.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawLSP), "port = 9000") {
		t.Errorf("lsp/lsp.toml missing bare TOML value %q; got:\n%s", "port = 9000", string(rawLSP))
	}

	// The loaded model must carry native number types, so the next render emits
	// JSON numbers (30), not strings ("30").
	got, ok, err := source.ReadMCP(home, "srv")
	if err != nil || !ok {
		t.Fatalf("read back: ok=%v err=%v", ok, err)
	}
	if v, isInt := got.Server.Extra["timeout"].(int64); !isInt || v != 30 {
		t.Errorf("loaded Extra timeout = %[1]v (%[1]T), want int64(30)", got.Server.Extra["timeout"])
	}
	if v, isInt := got.Server.Extra["snowflake"].(int64); !isInt || v != 9007199254740993 {
		t.Errorf("loaded Extra snowflake = %[1]v (%[1]T), want int64(9007199254740993)", got.Server.Extra["snowflake"])
	}
}

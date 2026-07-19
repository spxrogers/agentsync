package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The terminal-escape-injection sweep (issue #93/#171): every command that prints
// a config-/native-config-derived string must sanitize it, so a shareable
// dotfiles repo cannot smuggle ESC / bidi bytes into the terminal (or a CI log).
// These drive each plain, no-flag command with a hostile fixture and assert the
// dangerous rune never reaches stdout while the (sanitized) marker still shows.

// assertSanitized fails if out carries a raw ESC (0x1b) or a RIGHT-TO-LEFT
// OVERRIDE (U+202E) — the two representative runes Sanitize strips — and requires
// the visible marker to survive so the sanitization didn't blank the value.
func assertSanitized(t *testing.T, out, marker string) {
	t.Helper()
	if strings.ContainsRune(out, 0x1b) {
		t.Fatalf("output carries a raw ESC byte (escape injection); got:\n%q", out)
	}
	if strings.ContainsRune(out, 0x202e) {
		t.Fatalf("output carries a raw U+202E (bidi override); got:\n%q", out)
	}
	if marker != "" && !strings.Contains(out, marker) {
		t.Fatalf("sanitized marker %q missing — value was blanked, not just sanitized; got:\n%s", marker, out)
	}
}

func TestEscapeSweep_MarketplaceList(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp, "NO_COLOR": "1"}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	// url and head_sha are basic-string values →  decodes to a raw ESC.
	mp := filepath.Join(tmp, ".agentsync", "marketplaces", "mp.toml")
	if err := os.MkdirAll(filepath.Dir(mp), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[marketplace]\nurl = \"http://x\\u001b[31mURLHACK\\u001b[0m\"\nhead_sha = \"a\\u001b[31mSHAHACK\"\n"
	if err := os.WriteFile(mp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "marketplace", "list")
	if err != nil {
		t.Fatalf("marketplace list: %v\n%s", err, out)
	}
	assertSanitized(t, out, "URLHACK")
}

func TestEscapeSweep_AgentList(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp, "NO_COLOR": "1"}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	// A quoted [agents.<key>] TOML key and the display-only scope value both carry
	// raw ESC via .
	cfg := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	body := "[agents.\"cla\\u001b[31mKEYHACK\"]\nenabled = true\nscope = \"user\\u001b[31mSCOPEHACK\"\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "agent", "list")
	if err != nil {
		t.Fatalf("agent list: %v\n%s", err, out)
	}
	assertSanitized(t, out, "KEYHACK")
	assertSanitized(t, out, "SCOPEHACK")
}

func TestEscapeSweep_MCPList(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp, "NO_COLOR": "1"}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	// mcp list prints the filename stem verbatim; a POSIX filename may hold a raw
	// ESC byte (git preserves it on clone).
	dir := filepath.Join(tmp, ".agentsync", "mcp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "srv\x1b[31mNAMEHACK.toml"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("[server]\ntype=\"stdio\"\ncommand=\"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "mcp", "list")
	if err != nil {
		t.Fatalf("mcp list: %v\n%s", err, out)
	}
	assertSanitized(t, out, "NAMEHACK")
}

func TestEscapeSweep_DoctorSchema(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp, "NO_COLOR": "1"}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	// An unknown key makes go-toml's strict decoder echo the raw SOURCE LINE in
	// its error; a literal-string value can hold a raw U+202E (bidi override),
	// which doctor's schema check must sanitize before printing.
	cfg := filepath.Join(tmp, ".agentsync", "agentsync.toml")
	// A top-level unknown key trips go-toml's strict decoder, whose error echoes
	// the raw source line; the literal-string value holds a raw U+202E.
	body := "bogus_key = 'a\u202eSCHEMAHACK'\n[agents]\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _ := runCLI(t, env, "doctor")
	assertSanitized(t, out, "SCHEMAHACK")
}

func TestEscapeSweep_ApplyPlan(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp, "NO_COLOR": "1"}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	// A skill directory name carries a raw ESC byte; source.Load reads it verbatim
	// into the model, so the rendered dest path in the apply plan preview embeds it.
	skillDir := filepath.Join(tmp, ".agentsync", "skills", "sk\x1b[31mSKILLHACK")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: skillhack\ndescription: d\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, env, "apply", "--dry-run")
	if err != nil {
		t.Fatalf("apply --dry-run: %v\n%s", err, out)
	}
	assertSanitized(t, out, "SKILLHACK")
}

func TestEscapeSweep_ImportDryRun(t *testing.T) {
	tmp, env := importTestEnv(t)
	env["NO_COLOR"] = "1"
	mpDir := makeLocalMarketplace(t, t.TempDir())
	// A native marketplace id carrying a raw ESC byte (json.Marshal escapes it
	// into the on-disk .claude.json; import decodes it back). import --dry-run
	// previews the marketplace BY NATIVE ID via importIO.item() without fetching —
	// the one item() caller not gated by ValidateComponentID.
	mpID := "m\x1b[31mMKTHACK"
	writeClaudeSettings(t, tmp, directoryMarketplaceSettings(mpID, mpDir, "demo"))
	out, err := runCLI(t, env, "import", "claude:plugin", "--dry-run")
	if err != nil {
		t.Fatalf("import claude:plugin --dry-run: %v\n%s", err, out)
	}
	assertSanitized(t, out, "MKTHACK")
}

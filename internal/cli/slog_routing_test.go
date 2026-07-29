package cli_test

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/ui"
)

// TestSlogWarningRoutesThroughTheDiagnosticVocabulary is the end-to-end regression
// for the HALF of #211 that no other test covered.
//
// The library packages (internal/render, internal/marketplace) report through
// `slog`, and before this change NO handler was ever installed — so those records
// fell through to slog's default and printed via the standard log package:
//
//	2026/07/28 15:03:45 WARN plugin component frontmatter is not strict YAML …
//
// a timestamped, uncolored, unlabeled shape that made the fatal error one line
// below it indistinguishable. The fix is one line in the root command's
// PersistentPreRunE (`aslog.Install`). Deleting that line used to break NOTHING:
// the full unit + e2e suite stayed green, because every existing assertion covers
// either the ui package in isolation or the ADAPTER warning path
// (`import_warn_routing_test.go`, which goes through Fprintf → ui.WarnWriter and
// never touches slog).
//
// So this drives a real CLI invocation that trips a genuine slog.Warn deep in
// marketplace projection, and asserts the record comes out of the process as an
// agentsync diagnostic. It fails if the installation is removed, if the handler
// stops labeling, or if the timestamp comes back.
func TestSlogWarningRoutesThroughTheDiagnosticVocabulary(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp, "NO_COLOR": "1"}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// A local marketplace whose plugin ships an agent with NON-STRICT frontmatter:
	// the unquoted "description:" value contains ": ", which is what makes
	// marketplace projection fall back to lenient parsing and emit the slog.Warn.
	mp := filepath.Join(tmp, "fixture-mp")
	writeFile(t, filepath.Join(mp, ".claude-plugin", "marketplace.json"), mustJSON(t, map[string]any{
		"name":  "fixture-mp",
		"owner": map[string]any{"name": "test"},
		"plugins": []map[string]any{
			{"name": "demo-plugin", "source": "./plugins/demo-plugin"},
		},
	}))
	writeFile(t, filepath.Join(mp, "plugins", "demo-plugin", ".claude-plugin", "plugin.json"),
		mustJSON(t, map[string]any{"name": "demo-plugin", "version": "1.0.0"}))
	writeFile(t, filepath.Join(mp, "plugins", "demo-plugin", "agents", "hunter.md"),
		"---\nname: hunter\ndescription: Finds silent failures: swallowed errors\n---\nbody\n")

	if out, err := runCLI(t, env, "marketplace", "add", mp); err != nil {
		t.Fatalf("marketplace add: %v\n%s", err, out)
	}
	if out, err := runCLI(t, env, "plugin", "add", "demo-plugin@fixture-mp"); err != nil {
		t.Fatalf("plugin add: %v\n%s", err, out)
	}

	stdout, stderr, err := runCLISplit(t, env, "status")
	if err != nil {
		t.Fatalf("status: %v\nstderr:\n%s", err, stderr)
	}

	// The fixture must actually trip the warning, or this test proves nothing.
	// Assert the phrase FIRST so a fixture that stopped provoking slog fails
	// loudly here rather than silently making the label assertions vacuous.
	const phrase = "frontmatter is not strict YAML"
	if !strings.Contains(stderr, phrase) {
		// Two causes, and the captured buffer cannot tell them apart — say both.
		// With no handler installed, slog's default writes to the process's REAL
		// os.Stderr, which runCLISplit does not capture, so the record vanishes
		// from this buffer exactly as if the fixture had never provoked it.
		t.Fatalf("no slog warning reached the captured stderr. Either the handler is not "+
			"installed (aslog.Install in root.go's PersistentPreRunE — the stdlib default "+
			"writes to the real os.Stderr, not this buffer), or the fixture stopped "+
			"provoking the marketplace lenient-YAML warning.\nstderr:\n%s", stderr)
	}

	// It must carry the WARN label, on the SAME line as the message (a label
	// anywhere in the buffer could have come from an unrelated warning).
	plainLabel := ui.LevelWarn.Label(ui.New(io.Discard, io.Discard, ui.ColorNever))
	sameLine := regexp.MustCompile(regexp.QuoteMeta(plainLabel) + `[^\n\r]*` + regexp.QuoteMeta(phrase))
	if !sameLine.MatchString(stderr) {
		t.Fatalf("the slog warning is not labeled — the handler is not installed, or stopped labeling.\nstderr:\n%s", stderr)
	}

	// And it must NOT carry the stdlib log package's wall-clock prefix, which is
	// the single most visible thing that was wrong with the old output.
	stdlibTimestamp := regexp.MustCompile(`(?m)^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} `)
	if stdlibTimestamp.MatchString(stderr) {
		t.Fatalf("stdlib timestamped log line reappeared (handler not installed?):\n%s", stderr)
	}

	// A slog-sourced diagnostic is still a diagnostic: it belongs on stderr.
	if strings.Contains(stdout, phrase) {
		t.Fatalf("a slog warning leaked onto stdout:\n%s", stdout)
	}
}

// TestSlogWarningNeverEntersAJSONPayload pins the stdout/stderr split at the
// point where it actually matters: `status --json` promises a cleanly parseable
// payload, and a library warning printing into it would corrupt what a caller is
// piping. docs/architecture.md §11 states this; this is the assertion behind it.
func TestSlogWarningNeverEntersAJSONPayload(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp, "HOME": tmp, "NO_COLOR": "1"}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(tmp, "fixture-mp")
	writeFile(t, filepath.Join(mp, ".claude-plugin", "marketplace.json"), mustJSON(t, map[string]any{
		"name":  "fixture-mp",
		"owner": map[string]any{"name": "test"},
		"plugins": []map[string]any{
			{"name": "demo-plugin", "source": "./plugins/demo-plugin"},
		},
	}))
	writeFile(t, filepath.Join(mp, "plugins", "demo-plugin", ".claude-plugin", "plugin.json"),
		mustJSON(t, map[string]any{"name": "demo-plugin", "version": "1.0.0"}))
	writeFile(t, filepath.Join(mp, "plugins", "demo-plugin", "agents", "hunter.md"),
		"---\nname: hunter\ndescription: Finds silent failures: swallowed errors\n---\nbody\n")
	if _, err := runCLI(t, env, "marketplace", "add", mp); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "plugin", "add", "demo-plugin@fixture-mp"); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := runCLISplit(t, env, "status", "--json")
	if err != nil {
		t.Fatalf("status --json: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "frontmatter is not strict YAML") {
		t.Fatalf("no slog warning reached the captured stderr — handler not installed, or the "+
			"fixture stopped provoking it; either way this test proves nothing.\nstderr:\n%s", stderr)
	}
	var payload map[string]any
	if jerr := json.Unmarshal([]byte(stdout), &payload); jerr != nil {
		t.Fatalf("stdout is not clean JSON with a library warning in flight: %v\nstdout:\n%s", jerr, stdout)
	}
	// No level label of any severity may appear in the payload stream.
	for _, l := range []ui.Level{ui.LevelDebug, ui.LevelInfo, ui.LevelWarn, ui.LevelError} {
		if word := l.String(); strings.Contains(stdout, word) {
			t.Errorf("the %s level word leaked into the --json payload:\n%s", word, stdout)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExplain_PluginNotFound verifies that explain errors when the plugin id is unknown.
func TestExplain_PluginNotFound(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	_, err := runCLI(t, env, "explain", "nonexistent@mp")
	if err == nil {
		t.Fatal("expected error for unknown plugin; got nil")
	}
}

// TestExplain_TextOutput installs a plugin and verifies explain produces the
// per-agent translation table in text form.
func TestExplain_TextOutput(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	fixture := setupExplainFixture(t, tmp)

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "marketplace", "add", fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "plugin", "install", "demo@test-mp"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "explain", "demo@test-mp")
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, out)
	}
	if !strings.Contains(out, "demo@test-mp") {
		t.Fatalf("explain text output missing plugin label; got:\n%s", out)
	}
	if !strings.Contains(out, "claude") {
		t.Fatalf("explain text output missing claude agent; got:\n%s", out)
	}
}

// TestExplain_JSONOutput verifies --json emits parseable JSON with the expected fields.
func TestExplain_JSONOutput(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	fixture := setupExplainFixture(t, tmp)

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "marketplace", "add", fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "plugin", "install", "demo@test-mp"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "explain", "demo@test-mp", "--json")
	if err != nil {
		t.Fatalf("explain --json: %v\n%s", err, out)
	}

	var result struct {
		Rows []struct {
			Plugin   string `json:"plugin"`
			Agent    string `json:"agent"`
			Coverage string `json:"coverage"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("explain --json: not valid JSON: %v\noutput:\n%s", err, out)
	}
	if len(result.Rows) == 0 {
		t.Fatalf("explain --json returned zero rows; output:\n%s", out)
	}
	if result.Rows[0].Plugin == "" {
		t.Errorf("explain --json: first row has empty plugin field")
	}
}

// TestExplain_JSONAgentOrderDeterministic guards the sorted-row contract for
// `explain --json`: PrintJSON emits rows in the agents-slice order verbatim, and
// that slice was built from an unsorted map walk, so multi-agent JSON output was
// nondeterministic (spurious diffs). Rows must be sorted by agent.
func TestExplain_JSONAgentOrderDeterministic(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

	fixture := setupExplainFixture(t, tmp)

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"claude", "opencode"} {
		if _, err := runCLI(t, env, "agent", "add", a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runCLI(t, env, "marketplace", "add", fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "plugin", "install", "demo@test-mp"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "explain", "demo@test-mp", "--json")
	if err != nil {
		t.Fatalf("explain --json: %v\n%s", err, out)
	}
	var result struct {
		Rows []struct {
			Agent string `json:"agent"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if len(result.Rows) < 2 {
		t.Fatalf("expected >=2 agent rows; got %d:\n%s", len(result.Rows), out)
	}
	for i := 1; i < len(result.Rows); i++ {
		if result.Rows[i-1].Agent > result.Rows[i].Agent {
			t.Fatalf("explain --json rows not sorted by agent (nondeterministic): %q before %q",
				result.Rows[i-1].Agent, result.Rows[i].Agent)
		}
	}
}

// TestExplain_NoArgsRequiresSomething rejects a bare `explain` invocation with
// neither plugin ids nor --list/--all so the user gets a pointer to the new
// flags rather than a confusing usage error.
func TestExplain_NoArgsRequiresSomething(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "explain")
	if err == nil {
		t.Fatal("expected error for bare explain; got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "--all") || !strings.Contains(msg, "--list") {
		t.Errorf("error should point at --all and --list; got: %s", msg)
	}
}

// TestExplain_List prints installed plugin ids (and stays empty when none are
// installed, with an install hint).
func TestExplain_List(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	fixture := setupExplainFixture(t, tmp)

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}

	// Empty case: list works even with zero plugins; hints at install.
	out, err := runCLI(t, env, "explain", "--list")
	if err != nil {
		t.Fatalf("--list (empty): %v\n%s", err, out)
	}
	if !strings.Contains(out, "no plugins installed") {
		t.Errorf("empty list should hint at install; got:\n%s", out)
	}

	if _, err := runCLI(t, env, "marketplace", "add", fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "plugin", "install", "demo@test-mp"); err != nil {
		t.Fatal(err)
	}

	out, err = runCLI(t, env, "explain", "--list")
	if err != nil {
		t.Fatalf("--list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "demo@test-mp") {
		t.Errorf("--list missing plugin id; got:\n%s", out)
	}
	if !strings.Contains(out, "Installed plugins") {
		t.Errorf("--list missing header; got:\n%s", out)
	}

	// --list --json emits a `plugins` array.
	out, err = runCLI(t, env, "explain", "--list", "--json")
	if err != nil {
		t.Fatalf("--list --json: %v\n%s", err, out)
	}
	var parsed struct {
		Plugins []struct {
			ID string `json:"id"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("--list --json not valid JSON: %v\n%s", err, out)
	}
	if len(parsed.Plugins) != 1 || parsed.Plugins[0].ID != "demo@test-mp" {
		t.Errorf("--list --json plugins = %+v; want one demo@test-mp", parsed.Plugins)
	}
}

// TestExplain_MultipleArgs renders coverage for each requested plugin in argv
// order. This is the new multi-arg shape: `explain a b c`.
func TestExplain_MultipleArgs(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	fixture := setupExplainMultiFixture(t, tmp)

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "marketplace", "add", fixture); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alpha@multi-mp", "beta@multi-mp"} {
		if _, err := runCLI(t, env, "plugin", "install", id); err != nil {
			t.Fatal(err)
		}
	}

	// Note args order: beta first, then alpha — multi-arg explain must honor it.
	out, err := runCLI(t, env, "explain", "beta@multi-mp", "alpha@multi-mp", "--json")
	if err != nil {
		t.Fatalf("multi-arg --json: %v\n%s", err, out)
	}
	var parsed struct {
		Rows []struct {
			Plugin string `json:"plugin"`
			Agent  string `json:"agent"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("multi-arg not valid JSON: %v\n%s", err, out)
	}
	if len(parsed.Rows) < 2 {
		t.Fatalf("expected >=2 rows across the two plugins; got %d", len(parsed.Rows))
	}
	if parsed.Rows[0].Plugin != "beta@multi-mp" {
		t.Errorf("first row plugin = %q; want beta@multi-mp (argv-order)", parsed.Rows[0].Plugin)
	}
	// Both plugins must appear in the rows.
	seen := map[string]bool{}
	for _, r := range parsed.Rows {
		seen[r.Plugin] = true
	}
	for _, want := range []string{"alpha@multi-mp", "beta@multi-mp"} {
		if !seen[want] {
			t.Errorf("missing rows for %q; got rows: %+v", want, parsed.Rows)
		}
	}
}

// TestExplain_All explains every installed plugin without naming them.
func TestExplain_All(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	fixture := setupExplainMultiFixture(t, tmp)

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "marketplace", "add", fixture); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alpha@multi-mp", "beta@multi-mp"} {
		if _, err := runCLI(t, env, "plugin", "install", id); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runCLI(t, env, "explain", "--all", "--json")
	if err != nil {
		t.Fatalf("--all: %v\n%s", err, out)
	}
	var parsed struct {
		Rows []struct {
			Plugin string `json:"plugin"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("--all not valid JSON: %v\n%s", err, out)
	}
	seen := map[string]bool{}
	for _, r := range parsed.Rows {
		seen[r.Plugin] = true
	}
	for _, want := range []string{"alpha@multi-mp", "beta@multi-mp"} {
		if !seen[want] {
			t.Errorf("--all missing rows for %q; got rows: %+v", want, parsed.Rows)
		}
	}
}

// TestExplain_MultipleArgsMissingAggregated reports every missing plugin id in
// one message — typing two bad ids should not require two roundtrips to learn.
func TestExplain_MultipleArgsMissingAggregated(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	_, err := runCLI(t, env, "explain", "nope1", "nope2")
	if err == nil {
		t.Fatal("expected error for two missing plugins; got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "nope1") || !strings.Contains(msg, "nope2") {
		t.Errorf("error should list both missing ids; got: %s", msg)
	}
}

// TestExplain_FlagConflicts catches mutually-exclusive flag combinations early.
func TestExplain_FlagConflicts(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"list+all", []string{"explain", "--list", "--all"}, "mutually exclusive"},
		{"list+id", []string{"explain", "--list", "foo"}, "--list does not take"},
		{"all+id", []string{"explain", "--all", "foo"}, "--all does not take"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCLI(t, env, tc.args...)
			if err == nil {
				t.Fatal("expected error; got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q; got: %s", tc.want, err)
			}
		})
	}
}

// TestExplain_ListsSkips verifies that explain does not stop at a bare
// "(N skipped)" tally — it lists each skipped component (label + reason) under
// the agent row, in both text and JSON. The fixture ships an LSP server and a
// hook on a lifecycle event Codex does not recognize; Codex skips both
// deterministically (no LSP concept; unknown event), so the row is a stable
// two-skip partial.
func TestExplain_ListsSkips(t *testing.T) {
	tmp := t.TempDir()
	env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
	fixture := setupExplainSkipFixture(t, tmp)

	if _, err := runCLI(t, env, "init"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "agent", "add", "codex"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "marketplace", "add", fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := runCLI(t, env, "plugin", "install", "skipdemo@skip-mp"); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, env, "explain", "skipdemo@skip-mp")
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, out)
	}
	if !strings.Contains(out, "skipped") {
		t.Fatalf("expected a skipped tally; got:\n%s", out)
	}
	// The skip must be itemized, not just counted: the component and its reason.
	if !strings.Contains(out, "lsp") {
		t.Errorf("explain should name the skipped lsp component; got:\n%s", out)
	}
	if !strings.Contains(out, "Codex has no LSP configuration concept") {
		t.Errorf("explain should print the skip reason; got:\n%s", out)
	}

	// JSON carries the same detail under skipDetails.
	outJSON, err := runCLI(t, env, "explain", "skipdemo@skip-mp", "--json")
	if err != nil {
		t.Fatalf("explain --json: %v\n%s", err, outJSON)
	}
	var parsed struct {
		Rows []struct {
			SkipDetails []struct {
				Component string `json:"component"`
				Name      string `json:"name"`
				Reason    string `json:"reason"`
			} `json:"skipDetails"`
		} `json:"rows"`
	}
	if err := json.Unmarshal([]byte(outJSON), &parsed); err != nil {
		t.Fatalf("explain --json not valid JSON: %v\n%s", err, outJSON)
	}
	var lsp, hook bool
	for _, r := range parsed.Rows {
		for _, sd := range r.SkipDetails {
			if sd.Component == "lsp" && strings.Contains(sd.Reason, "no LSP configuration concept") {
				lsp = true
			}
			if sd.Component == "hook" && strings.Contains(sd.Reason, "does not recognize this lifecycle event") {
				hook = true
			}
		}
	}
	if !lsp || !hook {
		t.Errorf("explain --json missing skipDetails (lsp=%v hook=%v); got:\n%s", lsp, hook, outJSON)
	}
}

// setupExplainSkipFixture builds a marketplace whose single plugin ships an MCP
// server, an LSP server, and a hook on a lifecycle event Codex does not
// recognize. Codex renders the MCP but skips the LSP (no LSP concept) and the
// hook (unknown event), giving the skip-listing test a deterministic two-skip
// partial-coverage row.
func setupExplainSkipFixture(t *testing.T, tmp string) string {
	t.Helper()
	fixture := filepath.Join(tmp, "fixture-marketplace-explain-skip")
	if err := os.MkdirAll(filepath.Join(fixture, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"skip-mp","owner":{"name":"x"},"plugins":[{"name":"skipdemo","source":"./plugins/skipdemo"}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	plugDir := filepath.Join(fixture, "plugins", "skipdemo", ".claude-plugin")
	if err := os.MkdirAll(plugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugDir, "plugin.json"),
		[]byte(`{"name":"skipdemo","version":"1.0.0",`+
			`"mcpServers":{"skip-mcp":{"command":"echo","args":["hi"]}},`+
			`"lspServers":{"skip-lsp":{"command":"lang-server"}},`+
			`"hooks":{"SessionEnd":"echo bye"}}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// setupExplainFixture creates a minimal local marketplace with a single demo plugin.
func setupExplainFixture(t *testing.T, tmp string) string {
	t.Helper()
	fixture := filepath.Join(tmp, "fixture-marketplace-explain")
	if err := os.MkdirAll(filepath.Join(fixture, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"test-mp","owner":{"name":"x"},"plugins":[{"name":"demo","source":"./plugins/demo"}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	plugDir := filepath.Join(fixture, "plugins", "demo", ".claude-plugin")
	if err := os.MkdirAll(plugDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugDir, "plugin.json"),
		[]byte(`{"name":"demo","version":"1.0.0","mcpServers":{"demo-mcp":{"command":"echo","args":["hi"]}}}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// setupExplainMultiFixture creates a marketplace with two installable plugins
// (alpha + beta) used by the multi-arg / --all explain tests.
func setupExplainMultiFixture(t *testing.T, tmp string) string {
	t.Helper()
	fixture := filepath.Join(tmp, "fixture-marketplace-explain-multi")
	if err := os.MkdirAll(filepath.Join(fixture, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, ".claude-plugin", "marketplace.json"),
		[]byte(`{"name":"multi-mp","owner":{"name":"x"},"plugins":[`+
			`{"name":"alpha","source":"./plugins/alpha"},`+
			`{"name":"beta","source":"./plugins/beta"}]}`),
		0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		plugDir := filepath.Join(fixture, "plugins", name, ".claude-plugin")
		if err := os.MkdirAll(plugDir, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := `{"name":"` + name + `","version":"1.0.0","mcpServers":{"` + name + `-mcp":{"command":"echo","args":["hi"]}}}`
		if err := os.WriteFile(filepath.Join(plugDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

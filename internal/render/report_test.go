package render_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
)

func TestBuildReport_NoPlugins(t *testing.T) {
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github"}},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {
				Ops: []adapter.FileOp{
					{Action: "write", Path: "/home/.claude.json", MergeStrategy: "merge-json-keys"},
				},
				Skips: nil,
			},
		},
	}
	report := render.BuildReport(c, plan, []string{"claude"})
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(report.Rows))
	}
	row := report.Rows[0]
	if row.Plugin != "(base)" {
		t.Errorf("plugin = %q, want (base)", row.Plugin)
	}
	if row.Agent != "claude" {
		t.Errorf("agent = %q, want claude", row.Agent)
	}
	if row.MCP != 1 {
		t.Errorf("mcp = %d, want 1 (one canonical MCP server)", row.MCP)
	}
	if row.Coverage != "full" {
		t.Errorf("coverage = %q, want full", row.Coverage)
	}
}

func TestBuildReport_WithPlugin(t *testing.T) {
	c := source.Canonical{
		Plugins: []source.Plugin{
			{ID: "demo", Plugin: source.PluginSpec{ID: "demo@test-mp", Version: "1.0.0"}},
		},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {
				Ops: []adapter.FileOp{
					{Action: "write", Path: "/home/.claude.json", MergeStrategy: "merge-json-keys"},
				},
				Skips: nil,
			},
			"opencode": {
				Ops: []adapter.FileOp{
					{Action: "write", Path: "/home/.config/opencode/opencode.json", MergeStrategy: "merge-json-keys"},
				},
				Skips: nil,
			},
		},
	}
	report := render.BuildReport(c, plan, []string{"claude", "opencode"})
	if len(report.Rows) != 2 {
		t.Fatalf("expected 2 rows (one per agent), got %d", len(report.Rows))
	}
	for _, row := range report.Rows {
		if row.Plugin != "demo@test-mp" {
			t.Errorf("plugin = %q, want demo@test-mp", row.Plugin)
		}
		if row.Coverage != "full" {
			t.Errorf("coverage = %q, want full for %s", row.Coverage, row.Agent)
		}
	}
}

// A plugin disabled for the scope (e.g. project marker [plugins] disabled) is
// shown as a single disabled row, not omitted and not given misleading
// per-agent counts.
func TestBuildReport_DisabledPlugin(t *testing.T) {
	c := source.Canonical{
		Plugins: []source.Plugin{
			{ID: "demo", Plugin: source.PluginSpec{ID: "demo@test-mp", Disabled: true}},
		},
	}
	plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{"claude": {}}}
	report := render.BuildReport(c, plan, []string{"claude"})
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 disabled row, got %d: %+v", len(report.Rows), report.Rows)
	}
	row := report.Rows[0]
	if !row.Disabled || row.Coverage != "disabled" {
		t.Errorf("expected disabled row, got %+v", row)
	}
	if row.Plugin != "demo@test-mp" {
		t.Errorf("plugin = %q, want demo@test-mp", row.Plugin)
	}
}

func TestBuildReport_PartialCoverage(t *testing.T) {
	c := source.Canonical{
		// One server renders (MCP>0) and one component is skipped → partial.
		MCPServers: []source.MCPServer{{ID: "github"}},
		Plugins: []source.Plugin{
			{ID: "demo", Plugin: source.PluginSpec{ID: "demo@test-mp"}},
		},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {
				Ops: []adapter.FileOp{
					{Action: "write", MergeStrategy: "merge-json-keys"},
				},
				Skips: []adapter.Skip{
					{Component: "hook", Name: "pre-run", Reason: "unsupported", Kind: adapter.SkipDropped},
				},
			},
		},
	}
	report := render.BuildReport(c, plan, []string{"claude"})
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(report.Rows))
	}
	if report.Rows[0].Coverage != "partial" {
		t.Errorf("coverage = %q, want partial", report.Rows[0].Coverage)
	}
}

// TestBuildReport_SkipDetails verifies that each skipped component's detail
// (component, name, reason) is carried into the row, not just the count — and
// that it survives JSON round-trip with the lowercase keys the CLI surface
// promises. This is what lets `explain` list what is skipped instead of a bare
// "(N skipped)".
func TestBuildReport_SkipDetails(t *testing.T) {
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github"}},
		Plugins: []source.Plugin{
			{ID: "demo", Plugin: source.PluginSpec{ID: "demo@test-mp"}},
		},
	}
	skips := []adapter.Skip{
		{Component: "lsp", Name: "gopls", Reason: "Codex has no LSP configuration concept", Kind: adapter.SkipDropped},
		{Component: "hook", Name: "SessionEnd", Reason: "Codex does not recognize this lifecycle event", Kind: adapter.SkipDropped},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"codex": {
				Ops:   []adapter.FileOp{{Action: "write", MergeStrategy: "merge-toml-keys"}},
				Skips: skips,
			},
		},
	}
	report := render.BuildReport(c, plan, []string{"codex"})
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(report.Rows))
	}
	row := report.Rows[0]
	if row.Skips != 2 {
		t.Errorf("Skips = %d, want 2", row.Skips)
	}
	if len(row.SkipDetails) != 2 {
		t.Fatalf("SkipDetails len = %d, want 2: %+v", len(row.SkipDetails), row.SkipDetails)
	}
	if row.SkipDetails[0] != (render.SkipDetail{Component: "lsp", Name: "gopls", Reason: "Codex has no LSP configuration concept", Kind: adapter.SkipDropped}) {
		t.Errorf("SkipDetails[0] = %+v, want the gopls lsp skip", row.SkipDetails[0])
	}

	// JSON surface: lowercase component/name/reason/kind keys under skipDetails.
	var buf bytes.Buffer
	if err := report.PrintJSON(&buf); err != nil {
		t.Fatalf("PrintJSON: %v", err)
	}
	var parsed struct {
		Rows []struct {
			SkipDetails []struct {
				Component string `json:"component"`
				Name      string `json:"name"`
				Reason    string `json:"reason"`
				Kind      string `json:"kind"`
			} `json:"skipDetails"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("JSON parse: %v", err)
	}
	if len(parsed.Rows) != 1 || len(parsed.Rows[0].SkipDetails) != 2 {
		t.Fatalf("JSON skipDetails not emitted: %s", buf.String())
	}
	if parsed.Rows[0].SkipDetails[1].Reason != "Codex does not recognize this lifecycle event" {
		t.Errorf("JSON skipDetails[1].reason = %q", parsed.Rows[0].SkipDetails[1].Reason)
	}
	// kind is the explicit machine surface for the reduced/dropped split — no more
	// re-deriving it from a component-name suffix.
	if parsed.Rows[0].SkipDetails[0].Kind != "dropped" {
		t.Errorf("JSON skipDetails[0].kind = %q, want dropped", parsed.Rows[0].SkipDetails[0].Kind)
	}
}

// TestBuildReport_SkipDetails_BaseBranch covers the no-plugins "(base)" branch
// of BuildReport, which carries skip detail identically to the per-plugin
// branch. Without this, the base-branch SkipDetails assignment is unexercised.
func TestBuildReport_SkipDetails_BaseBranch(t *testing.T) {
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github"}},
		// No Plugins → BuildReport takes the "(base)" branch.
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"codex": {
				Ops:   []adapter.FileOp{{Action: "write", MergeStrategy: "merge-toml-keys"}},
				Skips: []adapter.Skip{{Component: "lsp", Name: "gopls", Reason: "Codex has no LSP configuration concept", Kind: adapter.SkipDropped}},
			},
		},
	}
	report := render.BuildReport(c, plan, []string{"codex"})
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(report.Rows))
	}
	row := report.Rows[0]
	if row.Plugin != "(base)" {
		t.Fatalf("plugin = %q, want (base) — this test must hit the no-plugins branch", row.Plugin)
	}
	if row.Skips != 1 || len(row.SkipDetails) != 1 {
		t.Fatalf("base-branch skips not carried: Skips=%d SkipDetails=%+v", row.Skips, row.SkipDetails)
	}
	if row.SkipDetails[0] != (render.SkipDetail{Component: "lsp", Name: "gopls", Reason: "Codex has no LSP configuration concept", Kind: adapter.SkipDropped}) {
		t.Errorf("SkipDetails[0] = %+v, want the gopls lsp skip", row.SkipDetails[0])
	}
}

// TestBuildReport_SkipDetails_OmittedWhenEmpty pins the omitempty contract: a
// row with zero skips must carry a nil SkipDetails AND emit no "skipDetails"
// key in JSON. A regression making skipDetails() return a non-nil empty slice
// would leak "skipDetails":[] onto every full-coverage row; this fails it.
func TestBuildReport_SkipDetails_OmittedWhenEmpty(t *testing.T) {
	c := source.Canonical{
		Plugins: []source.Plugin{{ID: "demo", Plugin: source.PluginSpec{ID: "demo@test-mp"}}},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {Ops: []adapter.FileOp{{Action: "write", MergeStrategy: "merge-json-keys"}}}, // no skips
		},
	}
	report := render.BuildReport(c, plan, []string{"claude"})
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(report.Rows))
	}
	if report.Rows[0].SkipDetails != nil {
		t.Errorf("SkipDetails = %+v, want nil for a no-skip row", report.Rows[0].SkipDetails)
	}
	var buf bytes.Buffer
	if err := report.PrintJSON(&buf); err != nil {
		t.Fatalf("PrintJSON: %v", err)
	}
	if strings.Contains(buf.String(), "skipDetails") {
		t.Errorf("a no-skip row must omit the skipDetails key; got:\n%s", buf.String())
	}
}

func TestTranslationReport_PrintText(t *testing.T) {
	c := source.Canonical{
		Plugins: []source.Plugin{
			{ID: "demo", Plugin: source.PluginSpec{ID: "demo@test-mp", Version: "1.0.0"}},
		},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {
				Ops: []adapter.FileOp{
					{Action: "write", MergeStrategy: "merge-json-keys"},
				},
			},
			"opencode": {
				Ops: []adapter.FileOp{
					{Action: "write", MergeStrategy: "merge-json-keys"},
				},
			},
		},
	}
	report := render.BuildReport(c, plan, []string{"claude", "opencode"})

	var buf bytes.Buffer
	report.PrintText(&buf)
	out := buf.String()

	if !strings.Contains(out, "plugin: demo@test-mp") {
		t.Errorf("missing plugin header; got:\n%s", out)
	}
	if !strings.Contains(out, "claude") {
		t.Errorf("missing claude row; got:\n%s", out)
	}
	if !strings.Contains(out, "opencode") {
		t.Errorf("missing opencode row; got:\n%s", out)
	}
	if !strings.Contains(out, "✓ full") {
		t.Errorf("missing full mark; got:\n%s", out)
	}
}

// TestTranslationReport_PrintTextStyled locks in two contracts:
//   - with color disabled, the styled renderer produces byte-identical output
//     to PrintText (so the same fixture passes through either path), and
//   - with color enabled, semantic ANSI is emitted around the right tokens
//     (green for "full", bold for "plugin:").
func TestTranslationReport_PrintTextStyled(t *testing.T) {
	c := source.Canonical{
		Plugins: []source.Plugin{
			{ID: "demo", Plugin: source.PluginSpec{ID: "demo@test-mp", Version: "1.0.0"}},
		},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude":   {Ops: []adapter.FileOp{{Action: "write", MergeStrategy: "merge-json-keys"}}},
			"opencode": {Ops: []adapter.FileOp{{Action: "write", MergeStrategy: "merge-json-keys"}}},
		},
	}
	report := render.BuildReport(c, plan, []string{"claude", "opencode"})

	var plainBuf, plainStyledBuf, coloredBuf bytes.Buffer
	report.PrintText(&plainBuf)
	report.PrintTextStyled(&plainStyledBuf, ui.New(&plainStyledBuf, &plainStyledBuf, ui.ColorNever))
	report.PrintTextStyled(&coloredBuf, ui.New(&coloredBuf, &coloredBuf, ui.ColorAlways))

	if plainBuf.String() != plainStyledBuf.String() {
		t.Errorf("PrintTextStyled under ColorNever must equal PrintText byte-for-byte\nPrintText:\n%q\nStyled:\n%q",
			plainBuf.String(), plainStyledBuf.String())
	}
	colored := coloredBuf.String()
	if !strings.Contains(colored, "\x1b[1m") {
		t.Errorf("styled report should bold the 'plugin:' label; got:\n%s", colored)
	}
	if !strings.Contains(colored, "\x1b[32m") {
		t.Errorf("a 'full' coverage row should render green; got:\n%s", colored)
	}
	if !strings.Contains(colored, "✓ full") {
		t.Errorf("styled report should still carry the ✓ full glyph; got:\n%s", colored)
	}
}

// TestTranslationReport_SanitizesUntrustedPluginLabel proves the shared
// translation report (printed by `apply`, and whose body `explain` reuses)
// strips terminal control bytes from a plugin id (untrusted fetched-marketplace
// metadata) before printing it, so a hostile plugin cannot smuggle ANSI/escape
// sequences into agentsync's output. The payload also embeds a NEWLINE that
// forges a second "plugin:" header line — sanitizing must drop it so the id
// stays on its own line and cannot fabricate rows. Both the plain (PrintText)
// and styled-under-ColorNever (PrintTextStyled) paths, and the disabled-plugin
// row, are covered; the inert CSI-parameter residue ("[2J[31m") is plain text.
func TestTranslationReport_SanitizesUntrustedPluginLabel(t *testing.T) {
	// A plugin id that tries to (a) clear the screen + set a color via CSI,
	// (b) hide a carriage return, and (c) inject a \n to forge a SECOND plugin
	// header row. Every control byte — including the \n — must be stripped.
	const evil = "x\x1b[2J\x1b[31m\r\nplugin: SPOOFED"
	const wantHeader = "plugin: x[2J[31mplugin: SPOOFED" // ESC/CR/LF gone, residue inert

	build := func(disabled bool) render.TranslationReport {
		c := source.Canonical{Plugins: []source.Plugin{
			{ID: "evil", Plugin: source.PluginSpec{ID: evil, Version: "1.0.0", Disabled: disabled}},
		}}
		plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{
			"claude": {Ops: []adapter.FileOp{{Action: "write", MergeStrategy: "merge-json-keys"}}},
		}}
		return render.BuildReport(c, plan, []string{"claude"})
	}

	// assertNoInjection pins three things at once: no raw ESC/CR survives, the
	// output is EXACTLY two lines (so the injected \n forged no third line), and
	// the whole hostile id collapsed onto the one header line (wantHeader). The
	// caller names the expected substring of the second (non-header) line.
	assertNoInjection := func(t *testing.T, where, out, wantSecond string) {
		t.Helper()
		if strings.ContainsRune(out, '\x1b') {
			t.Errorf("%s: ESC byte leaked into report output: %q", where, out)
		}
		if strings.ContainsRune(out, '\r') {
			t.Errorf("%s: CR byte leaked into report output: %q", where, out)
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("%s: newline injection — want exactly 2 lines (header + row), got %d: %q",
				where, len(lines), out)
		}
		if lines[0] != wantHeader {
			t.Errorf("%s: header line = %q, want %q", where, lines[0], wantHeader)
		}
		if !strings.Contains(lines[1], wantSecond) {
			t.Errorf("%s: second line = %q, want it to contain %q", where, lines[1], wantSecond)
		}
	}

	t.Run("enabled plugin: plain and styled-ColorNever", func(t *testing.T) {
		report := build(false)
		var plain, styled bytes.Buffer
		report.PrintText(&plain)
		report.PrintTextStyled(&styled, ui.New(&styled, &styled, ui.ColorNever))
		assertNoInjection(t, "PrintText", plain.String(), "claude")
		assertNoInjection(t, "PrintTextStyled", styled.String(), "claude")
	})

	t.Run("disabled plugin header is sanitized too", func(t *testing.T) {
		// A disabled plugin renders the (sanitized) header + a "(disabled…)"
		// marker line instead of agent rows — the hostile id must be defanged on
		// that path as well.
		var plain bytes.Buffer
		build(true).PrintText(&plain)
		assertNoInjection(t, "PrintText(disabled)", plain.String(), "(disabled by project)")
	})
}

// TestTranslationReport_JSONKeepsUntrustedLabelRaw pins the other half of the
// contract: `--json` is a machine surface where the consumer owns escaping, so
// PrintJSON must NOT strip control bytes from a plugin id (unlike the text
// path). Asserting the hostile id round-trips byte-for-byte proves the sanitize
// step is scoped to the terminal renderer and never leaks into the JSON.
func TestTranslationReport_JSONKeepsUntrustedLabelRaw(t *testing.T) {
	const evil = "x\x1b[2J\x1b[31m\r\nplugin: SPOOFED"
	c := source.Canonical{Plugins: []source.Plugin{
		{ID: "evil", Plugin: source.PluginSpec{ID: evil}},
	}}
	plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{
		"claude": {Ops: []adapter.FileOp{{Action: "write", MergeStrategy: "merge-json-keys"}}},
	}}
	report := render.BuildReport(c, plan, []string{"claude"})

	var buf bytes.Buffer
	if err := report.PrintJSON(&buf); err != nil {
		t.Fatalf("PrintJSON: %v", err)
	}
	var got render.TranslationReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal PrintJSON output: %v", err)
	}
	if len(got.Rows) == 0 {
		t.Fatal("no rows decoded from PrintJSON output")
	}
	if got.Rows[0].Plugin != evil {
		t.Errorf("JSON must keep the id raw (consumer owns escaping): got %q, want %q",
			got.Rows[0].Plugin, evil)
	}
}

func TestTranslationReport_PrintJSON(t *testing.T) {
	c := source.Canonical{
		Plugins: []source.Plugin{
			{ID: "demo", Plugin: source.PluginSpec{ID: "demo@test-mp"}},
		},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {
				Ops: []adapter.FileOp{{Action: "write", MergeStrategy: "merge-json-keys"}},
			},
		},
	}
	report := render.BuildReport(c, plan, []string{"claude"})

	var buf bytes.Buffer
	if err := report.PrintJSON(&buf); err != nil {
		t.Fatalf("PrintJSON: %v", err)
	}

	var out render.TranslationReport
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("JSON parse: %v", err)
	}
	if len(out.Rows) != 1 {
		t.Fatalf("expected 1 row in JSON, got %d", len(out.Rows))
	}
}

// TestBuildReport_CountsItemsNotOps is the regression for the translation
// report miscounting: it counted merge-json-keys OPS (always 1 for claude's
// single .claude.json merge, and wrongly including hooks/lsp ops) as "mcp",
// and counted every replace op (skills/subagents/memory) as "commands". The
// counts must reflect actual canonical items.
func TestBuildReport_CountsItemsNotOps(t *testing.T) {
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "github"}, {ID: "slack"}, {ID: "jira"}},
		Memory:     source.Memory{Body: "# mem"},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {
				Ops: []adapter.FileOp{
					{Action: "write", Path: "/h/.claude.json", MergeStrategy: "merge-json-keys"},
					{Action: "write", Path: "/h/.claude/CLAUDE.md", MergeStrategy: "replace"},
				},
			},
		},
	}
	report := render.BuildReport(c, plan, []string{"claude"})
	row := report.Rows[0]
	if row.MCP != 3 {
		t.Fatalf("MCP = %d, want 3 (server count, not op count)", row.MCP)
	}
	if row.Commands != 0 {
		t.Fatalf("Commands = %d, want 0 (memory must not be counted as a command)", row.Commands)
	}
}

// TestBuildReport_InventoryCountsAllKinds pins that a row describes EVERY
// component kind the model hosts for the agent, not just MCP + commands — so a
// plugin shipping skills / subagents / hooks / an LSP server is no longer
// reported as a bare "0 mcp · 0 commands".
func TestBuildReport_InventoryCountsAllKinds(t *testing.T) {
	c := source.Canonical{
		MCPServers: []source.MCPServer{{ID: "m"}},
		LSPServers: []source.LSPServer{{ID: "l"}},
		Commands:   []source.Command{{Name: "c"}},
		Skills:     []source.Skill{{Name: "s"}},
		Subagents:  []source.Subagent{{Name: "a"}},
		Hooks:      []source.Hook{{Event: "PreToolUse"}},
		Plugins:    []source.Plugin{{ID: "demo", Plugin: source.PluginSpec{ID: "demo@mp"}}},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"claude": {Ops: []adapter.FileOp{{Action: "write", MergeStrategy: "merge-json-keys"}}},
		},
	}
	report := render.BuildReport(c, plan, []string{"claude"})
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(report.Rows))
	}
	row := report.Rows[0]
	for _, tc := range []struct {
		got  int
		want int
		name string
	}{
		{row.MCP, 1, "mcp"},
		{row.LSP, 1, "lsp"},
		{row.Commands, 1, "commands"},
		{row.Skills, 1, "skills"},
		{row.Subagents, 1, "subagents"},
		{row.Hooks, 1, "hooks"},
	} {
		if tc.got != tc.want {
			t.Errorf("row.%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestBuildReport_CoverageNoneWhenNothingRendered pins the user-visible behavior
// for an LSP-only plugin on a non-LSP agent: the LSP is counted in the inventory
// (LSP=1) yet renders nothing, so the row is "none". This case reads the same
// under the old (mcp/commands) and new (rendered) coverage logic — it guards the
// inventory count + the none-case, NOT the rendered-vs-counts distinction (that
// is TestBuildReport_CoveragePartialWhenSomethingRendered's job).
func TestBuildReport_CoverageNoneWhenNothingRendered(t *testing.T) {
	c := source.Canonical{
		LSPServers: []source.LSPServer{{ID: "l"}},
		Plugins:    []source.Plugin{{ID: "demo", Plugin: source.PluginSpec{ID: "demo@mp"}}},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"codex": {Ops: nil, Skips: []adapter.Skip{{Component: "lsp", Name: "l", Reason: "no LSP concept", Kind: adapter.SkipDropped}}},
		},
	}
	row := render.BuildReport(c, plan, []string{"codex"}).Rows[0]
	if row.LSP != 1 {
		t.Errorf("LSP = %d, want 1 (the hosted server is still counted)", row.LSP)
	}
	if row.Coverage != "none" {
		t.Errorf("coverage = %q, want none (nothing rendered)", row.Coverage)
	}
}

// TestBuildReport_CoveragePartialWhenSomethingRendered is the regression for a
// latent bug the inventory change exposed: coverage was derived from
// (mcp>0||commands>0), so a plugin whose skills rendered but whose (say) hook was
// skipped was mislabeled "none" despite real output. Coverage now keys off
// whether the plan rendered any op, so this is correctly "partial".
func TestBuildReport_CoveragePartialWhenSomethingRendered(t *testing.T) {
	c := source.Canonical{
		Skills:  []source.Skill{{Name: "s"}},
		Plugins: []source.Plugin{{ID: "demo", Plugin: source.PluginSpec{ID: "demo@mp"}}},
	}
	plan := render.RenderPlan{
		PerAgent: map[string]render.AgentResult{
			"codex": {
				Ops:   []adapter.FileOp{{Action: "write", MergeStrategy: "replace"}}, // the skill rendered
				Skips: []adapter.Skip{{Component: "hook", Name: "x", Reason: "unknown event", Kind: adapter.SkipDropped}},
			},
		},
	}
	row := render.BuildReport(c, plan, []string{"codex"}).Rows[0]
	if row.Skills != 1 {
		t.Errorf("Skills = %d, want 1", row.Skills)
	}
	if row.Coverage != "partial" {
		t.Errorf("coverage = %q, want partial (a skill rendered; only the hook was skipped)", row.Coverage)
	}
}

// TestBuildReport_BaseCoverageFromRendered locks the rendered-based coverage on
// the "(base)" (no-plugins) branch too — the path apply/apply --dry-run summarize
// through. The computeCoverage change is shared by every BuildReport caller, but
// the other coverage tests all exercise the per-plugin branch; this pins that the
// base branch derives partial/none from the plan's ops identically.
func TestBuildReport_BaseCoverageFromRendered(t *testing.T) {
	c := source.Canonical{Skills: []source.Skill{{Name: "s"}}} // no Plugins → "(base)"
	rendered := render.RenderPlan{PerAgent: map[string]render.AgentResult{
		"codex": {
			Ops:   []adapter.FileOp{{Action: "write", MergeStrategy: "replace"}},
			Skips: []adapter.Skip{{Component: "hook", Reason: "unknown event", Kind: adapter.SkipDropped}},
		},
	}}
	if row := render.BuildReport(c, rendered, []string{"codex"}).Rows[0]; row.Plugin != "(base)" || row.Coverage != "partial" {
		t.Errorf("base row = {plugin:%q coverage:%q}, want {(base) partial}", row.Plugin, row.Coverage)
	}
	nothing := render.RenderPlan{PerAgent: map[string]render.AgentResult{
		"codex": {Ops: nil, Skips: []adapter.Skip{{Component: "lsp", Reason: "no LSP concept", Kind: adapter.SkipDropped}}},
	}}
	if row := render.BuildReport(c, nothing, []string{"codex"}).Rows[0]; row.Coverage != "none" {
		t.Errorf("base row coverage = %q, want none (nothing rendered)", row.Coverage)
	}
}

// TestBuildReport_CountsHonorTargeting exercises the enabled/agents filtering in
// countMCPServers and countLSPServers: a server scoped to other agents, or
// disabled, is not counted for this agent. Without this, a plugin's MCP/LSP
// server scoped to claude could be silently counted on codex's row.
func TestBuildReport_CountsHonorTargeting(t *testing.T) {
	off := false
	c := source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "claude-only", Server: source.MCPServerSpec{Agents: []string{"claude"}}},
			{ID: "disabled", Server: source.MCPServerSpec{Enabled: &off}},
			{ID: "all"},
		},
		LSPServers: []source.LSPServer{
			{ID: "lsp-claude", Spec: source.LSPServerSpec{Agents: []string{"claude"}}},
			{ID: "lsp-off", Spec: source.LSPServerSpec{Enabled: &off}},
			{ID: "lsp-all"},
		},
	}
	plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{
		"claude": {Ops: []adapter.FileOp{{Action: "write", MergeStrategy: "merge-json-keys"}}},
		"codex":  {Ops: []adapter.FileOp{{Action: "write", MergeStrategy: "merge-toml-keys"}}},
	}}
	byAgent := map[string]render.PluginRow{}
	for _, r := range render.BuildReport(c, plan, []string{"claude", "codex"}).Rows {
		byAgent[r.Agent] = r
	}
	// claude: claude-only + all = 2 mcp; lsp-claude + lsp-all = 2 lsp (disabled excluded).
	if g := byAgent["claude"]; g.MCP != 2 || g.LSP != 2 {
		t.Errorf("claude counts = mcp %d lsp %d; want mcp 2 lsp 2", g.MCP, g.LSP)
	}
	// codex: only the untargeted "all"/"lsp-all" reach it = 1 mcp, 1 lsp.
	if g := byAgent["codex"]; g.MCP != 1 || g.LSP != 1 {
		t.Errorf("codex counts = mcp %d lsp %d; want mcp 1 lsp 1", g.MCP, g.LSP)
	}
}

// TestBuildReport_NotTargetedRows pins the two per-agent "contributed nothing"
// outcomes. They matter because their row is built from a plan the plugin's
// components were FILTERED OUT of — so the ordinary counting path would report
// the whole model's totals under this plugin's name, telling the user a plugin
// rendered components it did not. And the two are distinguished on purpose: a
// narrowed allowlist is a choice the user made, while a deferral says the agent
// installs the plugin itself, and the remedies differ.
func TestBuildReport_NotTargetedRows(t *testing.T) {
	claudeOnly := []string{"claude"}
	cases := []struct {
		name         string
		spec         source.PluginSpec
		agent        string
		wantCoverage string
		wantFlag     bool
	}{
		{
			name:         "allowlist excludes this agent",
			spec:         source.PluginSpec{ID: "toolkit@mp", Agents: []string{"codex"}},
			agent:        "claude",
			wantCoverage: "not-targeted",
			wantFlag:     true,
		},
		{
			name:         "deferral names this agent",
			spec:         source.PluginSpec{ID: "toolkit@mp", Agents: []string{"*"}, NativeAgents: &claudeOnly},
			agent:        "claude",
			wantCoverage: "native",
			wantFlag:     true,
		},
		{
			name:         "targeted normally — an ordinary coverage row",
			spec:         source.PluginSpec{ID: "toolkit@mp", Agents: []string{"*"}},
			agent:        "claude",
			wantCoverage: "full",
			wantFlag:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := source.Canonical{
				MCPServers: []source.MCPServer{{ID: "srv"}},
				Plugins:    []source.Plugin{{ID: "toolkit", Plugin: tc.spec}},
			}
			plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{
				tc.agent: {Ops: []adapter.FileOp{{Action: "write", Path: "/home/.claude.json"}}},
			}}
			report := render.BuildReport(c, plan, []string{tc.agent})
			if len(report.Rows) != 1 {
				t.Fatalf("expected 1 row, got %d", len(report.Rows))
			}
			row := report.Rows[0]
			// The row's own Agent field, not just the printed line: it is a
			// --json field, and blanking it survived a mutation that the
			// PrintText assertion could not see.
			if row.Agent != tc.agent {
				t.Errorf("Agent = %q, want %q", row.Agent, tc.agent)
			}
			if row.Coverage != tc.wantCoverage {
				t.Errorf("Coverage = %q, want %q", row.Coverage, tc.wantCoverage)
			}
			if row.NotTargeted != tc.wantFlag {
				t.Errorf("NotTargeted = %v, want %v", row.NotTargeted, tc.wantFlag)
			}
			// A row for a plugin that contributed nothing must not carry counts
			// borrowed from the rest of the model — that is the misreport this
			// row exists to prevent.
			if tc.wantFlag && row.MCP != 0 {
				t.Errorf("a non-contributing row reported %d MCP servers; it rendered none", row.MCP)
			}
		})
	}
}

// TestBuildReport_CoverageOutcomes pins the three RENDER outcomes together with
// the MARK each prints. Other tests already assert the coverage strings; what
// nothing covered was the printed vocabulary, so a constant's value could change
// and only the JSON contract would notice. They are the --json contract too, so
// a silent value change is a silent contract break.
func TestBuildReport_CoverageOutcomes(t *testing.T) {
	cases := []struct {
		name     string
		skips    []adapter.Skip
		ops      []adapter.FileOp
		want     string
		wantMark string
	}{
		{name: "no skips", ops: []adapter.FileOp{{Action: "write", Path: "/x"}}, want: "full", wantMark: "✓ full"},
		{
			name:  "skipped something but still rendered",
			skips: []adapter.Skip{{Component: "lsp", Name: "l", Reason: "no concept"}},
			ops:   []adapter.FileOp{{Action: "write", Path: "/x"}},
			want:  "partial", wantMark: "◐ partial",
		},
		{
			name:  "skipped everything, rendered nothing",
			skips: []adapter.Skip{{Component: "lsp", Name: "l", Reason: "no concept"}},
			want:  "none", wantMark: "✗ none",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := source.Canonical{MCPServers: []source.MCPServer{{ID: "srv"}}}
			plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{
				"claude": {Ops: tc.ops, Skips: tc.skips},
			}}
			report := render.BuildReport(c, plan, []string{"claude"})
			if got := report.Rows[0].Coverage; got != tc.want {
				t.Errorf("Coverage = %q, want %q", got, tc.want)
			}
			var buf bytes.Buffer
			report.PrintText(&buf)
			if !strings.Contains(buf.String(), tc.wantMark) {
				t.Errorf("printed report should carry %q; got:\n%s", tc.wantMark, buf.String())
			}
		})
	}
}

// TestBuildReport_CountsHonourPluginTargeting pins that the per-agent inventory
// respects the plugin gates. The counts are computed over the WHOLE model, so a
// deferred plugin's MCP/LSP servers were attributed to whatever other plugin's
// row happened to be printed for that agent — apply reporting "2 mcp" to an
// agent that received one.
func TestBuildReport_CountsHonourPluginTargeting(t *testing.T) {
	claudeOnly := []string{"claude"}
	c := source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "beta-srv", Plugin: "beta", PluginAgents: []string{"*"}},
			{ID: "alpha-srv", Plugin: "alpha", PluginAgents: []string{"*"}, PluginNativeAgents: claudeOnly},
		},
		LSPServers: []source.LSPServer{
			{ID: "alpha-lsp", Plugin: "alpha", PluginAgents: []string{"*"}, PluginNativeAgents: claudeOnly},
		},
		// The text kinds and hooks have no per-component allowlist of their own,
		// so the plugin gate is the ONLY thing that can exclude them — and it was
		// missed for all four when MCP/LSP were fixed, leaving a row that
		// reported more commands than its own mcp count reflected.
		Commands:  []source.Command{{Name: "beta-c", Plugin: "beta", PluginAgents: []string{"*"}}, {Name: "alpha-c", Plugin: "alpha", PluginAgents: []string{"*"}, PluginNativeAgents: claudeOnly}},
		Skills:    []source.Skill{{Name: "alpha-s", Plugin: "alpha", PluginAgents: []string{"*"}, PluginNativeAgents: claudeOnly}},
		Subagents: []source.Subagent{{Name: "alpha-a", Plugin: "alpha", PluginAgents: []string{"*"}, PluginNativeAgents: claudeOnly}},
		Hooks:     []source.Hook{{Event: "PreToolUse", Command: "alpha-h", Plugin: "alpha", PluginAgents: []string{"*"}, PluginNativeAgents: claudeOnly}},
		Plugins: []source.Plugin{
			{ID: "beta", Plugin: source.PluginSpec{ID: "beta@mp", Agents: []string{"*"}}},
		},
	}
	plan := render.RenderPlan{PerAgent: map[string]render.AgentResult{
		"claude": {Ops: []adapter.FileOp{{Action: "write", Path: "/x"}}},
	}}
	report := render.BuildReport(c, plan, []string{"claude"})
	if len(report.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(report.Rows))
	}
	if got := report.Rows[0].MCP; got != 1 {
		t.Errorf("MCP count = %d, want 1 — the deferred plugin's server must not be counted here", got)
	}
	if got := report.Rows[0].LSP; got != 0 {
		t.Errorf("LSP count = %d, want 0 — the only LSP server belongs to the deferred plugin", got)
	}
	if got := report.Rows[0].Commands; got != 1 {
		t.Errorf("Commands count = %d, want 1 — the deferred plugin's command must not be counted", got)
	}
	for _, tc := range []struct {
		kind string
		got  int
	}{
		{"Skills", report.Rows[0].Skills},
		{"Subagents", report.Rows[0].Subagents},
		{"Hooks", report.Rows[0].Hooks},
	} {
		if tc.got != 0 {
			t.Errorf("%s count = %d, want 0 — every one belongs to the deferred plugin", tc.kind, tc.got)
		}
	}
}

// TestPrintText_NotTargetedRowsExplainThemselves pins the human output. The row
// carries no coverage mark, so without a dedicated line it would print as a bare
// agent name with an empty status — indistinguishable from a bug. Each variant
// must say WHICH gate excluded the agent, because the remedies differ.
func TestPrintText_NotTargetedRowsExplainThemselves(t *testing.T) {
	report := render.TranslationReport{Rows: []render.PluginRow{
		{Plugin: "toolkit@mp", Agent: "claude", Coverage: "native", NotTargeted: true},
		{Plugin: "toolkit@mp", Agent: "codex", Coverage: "not-targeted", NotTargeted: true},
	}}
	var buf bytes.Buffer
	report.PrintText(&buf)
	got := buf.String()
	if !strings.Contains(got, "served natively") || !strings.Contains(got, "native_agents") {
		t.Errorf("the deferral row must name the deferral as the reason; got:\n%s", got)
	}
	if !strings.Contains(got, "`agents` allowlist") {
		t.Errorf("the allowlist row must name the allowlist as the reason; got:\n%s", got)
	}
	// The agent NAME must be on the line. Without this the row says "something
	// was deferred" without saying for whom — and blanking Agent survived
	// mutation because nothing here read it.
	for _, agent := range []string{"claude", "codex"} {
		if !strings.Contains(got, agent) {
			t.Errorf("the row must name the agent it is about; %q missing from:\n%s", agent, got)
		}
	}
}

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
					{Component: "hook", Name: "pre-run", Reason: "unsupported"},
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
		{Component: "lsp", Name: "gopls", Reason: "Codex has no LSP configuration concept"},
		{Component: "hook", Name: "SessionEnd", Reason: "Codex does not recognize this lifecycle event"},
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
	if row.SkipDetails[0] != (render.SkipDetail{Component: "lsp", Name: "gopls", Reason: "Codex has no LSP configuration concept"}) {
		t.Errorf("SkipDetails[0] = %+v, want the gopls lsp skip", row.SkipDetails[0])
	}

	// JSON surface: lowercase component/name/reason keys under skipDetails.
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
				Skips: []adapter.Skip{{Component: "lsp", Name: "gopls", Reason: "Codex has no LSP configuration concept"}},
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
	if row.SkipDetails[0] != (render.SkipDetail{Component: "lsp", Name: "gopls", Reason: "Codex has no LSP configuration concept"}) {
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
			"codex": {Ops: nil, Skips: []adapter.Skip{{Component: "lsp", Name: "l", Reason: "no LSP concept"}}},
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
				Skips: []adapter.Skip{{Component: "hook", Name: "x", Reason: "unknown event"}},
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
			Skips: []adapter.Skip{{Component: "hook", Reason: "unknown event"}},
		},
	}}
	if row := render.BuildReport(c, rendered, []string{"codex"}).Rows[0]; row.Plugin != "(base)" || row.Coverage != "partial" {
		t.Errorf("base row = {plugin:%q coverage:%q}, want {(base) partial}", row.Plugin, row.Coverage)
	}
	nothing := render.RenderPlan{PerAgent: map[string]render.AgentResult{
		"codex": {Ops: nil, Skips: []adapter.Skip{{Component: "lsp", Reason: "no LSP concept"}}},
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

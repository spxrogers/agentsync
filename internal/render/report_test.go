package render_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/source"
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

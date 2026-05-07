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
	c := source.Canonical{}
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
		t.Errorf("mcp = %d, want 1", row.MCP)
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

func TestBuildReport_PartialCoverage(t *testing.T) {
	c := source.Canonical{
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

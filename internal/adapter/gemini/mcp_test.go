package gemini_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/gemini"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// TestGeminiResolvedDollarSurvives pins the theme-C5 behavior across env, headers,
// and url/httpUrl: a resolved value containing a literal '$' (both `$VAR`-shaped
// `ab$cd` and `${VAR}`-shaped `p${q}`) is written VERBATIM into settings.json —
// NOT mangled with a fabricated escape, which Gemini has none of and which would
// itself corrupt the value — and the corruption risk is surfaced as a SkipReduced.
// IngestMCPSpec of the rendered value returns the original unchanged, so the
// canonical round-trip is lossless. (Upstream: gemini-cli envVarResolver.ts
// expands $VAR/${VAR}/${VAR:-default} in every settings.json string with no escape.)
func TestGeminiResolvedDollarSurvives(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		build   func(v string) source.MCPServerSpec
		fromMap func(spec map[string]any) any       // read the rendered value
		fromIng func(s source.MCPServerSpec) string // read it back after IngestMCPSpec
		field   string                              // label expected in the SkipReduced reason
	}{
		{
			name:  "env $VAR",
			value: "ab$cd",
			build: func(v string) source.MCPServerSpec {
				return source.MCPServerSpec{Type: "stdio", Command: "x", Env: map[string]string{"TOKEN": v}}
			},
			fromMap: func(s map[string]any) any { return s["env"].(map[string]any)["TOKEN"] },
			fromIng: func(s source.MCPServerSpec) string { return s.Env["TOKEN"] },
			field:   "env[TOKEN]",
		},
		{
			name:  "headers ${VAR}",
			value: "Bearer p${q}",
			build: func(v string) source.MCPServerSpec {
				return source.MCPServerSpec{Type: "http", URL: "https://x/mcp", Headers: map[string]string{"Authorization": v}}
			},
			fromMap: func(s map[string]any) any { return s["headers"].(map[string]any)["Authorization"] },
			fromIng: func(s source.MCPServerSpec) string { return s.Headers["Authorization"] },
			field:   "headers[Authorization]",
		},
		{
			name:    "url $VAR",
			value:   "https://x/$cd/sse",
			build:   func(v string) source.MCPServerSpec { return source.MCPServerSpec{Type: "sse", URL: v} },
			fromMap: func(s map[string]any) any { return s["url"] },
			fromIng: func(s source.MCPServerSpec) string { return s.URL },
			field:   "url",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := source.Canonical{MCPServers: []source.MCPServer{{ID: "srv", Server: tt.build(tt.value)}}}
			ops, skips, err := gemini.New(gemini.Options{TargetRoot: t.TempDir()}).Render(secrets.ForRender(c), adapter.ScopeUser, "")
			if err != nil {
				t.Fatal(err)
			}
			op := findOp(ops, ".gemini/settings.json")
			if op == nil {
				t.Fatal("settings.json op missing")
			}
			var ours map[string]any
			if err := json.Unmarshal(op.Content, &ours); err != nil {
				t.Fatal(err)
			}
			spec := ours["mcpServers"].(map[string]any)["srv"].(map[string]any)

			// (1) Value written verbatim (no fabricated escape).
			if got := tt.fromMap(spec); got != tt.value {
				t.Fatalf("value not written verbatim: got %v, want %q\n%s", got, tt.value, op.Content)
			}
			// (2) The corruption risk is surfaced as a SkipReduced naming the field.
			sk := hasSkip(skips, "mcp", "srv", adapter.SkipReduced)
			if sk == nil {
				t.Fatalf("want a SkipReduced for the '$' value, got %+v", skips)
			}
			if !strings.Contains(sk.Reason, tt.field) {
				t.Fatalf("SkipReduced reason must name %q; got %q", tt.field, sk.Reason)
			}
			// (3) IngestMCPSpec returns the original (lossless — no escape to invert).
			if back := tt.fromIng(gemini.IngestMCPSpec(spec)); back != tt.value {
				t.Fatalf("IngestMCPSpec must return the original value: got %q, want %q", back, tt.value)
			}
		})
	}
}

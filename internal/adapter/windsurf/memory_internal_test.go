package windsurf

import "testing"

// TestStripMemoryRuleFrontmatter is a pure-unit table-driven check of the fence
// stripper's three-way signal: the exact agentsync `always_on` block round-trips
// silently (agentsync=true), any other leading fence is stripped AND flagged
// foreign (so the caller warns), and a file with no leading fence — including one
// with an in-body `---` horizontal rule — is left untouched with no signal.
func TestStripMemoryRuleFrontmatter(t *testing.T) {
	tests := []struct {
		name          string
		in            string
		wantBody      string
		wantAgentsync bool
		wantForeign   bool
	}{
		{
			name:          "exact always_on block",
			in:            "---\ntrigger: always_on\n---\n\n# Body\n",
			wantBody:      "# Body\n",
			wantAgentsync: true,
			wantForeign:   false,
		},
		{
			name:          "bare always_on without trailing blank line",
			in:            "---\ntrigger: always_on\n---\n# Body\n",
			wantBody:      "# Body\n",
			wantAgentsync: true,
			wantForeign:   false,
		},
		{
			name:          "foreign trigger manual with extra keys",
			in:            "---\ntrigger: manual\ndescription: X\n---\n\n# Body\n",
			wantBody:      "# Body\n",
			wantAgentsync: false,
			wantForeign:   true,
		},
		{
			name:          "foreign trigger glob with globs key",
			in:            "---\ntrigger: glob\nglobs: **/*.go\n---\n\n# Body\n",
			wantBody:      "# Body\n",
			wantAgentsync: false,
			wantForeign:   true,
		},
		{
			name:          "in-body horizontal rule, no leading fence",
			in:            "# Title\n\nsome text\n\n---\n\nmore text\n",
			wantBody:      "# Title\n\nsome text\n\n---\n\nmore text\n",
			wantAgentsync: false,
			wantForeign:   false,
		},
		{
			name:          "leading --- but no closing fence, not frontmatter",
			in:            "---\n# Title\n\nbody with no closing fence\n",
			wantBody:      "---\n# Title\n\nbody with no closing fence\n",
			wantAgentsync: false,
			wantForeign:   false,
		},
		{
			name:          "empty input",
			in:            "",
			wantBody:      "",
			wantAgentsync: false,
			wantForeign:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, agentsync, foreign := stripMemoryRuleFrontmatter([]byte(tt.in))
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
			if agentsync != tt.wantAgentsync {
				t.Errorf("wasAgentsyncBlock = %v, want %v", agentsync, tt.wantAgentsync)
			}
			if foreign != tt.wantForeign {
				t.Errorf("hadForeignFrontmatter = %v, want %v", foreign, tt.wantForeign)
			}
		})
	}
}

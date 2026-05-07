//go:build live

// Package marketplace_test contains live integration tests that make real
// network calls. These tests are opt-in: they are excluded from all standard
// CI runs and must be explicitly enabled via the build tag and env var.
//
// Run with:
//
//	AGENTSYNC_LIVE_PLUGIN_TEST=1 go test -tags=live ./internal/marketplace/... -run LiveSuperpowers -v
package marketplace_test

import (
	"os"
	"testing"

	"github.com/spxrogers/agentsync/internal/marketplace"
)

// TestLiveSuperpowers fetches the obra/superpowers Claude plugin from GitHub
// and verifies that its skills/commands/agents are fully projected (frontmatter
// + body + clean Name). It is gated behind AGENTSYNC_LIVE_PLUGIN_TEST=1 so
// it never runs unintentionally.
func TestLiveSuperpowers(t *testing.T) {
	if os.Getenv("AGENTSYNC_LIVE_PLUGIN_TEST") != "1" {
		t.Skip("set AGENTSYNC_LIVE_PLUGIN_TEST=1 to run the live obra/superpowers test")
	}

	cacheDir := t.TempDir()

	// Fetch obra/superpowers via the existing GitFetcher.
	fetcher := &marketplace.GitFetcher{}
	src := marketplace.Source{
		Kind: "github",
		Repo: "obra/superpowers",
	}
	result, err := fetcher.Fetch(src, cacheDir)
	if err != nil {
		t.Fatalf("GitFetcher.Fetch obra/superpowers: %v", err)
	}
	t.Logf("fetched obra/superpowers at HEAD %s", result.HeadSHA)

	// Project the fetched plugin.
	entry := marketplace.PluginEntry{Name: "superpowers"}
	pr, err := marketplace.Project(entry, cacheDir)
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	t.Logf("projection result: %d skills, %d commands, %d subagents, %d mcp servers",
		len(pr.Skills), len(pr.Commands), len(pr.Subagents), len(pr.MCPServers))

	// Assert at least 5 skills land with non-empty Body and a populated name.
	const minSkills = 5
	if len(pr.Skills) < minSkills {
		t.Errorf("expected at least %d skills, got %d", minSkills, len(pr.Skills))
	}
	for _, sk := range pr.Skills {
		if sk.Name == "" {
			t.Errorf("skill has empty name (frontmatter name and basename both empty?)")
		}
		if sk.Body == "" {
			t.Errorf("skill %q has empty body", sk.Name)
		}
		if sk.Frontmatter == nil {
			t.Errorf("skill %q has nil frontmatter map", sk.Name)
		}
		t.Logf("  skill: %q (frontmatter keys: %v)", sk.Name, frontmatterKeys(sk.Frontmatter))
	}

	// Commands: log how many landed; assert at least 1 if any exist.
	if len(pr.Commands) > 0 {
		t.Logf("commands (%d):", len(pr.Commands))
		for _, cmd := range pr.Commands {
			t.Logf("  command: %q", cmd.Name)
			if cmd.Name == "" {
				t.Errorf("command has empty name")
			}
			if cmd.Body == "" {
				t.Errorf("command %q has empty body", cmd.Name)
			}
		}
	} else {
		t.Logf("no commands in obra/superpowers (that's fine, it may not ship any)")
	}

	// Subagents: same as commands.
	if len(pr.Subagents) > 0 {
		t.Logf("subagents (%d):", len(pr.Subagents))
		for _, ag := range pr.Subagents {
			t.Logf("  subagent: %q", ag.Name)
			if ag.Name == "" {
				t.Errorf("subagent has empty name")
			}
			if ag.Body == "" {
				t.Errorf("subagent %q has empty body", ag.Name)
			}
		}
	} else {
		t.Logf("no subagents in obra/superpowers (that's fine)")
	}
}

// frontmatterKeys returns a sorted list of keys in a frontmatter map.
func frontmatterKeys(fm map[string]any) []string {
	if fm == nil {
		return nil
	}
	keys := make([]string, 0, len(fm))
	for k := range fm {
		keys = append(keys, k)
	}
	return keys
}

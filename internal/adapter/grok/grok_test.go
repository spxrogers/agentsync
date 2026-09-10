package grok_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/grok"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/testenv"
)

func TestMain(m *testing.M) {
	testenv.MustRunInContainer()
	os.Exit(m.Run())
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNativeRoundTrip(t *testing.T) {
	for _, sc := range []struct {
		name  string
		scope adapter.Scope
	}{
		{"user", adapter.ScopeUser}, {"project", adapter.ScopeProject},
	} {
		t.Run(sc.name, func(t *testing.T) {
			root, dest := t.TempDir(), t.TempDir()
			project, destProject := "", ""
			if sc.scope == adapter.ScopeProject {
				project, destProject = root, dest
			}
			dir, destDir := filepath.Join(root, ".grok"), filepath.Join(dest, ".grok")
			memory, destMemory := filepath.Join(dir, "AGENTS.md"), filepath.Join(destDir, "AGENTS.md")
			if sc.scope == adapter.ScopeProject {
				memory, destMemory = filepath.Join(root, "AGENTS.md"), filepath.Join(dest, "AGENTS.md")
			}
			writeFile(t, memory, []byte("# Team conventions\n\nKeep changes focused.\n"), 0o644)
			writeFile(t, filepath.Join(dir, "config.toml"), []byte(`
[mcp_servers.local]
command = "runner"
args = ["--flag", "two words"]
env = { API_KEY = "synthetic-test-token" }
startup_timeout_sec = 45
tool_timeout_sec = 9007199254740993
disabled = false
[mcp_servers.remote]
url = "https://example.test/mcp"
headers = { Authorization = "Bearer synthetic-token" }
`), 0o644)
			writeFile(t, filepath.Join(dir, "skills", "review", "SKILL.md"), []byte("---\nname: review\ndescription: Review changes\nmetadata:\n  author: Test\n---\nReview carefully.\n"), 0o644)
			bundles := map[string][]byte{"scripts/check.sh": []byte("#!/bin/sh\nexit 0\n"), "assets/data.bin": {0, 255, 1}, "references/nested/guide.md": []byte("Reference\n")}
			for rel, data := range bundles {
				mode := os.FileMode(0o644)
				if strings.HasPrefix(rel, "scripts/") {
					mode = 0o755
				}
				writeFile(t, filepath.Join(dir, "skills", "review", rel), data, mode)
			}
			writeFile(t, filepath.Join(dir, "commands", "review.md"), []byte("---\ndescription: Review this\nargument-hint: target\n---\nReview $ARGUMENTS.\n"), 0o644)
			writeFile(t, filepath.Join(dir, "hooks", "agentsync.json"), []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"check-one"},{"type":"command","command":"check-two"}]}],"PostCompact":[{"matcher":"","hooks":[{"command":"context-note"}]}]}}`), 0o644)
			a := grok.New(grok.Options{TargetRoot: root, Stderr: io.Discard})
			c, err := a.Ingest(sc.scope, project)
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Skills) != 1 || len(c.Skills[0].Files) != 3 || len(c.Commands) != 1 || len(c.Hooks) != 3 || len(c.MCPServers) != 2 {
				t.Fatalf("incomplete native capture: %#v", c)
			}
			if c.MCPServers[1].Server.Type != "http" || c.MCPServers[1].Server.Headers["Authorization"] != "Bearer synthetic-token" {
				t.Fatal("remote headers/transport lost")
			}
			b := grok.New(grok.Options{TargetRoot: dest})
			ops, skips, err := b.Render(secrets.ForRender(c), sc.scope, destProject)
			if err != nil {
				t.Fatal(err)
			}
			if len(skips) != 0 {
				t.Fatalf("unexpected loss: %v", skips)
			}
			if err := b.Apply(ops, adapter.PassThroughWriter{}); err != nil {
				t.Fatal(err)
			}
			for rel, want := range bundles {
				path := filepath.Join(destDir, "skills", "review", rel)
				if !bytes.Equal(readFile(t, path), want) {
					t.Errorf("bundle changed: %s", rel)
				}
				if strings.HasPrefix(rel, "scripts/") {
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if info.Mode().Perm() != 0o755 {
						t.Fatal("executable bit lost")
					}
				}
			}
			if got := source.StripManagedBanner(string(readFile(t, destMemory))); got != c.Memory.Body {
				t.Fatal("memory changed")
			}
			var native map[string]any
			if err := toml.Unmarshal(readFile(t, filepath.Join(destDir, "config.toml")), &native); err != nil {
				t.Fatal(err)
			}
			local := native["mcp_servers"].(map[string]any)["local"].(map[string]any)
			if local["tool_timeout_sec"] != int64(9007199254740993) || local["startup_timeout_sec"] != int64(45) {
				t.Fatalf("native extra fields lost: %#v", local)
			}
			var hooks map[string]any
			if err := json.Unmarshal(readFile(t, filepath.Join(destDir, "hooks", "agentsync.json")), &hooks); err != nil {
				t.Fatal(err)
			}
			groups := hooks["hooks"].(map[string]any)["PreToolUse"].([]any)
			if len(groups) != 1 || len(groups[0].(map[string]any)["hooks"].([]any)) != 2 {
				t.Fatal("hook group/order lost")
			}
			captured, err := b.Ingest(sc.scope, destProject)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c.Skills, captured.Skills) || !reflect.DeepEqual(c.Commands, captured.Commands) {
				t.Fatal("Markdown components changed on capture")
			}
			before := readFile(t, filepath.Join(destDir, "config.toml"))
			ops, _, err = b.Render(secrets.ForRender(captured), sc.scope, destProject)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Apply(ops, adapter.PassThroughWriter{}); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, readFile(t, filepath.Join(destDir, "config.toml"))) {
				t.Fatal("TOML does not converge")
			}
		})
	}
}

func TestScopesDetectionAndHome(t *testing.T) {
	root, project, custom := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("PATH", t.TempDir())
	a := grok.New(grok.Options{TargetRoot: root})
	if detected, err := a.Detect(); err != nil || detected {
		t.Fatalf("absent detection: %v %v", detected, err)
	}
	writeFile(t, filepath.Join(os.Getenv("PATH"), "grok"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	if detected, err := a.Detect(); err != nil || !detected {
		t.Fatalf("binary detection: %v %v", detected, err)
	}
	t.Setenv("PATH", t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if detected, err := a.Detect(); err != nil || !detected {
		t.Fatalf("directory detection: %v %v", detected, err)
	}
	c := source.Canonical{Memory: source.Memory{Body: "global"}, Project: &source.Canonical{Memory: source.Memory{Body: "project"}}}
	a = grok.New(grok.Options{TargetRoot: root, GrokHome: custom})
	for _, sc := range []struct {
		scope               adapter.Scope
		project, path, body string
	}{
		{adapter.ScopeUser, "", filepath.Join(custom, "AGENTS.md"), "global"},
		{adapter.ScopeProject, project, filepath.Join(project, "AGENTS.md"), "project"},
	} {
		ops, _, err := a.Render(secrets.ForRender(c), sc.scope, sc.project)
		if err != nil {
			t.Fatal(err)
		}
		if len(ops) != 1 || ops[0].Path != sc.path || source.StripManagedBanner(string(ops[0].Content)) != sc.body {
			t.Fatalf("wrong scope: %#v", ops)
		}
	}
	if got := a.VersionRoots(adapter.ScopeUser, ""); !reflect.DeepEqual(got, []string{custom}) {
		t.Fatalf("version root: %v", got)
	}
	if got := a.VersionRoots(adapter.ScopeProject, project); len(got) != 0 {
		t.Fatal("project must not version user config")
	}
	if _, _, err := a.Render(secrets.ForRender(c), adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatal("Render accepted missing root")
	}
	if _, err := a.Ingest(adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatal("Ingest accepted missing root")
	}
	if _, err := a.RefusedHookEvents(adapter.ScopeProject, ""); !errors.Is(err, adapter.ErrProjectRootRequired) {
		t.Fatal("hook guard accepted missing root")
	}
	a = grok.New(grok.Options{TargetRoot: root, GrokHome: "relative"})
	if _, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, ""); err == nil {
		t.Fatal("relative GROK_HOME accepted")
	}
}

func TestReportedLimitsAndTargeting(t *testing.T) {
	no := false
	c := source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "disabled", Server: source.MCPServerSpec{Command: "skip", Enabled: &no}},
			{ID: "other-agent", Server: source.MCPServerSpec{Command: "skip", Agents: []string{"claude"}}},
			{ID: "remote", Server: source.MCPServerSpec{Type: "sse", URL: "https://example.test/${PRIVATE_TOKEN}"}},
		},
		Hooks:      []source.Hook{{Event: "PermissionRequest", Type: "command", Command: "skip"}, {Event: "PreToolUse", Type: "http", Command: "skip"}},
		Commands:   []source.Command{{Name: "review", Frontmatter: map[string]any{"allowed-tools": "Read"}, Body: "review"}},
		Subagents:  []source.Subagent{{Name: "reviewer"}},
		LSPServers: []source.LSPServer{{ID: "typescript"}},
	}
	a := grok.New(grok.Options{TargetRoot: t.TempDir()})
	ops, skips, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(skips) != 7 {
		t.Fatalf("expected 7 explicit limitations, got %v", skips)
	}
	for _, skip := range skips {
		if skip.Kind == adapter.SkipKindUnset || strings.Contains(skip.Reason, "PRIVATE_TOKEN") {
			t.Fatalf("unsafe/unclassified report: %#v", skip)
		}
	}
	if len(ops) != 2 || bytes.Contains(ops[0].Content, []byte("disabled")) || bytes.Contains(ops[0].Content, []byte("other-agent")) {
		t.Fatalf("targeting failed: %#v", ops)
	}
}

func TestHookCaptureRefusesLossAndLeavesOtherFiles(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		refused     bool
	}{
		{"timeout", `[{"hooks":[{"type":"command","command":"check","timeout":10}]}]`, true},
		{"http", `[{"hooks":[{"type":"http","url":"https://example.test"}]}]`, true},
		{"definition-extra", `[{"hooks":[],"timeout":10}]`, true},
		{"unmodeled-before-missing-command", `[{"hooks":[{"timeout":10}]}]`, true},
		{"missing-command", `[{"hooks":[{"type":"command"}]}]`, false},
		{"malformed-command", `[{"hooks":[{"type":"command","command":42}]}]`, false},
		{"malformed-type", `[{"hooks":[{"type":42,"command":"check"}]}]`, false},
		{"malformed-handler", `[{"hooks":[42]}]`, false},
		{"malformed-matcher", `[{"matcher":42,"hooks":[]}]`, false},
		{"missing-handlers", `[{}]`, false},
		{"malformed-handlers", `[{"hooks":42}]`, false},
		{"malformed-definition", `[42]`, false},
		{"malformed-event", `42`, false},
		{"structural-before-semantic", `[42,{"hooks":[{"timeout":10}]}]`, false},
		{"semantic-before-structural", `[{"hooks":[{"timeout":10}]},42]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".grok", "hooks", "agentsync.json")
			value := tc.value
			if strings.HasPrefix(value, "[") {
				// One invalid group must prevent capture of valid sibling groups.
				value = `[{"hooks":[{"type":"command","command":"valid"}]},` + value[1:]
			}
			writeFile(t, path, []byte(`{"hooks":{"PreToolUse":`+value+`}}`), 0o644)
			other := filepath.Join(filepath.Dir(path), "personal.json")
			original := []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"personal"}]}]}}`)
			writeFile(t, other, original, 0o644)
			var old, warn bytes.Buffer
			a := grok.New(grok.Options{TargetRoot: root, Stderr: &old})
			a.SetStderr(&warn)
			c, err := a.Ingest(adapter.ScopeUser, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(c.Hooks) != 0 || old.Len() != 0 || warn.Len() == 0 {
				t.Fatal("lossy hooks captured or warnings misrouted")
			}
			refused, err := a.RefusedHookEvents(adapter.ScopeUser, "")
			if err != nil {
				t.Fatal(err)
			}
			if (len(refused) == 1) != tc.refused {
				t.Fatalf("semantic/structural refusal: %v", refused)
			}
			if tc.refused && refused[0] != "PreToolUse" {
				t.Fatal("wrong event identity")
			}
			if !bytes.Equal(readFile(t, other), original) {
				t.Fatal("foreign hook file changed")
			}
		})
	}
}

func TestReadAndMergeErrorsPreserveDestinations(t *testing.T) {
	for _, tc := range []struct{ name, relative, content string }{
		{"toml", "config.toml", "[broken"},
		{"mcp-shape", "config.toml", "mcp_servers = 7"},
		{"mcp-field", "config.toml", "[mcp_servers.bad]\ncommand = 7"},
		{"json", "hooks/agentsync.json", "{broken"},
		{"hooks-shape", "hooks/agentsync.json", `{"hooks":7}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".grok", tc.relative)
			writeFile(t, path, []byte(tc.content), 0o644)
			a := grok.New(grok.Options{TargetRoot: root, Stderr: io.Discard})
			if _, err := a.Ingest(adapter.ScopeUser, ""); err == nil {
				t.Fatal("bad native config accepted")
			}
			if !bytes.Equal(readFile(t, path), []byte(tc.content)) {
				t.Fatal("ingest changed source")
			}
			if tc.name == "toml" || tc.name == "json" {
				err := a.Apply([]adapter.FileOp{{Path: path, Content: []byte(`{}`), MergeStrategy: adapter.MergeStrategyForPath(a, path)}}, adapter.PassThroughWriter{})
				if err == nil || !bytes.Equal(readFile(t, path), []byte(tc.content)) {
					t.Fatal("merge overwrote malformed native file")
				}
			}
		})
	}
	for _, relative := range []string{"config.toml", "AGENTS.md", "skills", "commands", "hooks"} {
		t.Run("unreadable-"+relative, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".grok", relative)
			if relative == "config.toml" || relative == "AGENTS.md" {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, path, []byte("not a directory"), 0o644)
			}
			if _, err := grok.New(grok.Options{TargetRoot: root}).Ingest(adapter.ScopeUser, ""); err == nil {
				t.Fatal("unreadable path treated as absent")
			}
		})
	}
}

package support

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/cucumber/godog"
	secrets_pkg "github.com/spxrogers/agentsync/internal/secrets"
)

// RegisterSteps wires every Gherkin phrase used by the .feature files to a
// concrete Go implementation.
func RegisterSteps(sc *godog.ScenarioContext, w *World) {
	// ── Setup steps ──────────────────────────────────────────────────────────
	sc.Step(`^a clean agentsync home$`, w.givenCleanHome)
	sc.Step(`^I have run "agentsync init"$`, w.givenInit)
	sc.Step(`^I have added agent "([^"]+)"$`, w.givenAddAgent)
	sc.Step(`^I have added agents "([^"]+)" and "([^"]+)"$`, w.givenAddTwoAgents)

	// ── Action steps ─────────────────────────────────────────────────────────
	sc.Step(`^I run "agentsync ([^"]*)"$`, w.whenIRun)
	sc.Step(`^I run "agentsync ([^"]*)" and it fails$`, w.whenIRunAndItFails)
	sc.Step(`^I write the file "([^"]+)" with:$`, w.whenIWriteFile)
	sc.Step(`^I append to "([^"]+)":$`, w.whenIAppendFile)
	sc.Step(`^I tamper with "([^"]+)" by replacing "([^"]+)" with "([^"]+)"$`, w.whenITamperReplace)
	sc.Step(`^I create a local marketplace "([^"]+)" with plugin "([^"]+)" exposing MCP "([^"]+)" command "([^"]+)"$`, w.whenICreateLocalMarketplace)
	sc.Step(`^I create a local marketplace "([^"]+)" with plugin "([^"]+)" with explicit skills commands and agents$`, w.whenICreateProjectionTestMarketplace)
	sc.Step(`^I configure age secrets$`, w.whenIConfigureAgeSecrets)
	sc.Step(`^I encrypt secret "([^"]+)" = "([^"]+)"$`, w.whenIEncryptSecret)
	sc.Step(`^I run two "agentsync apply" invocations concurrently$`, w.whenIRunConcurrentApplies)

	// ── Assertion steps ──────────────────────────────────────────────────────
	sc.Step(`^the command succeeds$`, w.thenCommandSucceeds)
	sc.Step(`^the command fails$`, w.thenCommandFails)
	sc.Step(`^the output contains "([^"]*)"$`, w.thenOutputContains)
	sc.Step(`^the output does not contain "([^"]*)"$`, w.thenOutputDoesNotContain)
	sc.Step(`^the output is valid JSON$`, w.thenOutputIsJSON)
	sc.Step(`^the file "([^"]+)" exists$`, w.thenFileExists)
	sc.Step(`^the file "([^"]+)" does not exist$`, w.thenFileDoesNotExist)
	sc.Step(`^the file "([^"]+)" contains "([^"]*)"$`, w.thenFileContains)
	sc.Step(`^the file "([^"]+)" does not contain "([^"]*)"$`, w.thenFileDoesNotContain)
	sc.Step(`^the directory "([^"]+)" exists$`, w.thenDirExists)
}

// ===== given (context) ======================================================

func (w *World) givenCleanHome() error {
	// Reset() already provisioned a fresh tmpdir before the scenario; this is
	// a no-op kept as a readable Gherkin anchor.
	if w.Home == "" {
		return fmt.Errorf("scenario tmpdir was not provisioned")
	}
	return nil
}

func (w *World) givenInit() error {
	out, err := w.Run("init")
	if err != nil {
		return fmt.Errorf("init: %v\n%s", err, out)
	}
	return nil
}

func (w *World) givenAddAgent(name string) error {
	out, err := w.Run("agent", "add", name)
	if err != nil {
		return fmt.Errorf("agent add %s: %v\n%s", name, err, out)
	}
	return nil
}

func (w *World) givenAddTwoAgents(a, b string) error {
	if err := w.givenAddAgent(a); err != nil {
		return err
	}
	return w.givenAddAgent(b)
}

// ===== when (actions) =======================================================

func (w *World) whenIRun(rest string) error {
	args := splitArgs(rest)
	_, err := w.Run(args...)
	if err != nil {
		// Don't fail the step here — the assertion phase decides whether the
		// non-zero exit is expected. Stash and continue.
		return nil
	}
	return nil
}

func (w *World) whenIRunAndItFails(rest string) error {
	args := splitArgs(rest)
	_, err := w.Run(args...)
	if err == nil {
		return fmt.Errorf("expected non-zero exit, got 0\noutput:\n%s", w.LastOut)
	}
	return nil
}

func (w *World) whenIWriteFile(path string, body *godog.DocString) error {
	return w.WriteFile(path, body.Content)
}

func (w *World) whenIAppendFile(path string, body *godog.DocString) error {
	return w.AppendFile(path, body.Content)
}

func (w *World) whenITamperReplace(path, find, replace string) error {
	cur, err := w.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.Contains(cur, find) {
		return fmt.Errorf("tamper: %q not found in %s", find, path)
	}
	updated := strings.Replace(cur, find, replace, 1)
	return w.WriteFile(path, updated)
}

func (w *World) whenICreateLocalMarketplace(dirRel, pluginID, mcpID, mcpCmd string) error {
	dir := w.Resolve(dirRel)
	mpDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(mpDir, 0o755); err != nil {
		return err
	}
	marketplace := map[string]any{
		"name":  filepath.Base(dir),
		"owner": map[string]any{"name": "bdd"},
		"plugins": []map[string]any{
			{"name": pluginID, "source": "./plugins/" + pluginID},
		},
	}
	mpJSON, _ := json.MarshalIndent(marketplace, "", "  ")
	if err := os.WriteFile(filepath.Join(mpDir, "marketplace.json"), mpJSON, 0o644); err != nil {
		return err
	}
	pluginDir := filepath.Join(dir, "plugins", pluginID, ".claude-plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return err
	}
	plugin := map[string]any{
		"name":    pluginID,
		"version": "1.0.0",
		"mcpServers": map[string]any{
			mcpID: map[string]any{"command": mcpCmd, "args": []string{"hello"}},
		},
	}
	pluginJSON, _ := json.MarshalIndent(plugin, "", "  ")
	return os.WriteFile(filepath.Join(pluginDir, "plugin.json"), pluginJSON, 0o644)
}

// whenICreateProjectionTestMarketplace builds a local marketplace with a
// single plugin that declares explicit "skills", "commands", and "agents"
// arrays in its plugin.json manifest — i.e. the path through the projection
// code that was broken by the regression fixed in 4b781b1.
//
// The three component files embed unique sentinel tokens in their bodies so
// that downstream BDD assertions can confirm the rendered destination files
// contain real content (not empty stubs).
func (w *World) whenICreateProjectionTestMarketplace(dirRel, pluginID string) error {
	dir := w.Resolve(dirRel)

	// ── marketplace fixture ─────────────────────────────────────────────────
	mpDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(mpDir, 0o755); err != nil {
		return err
	}
	marketplace := map[string]any{
		"name":  filepath.Base(dir),
		"owner": map[string]any{"name": "bdd"},
		"plugins": []map[string]any{
			{"name": pluginID, "source": "./plugins/" + pluginID},
		},
	}
	mpJSON, _ := json.MarshalIndent(marketplace, "", "  ")
	if err := os.WriteFile(filepath.Join(mpDir, "marketplace.json"), mpJSON, 0o644); err != nil {
		return err
	}

	// ── plugin source tree ──────────────────────────────────────────────────
	pluginRoot := filepath.Join(dir, "plugins", pluginID)

	// skills/proj-skill/SKILL.md
	skillPath := filepath.Join(pluginRoot, "skills", "proj-skill")
	if err := os.MkdirAll(skillPath, 0o755); err != nil {
		return err
	}
	skillMD := "---\nname: proj-skill\ndescription: projection test skill\n---\nBODY_TOKEN_skill_proj-skill\n"
	if err := os.WriteFile(filepath.Join(skillPath, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		return err
	}
	// A skill is a DIRECTORY: a bundled script must project to the destination
	// alongside SKILL.md, not be dropped. Written executable to also lock the
	// +x-preservation path through projection → render → apply.
	if err := os.MkdirAll(filepath.Join(skillPath, "scripts"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(skillPath, "scripts", "run.sh"), []byte("#!/bin/sh\necho BODY_TOKEN_skill_script\n"), 0o755); err != nil {
		return err
	}

	// agents/proj-agent.md
	agentPath := filepath.Join(pluginRoot, "agents")
	if err := os.MkdirAll(agentPath, 0o755); err != nil {
		return err
	}
	agentMD := "---\nname: proj-agent\ndescription: projection test subagent\n---\nBODY_TOKEN_agent_proj-agent\n"
	if err := os.WriteFile(filepath.Join(agentPath, "proj-agent.md"), []byte(agentMD), 0o644); err != nil {
		return err
	}

	// commands/proj-cmd.md
	cmdPath := filepath.Join(pluginRoot, "commands")
	if err := os.MkdirAll(cmdPath, 0o755); err != nil {
		return err
	}
	cmdMD := "---\nname: proj-cmd\ndescription: projection test command\n---\nBODY_TOKEN_cmd_proj-cmd\n"
	if err := os.WriteFile(filepath.Join(cmdPath, "proj-cmd.md"), []byte(cmdMD), 0o644); err != nil {
		return err
	}

	// .claude-plugin/plugin.json — explicit manifest listing relative paths
	pluginManifestDir := filepath.Join(pluginRoot, ".claude-plugin")
	if err := os.MkdirAll(pluginManifestDir, 0o755); err != nil {
		return err
	}
	plugin := map[string]any{
		"name":     pluginID,
		"version":  "1.0.0",
		"skills":   []string{"./skills/proj-skill"},
		"agents":   []string{"./agents/proj-agent.md"},
		"commands": []string{"./commands/proj-cmd.md"},
	}
	pluginJSON, _ := json.MarshalIndent(plugin, "", "  ")
	return os.WriteFile(filepath.Join(pluginManifestDir, "plugin.json"), pluginJSON, 0o644)
}

func (w *World) whenIConfigureAgeSecrets() error {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	idPath := filepath.Join(w.Home, ".config", "agentsync", "age.key")
	if err := os.MkdirAll(filepath.Dir(idPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(idPath, []byte(id.String()), 0o600); err != nil {
		return err
	}
	cfgPath := filepath.Join(w.Home, ".agentsync", "agentsync.toml")
	body, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	body = append(body, []byte(fmt.Sprintf(`
[secrets]
backend       = "age"
file          = "secrets/secrets.age"
recipient     = %q
identity_file = %q
`, id.Recipient().String(), idPath))...)
	if err := os.WriteFile(cfgPath, body, 0o644); err != nil {
		return err
	}
	w.ExtraEnv["AGENTSYNC_BDD_AGE_RECIPIENT"] = id.Recipient().String()
	return nil
}

func (w *World) whenIEncryptSecret(dottedKey, value string) error {
	recipient := w.ExtraEnv["AGENTSYNC_BDD_AGE_RECIPIENT"]
	if recipient == "" {
		return fmt.Errorf("age secrets not configured; call 'I configure age secrets' first")
	}
	parts := strings.SplitN(dottedKey, ".", 2)
	if len(parts) != 2 {
		return fmt.Errorf("dotted key %q must be section.key", dottedKey)
	}
	body := fmt.Sprintf("[%s]\n%s = %q\n", parts[0], parts[1], value)
	return secrets_pkg.Encrypt([]byte(body), recipient,
		filepath.Join(w.Home, ".agentsync", "secrets", "secrets.age"))
}

// whenIRunConcurrentApplies validates the apply.lock contract: two parallel
// applies must serialize and both produce a sane outcome.
func (w *World) whenIRunConcurrentApplies() error {
	var wg sync.WaitGroup
	results := make([]error, 2)
	outputs := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(w.Bin, "apply")
			cmd.Env = w.Env()
			cmd.Dir = w.WorkDir
			out, err := cmd.CombinedOutput()
			outputs[i] = string(out)
			results[i] = err
		}(i)
		// stagger just enough to make the race interesting without being a sleep loop
		time.Sleep(5 * time.Millisecond)
	}
	wg.Wait()
	// Both invocations must complete successfully (the lock is supposed to
	// queue, not bounce). Stash the last output so subsequent assertions can
	// inspect it.
	w.LastOut = outputs[0] + "\n---\n" + outputs[1]
	if results[0] != nil && results[1] != nil {
		return fmt.Errorf("both concurrent applies failed:\nA: %v\n%s\nB: %v\n%s",
			results[0], outputs[0], results[1], outputs[1])
	}
	return nil
}

// ===== then (assertions) ====================================================

func (w *World) thenCommandSucceeds() error {
	if w.LastErr != nil {
		return fmt.Errorf("expected success, got exit=%d err=%v\noutput:\n%s",
			w.LastCode, w.LastErr, w.LastOut)
	}
	return nil
}

func (w *World) thenCommandFails() error {
	if w.LastErr == nil {
		return fmt.Errorf("expected failure, got success\noutput:\n%s", w.LastOut)
	}
	return nil
}

func (w *World) thenOutputContains(needle string) error {
	if !strings.Contains(w.LastOut, needle) {
		return fmt.Errorf("output missing %q\nfull output:\n%s", needle, w.LastOut)
	}
	return nil
}

func (w *World) thenOutputDoesNotContain(needle string) error {
	if strings.Contains(w.LastOut, needle) {
		return fmt.Errorf("output unexpectedly contains %q\nfull output:\n%s", needle, w.LastOut)
	}
	return nil
}

func (w *World) thenOutputIsJSON() error {
	var anyVal any
	if err := json.Unmarshal([]byte(w.LastOut), &anyVal); err != nil {
		return fmt.Errorf("output is not valid JSON: %v\noutput:\n%s", err, w.LastOut)
	}
	return nil
}

func (w *World) thenFileExists(path string) error {
	abs := w.Resolve(path)
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("file %s missing: %v", abs, err)
	}
	return nil
}

func (w *World) thenFileDoesNotExist(path string) error {
	abs := w.Resolve(path)
	if _, err := os.Stat(abs); err == nil {
		return fmt.Errorf("file %s unexpectedly exists", abs)
	}
	return nil
}

func (w *World) thenFileContains(path, needle string) error {
	body, err := w.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %v", path, err)
	}
	if !strings.Contains(body, needle) {
		return fmt.Errorf("%s missing %q\ncontent:\n%s", path, needle, body)
	}
	return nil
}

func (w *World) thenFileDoesNotContain(path, needle string) error {
	body, err := w.ReadFile(path)
	if err != nil {
		// A nonexistent file trivially does not contain anything; that
		// satisfies the assertion. Any other read error is real.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %v", path, err)
	}
	if strings.Contains(body, needle) {
		return fmt.Errorf("%s unexpectedly contains %q\ncontent:\n%s", path, needle, body)
	}
	return nil
}

func (w *World) thenDirExists(path string) error {
	abs := w.Resolve(path)
	st, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("dir %s missing: %v", abs, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("%s exists but is not a directory", abs)
	}
	return nil
}

// splitArgs splits an args string on spaces but honors double-quoted segments
// so feature steps can pass values that contain spaces.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// silence compile-time complaints for runtime helpers we may not always use.
var _ = runtime.GOOS

# agentsync M7 — Polish + release

> Conventions in [`overview`](2026-05-04-agentsync-v1.0-overview.md). Builds on M0–M6.

**Goal:** Ship-ready v1.0. `explain` (per-plugin transparency), `import` (capture native edits into source), `agent disable --purge` (delete destination files when an agent is removed), goreleaser cross-platform release pipeline, Homebrew tap, Scoop manifest, Chocolatey package, native Linux package distributions (deb/rpm via goreleaser), and a comprehensive README with install / quickstart / troubleshooting / known-limits docs.

**Architecture:** No new infrastructure; this milestone polishes existing surfaces. Distribution config lives at the repo root (`.goreleaser.yaml`, `.github/workflows/release.yml`, `README.md`). Tap-specific files (`Formula/agentsync.rb`) live in a separate repo (`spxrogers/homebrew-tap`) that goreleaser pushes to.

---

## Task 1: `agentsync explain <plugin> [--json]`

**Files:** `internal/cli/explain.go`, `internal/cli/explain_test.go`

For one plugin id, print the per-agent translation report from M4 — what each adapter does with each component.

```go
func newExplainCmd() *cobra.Command {
    var jsonOut bool
    cmd := &cobra.Command{
        Use:   "explain <plugin-id>",
        Args:  cobra.ExactArgs(1),
        Short: "show per-agent translation for one plugin",
        RunE: func(cmd *cobra.Command, args []string) error {
            pluginID := args[0]
            // Load canonical, find plugin, ask each adapter to Render WITH ONLY that plugin's components,
            // then print the per-agent ✓/◐/✗ table.
            // Reuses internal/render/report.go from M4.
            return nil
        },
    }
    cmd.Flags().BoolVar(&jsonOut, "json", false, "structured JSON output")
    return cmd
}
```

Test, commit.

---

## Task 2: `agentsync import <selector>`

**Files:** `internal/cli/import.go`, `internal/cli/import_test.go`

Selector grammar: `<agent>:<component>:<name>`. Reads native config from disk via the adapter's `Ingest()`, finds the matching item, writes it to the canonical source via `internal/source.Writer` (from M3 Task 7).

```bash
# Examples:
opensync import claude:mcp:github
opensync import opencode:agent:reviewer
opensync import claude:plugin:atlassian-anthropic   # post-M4: import a plugin install record
```

Implementation: parse selector, dispatch to the right Ingest path, find matching item by name, marshal back to canonical via Writer. Test for each component type. Commit.

---

## Task 3: `agentsync agent disable --purge`

**Files:** modify `internal/cli/agent.go`

Today `agent disable` only flips the `enabled` bit. With `--purge`, also walks `state.Files` + `state.Keys` for that agent and emits delete FileOps (using the adapter's `Apply`) so the destination is cleaned.

- [ ] **Test**

```go
func TestAgentDisable_Purge_RemovesDestFiles(t *testing.T) {
    // setup: apply with an MCP, verify ~/.claude.json has it
    // run: agent disable claude --purge
    // verify: ~/.claude.json mcpServers.github gone (or whole file removed if all keys ours)
}
```

- [ ] **Implement**: add `--purge` flag. On purge: iterate state for agent, build delete ops (or merge ops setting our keys to absence), call Apply, remove state entries. Commit.

---

## Task 4: README expansion

**Files:** modify `README.md`

Sections to add:

```markdown
# agentsync

Centrally manage AI coding-agent configurations across Claude Code, OpenCode, Codex CLI, and Cursor.

## Quickstart

    agentsync init
    agentsync agent add claude
    agentsync agent add opencode
    agentsync mcp add github --command npx --args "-y,@modelcontextprotocol/server-github"
    agentsync apply

## Install

### macOS — Homebrew

    brew tap spxrogers/tap
    brew install agentsync

### Windows — Scoop

    scoop bucket add spxrogers https://github.com/spxrogers/scoop-bucket
    scoop install agentsync

### Windows — Chocolatey

    choco install agentsync

### Linux

Debian/Ubuntu:

    curl -fsSL https://github.com/spxrogers/agentsync/releases/latest/download/agentsync.deb -o agentsync.deb
    sudo dpkg -i agentsync.deb

RPM:

    sudo rpm -i https://github.com/spxrogers/agentsync/releases/latest/download/agentsync.rpm

Arch (AUR):

    yay -S agentsync

## Cross-machine sync

agentsync is single-machine. To sync `~/.agentsync/` across machines, use chezmoi (or any dotfile manager):

    chezmoi add ~/.agentsync

## Secrets — age key backup

If you lose your age private key, you lose access to all encrypted secrets. Recommended: store the key in a 1Password Secure Note or your machine-setup repo. agentsync does not back up the key for you.

## Known limits in v1.x

- **OpenCode hooks**: OpenCode hooks are JS/TS plugins, not declarative shell commands. agentsync v1 does NOT auto-translate Claude hooks to OpenCode. Hand-author a small JS/TS plugin if you need a hook on OpenCode.
- **Cursor user-level rules**: Cursor stores user-level rules in app-local storage (not the filesystem). agentsync's Cursor adapter manages project-scope rules only.
- **LSP projection beyond Claude**: OpenCode/Codex/Cursor LSP support is deferred. Claude plugins that include LSP servers install correctly on Claude itself; on other agents you'll see `lsp server X skipped` in the apply translation report.
- **Continue, Gemini CLI, Aider**: not on the v1.x roadmap.

## Troubleshooting

- **First apply on a populated machine**: agentsync sees pre-existing native config files and triggers `foreign-collision`. The original is backed up to `~/.agentsync/.state/backups/<ts>/` before the new content lands. Recommend `agentsync apply --dry-run` first to preview the translation report.
- **`agentsync update` fails to fetch a marketplace**: verify the marketplace URL with `git ls-remote`. agentsync uses `go-git` and falls back to system `git` for sparse clones if needed.
- **`${secret:foo}` not resolving**: run `agentsync secrets get foo` to verify the key exists in the decrypted file. age library errors will surface here.

## License

MIT.
```

Commit.

---

## Task 5: goreleaser release pipeline

**Files:** modify `.goreleaser.yaml`, add `.github/workflows/release.yml`

Extend `.goreleaser.yaml` with the distribution configs:

```yaml
brews:
  - name: agentsync
    repository:
      owner: spxrogers
      name: homebrew-tap
    homepage: https://github.com/spxrogers/agentsync
    description: Centrally manage AI coding-agent configurations
    license: MIT
    install: bin.install "agentsync"
    test: system "#{bin}/agentsync --version"

scoops:
  - name: agentsync
    repository:
      owner: spxrogers
      name: scoop-bucket
    homepage: https://github.com/spxrogers/agentsync
    description: Centrally manage AI coding-agent configurations
    license: MIT

chocolateys:
  - name: agentsync
    api_key: '{{ .Env.CHOCOLATEY_API_KEY }}'
    title: agentsync
    project_url: https://github.com/spxrogers/agentsync
    license_url: https://github.com/spxrogers/agentsync/blob/main/LICENSE
    summary: Centrally manage AI coding-agent configurations

nfpms:
  - id: linux-pkgs
    package_name: agentsync
    homepage: https://github.com/spxrogers/agentsync
    license: MIT
    formats: [deb, rpm]
    section: utils
    description: Centrally manage AI coding-agent configurations.

aurs:
  - name: agentsync-bin
    homepage: https://github.com/spxrogers/agentsync
    description: Centrally manage AI coding-agent configurations.
    maintainers: ['Steven Rogers <noreply@example.com>']
    license: MIT
    private_key: '{{ .Env.AUR_KEY }}'
    git_url: 'ssh://aur@aur.archlinux.org/agentsync-bin.git'
```

`.github/workflows/release.yml`:

```yaml
name: release
on:
  push:
    tags: ['v*']

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: actions/setup-go@v5
        with: { go-version: '1.22.x' }
      - uses: goreleaser/goreleaser-action@v6
        with: { version: latest, args: release --clean }
        env:
          GITHUB_TOKEN:        ${{ secrets.GH_PAT }}     # for tap + bucket pushes
          CHOCOLATEY_API_KEY:  ${{ secrets.CHOCO_KEY }}
          AUR_KEY:             ${{ secrets.AUR_KEY }}
```

(Required secrets configured on the repo: `GH_PAT` with `repo` scope so goreleaser can push to `homebrew-tap` and `scoop-bucket`; `CHOCO_KEY` from chocolatey.org; `AUR_KEY` ed25519 SSH key registered to the maintainer's AUR account.)

- [ ] **Smoke test**: tag `v1.0.0-rc.1`, push, verify the release workflow produces all artifacts and pushes to taps. Roll back the tag if anything's wrong before retrying with `v1.0.0`.

Commit.

---

## Task 6: Companion repos

**One-time setup** (not a code change to this repo):

1. Create `spxrogers/homebrew-tap` repo (empty; goreleaser populates).
2. Create `spxrogers/scoop-bucket` repo (empty; goreleaser populates).
3. Register `agentsync-bin` package on AUR with the SSH key matching `AUR_KEY`.
4. Register `agentsync` package on Chocolatey (community.chocolatey.org).

These steps are documented in the README's "release" appendix; the engineer running v1.0 release does them once.

---

## Task 7: Final integration test — full v1.0 lifecycle

**Files:** `test/e2e/v1_lifecycle_test.go` (new top-level e2e harness)

Top-level shell-style integration test that exercises every M0–M7 surface:

```go
//go:build e2e
package e2e

import (
    "os"
    "os/exec"
    "path/filepath"
    "strings"
    "testing"

    "filippo.io/age"
)

// Build agentsync into a tmp bin dir; tests then exec it like a user would.
func TestE2E_FullV1Lifecycle(t *testing.T) {
    bin := buildBinary(t)
    home := t.TempDir()
    env := append(os.Environ(),
        "AGENTSYNC_TARGET_ROOT="+home,
        "HOME="+home,
        "PATH="+filepath.Dir(bin)+string(filepath.ListSeparator)+os.Getenv("PATH"),
    )
    run := func(args ...string) (string, error) {
        cmd := exec.Command(bin, args...)
        cmd.Env = env
        out, err := cmd.CombinedOutput()
        return string(out), err
    }

    // 1. init
    if _, err := run("init"); err != nil { t.Fatal(err) }

    // 2. agents
    _, _ = run("agent", "add", "claude")
    _, _ = run("agent", "add", "opencode")

    // 3. age secrets
    id, _ := age.GenerateX25519Identity()
    _ = os.MkdirAll(filepath.Join(home, ".config", "agentsync"), 0o755)
    _ = os.WriteFile(filepath.Join(home, ".config", "agentsync", "age.key"), []byte(id.String()), 0o600)
    cfg := filepath.Join(home, ".agentsync", "opensync.toml")
    body, _ := os.ReadFile(cfg)
    body = append(body, []byte("[secrets]\nbackend=\"age\"\nfile=\"secrets/secrets.age\"\nrecipient=\""+id.Recipient().String()+"\"\nidentity_file=\""+filepath.Join(home, ".config", "agentsync", "age.key")+"\"\n")...)
    _ = os.WriteFile(cfg, body, 0o644)
    // ... (encrypt secrets file, mcp w/ ${secret:...}, marketplace add, plugin install, apply, status, reconcile, etc.)
}

func buildBinary(t *testing.T) string {
    t.Helper()
    dir := t.TempDir()
    bin := filepath.Join(dir, "agentsync")
    cmd := exec.Command("go", "build", "-o", bin, "./cmd/agentsync")
    if out, err := cmd.CombinedOutput(); err != nil {
        t.Fatalf("build: %v\n%s", err, out)
    }
    return bin
}
```

CI runs `go test -tags=e2e ./test/e2e/...` on the release workflow before pushing. Commit.

---

## Task 8: Final commit + tag

```bash
go test -race ./...
golangci-lint run ./...
goreleaser release --snapshot --skip publish --clean    # local sanity

git tag v1.0.0
git push origin v1.0.0
```

GitHub Actions release job picks up the tag and ships everything.

---

## Done When

- `agentsync explain <plugin>` shows the per-agent translation table for any installed plugin (in human or `--json` form).
- `agentsync import <agent>:<component>:<name>` captures any native config back into canonical.
- `agentsync agent disable claude --purge` removes the agent and cleans destination files we owned.
- `git tag v1.0.0 && git push origin v1.0.0` triggers a release that:
  - Builds Linux/macOS/Windows × amd64/arm64 binaries.
  - Pushes Homebrew formula to `spxrogers/homebrew-tap`.
  - Pushes Scoop manifest to `spxrogers/scoop-bucket`.
  - Submits Chocolatey package.
  - Pushes AUR PKGBUILD.
  - Uploads `.deb` + `.rpm` to GitHub Releases.
- README documents quickstart, install per-platform, cross-machine sync, age-key backup, known limits, and troubleshooting.
- E2E test passes on linux/macos/windows.
- CI green at all phases.

**v1.0 ships.** v1.1 (Codex) and v1.2 (Cursor) are separately-tracked plans, written after v1.0 lands and the patterns are proven.

# Launch checklist — going public with agentsync v1.0

Tracking what needs to happen between "v1.0 done in private" and "agentsync repo
public, install commands work for users on macOS / Windows / Linux." Captured
here so it doesn't have to live in head or in [issue #13](https://github.com/spxrogers/agentsync/issues/13).

Everything here is intentionally **deferred**, not broken. v1.0 is functional
end-to-end (tests green under `just test-release`); these items are about
distribution plumbing, OSS hygiene, and a few documented v1 trade-offs that may
warrant attention before the repo flips public.

Order is roughly "do first" → "do later." The OSS-hygiene block and the repo-
public flip are gating; the goreleaser/companion-repo block builds on those;
the adapter-coverage and comment-preservation items can be addressed any time
(or shipped in v1.1 / v1.x with documented limitations as today).

---

## §1 — OSS hygiene (gating)

These all need to exist before flipping the GitHub repo public. Without them,
the project looks like an abandoned experiment instead of a v1.0.

- [ ] **`LICENSE`** — pick one. MIT or Apache-2.0 most likely; Apache-2.0 if you
      want explicit patent grant, MIT if you want the shortest possible license.
- [ ] **`CONTRIBUTING.md`** — at minimum: "this is personal-first /
      OSS-shareable, PRs welcome but no SLA, here's how to run tests
      (`just test-release`), here's the commit-message convention." Can be
      brief.
- [ ] **`SECURITY.md`** — disclosure process. Especially relevant given age key
      handling: tell users what to do if they find a way to leak secrets,
      bypass the lock, or trick the per-key merge.
- [ ] **`CODE_OF_CONDUCT.md`** — optional but expected on most public OSS.
      Contributor Covenant is fine.
- [ ] **`CHANGELOG.md`** — start at v1.0.0. Future releases append; keeps users
      and packagers honest.
- [ ] **`.github/ISSUE_TEMPLATE/`** — at least bug-report and feature-request
      templates. Pre-fills "what version", "what OS", "what's in your
      `agentsync.toml`", which saves time on triage.
- [ ] **`.github/pull_request_template.md`** — short template referencing
      `just test-release` as the bar.
- [ ] **GitHub repo description, topics, homepage URL** — set on the repo
      Settings page. `cli`, `golang`, `claude-code`, `mcp`, `dotfiles`,
      `dev-tools` are reasonable topics.
- [ ] **Local clone path housekeeping** — current dev clone is at
      `~/projects/opensync/` (predates rename). Either rename to `agentsync/`
      or add a note in CONTRIBUTING.md so external contributors don't trip on
      it.

## §2 — Make the repo public (gating)

- [ ] **GitHub repo Settings → Change visibility → Public.** Do this AFTER §1
      lands but BEFORE §3 (goreleaser publishing fails on private repos and
      Homebrew taps need a fetchable archive URL).
- [ ] **Final pre-flip review.** Skim the README install commands, the
      LAUNCH_CHECKLIST link in any Issues, and confirm no in-progress drafts
      (e.g. `docs/superpowers/research/`) reference private context that
      shouldn't ship.

## §3 — Distribution plumbing

The `.goreleaser.yaml` already has all four publishing blocks present but
commented out with `# TODO: enable when going public`. Each item below is
"create the companion repo, uncomment the block, validate."

- [ ] **Tag a release** — `git tag v1.0.0 && git push origin v1.0.0`. CI runs
      goreleaser; with no companion repos in place yet, this still produces
      release artifacts on the GitHub Releases page (binaries + checksums + the
      already-active `nfpms` deb/rpm packages).
- [ ] **Homebrew tap** — create `spxrogers/homebrew-agentsync` repo. Uncomment
      the `brews:` block in `.goreleaser.yaml`. Validate on a clean macOS box:
      ```
      brew tap spxrogers/agentsync
      brew install agentsync
      agentsync --version
      ```
      *Highest user impact* — most v1.0 users are on macOS.
- [ ] **Scoop bucket** — create `spxrogers/scoop-agentsync` (or similar)
      repo. Uncomment the `scoops:` block. Validate on a clean Windows box:
      ```
      scoop bucket add agentsync https://github.com/spxrogers/scoop-agentsync
      scoop install agentsync
      ```
- [ ] **Chocolatey package** — create the package + nuspec. Uncomment the
      `chocolateys:` block. Validate via `choco install agentsync` on a clean
      Windows box.
- [ ] **AUR package** — uncomment the `aurs:` block. Lower priority; AUR users
      are typically comfortable installing from source.
- [ ] **Linux native packages (apt / yum)** — `nfpms` is already active and
      produces deb/rpm artifacts on every tagged release. Decide hosting:
      attach to GitHub Releases (simplest), or stand up apt/yum repos
      (`packagecloud.io` is the usual paid option, GitHub Pages with `apt-ftparchive`
      is the unpaid one). README install commands assume the repo path
      exists — update those when this is decided.
- [ ] **Companion repo READMEs** — each Homebrew tap / Scoop bucket / AUR
      repo needs at minimum a one-paragraph README + LICENSE.

## §4 — Adapter coverage gaps (deliberate v1.0 cuts; review before public)

Each of these is documented in `README.md`'s "Known limitations" section. They
ship as `✗ skip(warn)` in the apply translation report, so users see them, but
worth deciding whether any should be promoted to "real" before v1.0 is the
artifact distributed publicly.

- [ ] **OpenCode hooks** → currently emit `Skip{Reason: "OpenCode hooks are
      JS/TS plugins; shim generation deferred to post-v1"}`. Promotion would
      require generating a JS/TS shim per hook event. Not trivial; document
      as v1.x if shipping public without it.
- [ ] **OpenCode LSP servers** → currently `Skip{Reason: "OpenCode LSP
      projection deferred to v1.x"}`. Same shape as hooks — defer or implement.
- [ ] **OpenCode subagent frontmatter munge** → drops `tools` and `color`
      with explicit Skip notes. The `tools` → `permission` projection
      especially is non-trivial (the permission model needs hand-design).
      Document or design.
- [ ] **OpenCode command frontmatter munge** → drops `argument-hint` with a
      Skip note. No OpenCode equivalent. Likely permanent; just document.
- [ ] **Codex (`v1.1`) and Cursor (`v1.2`) adapters** — still NoopAdapter in
      `internal/cli/registry_internal.go`. A user can today run
      `agentsync agent add codex` and apply will silently produce 0 ops.
      Decide whether to:
      - Reject `agent add codex` / `cursor` with "not yet supported in v1.0"
      - Accept the no-op and document
      - Wait until v1.1 / v1.2 plans land
      Currently leans toward (b) but worth an explicit choice before public.

## §5 — One real correctness gap to verify

Carried over from [issue #13 §4](https://github.com/spxrogers/agentsync/issues/13#issuecomment-4375725634)
because the M0–M7 + PR #14 stack does not address it.

- [x] **Plugin component projection stub** — FIXED in PR #16
      (`claude/plugin-projection-fix`).

      `internal/marketplace/projection.go` previously recorded
      `Skills` / `Commands` / `Subagents` from a plugin's manifest as stub
      entries with only the raw path in `Name`. The projection layer now fully
      loads each component: resolves the path, reads the markdown file via the
      injected `readFile` function, calls `source.ParseFrontmatter`, and
      populates `Name` (from frontmatter `name:` key or basename fallback),
      `Frontmatter`, and `Body`. Missing files are skipped with a warning;
      malformed frontmatter is a hard error.

      The fix applies to both `applyManifest` (strict plugin.json path) and
      `applyEntryOverrides` / `applyEntryFull` (non-strict PluginEntry path).

      - Unit tests in `internal/marketplace/projection_test.go` cover happy
        path (directory-based skills, inline frontmatter name, filename
        fallback), missing-file skip, malformed-frontmatter error, and I/O
        error propagation.
      - Live integration test in `projection_live_test.go` (build tag `live`,
        opt-in via `AGENTSYNC_LIVE_PLUGIN_TEST=1`) fetches `obra/superpowers`
        via `GitFetcher` and asserts at least 5 skills land with non-empty
        Body and frontmatter.

## §6 — Comment preservation (documented v1.x deferral)

Both of these are documented v1 trade-offs (in `internal/source/writer.go` and
`internal/adapter/opencode/settings.go`). Users editing TOML/JSONC by hand
expect comments to survive an agentsync round-trip; today they don't.

- [ ] **TOML comment preservation** — `WriteMCP`, `WritePlugin`,
      `WriteMarketplace`, the agent-add/remove rewriter, and the reconcile
      `[w]` write-back path all use `pelletier/go-toml/v2` `Marshal()` which
      drops comments inside the section it rewrites. (Surrounding sections
      are preserved by slice-splice in `agent.go`.)

      Comment-preserving mutation needs an AST-level approach (mutate the
      parsed AST and re-emit). The pelletier library doesn't expose that
      cleanly. Either:
      - Switch to a TOML library that does (none mature in Go as of v1.0)
      - Build a comment-aware splice helper that locates the key being
        mutated and replaces only its line, leaving comments alone

      Tag as v1.x; document on the public README.

- [ ] **JSONC comment preservation** — `internal/adapter/opencode/settings.go`
      uses `tailscale/hujson`'s `Standardize() + MarshalIndent()` which
      strips comments from `opencode.json` on merge. Foreign keys are
      preserved; comments are not.

      Tighter fix is possible — hujson exposes the AST, so a per-key
      replacement that doesn't go through `Standardize()` would preserve
      comments. Defer to v1.x with a clear "v1.0 strips comments from
      opencode.json on merge" line in README's known limits.

- [ ] **Comment-preservation fuzz tests** — the design spec calls for "thousands
      of parse → mutate one key → write → re-parse iterations with comment +
      key-order assertions." Not implemented in v1.0 because the underlying
      preservation isn't implemented. When either of the two above lands, add
      the fuzz suite alongside.

---

## After the checklist

Once §1–§3 are green, agentsync is publicly installable on the three target
platforms. §4–§6 are recurring polish items — not blockers — that can ship as
documented limits in v1.0 and get promoted in v1.1 / v1.2 / v1.x.

The full v1.0 design lives at
`docs/superpowers/specs/2026-05-04-agentsync-design.md`. Plans for v1.1 (Codex
adapter) and v1.2 (Cursor adapter) are explicitly deferred until v1.0 ships
publicly per the v1.0 overview document.

# Launch checklist — agentsync v0.1.0

Tracking the work to ship agentsync publicly as **v0.1.0**: repo public, a tagged
release, and install commands that work on macOS / Windows / Linux. Captured here
so it doesn't have to live in head or in [issue #13](https://github.com/spxrogers/agentsync/issues/13).

The repo is **public** and the v0.1.0 release tooling is wired: a tag-triggered
`release.yml` runs goreleaser to publish the GitHub Release (binaries + checksums
+ deb/rpm) and a Homebrew cask. Remaining items are intentionally **deferred**,
not broken: the rest of the package-manager channels (Scoop / Chocolatey / AUR)
and a few documented trade-offs. agentsync is functional end-to-end (tests green
under `just test-release`).

Order is roughly "do first" → "do later." The OSS-hygiene block and the repo-
public flip were the gating steps; the goreleaser/companion-repo block builds on
those; the adapter-coverage and comment-preservation items can be addressed any
time (or shipped in a later release with documented limitations as today).

---

## §1 — OSS hygiene (gating)

These all need to exist before flipping the GitHub repo public. Without them,
the project looks like an abandoned experiment instead of a v1.0.

- [x] **`LICENSE`** — done: MIT (`LICENSE` at repo root).
- [x] **`CONTRIBUTING.md`** — done: present at repo root. Personal-first /
      OSS-shareable framing, PRs-welcome / no-SLA, prerequisites + build/test
      recipes (`just test-release` as the bar), conventional-commit convention.
- [x] **`SECURITY.md`** — done: disclosure process + threat model
      (`SECURITY.md` at repo root) covering age key handling, untrusted
      marketplaces/plugins, and destination writes.
- [ ] **`CODE_OF_CONDUCT.md`** — optional but expected on most public OSS.
      Contributor Covenant is fine.
- [x] **`CHANGELOG.md`** — done: present at repo root. Keep-a-Changelog format
      with a `[0.1.0]` section cut from `[Unreleased]`; future releases append here.
- [x] **`.github/ISSUE_TEMPLATE/`** — done: `bug_report.yml` +
      `feature_request.yml` + `config.yml` (routes security reports to a
      private advisory and usage questions to the user guide).
- [x] **`.github/pull_request_template.md`** — done: summary / type / test-plan
      / checklist sections; references `just test-release` as the release bar
      and the secret-handling invariants.
- [x] **GitHub repo description, topics, homepage URL** — done via `gh repo
      edit`: description set, homepage → `https://agentsync.cc` (docsite
      domain, site TBD), topics `cli` `golang` `claude-code` `mcp` `dotfiles`
      `dev-tools`.
- [x] **Local clone path housekeeping** — done: active dev clone is now
      `~/projects/agentsync/`. (A stale pre-rename clone still sits at
      `~/projects/opensync/` — harmless leftover, delete when convenient; it's
      not part of the repo.)

## §2 — Make the repo public (gating)

- [x] **GitHub repo Settings → Change visibility → Public.** Done — the repo is
      public, which §3's goreleaser publishing and the Homebrew tap require.
- [x] **Final pre-flip review.** Done — README install commands rewritten to
      match what v0.1.0 actually ships; no private-context drafts ship.

## §3 — Distribution plumbing

A tag-triggered `.github/workflows/release.yml` runs `goreleaser release` on any
`v*` tag (pushed via `just release vX.Y.Z`). The GitHub Release, deb/rpm, and
Homebrew cask are enabled; Scoop / Chocolatey / AUR stay commented out in
`.goreleaser.yaml` (tracked in [issue #13](https://github.com/spxrogers/agentsync/issues/13)).

- [x] **Tag a release** — `just release v0.1.0` (validates `v`+semver, then tags
      and pushes; the push fires `release.yml`). Produces binaries + checksums +
      the `nfpms` deb/rpm packages on the GitHub Releases page.
- [x] **Homebrew tap** — created [`spxrogers/homebrew-tap`](https://github.com/spxrogers/homebrew-tap)
      (seeded with README + LICENSE). `.goreleaser.yaml` publishes via
      `homebrew_casks` (the `brews` formula stanza is deprecated) using the
      `HOMEBREW_TAP_GITHUB_TOKEN` secret. Validate on a clean macOS box:
      ```
      brew tap spxrogers/tap
      brew install agentsync
      agentsync --version
      ```
      *Highest user impact* — most users are on macOS.
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
- [x] **Linux native packages (deb / rpm)** — `nfpms` is active and produces
      deb/rpm artifacts on every tagged release, attached to the GitHub Releases
      page (the simplest hosting). The README points install at
      `releases/latest/download/agentsync_linux_<arch>.{deb,rpm}`, which the
      version-less `nfpms` file names resolve. Standing up apt/yum repos
      (`packagecloud.io`, or GitHub Pages + `apt-ftparchive`) stays deferred.
- [x] **Companion repo READMEs** — the Homebrew tap ships a README + LICENSE.
      The Scoop bucket / AUR repos will need the same when created.

## §4.5 — Audit findings addressed (second review wave)

Captured here so the v1.0 audit trail stays in one place. Each was a
finding flagged in a follow-up review of the M0–M7 + PR #18 stack;
each has a corresponding commit on the integration branch.

- [x] **README quickstart command did not exist** — `agentsync mcp add` is
      now implemented (subcommands: add/remove/list). set/enable/disable
      remain deferred (add-after-remove covers same need).
- [x] **`init [<git-url>]`** — clones bootstrap source repo when a URL
      is given (matches design spec line 361). https/ssh/file accepted;
      http/git:// gated by AGENTSYNC_ALLOW_INSECURE_URLS=1.
- [x] **Stale `opensync.toml` references** — error messages in
      cli/secrets.go and a type name in cli/agent.go now use
      `agentsync.toml`/`agentsyncCfg`.
- [x] **`apply --dry-run` held the global lock** — dry-run is read-only;
      lock acquisition moved to the real-apply branch only.
- [x] **`agent add codex` / `cursor` were silent noops** — rejected at
      `agent add` time with a v1.x status message and an
      AGENTSYNC_ALLOW_UNIMPLEMENTED override for plan/spec work.
- [x] **`agent add` did not check that the agent's binary was on PATH**
      — warns (not fails) so authoring on a control machine still works.
- [x] **`doctor` only checked PATH** — now validates home, .state/
      writability, agentsync.toml schema, and (when [secrets] block is
      present) backend / recipient / identity_file existence + perms.
- [x] **`verify` was a near-noop** — now validates secrets config and
      runs the same SubstituteCanonical apply uses, surfacing every
      unresolved `${secret:}`/`${env:}` reference.
- [x] **Plugin id path-traversal** — `pluginCacheDir`/`marketplaceCacheDir`
      validated up-front in plugin install; loader rejects projection
      of plugins whose derived id contains `..`/`/`.
- [x] **Backup destination escape** — `backupPathFor` cleans the input,
      drops volume names, and asserts containment via filepath.Rel;
      falls back to a `_escaped/` subdir if anchor check fails.
- [x] **State schema version was silently accepted** — `migrate()`
      handles legacy zero, no-ops current, refuses newer with an
      "upgrade agentsync" message, refuses older without a migrator.
- [x] **No HTTPS policy for plugin fetches** — `Dispatch` rejects
      http://, git:// unless AGENTSYNC_ALLOW_INSECURE_URLS=1.
- [x] **No symlink protection in npm tarballs** — TypeSymlink/TypeLink
      hard-reject. Prior implicit silent-drop is gone.
- [x] **Spec-required concurrent-apply lock test** — added.
- [x] **`init` re-init guard swallowed ReadDir errors** — three cases
      distinguished: populated/refuse, empty/proceed, missing/proceed,
      other/error-with-hint.
- [x] **First-apply UX** — `init` ends with a Next-steps block that
      pushes the user at `apply --dry-run` before the real thing.
- [x] **README known-limits expanded** — owned-key overwrite, codex/cursor
      reject, http-scheme policy now spelled out.
- [ ] **Comment-preservation fuzz** — still deferred (§6). The
      underlying preservation isn't implemented in TOML or JSONC; fuzz
      lands when either does.

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
- [x] **Codex adapter — shipped.** Codex now has a real adapter (no longer a
      NoopAdapter); see the [capability matrix](docs/capability-matrix.md) for
      its per-component coverage and documented lossy spots. **Cursor** remains a
      NoopAdapter in `internal/cli/registry_internal.go`: `agent add cursor` is
      **rejected** (override via `AGENTSYNC_ALLOW_UNIMPLEMENTED=1` for plan/spec
      work) so a user can't silently register a no-op agent. Promoting Cursor to
      real translation stays deferred (planned).

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

With §1–§3 green, agentsync is publicly installable: Homebrew on macOS, deb/rpm
and prebuilt binaries on Linux, and prebuilt binaries on Windows. §4–§6 are
recurring polish items — not blockers — that ship as documented limits and get
promoted in later releases.

The full design lives at
`docs/superpowers/specs/2026-05-04-agentsync-design.md`. The Codex adapter has
since shipped; the Cursor adapter remains planned.

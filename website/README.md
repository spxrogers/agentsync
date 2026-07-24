# agentsync documentation website

The source for **[agentsync.cc](https://agentsync.cc)** — an
[Astro Starlight](https://starlight.astro.build) site, deployed to GitHub Pages by
the reusable [`docs-publish` workflow](../.github/workflows/docs-publish.yml).

## Develop

This is a [bun](https://bun.sh) project. From the repo root you can also use the
`just docs-*` recipes.

```bash
cd website
bun install
bun run dev        # http://localhost:4321
```

| Command | Action |
| --- | --- |
| `bun run dev` | Local dev server with hot reload. |
| `bun run build` | Production build to `./dist/`. |
| `bun run preview` | Serve the production build locally. |
| `bun run sync:docs` | Regenerate the mirrored contract pages (see below). |
| `bun run check:links` | Validate in-site links & heading anchors (see below). |
| `bun run test` | Run the `scripts/` unit tests (`node --test`). |

`predev` and `prebuild` run `sync:docs` **and then** `check:links`
automatically, so a plain `bun run dev` / `bun run build` always has the
mirrored pages in place *and* fails fast on a broken in-site link or heading
anchor. Running `astro build` directly (bypassing the scripts) does neither.

## Content layout

Pages live in `src/content/docs/`. The sidebar is defined in `astro.config.mjs`.

```
src/content/docs/
├── index.mdx                 # landing / splash
├── getting-started/          # what is it, mental model, install, first sync, import
├── guides/                   # daily loop, rollback, mcp, memory, plugins, secrets, …
├── recipes/                  # task-shaped cookbook
├── reference/                # CLI, configuration, environment, capability matrix*
├── concepts/                 # concepts & glossary*
├── internals/                # architecture*, components*, security model
└── help/                     # FAQ, troubleshooting
```

## Mirrored contract pages (do not hand-edit)

The pages marked `*` above are **generated** from the canonical contract docs in
[`../docs`](../docs) by [`scripts/sync-docs.mjs`](scripts/sync-docs.mjs):

| Generated page | Source of truth |
| --- | --- |
| `concepts/index.md` | `docs/concepts.md` |
| `internals/architecture.md` | `docs/architecture.md` |
| `internals/components.md` | `docs/components.md` |
| `reference/capability-matrix.md` | `docs/capability-matrix.md` |
| `comparison/index.md` | `docs/comparison.md` |

These are gitignored and rebuilt on every dev/build. **Edit the `docs/*.md` file,
never the generated copy** — that's how the site stays in lock-step with the
in-repo contract docs (see [`../CLAUDE.md`](../CLAUDE.md)).

The remaining pages are authored here and are the source of truth for their own
prose. When the CLI surface changes, update both `docs/user-guide.md` and the
relevant page under `reference/` (the sync table in `CLAUDE.md` lists this).

## Link & anchor checking (build-time)

[`scripts/check-links.mjs`](scripts/check-links.mjs) is a build-time guard that
validates the **site-absolute** in-site links (`/route…` hrefs) and `#anchor`
targets in the content tree. It runs from `predev` /
`prebuild` **after** `sync:docs` (so it sees the generated + authored pages
together), walks all `.md`/`.mdx` under `src/content/docs/`, and fails the build
(non-zero exit, one report line per breakage naming file, line, and target) when:

- a site-absolute `/route` link points at a page that does not exist, or
- a `#anchor` does not match a real heading slug on the **target** page.

Heading slugs are computed with [`github-slugger`](https://github.com/Flet/github-slugger),
the same GitHub-style slugger Starlight uses, so the check matches the anchors
the site actually generates. External links (`http(s)://`, `mailto:`), the
GitHub blob/edit URLs the mirror emits, and **relative** links (`../foo`, bare
`foo.md` — outside the route/anchor scope the checker owns) are skipped. A
clean run prints a one-line summary and exits 0.

Run it standalone with `bun run check:links`. Its own unit tests (the slugger
fixture pinning the real slugs, plus a checker run over a temp fixture tree) live
in [`scripts/check-links.test.mjs`](scripts/check-links.test.mjs) and run via
`bun run test` (`node --test`). A rare **pre-existing** breakage owned by another
issue can be parked in the `ALLOWLIST` at the top of the script (each entry
carries a `// TODO(#NNN)` reference and is reported as a known exception, not a
failure) — never silently edit a mirrored/canonical doc to make the check pass.

## Deploy

The site is served by GitHub Pages from the `gh-pages` branch ("deploy from a
branch"), so serving it costs no GitHub Actions minutes. The build + force-push of
`dist/` to `gh-pages` lives in the reusable
[`docs-publish` workflow](../.github/workflows/docs-publish.yml). Cutting a release —
pushing a `vX.Y.Z` tag — runs the [`release` workflow](../.github/workflows/release.yml),
which calls `docs-publish` once the CLI is published. The result: the live docs
always track the latest released CLI. The custom domain is set via
[`public/CNAME`](public/CNAME).

To redeploy out of band (e.g. a docs-only fix between releases), trigger the
[`docs-publish` workflow](../.github/workflows/docs-publish.yml) manually from the
GitHub UI ("Actions → docs-publish → Run workflow"), or run `just docs-publish`
locally — all three do the same rebuild + force-push to `gh-pages`.

The page footer's "Last updated" line carries a **build/deploy commit hash** — a
short SHA linked to the exact commit on GitHub, so the live site always shows
"what's currently live." It's a Starlight component override
([`src/components/LastUpdated.astro`](src/components/LastUpdated.astro)); the SHA
is resolved at build time in [`astro.config.mjs`](astro.config.mjs) (CI's
`GITHUB_SHA`, falling back to local `git rev-parse HEAD`) and injected as
`PUBLIC_COMMIT_SHA`. If neither is available the hash is simply omitted.

One-time setup in the GitHub repo: **Settings → Pages → Source: Deploy from a
branch → `gh-pages` / `(root)`**, with `agentsync.cc` as the custom domain (the
`CNAME` file re-enforces it on each deploy).

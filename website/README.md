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

`predev` and `prebuild` run `sync:docs` automatically, so a plain
`bun run dev` / `bun run build` always has the mirrored pages in place. Running
`astro build` directly (bypassing the scripts) will not.

## Content layout

Pages live in `src/content/docs/`. The sidebar is defined in `astro.config.mjs`.

```
src/content/docs/
├── index.mdx                 # landing / splash
├── getting-started/          # what is it, mental model, install, first sync, import
├── guides/                   # daily loop, mcp, memory, plugins, secrets, projects, …
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

These are gitignored and rebuilt on every dev/build. **Edit the `docs/*.md` file,
never the generated copy** — that's how the site stays in lock-step with the
in-repo contract docs (see [`../CLAUDE.md`](../CLAUDE.md)).

The remaining pages are authored here and are the source of truth for their own
prose. When the CLI surface changes, update both `docs/user-guide.md` and the
relevant page under `reference/` (the sync table in `CLAUDE.md` lists this).

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

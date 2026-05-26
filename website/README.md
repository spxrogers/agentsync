# agentsync documentation website

The source for **[agentsync.cc](https://agentsync.cc)** — an
[Astro Starlight](https://starlight.astro.build) site, deployed to GitHub Pages by
[`.github/workflows/docs.yml`](../.github/workflows/docs.yml).

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

Pushing to `main` triggers the `docs` workflow, which builds `website/` and
publishes `dist/` to GitHub Pages. The custom domain is set via
[`public/CNAME`](public/CNAME). Pull requests build the site for validation but
do not deploy.

One-time setup in the GitHub repo: **Settings → Pages → Source: GitHub Actions**,
then add `agentsync.cc` as the custom domain (the `CNAME` file enforces it on each
deploy).

// Mirror the canonical contract docs from ../docs into the Starlight content
// tree. These four pages are the in-repo "contract" (see CLAUDE.md) and stay
// source-of-truth in docs/*.md; this script regenerates the site copies so the
// website can never drift from them. The generated files are gitignored and
// rebuilt on every `npm run dev` / `npm run build` (predev / prebuild hooks).
//
// Do NOT hand-edit the generated files — edit docs/*.md and re-run.

import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, '..', '..');
const docsDir = resolve(repoRoot, 'docs');
const outRoot = resolve(here, '..', 'src', 'content', 'docs');

const GH_BLOB = 'https://github.com/spxrogers/agentsync/blob/main';
const GH_EDIT = 'https://github.com/spxrogers/agentsync/edit/main';

// docs/<name>.md (relative links from within docs/) -> on-site route.
const pageMap = {
  'concepts.md': '/concepts/',
  'architecture.md': '/internals/architecture/',
  'components.md': '/internals/components/',
  'capability-matrix.md': '/reference/capability-matrix/',
  'user-guide.md': '/getting-started/introduction/',
};

// ../<name>.md (repo-root docs) -> route or GitHub.
const repoDocMap = {
  'README.md': `${GH_BLOB}/README.md`,
  'SECURITY.md': '/internals/security/',
  'CLAUDE.md': `${GH_BLOB}/CLAUDE.md`,
  'CONTRIBUTING.md': `${GH_BLOB}/CONTRIBUTING.md`,
  'CHANGELOG.md': `${GH_BLOB}/CHANGELOG.md`,
};

const jobs = [
  {
    src: 'concepts.md',
    out: 'concepts/index.md',
    title: 'Concepts & glossary',
    description:
      'The three-state model, drift, reconcile, and every agentsync term on one page.',
  },
  {
    src: 'architecture.md',
    out: 'internals/architecture.md',
    title: 'Architecture',
    description:
      'The apply/capture pipelines, the 3-way drift classifier, and the secret-safety invariants.',
  },
  {
    src: 'components.md',
    out: 'internals/components.md',
    title: 'Component map',
    description: 'A package-by-package index of the agentsync codebase.',
  },
  {
    src: 'capability-matrix.md',
    out: 'reference/capability-matrix.md',
    title: 'Capability matrix',
    description:
      'What each agent supports, per component — native, projected (lossy), or skipped.',
  },
];

function rewriteLinks(md) {
  return md.replace(/\]\(([^)]+)\)/g, (full, href) => {
    if (href.startsWith('#') || /^https?:/.test(href) || href.startsWith('mailto:')) {
      return full;
    }
    const hashIdx = href.indexOf('#');
    const path = hashIdx >= 0 ? href.slice(0, hashIdx) : href;
    const anchor = hashIdx >= 0 ? href.slice(hashIdx) : '';

    const sameDir = path.replace(/^\.\//, '');
    if (pageMap[sameDir]) return `](${pageMap[sameDir]}${anchor})`;

    const fromRoot = sameDir.replace(/^\.\.\//, '');
    if (repoDocMap[fromRoot]) return `](${repoDocMap[fromRoot]}${anchor})`;

    return full;
  });
}

function frontmatter({ title, description, src }) {
  return [
    '---',
    `title: ${JSON.stringify(title)}`,
    `description: ${JSON.stringify(description)}`,
    `editUrl: ${JSON.stringify(`${GH_EDIT}/docs/${src}`)}`,
    '---',
    '',
  ].join('\n');
}

function mirrorNote(src) {
  return [
    ':::note[Mirrored from the repo]',
    `This page is generated from [\`docs/${src}\`](${GH_BLOB}/docs/${src}), the`,
    'canonical in-repo contract doc. Edit that file, not this page.',
    ':::',
    '',
    '',
  ].join('\n');
}

let count = 0;
for (const job of jobs) {
  let md = readFileSync(resolve(docsDir, job.src), 'utf8');
  md = md.replace(/^#\s+.*\r?\n+/, ''); // drop leading H1 (Starlight renders the title)
  md = rewriteLinks(md);
  const outPath = resolve(outRoot, job.out);
  mkdirSync(dirname(outPath), { recursive: true });
  writeFileSync(outPath, frontmatter(job) + mirrorNote(job.src) + md);
  count++;
}

console.log(`sync-docs: regenerated ${count} contract page(s) from docs/`);

# Lens 2 — Adversarial

Cap: 800 words.

> **Your job — adversarial.** In a whole-PR round the change is
> the whole PR; in a diff-only round it is this round's diff. From
> round 2, layer 1 comes first.
>
> 1. **Did this round close the holes the previous adversarial
>    pass flagged?** (From round 2.) For each prior finding: read
>    the fix, attack it. A fix that closes a hole on the happy path
>    often leaves the error, retry or concurrent path still open.
>
> 2. **What attack surfaces does the change introduce?** New code,
>    new invariants, new contracts. What hostile inputs, orderings
>    or lifecycles could break it?
>
> 3. **Against the change, specifically consider:**
>    - Trust boundaries: can a caller reach, change or act as
>      something it should not — by id, path, credential, ordering
>      or omission?
>    - Hostile input reaching an interpreter or an output (a
>      query, a shell, a file path, a template, a log line, a
>      terminal, a rendered page): oversized, empty, control
>      characters, look-alike Unicode, crafted ids, paths and URLs.
>    - Races and replays: two actors at once, a retry, a step that
>      runs twice, an interrupted run resumed, an old and a new
>      version running side by side.
>    - Secrets and private data: a datum reaching an output, an
>      error, a log line, a stored file or a third party that it
>      should not.
>    - Lifecycle: something deleted, revoked or expired, a
>      half-finished flow resumed later.
>    - Doc-vs-code gaps: anywhere a document, a comment or the PR
>      body promises behaviour the code does not enforce.

**Repo additions.** If the repo's `.reviewlooprc` has an *Adversarial*
section under *Lens angles*, add its angles to the brief after this
template, under the line "This repo's own angles for your lens:"
(SKILL.md, Step 0).

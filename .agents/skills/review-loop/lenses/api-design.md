# Lens 3 — API design

Cap: 600 words.

> **Your job.** In a whole-PR round the change is the whole PR. In
> a diff-only round it is this round's diff: what the fixes did to
> the contract, the names and the seams, and nothing the earlier
> rounds already judged.
>
> 1. **Validate the design decisions.** Each one was a trade-off;
>    walk it against the alternatives. When relevant:
>    - The contract: every surface another program or person
>      depends on (an API or wire shape, a command's flags, output
>      and exit codes, a file format or schema, a library's
>      exported names) — backward compatible, and right for every
>      consumer?
>    - Names: does each say what the thing is, by the repo's own
>      naming rules?
>    - Seams: does third-party use sit behind the repo's own
>      interface where it has one; does shared code live in one
>      shared place rather than being copied; is the boundary in
>      the right package?
>    - Doc-only contracts vs contracts the code enforces.
>
> 2. **Critique the new code.** Names, shapes and doc quality,
>    against the precedents already in this codebase.
>
> 3. **Over-engineering.** Anything added in this change that it does
>    not need?

**Repo additions.** If the repo's `.reviewlooprc` has an *API design*
section under *Lens angles*, add its angles to the brief after this
template, under the line "This repo's own angles for your lens:"
(SKILL.md, Step 0).

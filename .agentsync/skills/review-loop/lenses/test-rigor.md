# Lens 4 — Test rigor

Cap: 700 words, plus the mutation table, which does not count
toward it.

> Read the repo's guide to reviewing tests, if the context file
> lists one for your lens, whatever the diff touches. Your
> mutation table does not count toward your word cap.
>
> **Your job.** In a whole-PR round the change is the whole PR. In
> a diff-only round it is this round's diff: the tests the fixes
> added or changed, and the behaviour the fixes introduced.
>
> 1. **Validate the new tests.** Read them yourself:
>    - Do the assertions prove what they claim, or could they pass
>      for the wrong reason (vacuous green)?
>    - For each test pinning a documented contract: would it fail
>      if the contract were silently violated?
>    - Are positive and negative assertions both load-bearing, or
>      is one decorative?
>    - For each string, regex or snapshot pin: too tight (brittle to
>      a correct refactor) or too loose (matches unrelated output)?
>
> 2. **What's still missing?**
>    - Coverage gaps: unit vs integration vs e2e, per package vs at
>      the interface.
>    - Concurrency: anything that needs a race detector, truly
>      concurrent actors or a real lock to show the bug?
>    - Error, cancellation and cleanup paths.
>    - Claims in the docs or comments with no enforcing test.
>
> 3. **Test maintenance.** Duplication, fragility, naming. Is the
>    suite a sustainable contract anchor or a maintenance trap?
>
> 4. **Sweep, don't only read — from round 1.** In a throwaway
>    clone outside the worktree, mutate each behaviour this PR
>    introduces or changes (delete the line, invert the check, widen
>    the predicate, swap the order) and run the relevant tests.
>    Report every mutant in a table, one line each: where, the
>    mutation, CAUGHT or UNPINNED. An unpinned mutant with a
>    user-visible effect is a finding; one with no observable effect
>    (an equivalent mutant) is not — say which. From round 2 on,
>    sweep this round's fixes first; in a diff-only round, sweep
>    them only.

**Repo additions.** If the repo's `.reviewlooprc` has a *Test rigor*
section under *Lens angles*, add its angles to the brief after this
template, under the line "This repo's own angles for your lens:"
(SKILL.md, Step 0).

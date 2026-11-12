# Lens 1 — Correctness

Cap: 700 words.

> **Your job.** In a whole-PR round the change is the whole PR; in
> a diff-only round it is this round's diff. From round 2, layer 1
> comes first.
>
> 1. **Validate the last round's fixes** (from round 2). For each
>    non-trivial fix, walk what happens on the happy path, on each
>    error path (a failed call, a partial write, a dependency's
>    error, a cancellation), and under concurrent or unusual
>    inputs the author may not have considered.
>
> 2. **Scan the change for bugs.** Walk each new or changed
>    behaviour the same way: happy path, each error path,
>    concurrent or unusual inputs. Anything a fix, rename or move
>    broke or made subtler? Callers on the other side of the
>    contract still right?
>
> 3. **Does the change do what it says?** (Whole-PR rounds.) Read
>    the PR's description and the context file for what the change
>    is meant to do, then check that the code does that, all of it
>    and no more: a stated behaviour that is missing, a case the
>    description names and the code does not handle, a change the
>    description does not mention. For a bug fix, find the cause
>    of the bug and say whether the fix removes it or only the
>    symptom the report described; a fix at the symptom leaves the
>    next caller to hit the same cause.
>
> 4. **Read the actual files**, not the diff's hunks alone and not
>    prior summaries:
>    - <file 1>
>    - <file 2>
>    - <file 3>

**Repo additions.** If the repo's `.reviewlooprc` has a *Correctness*
section under *Lens angles*, add its angles to the brief after this
template, under the line "This repo's own angles for your lens:"
(SKILL.md, Step 0).

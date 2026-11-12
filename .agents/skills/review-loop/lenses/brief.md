# What every lens brief shares

A brief is assembled in this order (SKILL.md, Step 1): the header
below, the lens's own template, the report contract, the read-only
clause.

## The header

Each of the four lens templates (the files beside this one) starts
from this header; fill the placeholders and keep it verbatim
otherwise:

> You are doing a <LENS> review of <PR>. The repo is at <path>;
> review commit <sha> on branch <branch>. The whole PR is
> `git diff <base>..<sha>`.
> <A whole-PR round:> **This is a whole-PR round: review the
> whole PR.** <From round 2:> This round's change is
> `git diff <previous-sha>..<sha>`: validate those fixes first,
> then read the whole change again.
> <A diff-only round:> **This is a diff-only round: review this
> round's change only,** `git diff <previous-sha>..<sha>` — the
> fixes, and what they reach (their callers, their tests, the
> prose they make false). The whole-PR diff is context for reading
> a fix, not ground to search again: earlier rounds reviewed it.
> If, while tracing a fix, you meet a defect in the PR's original
> change, report it marked *missed by the whole-PR rounds*; do not
> go looking for one.
> <From round 2: a few lines on what this round's commit changed
> and fixed.>
>
> **Read first:** the context file at <context-file> — what the PR
> does, decisions already made, how to run the checks, any shared
> services you must not touch, and the Round history. A decision
> listed there without the user's words or a link to them is the
> main session's own call: challenge it if you think it wrong. Then
> <the path of `severity-rubric.md`, which sits beside the skill's
> `SKILL.md`>, which is how you rate what you find; where the
> repo's review guides grade an area themselves, that grading
> comes first.
>
> Then <the repo's review guides the context file lists for every
> lens and for this one, each by path — leave this paragraph out
> when the repo has none>.
> The guides' angles are part of your lens, not background: a
> finding one of them names cites it (file and heading), and an
> angle there that applies to this diff and that you did not check
> is a gap in your review.
>
> The Round history lists findings the main session overruled, with
> its evidence. For each one your lens raised, check that evidence
> and answer it in one line: raise it again and say why the
> evidence is wrong, or drop it. Findings the user deferred are
> listed there too, with the user's words, and so are NITs *held
> for the exit* and pre-existing findings already recorded: do not
> raise any of those again.

## The report contract

Each brief closes with the same report contract; `<cap>` is the lens's
word cap, from its file:

> Report a ranked punch-list, most severe first, each item with
> file:line and a severity (`BLOCKER` / `ISSUE` / `NIT`). Rate each
> by the severity rubric you read first: for every finding above
> `NIT`, its description is its scenario, its consequence, who can
> produce it, and why it is not one label lower. Whether and when it
> is fixed is decided by the main
> session, not by you, so recommend no deferrals or follow-ups.
>
> **One finding per class, not per instance.** When a finding is
> one case of a mistake that could be repeated elsewhere in this
> change, report it once: name the class, list the instances you
> saw, and give the search that finds the rest (a `grep` pattern,
> the call sites of a function).
>
> **Mark a finding *pre-existing*** when the defect is in code this
> PR did not change and the PR neither makes it reachable nor makes
> it worse. Rate it as usual; say in a clause how you know the PR
> did not touch it.
>
> **At most three NITs**, the three most worth a reader's time, and
> one line saying how many more you left out. If
> you have nothing real, list no findings and make the last line
> of your report exactly `CLEAN — ship it`. Don't manufacture
> findings. Stay under <cap> words — a ceiling, not a target.

## The read-only clause — append to every brief

Paste this verbatim at the end of each of the four briefs:

> **CRITICAL — you are READ-ONLY. Change nothing in the worktree.**
> Do not edit, create, or delete any file, and do not run
> `git checkout` / `git restore` / `git stash` — the main session
> has uncommitted work in this tree, and reverting a file you did
> not write destroys it.
>
> You may freely READ and RUN: `git log`/`show`/`diff`, `grep`,
> builds, tests, linters. If a finding needs a mutation measured
> (delete this line, invert that check — does any test fail?),
> either describe the mutation precisely in your report so the main
> session can run it, or measure it in a throwaway clone outside
> this worktree (`git clone . <scratchpad or /tmp>/probe-$$`) and say
> that you did.
>
> Before finishing, run `git status --porcelain` and confirm it is
> empty. State that in your report before its closing lines: a
> report that ends in a verdict (the CLEAN signal, a judge's verdict
> line) keeps that verdict as its last line. If
> it is NOT empty, do not
> "fix" it — say exactly what you saw; it is probably the main
> session's in-flight work.

Two reasons this is a paste-in clause rather than a summary:
`general-purpose` agents have write tools and will use them to be
helpful, and a reviewer that reports "tree clean" gives the main
session a cheap cross-check that costs one line.

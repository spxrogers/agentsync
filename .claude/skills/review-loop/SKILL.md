---
name: review-loop
description: Multi-round adversarial code review loop — four specialized agents (correctness, adversarial, API design, test rigor) run in parallel against a PR, repeated until all four return `CLEAN — ship it` or a budgeted number of rounds (default 5) is exhausted. Use when the user wants a thorough review of a substantive PR — new interfaces, contract changes, load-bearing refactors — and has signalled they want both correctness AND polish. Heavy: up to 5 rounds × 4 agents = 20 sub-invocations, so not for tiny bug fixes, WIP sketches, or doc-only PRs. Invoke explicitly; do not auto-trigger from a generic "review this" request unless the user names the high-bar mandate.
---

# Review loop

A four-lens adversarial review pattern, iterated until the team converges or a
budgeted number of rounds elapses. Use it when the user wants the bar set
high on both **correctness** and **polish** — substantive PRs where a single
review pass would miss the second-order issues four lenses catch
independently.

Loop logic:

```
for round in 1..N:                          # N defaults to 5
  launch 4 agents IN PARALLEL: correctness, adversarial, api-design, test-rigor
  wait for all 4 to report
  synthesize findings (convergent vs single-reviewer; rank by severity)
  if every reviewer says "CLEAN — ship it":
    stop                                    # convergence reached
  fix every actionable finding              # disputed findings → user mandate decides
  commit + push
end
report final status
```

## When to use

Use it for:

- New interfaces / public API surface.
- Non-trivial refactors that touch contracts (renames, lifecycle
  changes, cross-package plumbing).
- Anything load-bearing that the user has signalled they want
  "right", not just "working".

Don't use it for:

- Tiny bug fixes, doc-only PRs, or work-in-progress sketches — the
  overhead is wasted.
- PRs where the user wants a quick sanity check, not a thorough
  review — use a single review agent instead.

If the user's request is ambiguous ("review this PR" with no
mandate signal), ASK before kicking off the loop. The cost is real.

## Step 0: framing

Before launching agents, you have to know what the PR is. Collect:

- **PR URL or branch name** the agents will read from.
- **What changed in this round** — a concise summary of the diff
  against the prior reviewed commit, OR against main on the first
  round.
- **What previous rounds found and fixed** — carried forward into
  each round's brief. Fresh subagents don't have the conversation
  history; they need that context to avoid re-flagging closed items.
- **The user's mandate**, in their own words: "ship it ASAP",
  "make it bulletproof", "fix everything", etc. This decides what
  to do with single-reviewer LOWs and reviewer disagreements.

## Step 1: launch the four agents in parallel

The agents run as separate subagents, in parallel, **in the same
message** (parallel tool calls). Each gets a self-contained brief
with:

1. The PR identifier + commit hash + branch (so they can read the
   actual files; do NOT paste the diff into the prompt — it'll
   exceed budget).
2. Their lens (which one of the four).
3. A short summary of what's in the PR and what changed in this
   round.
4. A short summary of what previous rounds found and fixed.
5. A specific list of angles to consider (the lens-specific
   section below).
6. The expected output shape: ranked punch-list, file:line refs,
   severity levels (`BLOCKER` / `ISSUE` / `NIT` / `CLEAN`), word
   cap (~300–450 words).
7. **The self-termination signal**: if they have nothing real,
   they must say `CLEAN — ship it` so convergence is
   programmatically detectable.

Use the **general-purpose** subagent type. Code review needs
reading whole files and reasoning across them; `Explore`-style
agents read excerpts and miss content past their read window.

### Lens 1 — Correctness

> You are doing a CORRECTNESS code review of <PR>. The repo is at
> <path> on branch <branch>; commits <commit-list>.
>
> **What changed in the round you're reviewing:** <summary>
>
> **What previous rounds covered:** <prior-rounds-summary>
>
> **Your job, three layers:**
>
> 1. **Validate the round-N fixes are correct.** Each fix was
>    break-verified at commit time, but I want a second pair of
>    eyes. For each non-trivial fix, walk through what happens:
>    - on the happy path,
>    - under each error / panic / Goexit path,
>    - under concurrent or unusual inputs the author may not have
>      considered.
>
> 2. **Regression-scan the fixes for new bugs.** Anything the
>    round-N fixes broke or made subtler? Anything the rename /
>    refactor / move shifted in a way that's now wrong?
>
> 3. **What did previous rounds miss?** Fresh eyes. Read the
>    actual files; don't trust prior summaries:
>    - <file 1>
>    - <file 2>
>    - <file 3>
>
> Report as a punch-list with file:line refs and severity
> (BLOCKER / ISSUE / NIT / CLEAN). If no real findings, say
> `CLEAN — ship it`. Don't manufacture findings. Under 350 words.

### Lens 2 — Adversarial

> You are doing an ADVERSARIAL code review of <PR>. The repo is at
> <path> on branch <branch>; commits <commit-list>.
>
> **What changed in this round:** <summary>
>
> **What previous adversarial rounds flagged + what was fixed:**
> <prior-rounds-with-disposition>
>
> **Your job — three layers, adversarial.**
>
> 1. **Did this round close the holes the previous adversarial
>    pass flagged?** For each prior finding: read the fix, attack
>    it. The fix that closes a hole on the happy path often leaves
>    the error / Goexit / panic / typed-nil / concurrent paths
>    still open.
>
> 2. **Did this round introduce new attack surfaces?** New code,
>    new invariants, new contracts. What hostile inputs /
>    lifecycles / orderings / concurrency could break it?
>
> 3. **What did the previous adversarial passes still miss?**
>    Fresh eyes. Specifically consider:
>    - Typed-nil pointers, untyped nils, nilable kinds beyond
>      pointers (map/chan/func/slice/interface).
>    - Defer ordering (LIFO), panic during deferred-arg
>      evaluation, Goexit unwinding through pending I/O.
>    - Goroutine lifecycle: leaks, blocked sends/receives on
>      buffered vs unbuffered channels, cleanup ordering.
>    - Process-global state (os.Stderr swaps, log defaults, env
>      vars) and cross-test pollution.
>    - Hostile-input injection: ANSI / control bytes / embedded
>      newlines in attacker-influenced strings flowing through
>      styling or logging code.
>    - Doc-vs-code gaps: anywhere the doc promises behaviour the
>      code does not enforce.
>
> Report as a ranked punch-list (most-concerning first) with
> file:line refs and severity. If no real findings, say
> `CLEAN — ship it`. Don't manufacture findings. Under 450 words.

### Lens 3 — API design

> You are doing an API DESIGN review of <PR>. The repo is at <path>
> on branch <branch>; commits <commit-list>.
>
> **What changed in this round:** <summary>
>
> **What previous API-design rounds asked + how this round
> answered:** <prior-rounds-with-disposition>
>
> **Your job:**
>
> 1. **Validate the round-N design decisions.** Each one was a
>    trade-off. Walk each one against alternatives. Examples to
>    consider when relevant:
>    - Interface naming: does it describe what the IMPLEMENTOR
>      does (PluginIngester precedent) or what the parameter is
>      named?
>    - Closure / restore-handle vs symmetric setup/teardown pair
>      (signal.Notify/Stop, log.SetOutput-via-defer,
>      context.WithCancel).
>    - `any` vs nominal interface parameter — type safety vs
>      cycle-breaking.
>    - Structural vs nominal interface duplication; when is the
>      compile-pin "good enough" and when does a neutral shared
>      package pay off?
>    - Shared test helpers vs per-package copies (httptest
>      precedent).
>    - Doc-only contracts vs runtime-enforced contracts.
>
> 2. **Critique the new code.** Names, shapes, doc quality
>    (compared against established precedents in this codebase).
>
> 3. **Ship readiness.** Anything still blocking? Anything to
>    defer to a follow-up? Anything you've over-engineered across
>    the rounds?
>
> If shipping: say `CLEAN — ship it`. Otherwise, name what one
> more round must address. Under 400 words.

### Lens 4 — Test rigor

> You are doing a TEST RIGOR review of <PR>. The repo is at <path>
> on branch <branch>; commits <commit-list>.
>
> **What changed in this round:** <summary>
>
> **What previous test-rigor rounds asked + how this round
> answered:** <prior-rounds-with-disposition>
>
> **Your job:**
>
> 1. **Validate the new tests.** Read them yourself:
>    - Do the assertions actually prove what they claim, or could
>      they pass for the wrong reason (vacuous green)?
>    - For each test pinning a documented contract, walk the
>      "would this fail if the contract were silently violated?"
>      question.
>    - Are positive and negative assertions both load-bearing,
>      or is one decorative?
>    - For each ANSI / regex / byte-sequence pin: is it too
>      tight (brittle to correct refactors) or too loose (matches
>      unrelated bytes)?
>
> 2. **What's still missing?**
>    - Coverage gaps per-package vs interface-level.
>    - Race detection — anything that needs `-race`?
>    - Lifecycle / cleanup / Goexit / panic paths.
>    - Doc claims with no enforcing test.
>
> 3. **Test maintenance.** Duplication, fragility, naming. Is the
>    suite a sustainable contract anchor or a maintenance trap?
>
> Read the files. If sufficient: say `CLEAN — ship it`. Otherwise
> name what's missing. Under 350 words.

## Step 2: synthesize

When all four reports are in, build the punch-list:

1. **Convergent findings** — flagged by ≥ 2 reviewers — go to the
   top. Two independent reviewers naming the same hole is much
   stronger signal than one.

2. **Single-reviewer findings** are ranked by severity (the
   reviewer's own rating, sanity-checked). A single MEDIUM is
   worth doing; a single LOW is judgement.

3. **Reviewer disagreements** are normal — one says "add this
   test", another says "YAGNI". Decide by the user's mandate:
   - "Ship it ASAP" → defer disputed items.
   - "Fix everything / make it bulletproof" → take the more
     defensive side, even if a minority of one.
   - Unclear mandate → tell the user and ask.

4. **`CLEAN — ship it`** from a reviewer counts as zero findings
   from that lens. Programmatically check for the substring to
   detect convergence.

## Step 3: fix

Apply every finding in the synthesized punch-list. For each fix:

- Make it the smallest correct change. Don't snowball.
- **Break-verify** non-trivial fixes: temporarily induce the bug
  (comment out the fix, sed-corrupt the contract), confirm the
  relevant test fails CLEANLY (the failure message points at the
  right thing), then restore. This catches "test passes for the
  wrong reason" before the next review round does.
- Update docs (CHANGELOG, architecture, prompts) in the SAME
  commit. The reviewers WILL flag drift.
- Commit with a clear message naming which round's findings the
  commit closes.

## Step 4: repeat (until convergence or budget)

Push. Launch the next round's four agents. Each one's brief
carries forward the running summary of what previous rounds found
and what this round's commit just fixed — fresh agents need that
context to avoid re-flagging closed items.

## Step 5: terminate

Stop when EITHER:

- **All four reviewers in the most recent round emit
  `CLEAN — ship it`**, OR
- **The configured round budget is exhausted** (default: 5).

If the budget is exhausted with unresolved findings, report the
residual to the user with a clear ship-it / one-more-round / defer
recommendation. Don't silently merge with open issues.

## Lessons from the field

Hard-won observations from the loop that this skill captures:

- **Four lenses, not three.** Correctness alone misses the design
  smell, adversarial alone misses the design rationale, API
  design alone misses the test coverage gap, test rigor alone
  misses the attack surface. The combinatorial coverage is the
  point.
- **Parallel, not sequential.** Running them serially lets one
  reviewer's output bias the next. Parallel keeps the lenses
  independent.
- **General-purpose agents, not Explore.** Code review needs
  reading whole files and reasoning across them; Explore reads
  excerpts and will miss content past its read window.
- **Word caps matter.** Without a cap, reviewers manufacture
  findings to fill space. With a cap (~300–450 words), they
  prioritise.
- **Severity labels (`BLOCKER`/`ISSUE`/`NIT`/`CLEAN`) prevent
  drift.** Reviewers without a vocabulary will rate everything
  equally important.
- **Convergence trumps unanimity.** Three SHIP-IT + one LOW from
  a minority of one is convergence. Four SHIP-IT is unanimity.
  Either terminates the loop; the difference is whether you act
  on the dissent before merging.
- **The user's mandate is load-bearing.** "Fix everything" and
  "ship it" point at opposite responses to disputed findings.
  Get it before the first round; carry it through every
  synthesis.
- **Reviewers reverse themselves.** It's normal for round-N's
  API design reviewer to say "keep per-package" and round-N+1's
  to say "consolidate". They're reading a different tree; the
  rationale often changes as the surface stabilises.
- **Break-verify every non-trivial fix.** The number-one source
  of "test passes for the wrong reason" is a fix that doesn't
  actually exercise the thing it claims to. Sed-corrupt, run the
  test, watch it fail with a clear message, then restore.

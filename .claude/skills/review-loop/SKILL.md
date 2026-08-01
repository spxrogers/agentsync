---
description: 'Multi-round adversarial code review loop — four specialized agents (correctness,
  adversarial, API design, test rigor) run in parallel against a PR, repeated until
  all four return `CLEAN — ship it` or a budgeted number of rounds (default 5) is
  exhausted — whichever comes first, with two ask-first soft stops in between (a NIT-only
  round; severity declining two rounds straight) so the loop pauses to check in on
  diminishing returns instead of always running to the cap. Use when the user wants
  a thorough review of a substantive PR — new interfaces, contract changes, load-bearing
  refactors — and has signalled they want both correctness AND polish. Heavy: up to
  5 rounds × 4 agents = 20 sub-invocations, so not for tiny bug fixes, WIP sketches,
  or doc-only PRs. Invoke explicitly; do not auto-trigger from a generic "review this"
  request unless the user names the high-bar mandate.'
disable-model-invocation: true
name: review-loop
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
  launch 4 READ-ONLY agents IN PARALLEL: correctness, adversarial, api-design, test-rigor
  wait for all 4 to report                  # they report; they never edit
  synthesize findings (convergent vs single-reviewer; rank by severity)
  if every reviewer says "CLEAN — ship it":
    stop                                    # full convergence
  if every finding this round is NIT severity (no BLOCKER/ISSUE anywhere):
    ask the user: stop here, or spend one more round?   # see Step 5
  if severity has trended down for 2 rounds straight and neither stop
     condition above has fired: ask the user ONCE — stop, or continue to budget?
  audit each finding yourself               # reviewers are wrong sometimes
  fix every accepted finding                # MAIN SESSION ONLY — see below
  commit + push
end
report final status
```

## The single-writer rule — non-negotiable

**The four reviewers are READ-ONLY. The main session is the only process that
edits the worktree.** Reviewers produce reports; the main session audits them,
decides what to accept, and makes every change itself.

This is not a style preference. Four agents editing one worktree in parallel
corrupts it, and the failure modes are quiet:

- A reviewer leaves a probe file behind (`zz_probe_test.go`), and the package
  stops compiling for everyone — including the next round's reviewers, who then
  report a "build failure" that is really litter.
- A reviewer break-verifies by mutating a source file and doesn't restore it. The
  mutation is now indistinguishable from the main session's in-flight work. In one
  real run a reviewer's stream-swap sat in `root.go` long enough that it was nearly
  committed; it was caught only because a test written minutes later happened to
  fail on it.
- A reviewer notices *another* reviewer's litter and helpfully runs
  `git checkout -- <file>` to clean up. That silently discarded the main session's
  uncommitted fix to the same file, which surfaced later as an unexplained build
  break.

Note the shape of the last two: the tree was wrong, and nothing said so. You find
out from a build failure, a mystery test result, or not at all.

So:

- **Every brief must end with an explicit read-only instruction** (see Step 1).
- **Break-verification is the MAIN SESSION's job**, not a reviewer's. It mutates
  source, so only the single writer may do it. A reviewer that wants a mutation
  measured says so in its report; you run it.
- **A reviewer that genuinely must measure a mutation** does it in a throwaway
  clone outside the worktree (`git clone . /tmp/…`), never in place — and says
  which in its report.
- **Before every commit, confirm the tree holds only your own changes.**
  `git status --porcelain` plus a skim of `git diff` beats trusting four agents to
  have cleaned up. If something appeared that you did not write, find out what it
  is before committing — do not `git checkout` it reflexively either, since it may
  be your own uncommitted work.

Reviewers are still free to *run* things: builds, tests, `go vet`, `git log`,
`grep`. Read-only means no writes to tracked files and no new files in the
worktree.

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
8. **The read-only instruction** (see below). Every brief, every
   round — the lens templates that follow omit it only because it
   is identical for all four.

Use the **general-purpose** subagent type. Code review needs
reading whole files and reasoning across them; `Explore`-style
agents read excerpts and miss content past their read window.

### The read-only clause — append to every brief

Paste this verbatim at the end of each of the four briefs:

> **CRITICAL — you are READ-ONLY. Change nothing in the worktree.**
> Do not edit, create, or delete any file, and do not run
> `git checkout` / `git restore` / `git stash` — the main session
> has uncommitted work in this tree, and reverting a file you did
> not write destroys it.
>
> You may freely READ and RUN: `git log`/`show`/`diff`, `grep`,
> builds, tests, `go vet`. If a finding needs a mutation measured
> (delete this line, invert that check — does any test fail?),
> either describe the mutation precisely in your report so the main
> session can run it, or measure it in a throwaway clone outside
> this worktree (`git clone . /tmp/probe-$$`) and say that you did.
>
> Before finishing, run `git status --porcelain` and confirm it is
> empty. State that in your report. If it is NOT empty, do not
> "fix" it — say exactly what you saw; it is probably the main
> session's in-flight work.

Two reasons this is a paste-in clause rather than a summary:
`general-purpose` agents have write tools and will use them to be
helpful, and a reviewer that reports "tree clean" gives the main
session a cheap cross-check that costs one line.

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

5. **Audit each finding before accepting it.** Reviewers are
   confidently wrong often enough that this is a real step, not a
   formality. Verify the claim against the code yourself — a
   `grep`, a `go list -deps`, a two-line probe. In practice this
   catches two kinds of error: a claim that is simply false (a
   reviewer flagged `%s` interpolations as unsanitized when the
   values were a self-sanitizing type), and two reviewers reaching
   opposite conclusions where one is *factually* right rather than
   merely more cautious (one said a deleted test was subsumed by
   its replacement; the other showed it was not, by recolouring the
   thing and watching the suite stay green). Verified facts beat
   reviewer confidence, and beat vote counts.

   Say so when you overrule a reviewer, and — if the loop
   continues — tell that lens next round that you did, so it can
   push back if you were the one who got it wrong.

## Step 3: fix — the main session, and only the main session

Apply every accepted finding yourself. Reviewers do not edit (see
the single-writer rule); this step is where all changes happen.

For each fix:

- Make it the smallest correct change. Don't snowball.
- **Break-verify** non-trivial fixes: temporarily induce the bug
  (comment out the fix, sed-corrupt the contract), confirm the
  relevant test fails CLEANLY (the failure message points at the
  right thing), then restore. This catches "test passes for the
  wrong reason" before the next review round does.

  Because break-verification mutates source, it belongs to the
  single writer. Back the file up first (`cp f /tmp/f.bak`),
  restore from that backup rather than from git — `git checkout`
  would also wipe your other uncommitted edits to that file — and
  confirm `git diff` is back to your intended change before moving
  on. A mutation left in place is the single worst thing you can do
  to this loop: the next round's reviewers will report it as a
  finding, and you may commit it.

  Two failure modes worth naming, both observed:
  *the mutation didn't apply* (a `sed`/`replace` that silently
  matched nothing, so "no test failed" meant nothing was tested),
  and *the test under test was a copy* (a helper duplicating the
  production function's body, so mutating production changed
  nothing). Assert the mutation landed, and make sure the test
  calls the real thing.
- Update docs (CHANGELOG, architecture, prompts) in the SAME
  commit. The reviewers WILL flag drift.
- Commit with a clear message naming which round's findings the
  commit closes.

## Step 4: repeat (until convergence or budget)

Confirm the tree is clean (`git status --porcelain` empty), then
push. Launch the next round's four agents. Each one's brief carries
forward the running summary of what previous rounds found and what
this round's commit just fixed — fresh agents need that context to
avoid re-flagging closed items.

Pin the round's commit SHA in every brief and tell them to review
**that** commit. Reviewers start at different times and a long
round can overlap your next edits; naming the SHA means all four
judge the same tree, and a report referencing a line you have since
moved is immediately recognizable as stale rather than confusing.

## Step 5: terminate

Full convergence and budget exhaustion are the two HARD stops — the
loop never needs permission for either:

- **All four reviewers in the most recent round emit
  `CLEAN — ship it`.** Stop immediately.
- **The configured round budget is exhausted.** Stop, and if
  unresolved findings remain, report the residual to the user with a
  clear ship-it / one-more-round / defer recommendation. Don't
  silently merge with open issues.

Between those two extremes sits the failure mode a real run exposed:
a small PR (a CLI flag and some doc wording) burned the full 5-round
budget because nothing between "unanimous CLEAN" and "budget
exhausted" was ever a reason to pause and ask. Every round after the
first found *something* — but each round's findings were smaller than
the last, well past the point where continuing served the PR rather
than the loop's own momentum. Two SOFT stops close that gap. Both are
**ask, don't assume** — the loop surfaces the signal and lets the user
decide; it never stops on its own here, and it never silently
continues either:

- **A NIT-only round.** If every finding across all four lenses this
  round is NIT severity — no BLOCKER, no ISSUE, anywhere — that is
  real signal distinct from unanimous CLEAN (a lens can still name a
  NIT and be functionally done). Report the round as normal, then ask
  explicitly: *"Only NITs this round — stop here, or spend one more
  round?"* Fix the NITs if the user says stop; otherwise continue as
  usual into the next round.
- **A declining-severity trend, once.** Track each round's worst
  finding (BLOCKER > ISSUE > NIT > CLEAN) across all four lenses. The
  first time that worst-severity has strictly decreased for two
  rounds running (e.g. BLOCKER → ISSUE → NIT, or ISSUE → NIT → NIT)
  and neither hard stop nor the NIT-only check above has already
  fired, ask the user once: *"Findings have been getting smaller for
  two rounds — keep going to the budget, or call it here?"* This fires
  **at most once per loop run** — asking every round would just be a
  slower version of the problem it's meant to fix. Whatever the user
  answers, don't ask again this run; either stop now, or run the rest
  of the budget without re-litigating it.

Both soft stops are explicitly proportionate to what the user asked
for: if the mandate was "make it bulletproof" or "fix everything,"
say so when asking — the user may well want the full budget even off
a NIT-only round. If the mandate was never stated (a bare "run the
review loop"), the soft-stop question doubles as the moment to get it.

## Lessons from the field

Hard-won observations from the loop that this skill captures:

- **Reviewers report; one process writes.** The most expensive
  problem in a real run was not a missed defect — it was four
  agents editing one worktree. Litter left behind, mutations not
  restored, and one reviewer "helpfully" `git checkout`-ing a file
  the main session had uncommitted work in. Every symptom was
  quiet: a build break with no obvious cause, or a mutation that
  almost got committed. Isolated worktrees per reviewer would also
  solve it, but read-only reviewers are simpler and lose nothing —
  a review's output is a report, not a diff.
- **The recurring defect has one shape: behavior whose deletion
  breaks no test.** Across five rounds of one PR, every round's
  real finding was that — the installation of a handler, a
  per-stream decision, a mutex, a colour. Each was found by luck,
  by a reviewer happening to try that mutation. If a loop keeps
  surfacing this, stop reading and start *sweeping*: mechanically
  mutate every behavior the change introduces and record
  CAUGHT/UNPINNED per item. One sweep found 13 gaps that four
  rounds of careful reading had missed. Consider spending a lens on
  it from round 1.
- **The second recurring defect is stale prose.** Comments and docs
  describing code the same commit moved: a comment citing a symbol
  that never shipped, a doc asserting the mechanism a fix had just
  replaced, sample output the code cannot produce. Nine instances
  in one PR. After any rename or deletion, grep for the old name
  *and* for claims about the old behavior — including in files you
  did not touch this round. Fixing the website and leaving the
  CHANGELOG is the characteristic version of this.
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
- **"When to use" is a real gate, not a suggestion — and once
  running, nothing between the two hard stops used to be a reason to
  pause.** A `status --legend` CLI-flag PR — a handful of files, no new
  interfaces, exactly the "doc-only / small" shape this skill's own
  "When to use" section says to skip — was run through the loop anyway
  on explicit request and burned the full 5-round budget. Round 1
  earned its keep (all four lenses independently caught a real flag-
  validation bug). Every round after that found something, but each
  round's findings were smaller than the last — round 4 and 5 were
  down to wording nits and a test hardening — while the loop kept
  auto-continuing because nothing short of unanimous CLEAN or the cap
  itself ever prompted a pause. The two soft stops in Step 5 (a
  NIT-only round; severity declining for two rounds straight) exist
  because of this run.
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

---
description: 'Multi-round adversarial review loop specialized for data engineering
  and data-pipeline development — four agents (data correctness, adversarial data,
  pipeline boundaries & contracts, data-validation rigor) run in parallel against
  a PR, repeated until all four return `CLEAN — ship it` or a budgeted number of rounds
  (default 5) is exhausted. Use when the user wants a thorough review of a substantive
  data-pipeline PR — new transforms, schema/contract changes, ingestion or load steps,
  backfills, orchestration DAG changes — and wants both correctness AND data-quality
  polish. The boundaries lens reasons about pipeline-step inputs/outputs, schemas,
  and data contracts (not service/CLI APIs); the rigor lens first snapshots how the
  repo already does automated data-validation testing and uses that harness as the
  baseline to find gaps. Heavy: up to 5 rounds × 4 agents = 20 sub-invocations, so
  not for tiny fixes, WIP sketches, or doc-only PRs. Invoke explicitly; do not auto-trigger
  from a generic "review this" request unless the user names the high-bar mandate.'
disable-model-invocation: true
name: data-review-loop
---

# Data review loop

A four-lens adversarial review pattern, **specialized for data engineering and
data-pipeline development**, iterated until the team converges or a budgeted
number of rounds elapses. Use it when the user wants the bar set high on both
**data correctness** and **data-quality polish** — substantive pipeline PRs
where a single review pass would miss the second-order issues (silent row
fan-out, schema drift, non-idempotent backfills, vacuous data tests) that four
independent lenses catch.

This is the data-pipeline retrofit of the general `review-loop` skill. The loop
machinery is the same; two of the four lenses are re-aimed at data work:

- **Lens 3** trades "service/CLI API design" for **pipeline boundaries & data
  contracts** — what is the input and what is the output at each *step*, are the
  data objects and schemas explicitly defined, are the contracts between steps
  enforced rather than assumed.
- **Lens 4** trades generic "test rigor" for **data-validation rigor** — it
  first takes a *snapshot* of however this repo already does automated data
  validation (the existing test/quality harness), then uses that as the
  structure to analyze where the gaps are.

Loop logic:

```
for round in 1..N:                          # N defaults to 5
  launch 4 agents IN PARALLEL:
    data-correctness, adversarial-data, pipeline-boundaries, data-validation-rigor
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

- New or rewritten **transforms / models** (SQL, dbt, Spark/PySpark, pandas,
  Beam/Flink, Airflow/Dagster/Prefect tasks).
- **Schema or data-contract changes** — new columns, type changes, renamed
  keys, partition/clustering changes, anything a downstream consumer depends on.
- **Ingestion / extract / load steps** — connectors, CDC, file/landing parsing,
  warehouse loads, incremental-merge logic.
- **Backfills, reprocessing, and idempotency-sensitive** changes.
- **Orchestration DAG** changes — task boundaries, dependencies, retries,
  partitioning of the run.
- Anything load-bearing in the data path that the user has signalled they want
  "right", not just "the numbers looked fine in one run".

Don't use it for:

- Tiny fixes, doc-only PRs, or WIP sketches — the overhead is wasted.
- Pure non-data code (build scripts, infra-only) — use the general
  `review-loop` instead.
- PRs where the user wants a quick sanity check — use a single review agent.

If the user's request is ambiguous ("review this pipeline PR" with no mandate
signal), ASK before kicking off the loop. The cost is real.

## Step 0: framing

Before launching agents, you have to know what the PR is. Collect:

- **PR URL or branch name** the agents will read from.
- **What changed in this round** — a concise summary of the diff against the
  prior reviewed commit, OR against the base branch on the first round. Note
  specifically which *pipeline steps / models / tasks* the diff touches.
- **What previous rounds found and fixed** — carried forward into each round's
  brief. Fresh subagents don't have the conversation history; they need that
  context to avoid re-flagging closed items.
- **The user's mandate**, in their own words: "ship it ASAP", "make the data
  bulletproof", "fix everything", etc. This decides what to do with
  single-reviewer LOWs and reviewer disagreements.

### Step 0.5: snapshot the data-validation harness (do this ONCE, up front)

Lens 4 needs to anchor on what the repo *already does* for automated data
validation, not on a generic wishlist. Before the first round, take a quick
inventory and write it into a short "validation harness snapshot" that you pass
into Lens 4's brief every round. Look for, and note presence/absence + where:

- **Schema / type tests** — dbt `schema.yml` tests (`not_null`, `unique`,
  `accepted_values`, `relationships`), `dbt_utils`/`dbt_expectations`,
  Great Expectations suites, Pandera schemas, Pydantic models, Spark
  `StructType` schemas, Avro/Protobuf/JSON-Schema definitions, Soda checks,
  Deequ/PyDeequ constraints.
- **Row-level / business-rule assertions** — uniqueness of grain, referential
  integrity, range/domain checks, accepted categories, non-negative measures,
  reconciliation totals.
- **Volume / freshness / anomaly checks** — row-count thresholds, freshness
  SLAs, distribution drift, null-rate monitors.
- **Pipeline tests** — unit tests on transform functions, fixture-driven
  input→output snapshot tests, staging/seed fixtures, golden datasets,
  integration tests against a test warehouse/db.
- **Where validation runs** — at apply/build time, in CI, as a runtime
  data-quality gate (fail-the-pipeline vs warn-only), or post-hoc monitoring.

The snapshot is two things at once: the **structure** Lens 4 reviews *within*
(does the PR extend the existing harness correctly?) and the **baseline** it
measures gaps *against* (what does the change introduce that the harness does
not yet cover?). Keep it short (a bulleted map of "harness X covers Y, lives
at Z; nothing covers W").

## Step 1: launch the four agents in parallel

The agents run as separate subagents, in parallel, **in the same message**
(parallel tool calls). Each gets a self-contained brief with:

1. The PR identifier + commit hash + branch (so they can read the actual files;
   do NOT paste the diff or full datasets into the prompt — point them at the
   files).
2. Their lens (which one of the four).
3. A short summary of what's in the PR and what changed in this round
   (which steps/models/tasks).
4. A short summary of what previous rounds found and fixed.
5. The lens-specific angles below.
6. The expected output shape: ranked punch-list, file:line refs (and
   model/table/column refs where relevant), severity levels
   (`BLOCKER` / `ISSUE` / `NIT` / `CLEAN`), word cap (~300–450 words).
7. **The self-termination signal**: if they have nothing real, they must say
   `CLEAN — ship it` so convergence is programmatically detectable.

Use the **general-purpose** subagent type. Data review needs reading whole
models/transforms and reasoning across them (and across `schema.yml` /
contract files); `Explore`-style agents read excerpts and miss content past
their read window.

### Lens 1 — Data correctness

> You are doing a DATA CORRECTNESS review of <PR>. The repo is at <path> on
> branch <branch>; commits <commit-list>.
>
> **What changed in the round you're reviewing:** <summary of steps/models/tasks>
>
> **What previous rounds covered:** <prior-rounds-summary>
>
> **Your job — does this change produce the right data, three layers:**
>
> 1. **Validate the round-N fixes are correct.** For each non-trivial
>    transform/fix, trace what happens to the data:
>    - on the happy path (representative rows in → expected rows out);
>    - **join semantics** — does any join fan out rows (one-to-many you
>      assumed was one-to-one), drop rows (inner where you needed left), or
>      duplicate on a non-unique key? Check the grain before and after.
>    - **aggregation correctness** — GROUP BY completeness, double-counting,
>      `COUNT` vs `COUNT DISTINCT`, AVG over nulls, window-frame bounds.
>    - **null / type handling** — null propagation through arithmetic and
>      filters (`NOT IN` with nulls, `= NULL`), implicit casts, precision loss
>      (float vs decimal for money), truncation, timezone/UTC handling, date
>      boundary off-by-one.
>    - **dedup / merge / upsert** — is the dedup key the true grain? Does the
>      MERGE/UPSERT update the rows you think? Tie-breaking deterministic?
>
> 2. **Idempotency & reprocessing.** If this step runs twice (retry, backfill,
>    late data), does it produce the same result, or double-load / drift? Are
>    incremental predicates and watermarks correct? Partition overwrite vs
>    append correct?
>
> 3. **What did previous rounds miss?** Fresh eyes — read the actual files:
>    - <file/model 1>
>    - <file/model 2>
>    - <file/model 3>
>
> Report as a punch-list with file:line (and model/table/column) refs and
> severity (BLOCKER / ISSUE / NIT / CLEAN). If no real findings, say
> `CLEAN — ship it`. Don't manufacture findings. Under 350 words.

### Lens 2 — Adversarial data

> You are doing an ADVERSARIAL DATA review of <PR>. The repo is at <path> on
> branch <branch>; commits <commit-list>.
>
> **What changed in this round:** <summary>
>
> **What previous adversarial rounds flagged + what was fixed:**
> <prior-rounds-with-disposition>
>
> **Your job — attack the data path, three layers.**
>
> 1. **Did this round close the holes the previous adversarial pass flagged?**
>    For each prior finding: read the fix, attack it. A fix that works on clean
>    sample data often still breaks on the hostile inputs below.
>
> 2. **Did this round introduce new data-failure surfaces?** New columns,
>    sources, transforms, contracts. What hostile data could break it?
>
> 3. **What did previous adversarial passes still miss?** Specifically consider:
>    - **Malformed / dirty input** — wrong delimiters, embedded newlines/commas
>      in CSV, mixed encodings, BOMs, ragged rows, unexpected nulls/empties,
>      whitespace, mixed types in a column, numbers as strings.
>    - **Schema drift** — added/removed/reordered/renamed columns upstream,
>      widened/narrowed types, a nullable field that starts arriving null, an
>      enum that gains a new value.
>    - **Late / duplicate / out-of-order events** — replays, at-least-once
>      delivery duplicates, watermark/window edge cases, events landing in the
>      wrong partition.
>    - **Scale / skew** — data volume 10–100×, hot partition/key skew, an
>      "always small" dimension that grows, cross-join blowups, OOM/spill.
>    - **Numeric & temporal edges** — overflow, divide-by-zero, NaN/Inf
>      propagation, DST/leap-day/epoch boundaries, naive vs aware timestamps,
>      currency rounding.
>    - **Partial failure** — step dies midway: partial writes, no transaction
>      boundary, downstream reads half-loaded partitions, non-atomic publish.
>    - **PII / sensitive data leakage** — fields that shouldn't flow downstream,
>      logs/error messages that print row contents, unmasked secrets.
>    - **Contract-vs-reality gaps** — anywhere the doc/schema promises a shape
>      the transform does not actually enforce.
>
> Report as a ranked punch-list (most-concerning first) with file:line (and
> table/column) refs and severity. If no real findings, say `CLEAN — ship it`.
> Don't manufacture findings. Under 450 words.

### Lens 3 — Pipeline boundaries & data contracts

> You are doing a PIPELINE BOUNDARIES & DATA CONTRACTS review of <PR>. The repo
> is at <path> on branch <branch>; commits <commit-list>.
>
> **What changed in this round:** <summary>
>
> **What previous boundary rounds asked + how this round answered:**
> <prior-rounds-with-disposition>
>
> **Your job — reason about each pipeline STEP as a boundary: what goes in,
> what comes out, and whether that contract is defined and enforced rather
> than assumed.**
>
> 1. **Map the steps and their I/O.** For each step the PR touches
>    (extract / land / stage / transform / aggregate / load / publish), state:
>    - **Input** — which source/table/topic/file, at what grain, with what
>      schema. Is the input schema explicitly defined (a struct/StructType/
>      `schema.yml`/Pydantic/Avro/Protobuf/JSON-Schema), or inferred/implicit?
>    - **Output** — which table/dataset/topic, at what grain, with what schema,
>      partitioning, and write-mode (append/overwrite/merge). Is it explicitly
>      defined?
>    - **The contract between this step and the next** — column names, types,
>      nullability, units, semantics, the unique grain. Is it written down and
>      enforced (a contract test, a typed schema, a constraint) or just
>      convention?
>
> 2. **Critique the boundary design.** Look for:
>    - **Undefined or implicit schemas** — a transform that trusts `SELECT *`
>      or untyped dataframes; an output nobody declared the shape of.
>    - **Grain ambiguity** — a step whose output grain isn't stated, so the
>      next step can't know whether to expect one row per X.
>    - **Leaky boundaries** — business logic smeared across steps that should
>      own distinct responsibilities; a staging model doing aggregation; a
>      load step silently transforming.
>    - **Contract evolution / breaking changes** — does this PR change a
>      column/type/grain a downstream consumer depends on without a version,
>      migration, or compatibility note? Forward/backward compatibility of the
>      serialization format?
>    - **Naming & semantics** — do column/dataset names describe the data and
>      its units/grain (compared against precedents already in this repo)? Are
>      keys/foreign keys named consistently across steps?
>    - **Idempotency & replay as a contract property** — is "safe to re-run"
>      part of the step's stated contract or an accident?
>
> 3. **Ship readiness.** Are the input and output of every changed step defined
>    and the inter-step contracts enforceable? Anything still implicit that a
>    downstream consumer would trip over? Anything over-specified you can defer?
>
> If shipping: say `CLEAN — ship it`. Otherwise, name what one more round must
> address. Under 400 words.

### Lens 4 — Data-validation rigor

> You are doing a DATA-VALIDATION RIGOR review of <PR>. The repo is at <path>
> on branch <branch>; commits <commit-list>.
>
> **What changed in this round:** <summary>
>
> **Existing data-validation harness snapshot (the baseline to anchor on):**
> <validation-harness-snapshot from Step 0.5 — what exists, what it covers,
> where it lives, what runs in CI vs runtime vs not at all>
>
> **What previous rigor rounds asked + how this round answered:**
> <prior-rounds-with-disposition>
>
> **Your job — use the EXISTING harness above as the structure. First confirm
> the snapshot is accurate by reading the repo yourself; correct it if stale.
> Then review within it and find the gaps the change opens.**
>
> 1. **Validate the new/changed checks.** Read them yourself:
>    - Do the data tests actually prove what they claim, or pass vacuously
>      (e.g. a `not_null` test on a column the source can't produce null for; a
>      uniqueness test on a key that's unique by construction; an assertion that
>      green on an empty fixture)?
>    - For each business rule the change relies on (grain, referential
>      integrity, accepted values, reconciliation totals), is there a check that
>      would actually FAIL if the rule were violated?
>    - Are fixtures/seeds/golden datasets representative, or so clean they can't
>      catch the dirty-data cases Lens 2 worries about?
>
> 2. **What's still missing — relative to the existing harness?**
>    - New columns/models/steps with NO schema or row-level test in the harness
>      that covers comparable existing assets.
>    - Coverage gaps: type/nullability, uniqueness of the new grain, referential
>      integrity to parents, volume/freshness, distribution/null-rate.
>    - Idempotency/backfill: is there a test that re-running produces identical
>      output?
>    - Is validation wired where it runs (CI gate vs runtime data-quality gate
>      vs nowhere)? A check that exists but never runs is a gap.
>    - Does the change follow the harness's own conventions (right framework,
>      right location, right severity), or bolt on a one-off?
>
> 3. **Harness health.** Duplication, brittle pins, warn-only checks that should
>    block, golden datasets that drift. Is the suite a sustainable data-quality
>    contract or a maintenance trap?
>
> Read the files. If the existing harness adequately covers the change: say
> `CLEAN — ship it`. Otherwise name the specific missing checks and where in
> the existing harness they belong. Under 400 words.

## Step 2: synthesize

When all four reports are in, build the punch-list:

1. **Convergent findings** — flagged by ≥ 2 reviewers — go to the top. Two
   independent reviewers naming the same hole (e.g. correctness sees a fan-out,
   rigor sees no uniqueness test for the same grain) is much stronger signal.

2. **Single-reviewer findings** are ranked by severity (the reviewer's own
   rating, sanity-checked). A single MEDIUM is worth doing; a single LOW is
   judgement.

3. **Reviewer disagreements** are normal — boundaries says "lock the schema
   now", rigor says "YAGNI until a consumer exists". Decide by the user's
   mandate:
   - "Ship it ASAP" → defer disputed items.
   - "Fix everything / make the data bulletproof" → take the more defensive
     side, even if a minority of one.
   - Unclear mandate → tell the user and ask.

4. **`CLEAN — ship it`** from a reviewer counts as zero findings from that lens.
   Programmatically check for the substring to detect convergence.

## Step 3: fix

Apply every finding in the synthesized punch-list. For each fix:

- Make it the smallest correct change. Don't snowball.
- **Break-verify** non-trivial data fixes the way you'd break-verify code:
  temporarily feed the transform a row that should violate the rule (a
  duplicate on the grain, a null in a `not_null` column, a fan-out join input),
  confirm the relevant data test fails CLEANLY (the failure points at the right
  column/rule), then restore. This catches "data test passes for the wrong
  reason" — the number-one rigor failure — before the next round does.
- Extend the **existing validation harness** rather than bolting on a parallel
  one; follow its conventions (Step 0.5 snapshot).
- Update docs/contracts (CHANGELOG, schema docs, `schema.yml` descriptions,
  data-contract files) in the SAME commit. The reviewers WILL flag drift.
- Commit with a clear message naming which round's findings the commit closes.

## Step 4: repeat (until convergence or budget)

Push. Launch the next round's four agents. Each one's brief carries forward the
running summary of what previous rounds found and what this round's commit just
fixed — and Lens 4 carries the (now possibly updated) harness snapshot. Fresh
agents need that context to avoid re-flagging closed items.

## Step 5: terminate

Stop when EITHER:

- **All four reviewers in the most recent round emit `CLEAN — ship it`**, OR
- **The configured round budget is exhausted** (default: 5).

If the budget is exhausted with unresolved findings, report the residual to the
user with a clear ship-it / one-more-round / defer recommendation. Don't
silently merge with open data-quality issues.

## Lessons from the field

Hard-won observations this skill captures, specialized for data work:

- **Four lenses, not three.** Correctness alone misses the missing test,
  adversarial alone misses the contract rationale, boundaries alone misses the
  hostile-data case, validation rigor alone misses the silent fan-out. The
  combinatorial coverage is the point.
- **Snapshot the harness before you judge it.** Lens 4 anchored on the repo's
  *actual* validation approach finds real gaps; a generic "you should add tests"
  reviewer manufactures noise and misses the framework the team already uses.
- **The grain is the load-bearing fact.** Most silent data bugs are a wrong
  assumption about "one row per what". Make every lens state the grain in vs
  out; convergence on a grain mismatch is the highest-signal finding the loop
  produces.
- **Clean fixtures hide dirty-data bugs.** A test suite that only runs on
  pristine seeds is vacuously green. Rigor must check that fixtures exercise the
  nulls, dupes, and drift the adversarial lens raises.
- **Idempotency is a contract, not an accident.** Backfills and retries are
  where data pipelines actually break in production; make "safe to re-run" an
  explicit thing the boundaries and rigor lenses both check.
- **Parallel, not sequential.** Running them serially lets one reviewer's output
  bias the next. Parallel keeps the lenses independent.
- **General-purpose agents, not Explore.** Data review needs reading whole
  models/transforms and their `schema.yml`/contract files and reasoning across
  them; Explore reads excerpts and will miss content past its read window.
- **Word caps matter.** Without a cap, reviewers manufacture findings to fill
  space. With a cap (~300–450 words), they prioritise.
- **Severity labels (`BLOCKER`/`ISSUE`/`NIT`/`CLEAN`) prevent drift.** Reviewers
  without a vocabulary rate everything equally important.
- **Convergence trumps unanimity.** Three SHIP-IT + one LOW from a minority of
  one is convergence. Four SHIP-IT is unanimity. Either terminates the loop; the
  difference is whether you act on the dissent before merging.
- **The user's mandate is load-bearing.** "Fix everything" and "ship it" point
  at opposite responses to disputed findings. Get it before the first round;
  carry it through every synthesis.
- **Break-verify every non-trivial data fix.** The number-one source of "test
  passes for the wrong reason" is a data check that never actually fires.
  Feed it a violating row, watch it fail with a clear message, then restore.

# Rating a finding's severity

A review rates each finding `BLOCKER`, `ISSUE` or `NIT`. This file says what each label means
and how to choose one, so that several reviewers reading the same line reach the same label, and
so that whoever asked for the review can check it.

It is used by every lens of `/review-loop` (`SKILL.md` points each brief here), and it works as
well for a single review pass. It rates a finding. What happens to one (fixed now, deferred,
filed, ends a round) is decided by whoever runs the review, not here.

**The repository's own grading comes first.** Where its review guides grade an area themselves
(a line that names a label outright, a table of what each kind of miss is worth), that grading
is the rating, and it already accounts for who can produce the input. The review guides you were told
to read say where it is. This file covers everything they do not.

## The three labels

A label answers one question: **if this merged exactly as it is, what goes wrong, and for whom?**

"As it is" covers every input and state ordinary use reaches by itself, not only today's data:
data grows, time passes, a call is retried, two actors act at the same moment. A defect that
waits for one of those is rated by what it does when it fires. "Nobody has that much yet" is not
a bound.

| Label | If it merged as it is | Typical shapes |
|---|---|---|
| `BLOCKER` | Something breaks, leaks or is spent. A user gets wrong behaviour; data is lost, exposed, or collected against the project's own rules; a commitment the project makes to its users is broken (what it says it will never do, collect or show); someone can make another party pay, or make the software unusable, with nothing bounding it; or the build, CI, a release or a step that cannot be undone fails or does damage | a wrong access decision; a secret or a private datum in a log line, an output or a call to a third party; an action that costs money or quota with no limit; work that grows with a user's data until it fails; an upgrade step that cannot apply or destroys data; a crash on an input ordinary use sends |
| `ISSUE` | Nothing breaks at any state ordinary use reaches, but the change is wrong in a way someone will pay for in time or trust. Code does something other than what it documents; a contract the change promises is not enforced; a rule or practice of the project is not followed; what the software does is right, but what it says about its own work to someone following or diagnosing it is wrong (a count in a summary, a progress line, a log field); output that is the result the user asked for is behaviour, not a report; a test cannot fail; prose would lead a reader to a wrong action | a kind of item left out of a count documented as complete; a status or exit code the documentation does not promise; a documented rule the code does not follow; a documented step that no longer works; a test that passes with the feature deleted |
| `NIT` | Nothing goes wrong. A reader is slowed or briefly misled, or the code could be tidier, and no behaviour, contract or action changes either way | wording; a comment that describes nearby code inaccurately; a name; an exported symbol nothing uses; duplicated test helpers |

"Nothing goes wrong, but I would have written it differently" is not a finding. A name that says
something false about what it names is a `NIT`; a name you would merely have chosen differently
is not a finding.

## How to rate

For every finding you rate above `NIT`, write these four things in the report. They are the
finding's description, not an addition to it, and a sentence or two covers them.

1. **The scenario.** Who or what does what, on which input, and gets which wrong result. "A
   run that takes the batch path reports a count one short." "Someone following the install
   guide's step 4 runs a command that no longer exists." A finding with no scenario
   is not above `NIT`.
2. **The consequence**, in the table's terms: breaks, leaks or is spent (`BLOCKER`); wrong, and
   paid for in time or trust, but nothing breaks (`ISSUE`); nothing goes wrong (`NIT`).
3. **Who can produce it.** A program or client the project ships, a scheduled run, a person
   following a doc, a later contributor making an ordinary change: the rating stands. Only someone
   going around the project's own programs and public interfaces to hand-craft an input those
   would never produce or accept in ordinary use (a state file edited by hand, a wire message
   no shipped client sends), where the one harmed is the person crafting it: one label lower.
   What a person types into a shipped program, writes in a file it is documented to read, or
   passes to a library's public interface is ordinary use. A
   hostile caller who harms someone else is the point of the finding, so it is never lowered:
   another person's data, access control, someone else's bill, availability. Apply this once. The
   repository's own grading and the test-gap ruling below already include it.
4. **Why it is not one label lower.** One clause, naming what in the scenario puts it at this
   label. This tests the label you have after step 3; it is not a second adjustment for the
   producer. If you cannot write it, the label is one lower.

A `NIT` on a contract, a rule or a path a user reaches also says, in a clause, why nothing goes
wrong. That is what lets the receiver check a label that may be too low.

Rate what you found, at the commit under review. Two things that are not severity:

- **Confidence.** A claim you could not verify is rated as if it were true and marked
  unverified, with what the suspicion rests on (the line, the document, the behaviour you saw),
  what you tried, and what would confirm it. A suspicion with nothing under it is not a finding.
  It is not rated up to be safe or down to be modest.
- **How many reviewers said it.** Two reviewers naming the same line makes it more likely to be
  true. It does not make it more severe.

## Rulings for the kinds that recur

These are the cases where reviewers disagree with each other.

- **Stale or false prose is rated by what a reader would do wrong, not by how often prose goes
  stale.** Common is not severe.
  - `NIT`: no reader would act wrongly on it. A code comment that describes its neighbouring
    code inaccurately, a doc comment whose wording lags a rename, a commit message, a test name:
    a reader checks the code beside it and moves on. So is a slip in any document that the
    reader corrects on sight (a wrong number with the right one beside it).
  - `ISSUE`: a reader acting on it would do something wrong. The test is that action, not the
    kind of document. It is most often found in prose read without the code in front of the
    reader: a design document, contributor instructions, a checklist or runbook, a
    README's operating steps, a PR description;
    and in a comment that is itself a contract, when what it promises is false (an exported
    symbol's doc comment, a schema or API description).
  - `BLOCKER`: prose that, followed exactly, breaks something that runs or does damage (a
    command that deletes or exposes, a step in the wrong order, a setting that stops the
    software running), whatever kind of document it is in; or a user-facing statement that is false about
    the user's data.
- **A test gap is rated by what it would let through, and by who could introduce that.** An
  unpinned mutant, a vacuous assertion, a contract with no test. A surviving mutant is a gap
  even when the code is right.
  - `ISSUE`: the unheld behaviour would itself be a `BLOCKER` or `ISSUE` if it broke, nothing
    else holds it (another test, the compiler, a lint), and an ordinary later change could break
    it: a wrong argument, a dropped line, a copy of the wrong neighbour.
  - `NIT`: the unheld behaviour would be a `NIT`; or something else holds it; or only a
    deliberate rewrite could break it.
  - Not a finding: an equivalent mutant, one with no observable effect.
  - A gap is never rated above `ISSUE`, and never above the defect it would let through.
- **A pre-existing defect is rated like any other and marked pre-existing.** It is pre-existing
  when it is in code the change did not touch, and the change neither makes it reachable nor
  makes it worse; a defect the change newly exposes is the change's own. The label says how
  bad it is. That the change did not introduce it is a fact the receiver needs beside the label,
  not a reason to lower it.
- **A design alternative needs a scenario like anything else.** "This should be an interface",
  "this belongs in another package": `NIT`, unless you can name what goes wrong with the design
  as built (two copies that will drift and one already has; a caller that cannot be written).
- **A recorded decision is not a finding.** Its implementation is rated as usual, and a record
  that understates its own cost is an `ISSUE` against the record.

## Worked examples

The first column is the finding as raised.

| Finding | Label | Why |
|---|---|---|
| A comment above a call still describes a design the same change replaced | `NIT` | A code comment beside the code it describes; no reader acts on it. Rating it higher because stale prose is common is not a rating |
| The contributor guide says the integration tests run whenever the test command runs. Since this change they are skipped unless an environment variable is set | `ISSUE` | A contributor acts on it without the code: they read a green run as coverage it did not give |
| A design document says an option was removed in version 2.3; it was 2.4, and every supported version is later than both | `NIT` | A design document, but no reader acts wrongly: no choice anyone makes today turns on which of the two it was. The kind of document does not make it an `ISSUE` |
| A summary line reports how many files were processed and leaves out the skipped ones; the documentation says it counts every file | `ISSUE` | Code does other than it documents, on a path that runs, and the reader gets a wrong number. Not a `BLOCKER`: every file was handled correctly, and only the summary of it is wrong |
| No test pins the order of two fields in a debug line that no document describes | `NIT` | A test gap. If the order changed, that would be a `NIT` itself: nothing documents it and nobody acts on it |
| A rounding test passes whether values are rounded before or after summing | `ISSUE` | A test that cannot fail for the behaviour it names; a swapped order changes the total shown; an ordinary edit could swap it; nothing else holds it |
| A wrapper's tests do not check the timeout it passes on, so a wrapper that drops the timeout passes the suite | `ISSUE` | A dropped timeout lets one stuck call hang the run; a one-word slip introduces it; nothing else holds it |
| A test pins how the program's entry point builds a dependency; it would not notice a second private constructor added beside the first | `NIT` | Only a deliberate edit gets there |
| A new setup-guide step names a setting the code does not read, so the software silently uses its default output location and overwrites what is there | `BLOCKER` | Followed exactly, the guide destroys the reader's files. The kind of document does not cap it |
| A hand-built input with a 60 KB field makes one log line 60 KB long; the parser caps a field's size | `NIT` | Nothing goes wrong: one line, and it is bounded |
| An action anyone can trigger calls a third-party service metered to the project's account, and has no limit | `BLOCKER` | A script makes someone else pay without bound. Hand-crafted, and not lowered: the one harmed is not the caller |
| A new step loads the whole of a collection its users can grow; the largest has 300 items today | `BLOCKER` | Ordinary growth reaches the size where it fails. Rated by what it does then, not by today's data |
| One user reads another's record by guessing its id | `BLOCKER` | A wrong access decision. It harms someone other than the caller, so "only by hand" does not lower it |
| A reviewer suspects a dependency rejects a batch above 50 items and that a new path can exceed it, but could not confirm the limit | `BLOCKER`, unverified | Rated as if true: those batches would fail. Marked unverified, with what was tried and that the dependency's limit would confirm it |

A repository's review guides may carry worked examples of their own, drawn from its real
findings. Where they do, read them as well: they show these labels on that repository's code.

## Checking a label you were given

For whoever asked for the review: the main session of a `/review-loop` run, or a session that
asked for one review pass.

- **Check the label as well as the claim.** First whether the finding is true (a grep, a probe,
  a failing test). Then its four parts: is the scenario real, does the consequence match the
  label, who can produce it. A true finding with the wrong label is corrected, up or down, and
  the correction is recorded beside the reviewer's own words.
- **A label you have not checked is the reviewer's, and you say so.** When you report progress
  before the check is done, say "raised as an ISSUE, not yet checked", or give your own rating.
  Repeating a reviewer's label as your own is how an unchecked rating reaches the person you
  work for looking settled.
- **When that person rates a finding differently from you, give your rating and its scenario
  before you agree or disagree.** Their rating stands. The record keeps the reviewer's label,
  yours and theirs, so the next reader can see which of the three moved.

# The final report — a comment on the PR

Every report the loop makes when it ends (SKILL.md, Step 5: converged, a report to the owner at
the round gate or in the extension, the end of rounds the owner granted) is posted as **one new
comment on the PR**, and given in the conversation as well. It is never an edit to the PR body
or to an earlier report: a run that reports to the owner and later converges leaves two
comments, and the newest is the run's state.

The comment has a fixed first line and opens with a scorecard, so a script can find every run in
a repository and read how it went. Everything after the scorecard is for a person.

## The shape

````markdown
## review-loop report

```text
review-loop-scorecard v1
outcome: converged
head: <full sha the report describes>
rounds: 3
budget: 3
whole_pr_rounds: 2
extended: false
findings: 9
blocker: 1
issue: 5
nit: 3
verified: 5
accepted_unverified: 0
overruled: 1
pre_existing: 1
missed_by_whole_pr_rounds: 0
from_earlier_fix: 2
classes_swept: 2
by_lens: correctness=3 adversarial=2 api-design=1 test-rigor=4
```

<the report itself: SKILL.md, Step 5>
````

- **The first line is exactly `## review-loop report`**, with nothing before it. Whatever the
  session's own rules add to a comment (an attribution footer) goes at the end.
- **The scorecard is the first fenced block**, one `key: value` per line, every key present, in
  this order, a count of `0` written as `0`. Its first line names the format's version; a change
  to the keys or to what one counts raises it.
- **After it, the report Step 5 requires**, written for a reader with none of the run's context:
  how the run ended, then every finding left open (held, deferred, pre-existing, NITs not
  fixed), every overrule of a BLOCKER or ISSUE with the lens's words beside the evidence, every
  one accepted without verification, the issues filed, and that the loop did not run the full
  checks on the head.
  When nothing is left open, say so in one line.

## What each key counts

Count from the context file's Round history, never from memory. One *finding* is one entry
there: a class with five instances is one, and a finding two lenses raised is one.

| Key | Value |
|---|---|
| `outcome` | `converged`; `owner-report` (at the round gate: a gated path, a finding awaiting the user, a judge that did not pass; or a gated fix met in an extension or granted round); `extension-exhausted`; `granted-rounds-exhausted` |
| `head` | the commit pushed last, in full; at Convergence, the one carrying the NIT fixes |
| `rounds` | rounds whose reviewers reported, over the whole run so far |
| `budget` | the budget the run started with |
| `whole_pr_rounds` | the setting, not a count of rounds run: 2, or the number the user or a calling skill set |
| `extended` | `true` once the judge has granted the two extra rounds |
| `findings` | every entry, at its audited severity; one dropped as false is included at the severity the lens gave it |
| `blocker`, `issue`, `nit` | `findings` split by that severity; they sum to it |
| `verified` | BLOCKERs and ISSUEs, by the lens's label and not pre-existing, accepted at that label or above with the line of evidence that showed them real |
| `accepted_unverified` | BLOCKERs and ISSUEs, not pre-existing, accepted with the audit skipped |
| `overruled` | BLOCKERs and ISSUEs, not pre-existing, dropped as false or rated below the lens's label, and not withdrawn; one rated down and then fixed counts here only |
| `pre_existing` | entries of any severity confirmed pre-existing |
| `missed_by_whole_pr_rounds` | entries in the PR's original change first raised in a diff-only round |
| `from_earlier_fix` | BLOCKERs and ISSUEs introduced by an earlier round's fix |
| `classes_swept` | entries whose class was searched for, whether or not the search found more |
| `by_lens` | per lens, the entries it raised; a finding two lenses raised counts for both |

`verified`, `accepted_unverified` and `overruled` together are every finding a lens labelled
BLOCKER or ISSUE, apart from the pre-existing ones and those still awaiting the user. A high `overruled`
for one lens says that lens is noisy; a high `from_earlier_fix` says the fixes are the problem;
a high `missed_by_whole_pr_rounds` says the whole-PR rounds are too few or too shallow.

## Finding the reports

Every issue and PR comment in a repository, filtered to the reports (GitHub's REST endpoint
`GET /repos/{owner}/{repo}/issues/comments`):

```bash
gh api "repos/{owner}/{repo}/issues/comments" --paginate \
  --jq '.[] | select(.body | startswith("## review-loop report")) | {pr: .issue_url, at: .created_at, body}'
```

One value across every run, here the rounds each took:

```bash
gh api "repos/{owner}/{repo}/issues/comments" --paginate \
  --jq '.[] | select(.body | startswith("## review-loop report")) | (.body | capture("\nrounds: (?<n>[0-9]+)").n)'
```

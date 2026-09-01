package cli

import (
	"sort"
	"strings"
	"testing"
)

// secretsTokenRule is one token whose interpretation must have a single home,
// plus the production files allowed to contain it.
type secretsTokenRule struct {
	name string
	// token is matched as a plain substring against production Go sources.
	token   string
	why     string
	allowed map[string]string
}

// scanTokenViolations is the guard's matcher, factored out of the repo walk so
// the guard can be exercised against a SYNTHETIC file set — see the
// negative-control subtest. files maps a repo-relative slash path to its source.
// It returns the non-allowlisted files containing token (sorted, so a failure
// message is stable) and the set of allowlisted files that still contain it.
func scanTokenViolations(files map[string]string, token string, allowed map[string]string) (unexpected []string, seen map[string]bool) {
	seen = map[string]bool{}
	for rel, src := range files {
		if !strings.Contains(src, token) {
			continue
		}
		if _, ok := allowed[rel]; ok {
			seen[rel] = true
			continue
		}
		unexpected = append(unexpected, rel)
	}
	sort.Strings(unexpected)
	return unexpected, seen
}

// TestSecretsConfigIsInterpretedInOnePlace pins that the [secrets] contract has
// exactly one definition.
//
// Why a guard and not a doc line: before #228 the contract was written out
// SEVEN times — `check`'s verifySecrets, `doctor`'s checkSecrets, and one gate
// in each of the five `secret` subcommands. Every copy compared
// `cfg.Backend` against a lowercase literal while the resolver `apply` renders
// through (secrets.SelectBackend) lower-cases it first, so `backend = "AGE"`
// applied cleanly and failed all seven; and `check` and `doctor` had drifted
// apart on `backend = "env"` and on an un-stat-able vault path. Nothing in the
// type system distinguishes "reads a [secrets] field" from "re-decides what a
// valid [secrets] field is", so a new copy is invisible in review and silent at
// runtime.
//
// WHAT THIS GUARD CATCHES, EXACTLY — it is a substring scan, so its reach is
// uneven and worth stating rather than trusting:
//   - `.Backend` is the ROBUST axis. Any new gate that interprets the backend
//     name must READ the field, and reading it means writing `.Backend`
//     somewhere — whatever the comparison looks like. This one is hard to evade
//     by accident.
//   - `.Recipient == ""` / `.IdentityFile == ""` are EXACT-MATCH tokens: a copy
//     written `!= ""`, `len(cfg.Recipient) == 0`, or with the operands swapped
//     slips past. Each is a cheap regression pin on the four copies of ITSELF
//     that #228 deleted (check, doctor, `secret edit`, `secret set`), not a
//     complete fence. A required-field rule that evades them still has to read
//     `.Backend` to know the backend is age, so the first rule still catches
//     any copy that gates on the backend at all. The decoy arm of the negative
//     control below pins that gap as a KNOWN one rather than leaving it to
//     this prose.
//   - It over-approximates in the other direction too (a future `.BackendKind`
//     field would match `.Backend`). A false positive costs one allowlist line;
//     a false negative costs another silent divergence.
//
// The bar for adding a file to an allowlist here: it IS the single definition,
// or it is the path resolver that must return "" for an unset value.
func TestSecretsConfigIsInterpretedInOnePlace(t *testing.T) {
	repoRoot := repoRootFromCaller(t)

	files := map[string]string{}
	if err := walkRepoGoFiles(repoRoot, func(rel, src string) { files[rel] = src }); err != nil {
		t.Fatalf("walk: %v", err)
	}

	rules := []secretsTokenRule{
		{
			name:  "backend name",
			token: ".Backend",
			why: "the [secrets].backend name is interpreted in exactly two places: SelectBackend " +
				"(what apply resolves through) and validate.go (what check, doctor and the secret " +
				"group gate on). A third comparison is how `backend = \"AGE\"` came to apply " +
				"cleanly and fail every validator. Route it through secrets.ValidateConfig or " +
				"secrets.RequireAgeVault instead — they take the whole [secrets] block, so a " +
				"caller never reads .Backend at all. (secrets.NormalizeBackend is the shared " +
				"lower-casing primitive those two and SelectBackend agree through; calling it " +
				"from a new file still reads .Backend and so still trips this guard — correctly, " +
				"because it means a third interpretation is being written.)",
			allowed: map[string]string{
				"internal/secrets/secrets.go":  "SelectBackend — the resolver apply renders through",
				"internal/secrets/validate.go": "NormalizeBackend, ValidateConfig, RequireAgeVault",
			},
		},
		{
			name:  "recipient requirement",
			token: `.Recipient == ""`,
			why: "whether [secrets].recipient is required is decided once, in validate.go " +
				"(ValidateConfig for check/doctor, RequireAgeVault for the secret group).",
			allowed: map[string]string{
				"internal/secrets/validate.go": "the one required-field rule",
			},
		},
		{
			name:  "identity_file requirement",
			token: `.IdentityFile == ""`,
			why: "whether [secrets].identity_file is required is decided once, in validate.go; " +
				"secretpaths.go's check is the path resolver's own empty-input guard, not a rule.",
			allowed: map[string]string{
				"internal/secrets/secretpaths.go": "ResolveIdentityFile returns \"\" for an unset identity_file",
				"internal/secrets/validate.go":    "the one required-field rule",
			},
		},
	}

	for _, r := range rules {
		t.Run(r.name, func(t *testing.T) {
			unexpected, seen := scanTokenViolations(files, r.token, r.allowed)
			if len(unexpected) > 0 {
				t.Errorf("%q appears outside its single definition:\n  %s\n\n%s",
					r.token, strings.Join(unexpected, "\n  "), r.why)
			}
			// An allowlist that has gone stale is worse than no guard: it reads as
			// coverage while pinning nothing.
			for rel, reason := range r.allowed {
				if !seen[rel] {
					t.Errorf("%s is allowlisted for %q (%s) but no longer contains it — drop it from the allowlist",
						rel, r.token, reason)
				}
			}
		})
	}

	// NEGATIVE CONTROL. A guard that passes whatever the tree looks like is
	// worse than none, so all three of its arms are exercised against a
	// synthetic file set on every run: a guard that stops biting fails HERE
	// rather than going quietly green.
	t.Run("negative control", func(t *testing.T) {
		r := rules[0] // the .Backend rule — the robust axis
		violating := map[string]string{
			"internal/cli/newgate.go": "func gate(cfg source.SecretsConfig) error {\n" +
				"\tif cfg.Backend != \"age\" {\n\t\treturn errRefused\n\t}\n\treturn nil\n}\n",
		}
		for rel := range r.allowed {
			violating[rel] = "switch NormalizeBackend(cfg.Backend) {\n"
		}
		unexpected, seen := scanTokenViolations(violating, r.token, r.allowed)
		if len(unexpected) != 1 || unexpected[0] != "internal/cli/newgate.go" {
			t.Fatalf("the guard must flag a sixth copy of the backend gate; got %v", unexpected)
		}
		for rel := range r.allowed {
			if !seen[rel] {
				t.Fatalf("%s contains the token in the synthetic set but was not seen", rel)
			}
		}

		// The stale-allowlist arm: an allowlisted file that no longer contains
		// its token must be reported as unseen, or the allowlist rots into a
		// no-op that reads like coverage.
		_, seenStale := scanTokenViolations(map[string]string{"internal/cli/newgate.go": "nothing here"}, r.token, r.allowed)
		for rel := range r.allowed {
			if seenStale[rel] {
				t.Fatalf("%s was reported as still containing %q when it does not", rel, r.token)
			}
		}

		// The decoy arm: the required-field tokens are plain substrings, not
		// patterns, so a copy that spells the comparison differently — or splits
		// it across lines — is NOT flagged. That is the limit documented above;
		// pinning it here keeps it a known gap that a reader can measure rather
		// than a claim in a comment. Widening the matcher to catch this should
		// fail here, so the prose above gets rewritten with it.
		recipient := rules[1]
		decoy := map[string]string{
			// `.Recipient` and `== ""` both appear, never adjacent.
			"internal/cli/newgate.go": "if len(cfg.Recipient) == 0 {\n\t\treturn errRefused\n\t}\n" +
				"\tif other == \"\" {\n\t\treturn nil\n\t}\n",
		}
		if unexpectedDecoy, _ := scanTokenViolations(decoy, recipient.token, recipient.allowed); len(unexpectedDecoy) != 0 {
			t.Fatalf("the %q rule is an exact substring match and must not flag a variant spelling; got %v",
				recipient.token, unexpectedDecoy)
		}
	})
}

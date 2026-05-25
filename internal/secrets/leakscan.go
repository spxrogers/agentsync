package secrets

import (
	"fmt"
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// secretGroup identifies one server/hook-event in the canonical (scope+kind+id),
// independent of intra-group position (arg index, env/header key). Re-reference
// can relocate a secret WITHIN a group (a shifted arg, a renamed env key), so
// the leak scan reasons per group, not per field — otherwise a legitimately
// shifted-but-restored secret would look like a missing slot.
type secretGroup struct{ scope, kind, id string }

func groupOf(loc secretFieldLoc) secretGroup {
	return secretGroup{scope: loc.scope, kind: loc.kind, id: loc.id}
}

// ResidualSecretCleartext is the fail-closed backstop for the dest->source write
// path. After ReReferenceCanonical has done its best to restore ${secret:…}
// placeholders, this scans the about-to-be-written canonical for a resolved
// secret that would still persist as CLEARTEXT into the source (a committed
// dotfiles repo) — the dangerous class re-reference alone cannot fully close,
// because by value it cannot tell "moved/rotated secret" from "deliberate
// non-secret literal edit." capture.Capture refuses to write when this returns
// anything (it errs toward refusing rather than guessing).
//
// `ingested` is the re-referenced canonical about to be written; `against` is
// the current templated source. Two leak shapes, evaluated per secretGroup:
//
//   - VALUE: a live vault secret value appears verbatim in any field of the
//     group — a secret moved into a field whose source counterpart is a literal,
//     so re-reference left it unmasked.
//   - SLOT: a ${secret:K} the source group referenced is ABSENT from the entire
//     ingested group — a rotated/edited secret value re-reference couldn't match
//     (its cleartext, or a now-unreferenced credential, would persist). A
//     legitimately *shifted* secret keeps its placeholder somewhere in the
//     group, so it does NOT trip this.
//
// Returns human-readable group descriptions (empty = safe to write).
func ResidualSecretCleartext(ingested, against *source.Canonical, sec, env Resolver) []string {
	if ingested == nil || against == nil {
		return nil
	}
	secretVals := sourceSecretValues(against, sec) // resolved value -> placeholder

	// Source ${secret:K} placeholders referenced by each group.
	srcPlaceholders := map[secretGroup][]string{}
	walkSecretFields(against, func(loc secretFieldLoc, s string) string {
		if phs := secretPlaceholders(s); len(phs) > 0 {
			srcPlaceholders[groupOf(loc)] = append(srcPlaceholders[groupOf(loc)], phs...)
		}
		return s
	})

	// All ingested field values, per group.
	ingestedByGroup := map[secretGroup][]string{}
	walkSecretFields(ingested, func(loc secretFieldLoc, s string) string {
		g := groupOf(loc)
		ingestedByGroup[g] = append(ingestedByGroup[g], s)
		return s
	})

	seen := map[secretGroup]bool{}
	var leaks []string
	flag := func(g secretGroup) {
		if !seen[g] {
			seen[g] = true
			leaks = append(leaks, describeGroup(g))
		}
	}

	// VALUE prong: a live vault secret value sits verbatim in an ingested field.
	for g, vals := range ingestedByGroup {
		for _, s := range vals {
			for v := range secretVals {
				if strings.Contains(s, v) {
					flag(g)
				}
			}
		}
	}
	// SLOT prong: a ${secret:K} the source group referenced no longer appears
	// anywhere in the ingested group (rotated / edited away).
	for g, phs := range srcPlaceholders {
		joined := strings.Join(ingestedByGroup[g], "\x00")
		for _, ph := range phs {
			if !strings.Contains(joined, ph) {
				flag(g)
			}
		}
	}
	return leaks
}

// secretPlaceholders extracts the ${secret:K} markers in s (ignores ${env:…}).
func secretPlaceholders(s string) []string {
	if !strings.Contains(s, "${secret:") {
		return nil
	}
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if len(m) >= 3 && m[1] == "secret" {
			out = append(out, m[0])
		}
	}
	return out
}

func describeGroup(g secretGroup) string {
	scope := ""
	if g.scope != "" {
		scope = g.scope + " "
	}
	return fmt.Sprintf("%s%s %q", scope, g.kind, g.id)
}

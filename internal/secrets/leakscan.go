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
	srcByLoc := make(map[secretFieldLoc]string)
	walkSecretFields(against, func(loc secretFieldLoc, s string) string {
		srcByLoc[loc] = s
		return s
	})
	// Per-group concatenation of the ingested fields, so a secret legitimately
	// SHIFTED within a group (its placeholder restored at a new position) is not
	// mistaken for one rotated away.
	groupText := map[secretGroup]string{}
	walkSecretFields(ingested, func(loc secretFieldLoc, s string) string {
		groupText[groupOf(loc)] += "\x00" + s
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

	walkSecretFields(ingested, func(loc secretFieldLoc, s string) string {
		g := groupOf(loc)
		// VALUE prong: a live vault secret value sits verbatim in this field —
		// a known secret moved/embedded where re-reference couldn't mask it.
		for v := range secretVals {
			if strings.Contains(s, v) {
				flag(g)
				return s
			}
		}
		// SLOT prong: this field's OWN source counterpart was a ${secret:K} slot,
		// but the field now holds a non-empty value that is NOT the placeholder
		// AND the placeholder is gone from the whole group — an in-place
		// rotation/edit to cleartext that re-reference couldn't match. Keying on
		// THIS field's counterpart (not the whole source) means a single-item
		// write-back isn't refused over an unrelated server's secret, and a field
		// removed entirely (absent / empty here) isn't mistaken for a rotation;
		// the group check spares a legitimately shifted-and-restored secret.
		if s == "" {
			return s
		}
		for _, ph := range secretPlaceholders(srcByLoc[loc]) {
			if !strings.Contains(s, ph) && !strings.Contains(groupText[g], ph) {
				flag(g)
				return s
			}
		}
		return s
	})
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

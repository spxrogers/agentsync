package secrets

import (
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// ReReferenceCanonical restores ${secret:…} placeholders in c — a canonical
// reconstructed from a destination (or resolved by SubstituteCanonical) — using
// against, the current templated source, as the reference. It is the inverse of
// SubstituteCanonical and the single boundary through which a resolved /
// ingested canonical is converted back toward the templated source form before
// any write-back. apply substitutes ${secret:…} to cleartext into the
// destination; capture reads that destination back, so without this re-reference
// a live credential would be persisted into ~/.agentsync (often a committed
// dotfiles repo).
//
// Each secret-bearing field is decided FIELD-LOCALLY, from its own source
// counterpart, so re-referencing can never over-mask a value the user typed as a
// literal in an unrelated field:
//
//   - FIELD-POSITIONAL restore (primary): an unchanged templated field whose
//     source counterpart resolves to the same value is restored to its
//     ${secret:…} placeholder. A field the source did NOT template (e.g.
//     command = "npx") is never rewritten here, even if its literal happens to
//     equal some secret's value.
//   - VALUE-BASED fallback (for structural edits the positional pass can't see —
//     a shifted MCP arg index, a renamed env/header key or server id, a secret
//     embedded in an edited command): if the field's source counterpart was
//     templated, re-reference its resolved cleartext (substring); if the field
//     has NO counterpart (shifted/renamed), re-reference only when the WHOLE
//     value is a known secret. A field whose counterpart is a literal is left
//     untouched. ${env:…} is never inverted.
//
// Both c and against are walked through the one walkSecretFields enumeration, so
// a new secret-bearing field is re-referenced automatically. Hooks have no
// stable id, so they are matched by event + resolution (see rereferenceHook),
// with a value-based fallback for a hook command edited out of position.
func ReReferenceCanonical(c *source.Canonical, against *source.Canonical, sec, env Resolver) {
	if c == nil || against == nil {
		return
	}
	srcByLoc := make(map[secretFieldLoc]string)
	walkSecretFields(against, func(loc secretFieldLoc, s string) string {
		srcByLoc[loc] = s
		return s
	})
	// secretVals maps each source-referenced secret's resolved cleartext to its
	// ${secret:…} placeholder, for the field-local value-based fallback below.
	secretVals := sourceSecretValues(against, sec)

	walkSecretFields(c, func(loc secretFieldLoc, ingested string) string {
		if loc.kind == "hook" {
			// Positional-by-event restore first; then a value-based fallback for
			// a hook command edited out of its positional match.
			if restored := rereferenceHook(against, loc.id, ingested, sec, env); restored != ingested {
				return restored
			}
			return rereferenceHookByValue(against, loc.id, ingested, secretVals)
		}
		srcVal, hasCounterpart := srcByLoc[loc]
		// 1. Field-positional restore: an unchanged templated field whose source
		//    counterpart resolves to the same value is restored to its placeholder.
		if restored := restoreField(srcVal, ingested, sec, env); restored != ingested {
			return restored
		}
		// 2. Field-LOCAL value-based fallback for structural changes the
		//    positional pass can't see (a shifted arg index, a renamed env/header
		//    key or server id, a secret embedded in an edited command). Deciding
		//    per field from that field's source counterpart means it can NEVER
		//    over-mask a value the user typed as a literal in an unrelated field
		//    (the bug a blind value-wide replace would cause):
		//      - counterpart TEMPLATED but changed → the secret is genuinely this
		//        field's; re-reference its resolved cleartext (substring ok).
		//      - NO counterpart (shifted/renamed) → re-reference only when the
		//        WHOLE value is a known secret, never a substring, so an unrelated
		//        new field that merely contains a secret value is left alone.
		//      - counterpart is a LITERAL → leave it exactly as written.
		//    Only ${secret:…} is inverted (never ${env:…}, matching restoreField).
		switch {
		case hasCounterpart && strings.Contains(srcVal, "${secret:"):
			return MaskResolved(ingested, secretVals)
		case !hasCounterpart:
			if ph, ok := secretVals[ingested]; ok {
				return ph
			}
		}
		return ingested
	})
}

// rereferenceHookByValue is the value-based fallback for a hook command that was
// edited out of its positional match (rereferenceHook already ran). Hooks have
// no stable id, so scope the masking to events that have at least one templated
// source hook — a hook the user never templated is left untouched.
func rereferenceHookByValue(against *source.Canonical, event, ingested string, secretVals map[string]string) string {
	for _, h := range against.Hooks {
		if h.Event == event && strings.Contains(h.Command, "${secret:") {
			return MaskResolved(ingested, secretVals)
		}
	}
	return ingested
}

// minReReferenceLen is the shortest resolved secret value the value-based
// fallback will invert. A 1–3 char "secret" is not a credential worth the risk
// of substring-rewriting unrelated text (a flag, a path segment); the
// field-positional pass still restores it when the dest field is unchanged.
const minReReferenceLen = 4

// sourceSecretValues resolves every ${secret:…} reference in against and returns
// a map from the resolved cleartext to its placeholder. ${env:…} is excluded:
// an env value is not a credential-at-rest concern and is never inverted (see
// restoreField). Values shorter than minReReferenceLen are skipped.
func sourceSecretValues(against *source.Canonical, sec Resolver) map[string]string {
	out := map[string]string{}
	if against == nil || sec == nil {
		return out
	}
	walkSecretFields(against, func(_ secretFieldLoc, s string) string {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if len(m) < 3 || m[1] != "secret" {
				continue
			}
			v, err := sec.Resolve(m[2])
			if err != nil || len(v) < minReReferenceLen {
				continue
			}
			out[v] = m[0]
		}
		return s
	})
	return out
}

// restoreField returns the source field's templated value when it referenced a
// secret and still resolves to the (unchanged) ingested value; otherwise it
// keeps the ingested value verbatim. ${env:…} references are intentionally not
// inverted — an env value is not a credential-at-rest concern the way a
// ${secret:…} backend value is, and inverting it risks rewriting an unrelated
// literal that happens to equal the env var's value.
func restoreField(srcVal, ingested string, sec, env Resolver) string {
	if !strings.Contains(srcVal, "${secret:") {
		return ingested
	}
	if resolved, _, err := SubstituteRefs(srcVal, sec, env); err == nil && resolved == ingested {
		return srcVal
	}
	return ingested
}

// rereferenceHook restores a hook command. Hooks have no stable id, so a
// templated source command for the same event whose resolution equals the
// ingested command is the match.
func rereferenceHook(against *source.Canonical, event, ingested string, sec, env Resolver) string {
	for _, h := range against.Hooks {
		if h.Event != event || !strings.Contains(h.Command, "${secret:") {
			continue
		}
		if resolved, _, err := SubstituteRefs(h.Command, sec, env); err == nil && resolved == ingested {
			return h.Command
		}
	}
	return ingested
}

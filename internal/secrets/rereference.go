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
// Matching is FIELD-POSITIONAL, not value-based: a field is restored to its
// templated form only when (a) the corresponding source field actually
// referenced a secret and (b) the ingested value still resolves to the same
// thing (the field is unchanged). A field the source did NOT template (e.g.
// command = "npx") is never rewritten, even if its literal happens to equal
// some secret's value — value-based masking would corrupt it.
//
// Both c and against are walked through the one walkSecretFields enumeration, so
// a new secret-bearing field is re-referenced automatically. Hooks have no
// stable id, so they are matched by event + resolution (see rereferenceHook).
func ReReferenceCanonical(c *source.Canonical, against *source.Canonical, sec, env Resolver) {
	if c == nil || against == nil {
		return
	}
	srcByLoc := make(map[secretFieldLoc]string)
	walkSecretFields(against, func(loc secretFieldLoc, s string) string {
		srcByLoc[loc] = s
		return s
	})
	walkSecretFields(c, func(loc secretFieldLoc, ingested string) string {
		if loc.kind == "hook" {
			return rereferenceHook(against, loc.id, ingested, sec, env)
		}
		return restoreField(srcByLoc[loc], ingested, sec, env)
	})
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

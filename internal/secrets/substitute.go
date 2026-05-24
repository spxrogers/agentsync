package secrets

import (
	"fmt"
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// SubstituteCanonical resolves every secret-bearing string field of c (see
// walkSecretFields for the authoritative set) — any ${secret:...} / ${env:...}
// reference — and returns the result as a Resolved. The input c is left
// untouched (it stays the templated source form); resolution happens on an
// internal copy, so a resolved cleartext value can never alias back into the
// caller's source.Canonical.
//
// The Resolved return type is what makes the apply model render-only: it cannot
// be handed to source.Write* or capture.Capture (compile error). If any
// reference cannot be resolved, it returns an error listing all unresolved
// markers — apply is blocked, never silent about a missing secret.
func SubstituteCanonical(c source.Canonical, sec Resolver, env Resolver) (Resolved, error) {
	cp := cloneForResolve(c)
	var allUnresolved []string
	walkSecretFields(&cp, func(_ secretFieldLoc, s string) string {
		v, u, _ := SubstituteRefs(s, sec, env)
		allUnresolved = append(allUnresolved, u...)
		return v
	})
	if len(allUnresolved) > 0 {
		return Resolved{}, fmt.Errorf("unresolved secret references: %s", strings.Join(allUnresolved, ", "))
	}
	return Resolved{c: cp}, nil
}

package secrets

import (
	"fmt"
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// SubstituteCanonical walks every secret-bearing string field of the canonical
// model (see walkSecretFields for the authoritative set) and resolves any
// ${secret:...} / ${env:...} reference in-place, using sec as the secrets
// backend and env as the environment resolver.
//
// If any reference cannot be resolved, it returns an error listing all
// unresolved markers so the user knows exactly which secret is missing. Apply
// is blocked — never silent about missing secrets.
func SubstituteCanonical(c *source.Canonical, sec Resolver, env Resolver) error {
	var allUnresolved []string
	walkSecretFields(c, func(_ secretFieldLoc, s string) string {
		v, u, _ := SubstituteRefs(s, sec, env)
		allUnresolved = append(allUnresolved, u...)
		return v
	})
	if len(allUnresolved) > 0 {
		return fmt.Errorf("unresolved secret references: %s", strings.Join(allUnresolved, ", "))
	}
	return nil
}

//go:build leakfixture

// This file is compiled ONLY under -tags=leakfixture, exclusively by
// TestResolvedIsNotWritableToSource. It is the executable proof of the Part C
// type wall: each statement below MUST fail to compile, because a
// secrets.Resolved (the resolved apply model) is deliberately not assignable to
// anything on the dest->source write path. If this file ever compiles, the wall
// has a hole and a future capture path could leak resolved cleartext into
// ~/.agentsync — exactly the leak the refactor makes unrepresentable.
package capture

import (
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

func leakFixture(home string, r secrets.Resolved) {
	// A source writer takes the templated source.MCPServer — never a Resolved.
	_ = source.WriteMCP(home, "x", r)
	// Capture takes the templated *source.Canonical — never a Resolved.
	_, _ = Capture(home, r, Opts{})
}

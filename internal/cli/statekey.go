package cli

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/state"
)

// stateFileKey builds the state Files map key for a destination. It exists only
// to convert adapter.Scope to the plain string internal/state takes (state sits
// below adapter in the package layering); the format itself is owned by
// state.Key, so this can no longer drift from render.RecordOpsState.
func stateFileKey(userHome, agent string, sc adapter.Scope, projectRoot, path string) state.Key {
	return state.NewFileKey(userHome, agent, sc.String(), projectRoot, path)
}

// stateKeyKey builds the state Keys map key for one JSON pointer inside a
// shared destination file. Same conversion-only role as stateFileKey.
func stateKeyKey(userHome, agent string, sc adapter.Scope, projectRoot, path, ptr string) state.Key {
	return state.NewPointerKey(userHome, agent, sc.String(), projectRoot, path, ptr)
}

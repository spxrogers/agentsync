// Package paths centralizes filesystem path resolution honoring AGENTSYNC_HOME
// and AGENTSYNC_TARGET_ROOT. Production code MUST use this package; lint forbids
// os.UserHomeDir in *_test.go files.
package paths

import (
	"os"
	"path/filepath"
)

// Env abstracts environment-variable lookup so tests can inject a fake.
type Env interface {
	Get(key string) string
}

// OSEnv reads the live process environment.
type OSEnv struct{}

func (OSEnv) Get(key string) string { return os.Getenv(key) }

// MapEnv is a fake Env backed by a map (for tests).
type MapEnv map[string]string

func (m MapEnv) Get(key string) string { return m[key] }

// HomeDir returns the effective home dir. AGENTSYNC_TARGET_ROOT takes precedence
// (used by tests to redirect away from the real $HOME); otherwise falls back to
// $HOME.
func HomeDir(e Env) string {
	if root := e.Get("AGENTSYNC_TARGET_ROOT"); root != "" {
		return root
	}
	return e.Get("HOME")
}

// AgentsyncHome returns the directory where agentsync stores its source repo.
// Resolution order:
//  1. $AGENTSYNC_HOME (explicit override; absolute path)
//  2. <HomeDir>/.agentsync
func AgentsyncHome(e Env) string {
	if h := e.Get("AGENTSYNC_HOME"); h != "" {
		return h
	}
	return filepath.Join(HomeDir(e), ".agentsync")
}

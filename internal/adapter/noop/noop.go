// Package noop provides a do-nothing Adapter used as a registry/test placeholder:
// it Renders no FileOps, Ingests an empty canonical, and its Apply is a true no-op
// (it never writes, even when handed FileOps). It exists so tests can register a
// named adapter without wiring a real one, and so code paths that fan out over the
// registry have a trivial implementation to exercise. It is NOT part of the shipped
// adapter set (the real 31-agent registry has no noop rows) — keep it a pure no-op.
package noop

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// defaultName is used when New is called with an empty name, so a noop adapter
// always has a non-empty Name() (the registry keys adapters by name).
const defaultName = "noop"

type Adapter struct {
	AdapterName string // overridable for tests
}

// New constructs a noop adapter named name. An empty name is clamped to
// defaultName so the adapter always reports a usable, non-empty Name().
func New(name string) *Adapter {
	if name == "" {
		name = defaultName
	}
	return &Adapter{AdapterName: name}
}

func (a *Adapter) Name() string { return a.AdapterName }

// Detect always reports present. noop is a TEST-ONLY sentinel that is never in the
// shipped registry, so this is dead in production; returning true lets a test that
// registers a noop adapter see it as "detected" via `doctor` without extra setup.
// (Detect is informational only — it never gates apply.) The behavior is pinned by
// TestNoop_Detect so a future change is a deliberate, visible one.
func (a *Adapter) Detect() (bool, error) { return true, nil }

func (a *Adapter) Render(secrets.Resolved, adapter.Scope, string) ([]adapter.FileOp, []adapter.Skip, error) {
	return nil, nil, nil
}

func (a *Adapter) Ingest(adapter.Scope, string) (source.Canonical, error) {
	return source.Canonical{}, nil
}

// Apply is a true no-op: it ignores every op and writes nothing (it never touches
// the DestWriter), which is the load-bearing property TestNoop_ApplyIgnoresOps pins.
func (a *Adapter) Apply([]adapter.FileOp, adapter.DestWriter) error { return nil }

func (a *Adapter) KeyMergeStrategy() string { return "" }

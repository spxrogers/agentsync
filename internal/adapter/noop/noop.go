// Package noop provides a NoopAdapter that always Detect()s true, Renders no
// FileOps, and Apply()s nothing. Used as a registry placeholder in tests and
// as the default adapter set in M0 before M1+ adds real ones.
package noop

import (
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

type Adapter struct {
	AdapterName string // overridable for tests
}

func New(name string) *Adapter { return &Adapter{AdapterName: name} }

func (a *Adapter) Name() string                     { return a.AdapterName }
func (a *Adapter) Capabilities() adapter.Capability { return 0 }
func (a *Adapter) Detect() (bool, error)            { return true, nil }
func (a *Adapter) Render(source.Canonical, adapter.Scope, string) ([]adapter.FileOp, []adapter.Skip, error) {
	return nil, nil, nil
}

func (a *Adapter) Ingest(adapter.Scope, string) (source.Canonical, error) {
	return source.Canonical{}, nil
}
func (a *Adapter) Apply([]adapter.FileOp, adapter.DestWriter) error { return nil }
func (a *Adapter) KeyMergeStrategy() string                         { return "" }

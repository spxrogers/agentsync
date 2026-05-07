package adapter_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

type stub struct{ name string }

func (s stub) Name() string { return s.name }

func (stub) Capabilities() adapter.Capability { return 0 }

func (stub) Detect() (bool, error) { return false, nil }

func (stub) Render(source.Canonical, adapter.Scope, string) ([]adapter.FileOp, []adapter.Skip, error) {
	return nil, nil, nil
}

func (stub) Ingest(adapter.Scope, string) (source.Canonical, error) { return source.Canonical{}, nil }
func (stub) Apply([]adapter.FileOp, adapter.DestWriter) error       { return nil }

func TestRegistry_RegisterLookup(t *testing.T) {
	r := adapter.NewRegistry()
	if err := r.Register(stub{name: "x"}); err != nil {
		t.Fatal(err)
	}
	if r.Lookup("x") == nil {
		t.Fatalf("lookup x = nil")
	}
	if r.Lookup("missing") != nil {
		t.Fatalf("lookup missing should be nil")
	}
}

func TestRegistry_DuplicateError(t *testing.T) {
	r := adapter.NewRegistry()
	_ = r.Register(stub{name: "x"})
	if err := r.Register(stub{name: "x"}); err == nil {
		t.Fatal("expected duplicate-register error")
	}
}

func TestRegistry_NamesSorted(t *testing.T) {
	r := adapter.NewRegistry()
	_ = r.Register(stub{name: "c"})
	_ = r.Register(stub{name: "a"})
	_ = r.Register(stub{name: "b"})
	names := r.Names()
	if names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Fatalf("names not sorted: %v", names)
	}
}

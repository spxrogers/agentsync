package noop_test

import (
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/noop"
	"github.com/spxrogers/agentsync/internal/source"
)

func TestNoop_Implements(t *testing.T) {
	var _ adapter.Adapter = noop.New("test")
}

func TestNoop_Render(t *testing.T) {
	a := noop.New("test")
	ops, skips, err := a.Render(source.Canonical{}, adapter.ScopeUser, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 0 || len(skips) != 0 {
		t.Fatalf("noop render returned %d ops, %d skips", len(ops), len(skips))
	}
}

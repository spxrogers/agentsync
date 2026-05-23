package state

import (
	"strings"
	"testing"
)

func TestMigrate_LegacyZeroBecomesCurrent(t *testing.T) {
	tt := &Targets{SchemaVersion: 0}
	if err := migrate(tt); err != nil {
		t.Fatalf("legacy zero should migrate cleanly: %v", err)
	}
	if tt.SchemaVersion != SchemaVersion {
		t.Errorf("schema not stamped: got %d want %d", tt.SchemaVersion, SchemaVersion)
	}
}

func TestMigrate_CurrentIsNoop(t *testing.T) {
	tt := &Targets{SchemaVersion: SchemaVersion}
	if err := migrate(tt); err != nil {
		t.Fatalf("current schema should be no-op: %v", err)
	}
}

func TestMigrate_FutureRejected(t *testing.T) {
	tt := &Targets{SchemaVersion: SchemaVersion + 1}
	err := migrate(tt)
	if err == nil {
		t.Fatal("future schema should be rejected")
	}
	if !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Errorf("error should explain upgrade path; got %q", err)
	}
}

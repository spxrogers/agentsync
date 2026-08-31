package state

import (
	"strings"
	"testing"
)

func TestMigrate_LegacyZeroBecomesCurrent(t *testing.T) {
	got, err := migrate(&rawTargets{
		SchemaVersion: 0,
		Files:         map[string]FileEntry{"claude:user::x": {SHA256: "a"}},
	})
	if err != nil {
		t.Fatalf("legacy zero should migrate cleanly: %v", err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("schema not stamped: got %d want %d", got.SchemaVersion, SchemaVersion)
	}
	want := Key{Agent: "claude", Scope: "user", Path: "x"}
	if entry, ok := got.Files[want]; !ok || entry.SHA256 != "a" {
		t.Errorf("legacy key not re-keyed: have %+v", got.Files)
	}
}

func TestMigrate_CurrentIsNoop(t *testing.T) {
	k := Key{Agent: "claude", Scope: "user", Path: "x"}
	got, err := migrate(&rawTargets{
		SchemaVersion: SchemaVersion,
		Files:         map[string]FileEntry{k.String(): {SHA256: "a"}},
	})
	if err != nil {
		t.Fatalf("current schema should be no-op: %v", err)
	}
	if entry, ok := got.Files[k]; !ok || entry.SHA256 != "a" {
		t.Errorf("current-schema key was not preserved: have %+v", got.Files)
	}
}

func TestMigrate_FutureRejected(t *testing.T) {
	_, err := migrate(&rawTargets{SchemaVersion: SchemaVersion + 1})
	if err == nil {
		t.Fatal("future schema should be rejected")
	}
	if !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Errorf("error should explain upgrade path; got %q", err)
	}
}

// TestMigrate_UnreadableKeysRefuseWithRemedy pins the refusal legacy.go's
// errAmbiguousLegacyKey doc comment points at ("See migrate for the user-facing
// remedy"). Both kinds of unreadable v1 key stop the load and carry the cheap
// delete-and-re-adopt remedy; only a genuinely AMBIGUOUS one — several valid
// readings, possible solely when a project root or dest path contains ':' —
// additionally explains why agentsync will not pick one, since that is the only
// case where guessing was even an option.
func TestMigrate_UnreadableKeysRefuseWithRemedy(t *testing.T) {
	const ambiguityNote = "refuses to guess the project/path split"
	tests := []struct {
		name          string
		raw           *rawTargets
		wantSubstrs   []string
		wantAmbiguity bool
	}{
		{
			name: "unparseable v1 key refuses with the base remedy only",
			raw: &rawTargets{
				SchemaVersion: 1,
				Files:         map[string]FileEntry{"no-scope-field": {SHA256: "a"}},
			},
			wantSubstrs: []string{
				"1 state key(s) could not be read at schema_version=1",
				"no-scope-field",
				"targets.json is bookkeeping only",
				"re-adopts each destination",
			},
		},
		{
			name: "ambiguous v1 key also explains why nothing is guessed",
			raw: &rawTargets{
				SchemaVersion: 1,
				Files:         map[string]FileEntry{"claude:project:${HOME}/a:${HOME}/b:${HOME}/c": {SHA256: "a"}},
			},
			wantSubstrs: []string{
				"claude:project:${HOME}/a:${HOME}/b:${HOME}/c",
				"has 2 readings",
				"targets.json is bookkeeping only",
			},
			wantAmbiguity: true,
		},
		{
			name: "a v2 key that does not decode refuses the same way",
			raw: &rawTargets{
				SchemaVersion: SchemaVersion,
				Keys:          map[string]KeyEntry{"as1|6:claude|4:user": {SHA256: "a"}},
			},
			wantSubstrs: []string{
				"could not be read at schema_version=2",
				"targets.json is bookkeeping only",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := migrate(tc.raw)
			if err == nil {
				t.Fatalf("an unreadable key must refuse the whole load; got %+v", got)
			}
			for _, want := range tc.wantSubstrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal must mention %q; got %q", want, err)
				}
			}
			if hasNote := strings.Contains(err.Error(), ambiguityNote); hasNote != tc.wantAmbiguity {
				t.Errorf("ambiguity remedy present = %v, want %v; got %q", hasNote, tc.wantAmbiguity, err)
			}
		})
	}
}

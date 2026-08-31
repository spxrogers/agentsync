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
	ptr := Key{Agent: "claude", Scope: "user", Path: "y", Pointer: "/mcpServers/gh"}
	got, err := migrate(&rawTargets{
		SchemaVersion: SchemaVersion,
		Files:         map[string]FileEntry{k.String(): {SHA256: "a"}},
		Keys:          map[string]KeyEntry{ptr.String(): {SHA256: "b"}},
	})
	if err != nil {
		t.Fatalf("current schema should be no-op: %v", err)
	}
	if entry, ok := got.Files[k]; !ok || entry.SHA256 != "a" {
		t.Errorf("current-schema key was not preserved: have %+v", got.Files)
	}
	// The role check parseCurrentKey adds must not reject a correctly-filed
	// pointer entry — pin the accepting half beside the two refusals below.
	if entry, ok := got.Keys[ptr]; !ok || entry.SHA256 != "b" {
		t.Errorf("current-schema pointer key was not preserved: have %+v", got.Keys)
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
		// wantKeyOnce, when set, must appear EXACTLY once in the refusal. Every
		// error the key parsers return already names the key with %q, so the
		// message used to print it twice (three times when the error also quoted
		// the offending field). See migrate's note closure.
		wantKeyOnce string
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
			wantKeyOnce: "no-scope-field",
		},
		{
			name: "ambiguous v1 key also explains why nothing is guessed",
			raw: &rawTargets{
				SchemaVersion: 1,
				Files:         map[string]FileEntry{"claude:project:${HOME}/a:${HOME}/b:${HOME}/c": {SHA256: "a"}},
			},
			wantSubstrs: []string{
				"claude:project:${HOME}/a:${HOME}/b:${HOME}/c",
				"(2 readings)",
				"targets.json is bookkeeping only",
			},
			wantAmbiguity: true,
			wantKeyOnce:   "claude:project:${HOME}/a:${HOME}/b:${HOME}/c",
		},
		{
			// Containment narrowed three candidate splits to two but could not
			// settle them. The count must name what the reader still has to
			// disambiguate, not the raw candidate count — otherwise it sends them
			// looking for a split agentsync has already ruled out.
			name: "an ambiguity narrowed by containment reports the narrowed count",
			raw: &rawTargets{
				SchemaVersion: 1,
				Files:         map[string]FileEntry{"claude:project:/a:/a/p:/a:/a/p/q": {SHA256: "a"}},
			},
			wantSubstrs: []string{
				"claude:project:/a:/a/p:/a:/a/p/q",
				"(2 readings)",
			},
			wantAmbiguity: true,
		},
		{
			// The key is printed Go-quoted, which DOUBLES a backslash — a
			// Windows-shaped key pasted straight from this message into an editor
			// search of targets.json finds nothing. Say so rather than leave the
			// reader to work it out.
			name: "a backslash-bearing key is quoted and the remedy says so",
			raw: &rawTargets{
				SchemaVersion: 1,
				Files:         map[string]FileEntry{`claude:project:C:\dev\repo:C:\other\x.md`: {SHA256: "a"}},
			},
			wantSubstrs: []string{
				`C:\\dev\\repo`, // %q doubled the separators
				"shown Go-quoted",
				"unquote it before searching targets.json",
			},
			wantAmbiguity: true,
		},
		{
			// A key whose candidate readings exceed maxLegacyReadings is refused
			// without enumerating them; parseLegacyKey's enumeration is a product
			// and the containment tie-break walks its result, so an uncapped key
			// of a few KB could OOM-kill the process (see maxLegacyReadings).
			name: "a key past the reading cap is refused as ambiguous",
			raw: &rawTargets{
				SchemaVersion: 1,
				Files:         map[string]FileEntry{"claude:project:" + strings.Repeat(":/", 200): {SHA256: "a"}},
			},
			wantSubstrs: []string{
				"more than 64 candidate project/path splits",
				"targets.json is bookkeeping only",
			},
			wantAmbiguity: true,
		},
		{
			// v1 distinguishes the two maps structurally, so v2 must too: without
			// the role check a hand-edited file could put a whole-file key in
			// "keys" (it would reach jsonkeys.MergeKeys as the pointer "") or a
			// pointered key in "files", and load silently.
			name: "a v2 pointered key filed under files is refused",
			raw: &rawTargets{
				SchemaVersion: SchemaVersion,
				Files: map[string]FileEntry{
					Key{Agent: "claude", Scope: "user", Path: "x", Pointer: "/mcpServers/gh"}.String(): {SHA256: "a"},
				},
			},
			wantSubstrs: []string{
				"must not carry a JSON pointer",
				`"/mcpServers/gh"`,
				"targets.json is bookkeeping only",
			},
		},
		{
			name: "a v2 whole-file key filed under keys is refused",
			raw: &rawTargets{
				SchemaVersion: SchemaVersion,
				Keys: map[string]KeyEntry{
					Key{Agent: "claude", Scope: "user", Path: "x"}.String(): {SHA256: "a"},
				},
			},
			wantSubstrs: []string{
				"must name a JSON pointer",
				"targets.json is bookkeeping only",
			},
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
			if tc.wantKeyOnce != "" {
				if n := strings.Count(err.Error(), tc.wantKeyOnce); n != 1 {
					t.Errorf("refusal names %q %d times, want exactly 1; got %q", tc.wantKeyOnce, n, err)
				}
			}
		})
	}
}

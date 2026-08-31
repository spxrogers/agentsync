package state_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/state"
)

// TestLoad_RefusesUnreadableKeys pins the fail-closed half of the schema-2
// migration. A v1 key with more than one possible reading must NOT be guessed:
// a wrong project/path split records the entry under the wrong project, which
// is the over-disown bug the typed key exists to remove. A malformed key in a
// file that already claims schema_version 2 means a hand-edit or corruption and
// is refused for the same reason.
func TestLoad_RefusesUnreadableKeys(t *testing.T) {
	// The ambiguity-specific half of the remedy. Asserting only the key name and
	// the shared "bookkeeping only" line would pass for ANY refusal, so this file
	// — the one named for the refusals — could not tell an ambiguous key from a
	// plainly unparseable one. Each row pins which of the two it is.
	const ambiguityNote = "refuses to guess the project/path split"
	tests := []struct {
		name          string
		content       string
		wantInError   string
		wantAmbiguity bool
	}{
		{
			name: "ambiguous legacy files key",
			content: `{"schema_version":1,"files":{` +
				`"claude:project:${HOME}/a:${HOME}/b:${HOME}/c":{"sha256":"x"}}}`,
			wantInError:   "claude:project:${HOME}/a:${HOME}/b:${HOME}/c",
			wantAmbiguity: true,
		},
		{
			name: "ambiguous legacy keys key",
			content: `{"schema_version":1,"keys":{` +
				`"claude:user::a:/b:/c":{"sha256":"x"}}}`,
			wantInError:   "claude:user::a:/b:/c",
			wantAmbiguity: true,
		},
		{
			name:        "malformed key in a schema-2 file",
			content:     `{"schema_version":2,"files":{"claude:user::x":{"sha256":"x"}}}`,
			wantInError: "claude:user::x",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "targets.json")
			if err := os.WriteFile(p, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := state.Load(p)
			if err == nil {
				t.Fatal("Load must refuse a key it cannot read unambiguously")
			}
			if !strings.Contains(err.Error(), tc.wantInError) {
				t.Fatalf("error must name the offending key %q; got %q", tc.wantInError, err)
			}
			if !strings.Contains(err.Error(), "bookkeeping only") {
				t.Fatalf("error must state the remedy; got %q", err)
			}
			if got := strings.Contains(err.Error(), ambiguityNote); got != tc.wantAmbiguity {
				t.Fatalf("ambiguity-specific remedy present = %v, want %v; got %q",
					got, tc.wantAmbiguity, err)
			}
		})
	}
}

// TestLoad_RefusalEscapesUntrustedKeyBytes pins that a state key — read
// VERBATIM from the hand-editable targets.json — cannot carry terminal control
// bytes into the refusal message.
//
// The refusal is always MULTI-LINE, and ui.WarnWriter passes lines 2..n through
// unsanitized, so a raw ESC/CR in a key reaches the terminal as a control
// sequence at several sinks (opencode ingest's warn, import's warnf, doctor).
// A raw newline is worse than a control byte even at a SANITIZING sink:
// sanitization preserves newlines, so the key could forge whole extra output
// lines. migrate therefore quotes the key at the SOURCE (%q), which escapes all
// three, rather than relying on every present and future sink to do it.
func TestLoad_RefusalEscapesUntrustedKeyBytes(t *testing.T) {
	const hostile = "\x1b[2KSPOOFED\r\nevil"
	// Same shape, no control bytes: the structural newline count of the refusal
	// must not depend on the key's content.
	const benign = "x[2KSPOOFEDxxevil"

	load := func(t *testing.T, key string) error {
		t.Helper()
		enc, err := json.Marshal(key)
		if err != nil {
			t.Fatal(err)
		}
		doc := `{"schema_version":2,"files":{` + string(enc) + `:{"sha256":"x"}}}`
		p := filepath.Join(t.TempDir(), "targets.json")
		if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
		lerr := func() error { _, e := state.Load(p); return e }()
		if lerr == nil {
			t.Fatalf("Load must refuse the unreadable key %q", key)
		}
		return lerr
	}

	got := load(t, hostile).Error()
	for _, raw := range []struct{ name, b string }{
		{"ESC", "\x1b"},
		{"CR", "\r"},
	} {
		if strings.Contains(got, raw.b) {
			t.Fatalf("refusal leaked a raw %s byte from the state key: %q", raw.name, got)
		}
	}
	for _, want := range []string{`\x1b`, `\r`, `\n`} {
		if !strings.Contains(got, want) {
			t.Fatalf("refusal must escape the key (missing %s); got %q", want, got)
		}
	}
	// The key must not be able to forge output lines: the refusal for a hostile
	// key has exactly as many real newlines as the refusal for a benign one.
	if h, b := strings.Count(got, "\n"), strings.Count(load(t, benign).Error(), "\n"); h != b {
		t.Fatalf("state key forged %d extra output line(s): hostile=%d newlines, benign=%d\n%q", h-b, h, b, got)
	}
}

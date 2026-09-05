package render_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestWrite_ChmodReconverges is the regression for issue #162 item D: the
// convergence short-circuit must still re-converge a content-identical file
// whose MODE drifted (an executable skill script that lost its +x bit), and
// report that as a change — while a genuinely-unchanged file+mode stays a no-op
// (no mtime churn).
func TestWrite_ChmodReconverges(t *testing.T) {
	testenv.RequireContainer(t)
	home := t.TempDir()
	dest := filepath.Join(t.TempDir(), "run.sh")
	content := []byte("#!/bin/sh\necho hi\n")
	op := adapter.FileOp{Action: adapter.ActionWrite, Path: dest, Content: content, Mode: 0o755}
	st := state.New()

	// Initial write establishes content + 0755.
	w1 := render.NewWriter(st, home, home, adapter.ScopeUser, "", "claude")
	if err := w1.Write(op, content); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o755 {
		t.Fatalf("initial mode = %v, want 0755", fi.Mode().Perm())
	}

	// A genuine no-op (same content + mode) must NOT churn: reported unchanged.
	wNoop := render.NewWriter(st, home, home, adapter.ScopeUser, "", "claude")
	if err := wNoop.Write(op, content); err != nil {
		t.Fatalf("no-op write: %v", err)
	}
	if !wNoop.Unchanged()[dest] {
		t.Fatalf("genuine no-op (content+mode identical) must be reported unchanged")
	}

	// Now strip the +x bit WITHOUT changing content.
	if err := os.Chmod(dest, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	w2 := render.NewWriter(st, home, home, adapter.ScopeUser, "", "claude")
	if err := w2.Write(op, content); err != nil {
		t.Fatalf("reconverge write: %v", err)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode not re-converged: %v, want 0755", fi.Mode().Perm())
	}
	if w2.Unchanged()[dest] {
		t.Fatalf("a content-identical mode change must NOT be reported unchanged")
	}
	if !w2.Wrote()[dest] {
		t.Fatalf("a content-identical mode change must be reported as written")
	}
}

// TestRecordOpsState_MergeTomlNumericNoFalseDrift pins issue #162 item F after
// the review-loop correction: a merge-toml-keys owned value whose leaf is numeric
// (e.g. a codex MCP `timeout` — a routine passthrough Extra field) records
// WITHOUT error. A hard guard here used to FAIL apply on exactly this ordinary
// config; it was removed because an in-range integer hashes identically as the
// JSON source and the TOML dest, so there is no false drift. The only residual —
// an integer > 2^53 or a TOML datetime, unreachable with real configs — is
// documented at the record site, not guarded, so apply never fails on a number.
func TestRecordOpsState_MergeTomlNumericNoFalseDrift(t *testing.T) {
	testenv.RequireContainer(t)
	cases := []struct {
		name    string
		dest    string // on-disk TOML dest content
		ours    string // op.Content (JSON)
		wantErr string // "" = expect success
		wantPtr string // the single owned pointer that must be recorded
	}{
		{
			name:    "numeric owned value records cleanly (no apply failure)",
			dest:    "[mcp_servers.github]\ntimeout = 30\n",
			ours:    `{"mcp_servers":{"github":{"timeout":30}}}`,
			wantErr: "",
			wantPtr: "/mcp_servers/github",
		},
		{
			name:    "string owned value records cleanly",
			dest:    "[mcp_servers.github]\ncommand = \"npx\"\n",
			ours:    `{"mcp_servers":{"github":{"command":"npx"}}}`,
			wantErr: "",
			wantPtr: "/mcp_servers/github",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			dest := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(dest, []byte(tc.dest), 0o644); err != nil {
				t.Fatal(err)
			}
			st := state.New()
			op := adapter.FileOp{
				Action:        adapter.ActionWrite,
				Path:          dest,
				Content:       []byte(tc.ours),
				MergeStrategy: "merge-toml-keys",
				SourceID:      "mcp/github.toml",
			}
			err := render.RecordOpsState(st, home, "codex", adapter.ScopeUser, "", []adapter.FileOp{op})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("owned value should record cleanly, got: %v", err)
			}
			// Exactly one owned pointer recorded, and it is the RIGHT one — proving
			// the value landed under wantPtr, not merely that "a" key exists.
			if len(st.Keys) != 1 {
				t.Fatalf("want exactly one recorded key, got %d: %v", len(st.Keys), st.Keys)
			}
			var gotKey state.Key
			var gotEntry state.KeyEntry
			for k, e := range st.Keys {
				gotKey, gotEntry = k, e
			}
			if gotKey.Pointer != tc.wantPtr {
				t.Errorf("recorded key %+v does not carry pointer %q", gotKey, tc.wantPtr)
			}
			// No false drift (the actual #162 bug class): the recorded hash is taken
			// from the TOML-decoded dest value, while status/diff hash the JSON-decoded
			// SOURCE value for the same pointer. For a value that decodes equivalently
			// in both (an in-range int, a string), those two hashes MUST match, or the
			// 3-way drift classifier reports perpetual drift no apply can converge. The
			// removed assertHashStableTOMLValue guard worried about exactly this —
			// assert it directly instead of merely asserting "recorded without error".
			if want := jsonPointerHash(t, tc.ours, tc.wantPtr); gotEntry.SHA256 != want {
				t.Errorf("recorded hash %s != source-side hash %s — %s would false-drift on the next status",
					gotEntry.SHA256, want, tc.wantPtr)
			}
		})
	}
}

// jsonPointerHash replicates RecordOpsState's owned-key hashing
// (hex(sha256(json.Marshal(v)))) over the JSON-decoded SOURCE value at ptr — i.e.
// exactly what status/diff hash for the source side. RecordOpsState hashes the
// TOML-decoded DEST value the same way; the two must agree for a shape-stable
// scalar, which is what the caller asserts.
func jsonPointerHash(t *testing.T, jsonStr, ptr string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		t.Fatalf("unmarshal source %q: %v", jsonStr, err)
	}
	cur := any(m)
	for _, seg := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("pointer %s: no object at segment %q", ptr, seg)
		}
		cur = obj[seg]
	}
	b, err := json.Marshal(cur)
	if err != nil {
		t.Fatalf("marshal value at %s: %v", ptr, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

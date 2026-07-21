package adapter_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// recordingWriter records Write/Delete calls (by op.Path) and can fail Delete.
type recordingWriter struct {
	writes  []string // paths passed to Write (unused by DispatchOps directly; here for completeness)
	deletes []string // paths passed to Delete
	delErr  error
}

func (w *recordingWriter) Write(op adapter.FileOp, _ []byte) error {
	w.writes = append(w.writes, op.Path)
	return nil
}

func (w *recordingWriter) Delete(op adapter.FileOp) error {
	w.deletes = append(w.deletes, op.Path)
	return w.delErr
}

func TestDispatchOps(t *testing.T) {
	tests := []struct {
		name       string
		ops        []adapter.FileOp
		delErr     error
		writeErr   error
		wantWrites []string // op.Paths handed to the write closure
		wantDelete []string // op.Paths handed to w.Delete
		wantErr    string   // substring of the returned error; "" means nil
	}{
		{
			name:       "write action calls the write closure",
			ops:        []adapter.FileOp{{Action: "write", Path: "a"}},
			wantWrites: []string{"a"},
		},
		{
			name:       "empty action is treated as write",
			ops:        []adapter.FileOp{{Action: "", Path: "b"}},
			wantWrites: []string{"b"},
		},
		{
			name:       "delete action calls w.Delete",
			ops:        []adapter.FileOp{{Action: "delete", Path: "c"}},
			wantDelete: []string{"c"},
		},
		{
			name:    "unknown action errors",
			ops:     []adapter.FileOp{{Action: "frob", Path: "d"}},
			wantErr: `unknown action "frob"`,
		},
		{
			name:       "delete error is wrapped with path",
			ops:        []adapter.FileOp{{Action: "delete", Path: "e"}},
			delErr:     errors.New("boom"),
			wantDelete: []string{"e"},
			wantErr:    "delete e: boom",
		},
		{
			name:       "write closure error propagates verbatim",
			ops:        []adapter.FileOp{{Action: "write", Path: "f"}},
			writeErr:   errors.New("write-boom"),
			wantWrites: []string{"f"},
			wantErr:    "write-boom",
		},
		{
			name: "mixed sequence in order",
			ops: []adapter.FileOp{
				{Action: "write", Path: "w1"},
				{Action: "delete", Path: "d1"},
				{Action: "", Path: "w2"},
			},
			wantWrites: []string{"w1", "w2"},
			wantDelete: []string{"d1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &recordingWriter{delErr: tt.delErr}
			var writes []string
			writeOp := func(op adapter.FileOp) error {
				writes = append(writes, op.Path)
				return tt.writeErr
			}

			err := adapter.DispatchOps(tt.ops, w, writeOp)

			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
			if tt.name == "write closure error propagates verbatim" && !errors.Is(err, tt.writeErr) {
				t.Fatalf("write closure error not returned verbatim: %v", err)
			}
			if !slices.Equal(writes, tt.wantWrites) {
				t.Errorf("write closure calls = %v, want %v", writes, tt.wantWrites)
			}
			if !slices.Equal(w.deletes, tt.wantDelete) {
				t.Errorf("Delete calls = %v, want %v", w.deletes, tt.wantDelete)
			}
		})
	}
}

func TestNames_Sorted(t *testing.T) {
	tests := []struct {
		name  string
		order []string
		want  []string
	}{
		{name: "out of order", order: []string{"c", "a", "b"}, want: []string{"a", "b", "c"}},
		{name: "single element", order: []string{"only"}, want: []string{"only"}},
		{name: "empty", order: nil, want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := adapter.NewRegistry()
			for _, n := range tt.order {
				if err := r.Register(stub{name: n}); err != nil {
					t.Fatal(err)
				}
			}
			got := r.Names()
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Names() = %v, want %v", got, tt.want)
			}
		})
	}
}

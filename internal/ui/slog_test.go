package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func handlerBuf(t *testing.T, level slog.Leveler) (*bytes.Buffer, *slog.Logger) {
	t.Helper()
	var buf bytes.Buffer
	p := New(&buf, &buf, ColorNever)
	return &buf, slog.New(NewSlogHandler(&buf, p, level))
}

// The regression from #211: a library-side slog.Warn used to print through the
// stdlib default handler as `2026/07/28 15:03:45 WARN <msg> path=<p>` — a
// timestamp nothing else in the CLI carried, and a shape that made a fatal
// error one line below it indistinguishable. It must now render exactly like a
// p.Warnf, with attributes hanging in the message column.
func TestSlogHandlerRendersAsDiagnostic(t *testing.T) {
	buf, lg := handlerBuf(t, slog.LevelInfo)
	lg.Warn("plugin component frontmatter is not strict YAML; parsed leniently",
		"path", "/home/me/.agentsync/.state/cache/plugins/x/agents/y.md")

	want := "⚠ WARN   plugin component frontmatter is not strict YAML; parsed leniently\n" +
		diagIndent + "path=/home/me/.agentsync/.state/cache/plugins/x/agents/y.md\n"
	if got := buf.String(); got != want {
		t.Fatalf("slog warning render:\ngot:  %q\nwant: %q", got, want)
	}
}

// No wall-clock timestamp, ever. This is the single most visible thing that was
// wrong with the pre-existing output, and a handler rewrite could reintroduce
// it without any other assertion noticing.
func TestSlogHandlerDropsTimestamp(t *testing.T) {
	buf, lg := handlerBuf(t, slog.LevelInfo)
	lg.Info("hello")
	if !strings.HasPrefix(buf.String(), "ℹ INFO") {
		t.Fatalf("line must start with the level label, got: %q", buf.String())
	}
}

func TestSlogHandlerLevelMapping(t *testing.T) {
	tests := []struct {
		name  string
		level slog.Level
		want  string
	}{
		{"debug", slog.LevelDebug, "• DEBUG"},
		{"info", slog.LevelInfo, "ℹ INFO"},
		{"warn", slog.LevelWarn, "⚠ WARN"},
		{"error", slog.LevelError, "✗ ERROR"},
		// slog levels are an open integer scale: a custom level between two
		// named ones must land on the nearest label BELOW it, never be dropped.
		{"between info and warn", slog.LevelInfo + 2, "ℹ INFO"},
		{"above error", slog.LevelError + 4, "✗ ERROR"},
		{"below debug", slog.LevelDebug - 4, "• DEBUG"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf, lg := handlerBuf(t, slog.LevelDebug-8)
			lg.Log(context.Background(), tc.level, "msg")
			if !strings.HasPrefix(buf.String(), tc.want) {
				t.Fatalf("level %v rendered as %q, want prefix %q", tc.level, buf.String(), tc.want)
			}
		})
	}
}

func TestSlogHandlerEnabledGatesByLevel(t *testing.T) {
	buf, lg := handlerBuf(t, slog.LevelWarn)
	lg.Info("suppressed")
	lg.Warn("shown")
	got := buf.String()
	if strings.Contains(got, "suppressed") {
		t.Fatalf("below-threshold record was emitted: %q", got)
	}
	if !strings.Contains(got, "shown") {
		t.Fatalf("at-threshold record was dropped: %q", got)
	}
}

// WithAttrs/WithGroup must compose without one derived handler clobbering
// another — the classic append-shares-backing-array bug. Two loggers derived
// from the same parent must not see each other's attributes.
func TestSlogHandlerDerivedHandlersAreIndependent(t *testing.T) {
	var buf bytes.Buffer
	p := New(&buf, &buf, ColorNever)
	base := slog.New(NewSlogHandler(&buf, p, slog.LevelInfo)).With("shared", "1")

	a := base.With("only", "a")
	b := base.With("only", "b")

	buf.Reset()
	a.Info("x")
	gotA := buf.String()
	buf.Reset()
	b.Info("x")
	gotB := buf.String()

	if !strings.Contains(gotA, "only=a") || strings.Contains(gotA, "only=b") {
		t.Fatalf("handler a saw the wrong attrs: %q", gotA)
	}
	if !strings.Contains(gotB, "only=b") || strings.Contains(gotB, "only=a") {
		t.Fatalf("handler b saw the wrong attrs: %q", gotB)
	}
	if !strings.Contains(gotA, "shared=1") || !strings.Contains(gotB, "shared=1") {
		t.Fatalf("both handlers should keep the parent attr; a=%q b=%q", gotA, gotB)
	}
}

func TestSlogHandlerGroupsQualifyKeys(t *testing.T) {
	buf, lg := handlerBuf(t, slog.LevelInfo)
	lg.WithGroup("plugin").With("id", "feature-dev").Info("msg", "sha", "abc")
	got := buf.String()
	if !strings.Contains(got, "plugin.id=feature-dev") {
		t.Fatalf("WithGroup did not qualify the WithAttrs key: %q", got)
	}
	if !strings.Contains(got, "plugin.sha=abc") {
		t.Fatalf("WithGroup did not qualify the record key: %q", got)
	}
}

// slog.Handler contract corners: an empty Attr is dropped, an empty group is
// elided key and all, and a nested group-valued Attr flattens with a dotted key.
func TestSlogHandlerAttrEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		attr slog.Attr
		want []string // substrings that must appear; empty means "no attr line"
	}{
		{"empty attr dropped", slog.Attr{}, nil},
		{"empty group elided", slog.Group("g"), nil},
		{"nested group flattened", slog.Group("outer", slog.String("k", "v")), []string{"outer.k=v"}},
		{"value with space is quoted", slog.String("k", "a b"), []string{`k="a b"`}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf, lg := handlerBuf(t, slog.LevelInfo)
			lg.Info("msg", tc.attr)
			lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
			if len(tc.want) == 0 {
				if len(lines) != 1 {
					t.Fatalf("expected no attribute line, got: %q", buf.String())
				}
				return
			}
			for _, w := range tc.want {
				if !strings.Contains(buf.String(), w) {
					t.Fatalf("missing %q in: %q", w, buf.String())
				}
			}
		})
	}
}

// slog records carry filesystem paths and error strings harvested from
// third-party agent config — untrusted text headed for a terminal. It must be
// sanitized on the way out, in both the message and the attributes (#93/#171).
func TestSlogHandlerSanitizesUntrustedText(t *testing.T) {
	buf, lg := handlerBuf(t, slog.LevelInfo)
	lg.Warn("skill \x1b[31mred\x1b[0m is odd", "path", "/tmp/\x1b]0;pwned\x07x")

	got := buf.String()
	if strings.Contains(got, "\x1b]") || strings.Contains(got, "\x07") {
		t.Fatalf("escape sequences survived into terminal output: %q", got)
	}
	// The ESC introducer is stripped; the inert parameter bytes remain, which
	// is Sanitize's documented behavior.
	if !strings.Contains(got, "[31mred") {
		t.Fatalf("message body was lost, not just neutralized: %q", got)
	}
}

// SlogHandler must be safe for concurrent use: slog.Handler implementations are
// contractually expected to be, callers reasonably share a *slog.Logger across
// goroutines, and the stdlib's own handlers lock their writer.
//
// Nothing exercised the mutex before this, so `-race` never saw the handler at
// all. Two things are asserted: no race (under -race), and no INTERLEAVING — a
// record renders as a label line plus an optional attribute line, and those two
// must stay adjacent or an attribute gets orphaned under someone else's warning.
func TestSlogHandlerIsConcurrencySafe(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	// A writer that records each Write as one unit, so an interleaved pair of
	// records would show up as a torn entry.
	rec := writeRecorder{mu: &mu, lines: &lines}

	p := New(io.Discard, io.Discard, ColorNever)
	lg := slog.New(NewSlogHandler(rec, p, slog.LevelInfo))

	const goroutines, each = 8, 25
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			// Derive per goroutine too, exercising the shared mutex through clone().
			sub := lg.With("g", g)
			for i := 0; i < each; i++ {
				sub.Warn("concurrent record", "i", i)
			}
		}(g)
	}
	wg.Wait()

	if len(lines) != goroutines*each {
		t.Fatalf("got %d writes, want %d", len(lines), goroutines*each)
	}
	// Every write must be a complete record: the label line, then exactly one
	// indented attribute line, and nothing else.
	for _, ln := range lines {
		parts := strings.SplitAfter(ln, "\n")
		if len(parts) != 3 || parts[2] != "" {
			t.Fatalf("record was not written as one whole unit: %q", ln)
		}
		if !strings.HasPrefix(parts[0], "⚠ WARN") {
			t.Fatalf("record does not start with its label: %q", ln)
		}
		if !strings.HasPrefix(parts[1], diagIndent) {
			t.Fatalf("attribute line is not in the message column: %q", ln)
		}
	}
}

// writeRecorder captures each Write call as a discrete entry.
type writeRecorder struct {
	mu    *sync.Mutex
	lines *[]string
}

func (w writeRecorder) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	*w.lines = append(*w.lines, string(b))
	return len(b), nil
}

// Handle must report a write failure rather than swallowing it. slog's Logger
// discards the return, so this changes nothing for a live CLI — but a handler
// that claims success after a failed write makes a tee'd sink undebuggable.
func TestSlogHandlerReportsWriteErrors(t *testing.T) {
	want := errors.New("disk on fire")
	p := New(io.Discard, io.Discard, ColorNever)
	h := NewSlogHandler(failingWriter{err: want}, p, slog.LevelInfo)

	err := h.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelWarn, "msg", 0))
	if !errors.Is(err, want) {
		t.Fatalf("Handle returned %v, want %v", err, want)
	}
}

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

// TestSlogHandlerMutexIsLoadBearing is the companion to the test above, and exists
// because that one CANNOT detect a missing lock: its writeRecorder takes its own
// mutex, so `-race` sees a properly synchronized writer no matter what the handler
// does. Deleting h.mu.Lock()/Unlock() left it green.
//
// This one writes into a bare *bytes.Buffer shared across goroutines with NO
// external synchronization, so the handler's own lock is the only thing making the
// writes safe. Under `-race` (which CI runs), removing that lock is a reported data
// race here.
func TestSlogHandlerMutexIsLoadBearing(t *testing.T) {
	var unsynchronized bytes.Buffer // deliberately NOT guarded by the test
	p := New(io.Discard, io.Discard, ColorNever)
	lg := slog.New(NewSlogHandler(&unsynchronized, p, slog.LevelInfo))

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			sub := lg.With("g", g)
			for i := 0; i < 25; i++ {
				sub.Warn("racy record", "i", i)
			}
		}(g)
	}
	wg.Wait()

	// Every record still landed intact — the lock serializes whole records, so no
	// write is torn even though the buffer itself is unguarded.
	if n := strings.Count(unsynchronized.String(), "⚠ WARN"); n != 8*25 {
		t.Fatalf("got %d labeled records, want %d", n, 8*25)
	}
}

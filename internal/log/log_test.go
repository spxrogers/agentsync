package log_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	aslog "github.com/spxrogers/agentsync/internal/log"
	"github.com/spxrogers/agentsync/internal/ui"
)

func plainPrinter(buf *bytes.Buffer) *ui.Printer {
	return ui.New(buf, buf, ui.ColorNever)
}

func TestNew_DefaultLevel(t *testing.T) {
	var buf bytes.Buffer
	lg := aslog.New(&buf, plainPrinter(&buf), false)
	lg.Info("hello", slog.String("k", "v"))
	lg.Debug("invisible at default level")

	out := buf.String()
	if !strings.Contains(out, "ℹ INFO   hello") {
		t.Fatalf("expected labeled info line, got: %s", out)
	}
	if !strings.Contains(out, "k=v") {
		t.Fatalf("expected attribute on the continuation line, got: %s", out)
	}
	if strings.Contains(out, "invisible") {
		t.Fatalf("debug message leaked into default-level output: %s", out)
	}
}

func TestNew_VerboseLevel(t *testing.T) {
	var buf bytes.Buffer
	lg := aslog.New(&buf, plainPrinter(&buf), true)
	lg.Debug("now visible")

	if !strings.Contains(buf.String(), "• DEBUG  now visible") {
		t.Fatalf("debug message missing in verbose output: %s", buf.String())
	}
}

// Install must make the logger the process default — that is the whole reason
// the package exists — and its restore must put the previous default back so a
// test never leaks a handler bound to its own finished buffer.
func TestInstall_SetsAndRestoresDefault(t *testing.T) {
	var buf bytes.Buffer
	before := slog.Default()

	restore := aslog.Install(&buf, plainPrinter(&buf), false)
	slog.Warn("library-side warning", "path", "/tmp/x")
	if got := buf.String(); !strings.Contains(got, "⚠ WARN   library-side warning") {
		t.Fatalf("slog.Warn did not route through the installed handler, got: %q", got)
	}
	// The stdlib default prefixes every line with a wall-clock date; the whole
	// point of installing is that the line now STARTS with the level label.
	if !strings.HasPrefix(buf.String(), "⚠ WARN") {
		t.Fatalf("expected the line to start with the level label, got: %q", buf.String())
	}

	restore()
	if slog.Default() != before {
		t.Fatal("restore did not put the previous default logger back")
	}
}

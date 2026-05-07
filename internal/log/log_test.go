package log_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	aslog "github.com/spxrogers/agentsync/internal/log"
)

func TestNew_DefaultLevel(t *testing.T) {
	var buf bytes.Buffer
	lg := aslog.New(&buf, false)
	lg.Info("hello", slog.String("k", "v"))
	lg.Debug("invisible at default level")

	out := buf.String()
	if !strings.Contains(out, `"msg":"hello"`) {
		t.Fatalf("expected info-level msg, got: %s", out)
	}
	if strings.Contains(out, "invisible") {
		t.Fatalf("debug message leaked into default-level output: %s", out)
	}
}

func TestNew_VerboseLevel(t *testing.T) {
	var buf bytes.Buffer
	lg := aslog.New(&buf, true)
	lg.Debug("now visible")

	if !strings.Contains(buf.String(), "now visible") {
		t.Fatalf("debug message missing in verbose output: %s", buf.String())
	}
}

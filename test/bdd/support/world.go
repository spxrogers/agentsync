// Package support contains the BDD world / step definitions for agentsync.
//
// Hermeticity contract:
//   - Every Scenario runs against a fresh tmpdir.
//   - HOME and AGENTSYNC_TARGET_ROOT both point at that tmpdir.
//   - No real $HOME path is ever read or written.
//   - The agentsync binary is the System Under Test; the suite shells out to
//     it instead of importing internal packages, so we exercise the same
//     entrypoint the engineer ships.
package support

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// World is the per-scenario state.
type World struct {
	t *testing.T

	Bin       string
	Home      string
	WorkDir   string
	ExtraEnv  map[string]string
	LastOut   string
	LastErr   error
	LastCode  int
	StartedAt time.Time
}

// Reset wipes per-scenario state and provisions a fresh hermetic root.
func (w *World) Reset(t *testing.T, bin string) error {
	w.t = t
	w.Bin = bin
	w.ExtraEnv = map[string]string{}
	w.LastOut = ""
	w.LastErr = nil
	w.LastCode = 0
	w.StartedAt = time.Now()

	dir, err := os.MkdirTemp("", "agentsync-bdd-*")
	if err != nil {
		return fmt.Errorf("mktemp: %w", err)
	}
	w.Home = dir
	w.WorkDir = dir
	return nil
}

// Cleanup removes the scenario tmpdir.
func (w *World) Cleanup() {
	if w.Home != "" {
		_ = os.RemoveAll(w.Home)
	}
}

// Env returns the env slice passed to every binary invocation. It does NOT
// inherit the host $HOME or XDG vars; AGENTSYNC_TARGET_ROOT is the redirect
// guarantee documented in the design spec.
func (w *World) Env() []string {
	base := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + w.Home,
		"AGENTSYNC_TARGET_ROOT=" + w.Home,
		"XDG_CONFIG_HOME=" + filepath.Join(w.Home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(w.Home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(w.Home, ".cache"),
	}
	if tz := os.Getenv("TZ"); tz != "" {
		base = append(base, "TZ="+tz)
	}
	for k, v := range w.ExtraEnv {
		base = append(base, k+"="+v)
	}
	return base
}

// Run invokes the binary with args; output is captured combined.
func (w *World) Run(args ...string) (string, error) {
	cmd := exec.Command(w.Bin, args...)
	cmd.Env = w.Env()
	cmd.Dir = w.WorkDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	w.LastOut = strings.TrimRight(buf.String(), "\n")
	w.LastErr = err
	if exitErr, ok := err.(*exec.ExitError); ok {
		w.LastCode = exitErr.ExitCode()
	} else if err != nil {
		w.LastCode = -1
	} else {
		w.LastCode = 0
	}
	return w.LastOut, err
}

// MustRun runs and fatals the scenario on error.
func (w *World) MustRun(args ...string) string {
	out, err := w.Run(args...)
	if err != nil {
		panic(fmt.Errorf("agentsync %s failed: %v\noutput:\n%s",
			strings.Join(args, " "), err, out))
	}
	return out
}

// WriteFile writes content to a path inside the hermetic Home, creating
// parent directories. The path is treated as relative to Home unless absolute.
func (w *World) WriteFile(rel, content string) error {
	abs := w.Resolve(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, []byte(content), 0o644)
}

// AppendFile appends content to a path inside Home.
func (w *World) AppendFile(rel, content string) error {
	abs := w.Resolve(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(abs, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// ReadFile reads a path inside Home.
func (w *World) ReadFile(rel string) (string, error) {
	b, err := os.ReadFile(w.Resolve(rel))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Resolve turns a path into an absolute path under Home if it is not already
// absolute. This keeps every scenario rooted in its tmpdir without surprises.
func (w *World) Resolve(rel string) string {
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(w.Home, rel)
}

// ----- agentsync binary build singleton ------------------------------------

var (
	binOnce sync.Once
	binPath string
	binErr  error
)

// BuildBinary compiles the agentsync binary once per test process and returns
// its absolute path. The binary lives in a stable temp dir so subsequent
// scenarios don't pay the build cost.
func BuildBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		dir, err := os.MkdirTemp("", "agentsync-bdd-bin-*")
		if err != nil {
			binErr = err
			return
		}
		name := "agentsync"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		bin := filepath.Join(dir, name)
		root := repoRoot()
		cmd := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/agentsync")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			binErr = fmt.Errorf("build agentsync: %v\n%s", err, out)
			return
		}
		binPath = bin
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

// repoRoot walks up from CWD until it finds go.mod.
func repoRoot() string {
	dir, _ := filepath.Abs(".")
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("repoRoot: go.mod not found anywhere in ancestors")
		}
		dir = parent
	}
}

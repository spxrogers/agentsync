package filemode

import (
	"os"
	"runtime"
	"testing"
)

func TestPermissionSemantics(t *testing.T) {
	cases := []struct {
		name, goos      string
		actual, desired os.FileMode
		equal           bool
	}{
		{"windows-generated-markdown", "windows", 0o666, 0o644, true},
		{"windows-private-file", "windows", 0o666, 0o600, true},
		{"windows-executable", "windows", 0o666, 0o755, true},
		{"windows-readonly-equivalent", "windows", 0o444, 0o400, true},
		{"windows-readonly-drift", "windows", 0o444, 0o644, false},
		{"windows-writable-drift", "windows", 0o666, 0o444, false},
		{"windows-owner-write-only", "windows", 0o444, 0o022, true},
		{"unix-group-write-drift", "linux", 0o666, 0o644, false},
		{"unix-execute-drift", "linux", 0o644, 0o755, false},
		{"unix-private-drift", "darwin", 0o644, 0o600, false},
		{"unix-readonly-drift", "linux", 0o444, 0o644, false},
		{"unix-equal", "linux", 0o755, 0o755, true},
		{"unix-file-type-ignored", "linux", os.ModeDir | 0o755, 0o755, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := equalForOS(tc.actual, tc.desired, tc.goos); got != tc.equal {
				t.Fatalf("equal(%o,%o,%s)=%v, want %v", tc.actual, tc.desired, tc.goos, got, tc.equal)
			}
		})
	}
	if Equal(0o666, 0o644) != (runtime.GOOS == "windows") {
		t.Fatal("host dispatch does not use host permission semantics")
	}
}

package cli

import "testing"

func TestSanitizeCacheKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"simple", "simple"},
		{"../../etc/passwd", "____etc_passwd"},
		{"a/b", "a_b"},
		{"a\\b", "a_b"},
		{"", "_"},
		{".", "_"},
		{"..", "_"},
	}
	for _, tc := range cases {
		got := sanitizeCacheKey(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeCacheKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateCacheKey(t *testing.T) {
	bad := []string{"", "..", ".", "a/b", "a\\b", "a/../b", "../etc/passwd"}
	for _, s := range bad {
		if err := validateCacheKey("plugin", s); err == nil {
			t.Errorf("validateCacheKey(%q) should reject", s)
		}
	}
	good := []string{"atlassian", "obra-superpowers", "a.b.c", "abc123"}
	for _, s := range good {
		if err := validateCacheKey("plugin", s); err != nil {
			t.Errorf("validateCacheKey(%q) should accept: %v", s, err)
		}
	}
	// The validator sees the BARE id: an `id@marketplace` ref is split by
	// splitPluginRef before reaching it (#168), so `@` never arrives here. The
	// validator therefore neither needs nor performs an `@` rejection — a bare
	// `demo` is accepted and a path-separator id is still rejected.
	if err := validateCacheKey("plugin", "demo"); err != nil {
		t.Errorf("validateCacheKey(\"demo\") should accept the bare id: %v", err)
	}
	if err := validateCacheKey("plugin", "a/b"); err == nil {
		t.Error("validateCacheKey(\"a/b\") should still reject path separators")
	}
}

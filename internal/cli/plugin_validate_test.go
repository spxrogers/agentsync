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
}

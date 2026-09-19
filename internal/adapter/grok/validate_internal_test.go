package grok

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateHome_CleansItsOwnInput pins validateHome's defensive Clean. New
// cleans GrokHome before it is stored, so through the public constructor the
// Clean inside validateHome is unreachable — this test builds the Adapter
// directly, as a zero-value struct literal would, and hands validateHome the raw
// spelling. Without its own Clean, `$HOME/x/..` would slip past the $HOME check
// and `/tmp/..` past the root check.
func TestValidateHome_CleansItsOwnInput(t *testing.T) {
	home := t.TempDir()
	sep := string(filepath.Separator)
	cases := []struct {
		name     string
		grokHome string
		wantErr  string
	}{
		{name: "$HOME via ..", grokHome: home + sep + "x" + sep + "..", wantErr: "home directory"},
		{name: "$HOME via trailing separator", grokHome: home + sep, wantErr: "home directory"},
		{name: "root via ..", grokHome: sep + "tmp" + sep + "..", wantErr: "filesystem root"},
		{name: "clean sibling is fine", grokHome: home + sep + "grok", wantErr: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Adapter{opts: Options{TargetRoot: home, GrokHome: tc.grokHome}}
			err := a.validateHome()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateHome(%q) = %v; want nil", tc.grokHome, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateHome(%q) = %v; want error containing %q", tc.grokHome, err, tc.wantErr)
			}
		})
	}
}

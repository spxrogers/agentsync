package secrets

import (
	"fmt"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

type fakeResolver map[string]string

func (f fakeResolver) Resolve(k string) (string, error) {
	if v, ok := f[k]; ok {
		return v, nil
	}
	return "", fmt.Errorf("missing %q", k)
}

// TestReReferenceCanonical_FieldPositional covers the three behaviours the
// capture boundary depends on:
//   - a templated source field whose resolution matches the ingested value is
//     restored to its ${secret:…} placeholder;
//   - a non-secret source field whose literal happens to equal a secret value
//     is left alone (over-mask regression — proves the match is field-positional,
//     not value-based);
//   - an ingested value that no longer resolves to the source template (a real
//     dest edit) is kept verbatim rather than clobbered back to the placeholder.
func TestReReferenceCanonical_FieldPositional(t *testing.T) {
	sec := fakeResolver{"GH_TOKEN": "ghp_LIVE"}
	env := fakeResolver{}

	against := &source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "github",
			Server: source.MCPServerSpec{
				Type:    "stdio",
				Command: "npx", // literal, never templated
				// arg[1] is a NON-secret literal that happens to equal the secret value.
				Args: []string{"--token", "ghp_LIVE"},
				Env: map[string]string{
					"GH_TOKEN": "${secret:GH_TOKEN}", // templated
					"NOTE":     "${secret:GH_TOKEN}", // templated but edited in dest
				},
			},
		}},
		Hooks: []source.Hook{{
			Event: "PreToolUse", Type: "command",
			Command: "auth ${secret:GH_TOKEN}",
		}},
	}

	// Ingested == what apply wrote to the dest (secrets resolved to cleartext),
	// then read back. NOTE was hand-edited in the dest to a different value.
	ingested := &source.Canonical{
		MCPServers: []source.MCPServer{{
			ID: "github",
			Server: source.MCPServerSpec{
				Type:    "stdio",
				Command: "npx",
				Args:    []string{"--token", "ghp_LIVE"},
				Env: map[string]string{
					"GH_TOKEN": "ghp_LIVE",
					"NOTE":     "hand-edited",
				},
			},
		}},
		Hooks: []source.Hook{{
			Event: "PreToolUse", Type: "command",
			Command: "auth ghp_LIVE",
		}},
	}

	ReReferenceCanonical(ingested, against, sec, env)

	srv := ingested.MCPServers[0].Server
	if srv.Env["GH_TOKEN"] != "${secret:GH_TOKEN}" {
		t.Errorf("templated env field not re-referenced: got %q", srv.Env["GH_TOKEN"])
	}
	if srv.Env["NOTE"] != "hand-edited" {
		t.Errorf("genuinely edited field was clobbered back to the placeholder: got %q", srv.Env["NOTE"])
	}
	if srv.Args[1] != "ghp_LIVE" {
		t.Errorf("over-mask: a non-secret literal equal to a secret value was rewritten: got %q", srv.Args[1])
	}
	if srv.Command != "npx" {
		t.Errorf("non-secret command rewritten: got %q", srv.Command)
	}
	if ingested.Hooks[0].Command != "auth ${secret:GH_TOKEN}" {
		t.Errorf("hook command not re-referenced: got %q", ingested.Hooks[0].Command)
	}
}

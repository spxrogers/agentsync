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

// TestReReferenceCanonical_ValueFallbackOnStructuralShift is the regression for
// a silent cleartext-secret leak: the positional restore matches a templated
// source field to its ingested counterpart by location, so a native edit that
// SHIFTS structure (prepend an MCP arg, rename an env key / server id) moves the
// resolved cleartext to a location with no source counterpart — the positional
// lookup misses and the resolved secret would be persisted into ~/.agentsync.
// The value-based fallback must restore the placeholder in each of these cases.
func TestReReferenceCanonical_ValueFallbackOnStructuralShift(t *testing.T) {
	const live = "ghp_LIVE_SECRET_TOKEN"
	sec := fakeResolver{"T": live}
	env := fakeResolver{}

	t.Run("arg index shift", func(t *testing.T) {
		against := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Type: "stdio", Command: "x", Args: []string{"--token", "${secret:T}"}},
		}}}
		ingested := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Type: "stdio", Command: "x", Args: []string{"--verbose", "--token", live}},
		}}}
		ReReferenceCanonical(ingested, against, sec, env)
		for _, a := range ingested.MCPServers[0].Server.Args {
			if a == live {
				t.Fatalf("LEAK: resolved secret persisted in args: %v", ingested.MCPServers[0].Server.Args)
			}
		}
		if ingested.MCPServers[0].Server.Args[2] != "${secret:T}" {
			t.Errorf("shifted secret arg not re-referenced: %q", ingested.MCPServers[0].Server.Args[2])
		}
	})

	t.Run("env key rename", func(t *testing.T) {
		against := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Env: map[string]string{"OLD": "${secret:T}"}},
		}}}
		ingested := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Env: map[string]string{"NEW": live}},
		}}}
		ReReferenceCanonical(ingested, against, sec, env)
		if got := ingested.MCPServers[0].Server.Env["NEW"]; got != "${secret:T}" {
			t.Errorf("renamed env secret not re-referenced: %q", got)
		}
	})

	t.Run("server id rename", func(t *testing.T) {
		against := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "old-id",
			Server: source.MCPServerSpec{Env: map[string]string{"K": "${secret:T}"}},
		}}}
		ingested := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "new-id",
			Server: source.MCPServerSpec{Env: map[string]string{"K": live}},
		}}}
		ReReferenceCanonical(ingested, against, sec, env)
		if got := ingested.MCPServers[0].Server.Env["K"]; got != "${secret:T}" {
			t.Errorf("renamed-server env secret not re-referenced: %q", got)
		}
	})

	t.Run("secret embedded in command, structurally changed", func(t *testing.T) {
		against := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Command: "auth --tok=${secret:T}"},
		}}}
		// User appended a flag to the command in the native UI.
		ingested := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Command: "auth --tok=" + live + " --verbose"},
		}}}
		ReReferenceCanonical(ingested, against, sec, env)
		if got := ingested.MCPServers[0].Server.Command; got != "auth --tok=${secret:T} --verbose" {
			t.Errorf("embedded secret not re-referenced after edit: %q", got)
		}
	})

	t.Run("renamed header embeds the secret in a larger value", func(t *testing.T) {
		// A renamed header key has no source counterpart, and the secret is
		// embedded in "Bearer <token>" — a whole-value-only match would miss it
		// and persist the cleartext. The substring fallback must re-reference it.
		against := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Headers: map[string]string{"Authorization": "Bearer ${secret:T}"}},
		}}}
		ingested := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Headers: map[string]string{"Auth": "Bearer " + live}},
		}}}
		ReReferenceCanonical(ingested, against, sec, env)
		if got := ingested.MCPServers[0].Server.Headers["Auth"]; got != "Bearer ${secret:T}" {
			t.Fatalf("embedded secret in a relocated header not re-referenced (leak): %q", got)
		}
	})

	t.Run("idempotent: re-referenced value resolves back unchanged", func(t *testing.T) {
		against := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Args: []string{"${secret:T}"}},
		}}}
		ingested := &source.Canonical{MCPServers: []source.MCPServer{{
			ID:     "srv",
			Server: source.MCPServerSpec{Args: []string{"pre", live}},
		}}}
		ReReferenceCanonical(ingested, against, sec, env)
		// Re-render: the templated arg must resolve back to the same cleartext,
		// so the next apply sees no spurious drift.
		got := ingested.MCPServers[0].Server.Args[1]
		if got != "${secret:T}" {
			t.Fatalf("not re-referenced: %q", got)
		}
		resolved, _, _ := SubstituteRefs(got, sec, env)
		if resolved != live {
			t.Errorf("re-referenced value does not round-trip: %q -> %q", got, resolved)
		}
	})
}

// TestReReferenceCanonical_NoOverMaskSubstringLiteral is the regression for an
// over-mask the value-based fallback introduced: a field the user authored as a
// LITERAL that merely CONTAINS a secret's value as a substring must not be
// rewritten into a ${secret:…} reference. Here server "b"'s command is a literal
// that contains server "a"'s resolved secret value; capturing an unchanged "b"
// must leave it byte-for-byte, while "a"'s genuine templated field is restored.
func TestReReferenceCanonical_NoOverMaskSubstringLiteral(t *testing.T) {
	sec := fakeResolver{"TOK": "tok12345abc"}
	env := fakeResolver{}
	against := &source.Canonical{MCPServers: []source.MCPServer{
		{ID: "a", Server: source.MCPServerSpec{Env: map[string]string{"K": "${secret:TOK}"}}},
		{ID: "b", Server: source.MCPServerSpec{Command: "run --note=tok12345abc-extra"}},
	}}
	ingested := &source.Canonical{MCPServers: []source.MCPServer{
		{ID: "a", Server: source.MCPServerSpec{Env: map[string]string{"K": "tok12345abc"}}},
		{ID: "b", Server: source.MCPServerSpec{Command: "run --note=tok12345abc-extra"}},
	}}
	ReReferenceCanonical(ingested, against, sec, env)
	if got := ingested.MCPServers[0].Server.Env["K"]; got != "${secret:TOK}" {
		t.Errorf("server a templated secret not restored: %q", got)
	}
	if got := ingested.MCPServers[1].Server.Command; got != "run --note=tok12345abc-extra" {
		t.Fatalf("over-mask: server b's literal command rewritten to %q", got)
	}
}

// TestReReferenceCanonical_NoOverMaskCrossScopeLiteral proves a user-scope
// secret that is structurally shifted is still re-referenced even when the same
// value appears as a PROJECT-scope literal (the per-field decision is keyed by
// scope, so the project literal does not disable the user-scope safety net).
func TestReReferenceCanonical_NoOverMaskCrossScopeLiteral(t *testing.T) {
	sec := fakeResolver{"TOK": "tok12345abc"}
	env := fakeResolver{}
	against := &source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "u", Server: source.MCPServerSpec{Args: []string{"--tok", "${secret:TOK}"}}},
		},
		Project: &source.Canonical{
			MCPServers: []source.MCPServer{
				{ID: "p", Server: source.MCPServerSpec{Command: "tok12345abc"}}, // literal
			},
		},
	}
	ingested := &source.Canonical{
		MCPServers: []source.MCPServer{
			{ID: "u", Server: source.MCPServerSpec{Args: []string{"--verbose", "--tok", "tok12345abc"}}},
		},
		Project: &source.Canonical{
			MCPServers: []source.MCPServer{
				{ID: "p", Server: source.MCPServerSpec{Command: "tok12345abc"}},
			},
		},
	}
	ReReferenceCanonical(ingested, against, sec, env)
	if got := ingested.MCPServers[0].Server.Args[2]; got != "${secret:TOK}" {
		t.Errorf("user-scope shifted secret not re-referenced (cross-scope literal disabled the net): %q", got)
	}
	if got := ingested.Project.MCPServers[0].Server.Command; got != "tok12345abc" {
		t.Errorf("project-scope literal over-masked: %q", got)
	}
}

// TestReReferenceCanonical_ProjectOverlay proves the field-positional restore
// recurses into the project overlay: a project-scope secret field is matched to
// its project-scope source counterpart (the secretFieldLoc carries a scope
// marker so a user-scope server of the same ID can't be mismatched against it).
func TestReReferenceCanonical_ProjectOverlay(t *testing.T) {
	sec := fakeResolver{"PTOK": "proj-secret"}
	env := fakeResolver{}

	against := &source.Canonical{
		// Same ID at user scope, NOT templated — must not be matched against the
		// project-scope field below.
		MCPServers: []source.MCPServer{{
			ID:     "psrv",
			Server: source.MCPServerSpec{Env: map[string]string{"K": "user-literal"}},
		}},
		Project: &source.Canonical{
			MCPServers: []source.MCPServer{{
				ID:     "psrv",
				Server: source.MCPServerSpec{Env: map[string]string{"K": "${secret:PTOK}"}},
			}},
		},
	}
	ingested := &source.Canonical{
		MCPServers: []source.MCPServer{{
			ID:     "psrv",
			Server: source.MCPServerSpec{Env: map[string]string{"K": "user-literal"}},
		}},
		Project: &source.Canonical{
			MCPServers: []source.MCPServer{{
				ID:     "psrv",
				Server: source.MCPServerSpec{Env: map[string]string{"K": "proj-secret"}},
			}},
		},
	}

	ReReferenceCanonical(ingested, against, sec, env)

	if got := ingested.Project.MCPServers[0].Server.Env["K"]; got != "${secret:PTOK}" {
		t.Errorf("project-overlay secret not re-referenced: got %q", got)
	}
	if got := ingested.MCPServers[0].Server.Env["K"]; got != "user-literal" {
		t.Errorf("user-scope non-secret field corrupted by project-scope match: got %q", got)
	}
}

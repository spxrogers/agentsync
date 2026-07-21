package secrets

import (
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// mcpSrv is a tiny builder for a single MCP server, keeping the adversarial
// table rows below readable.
func mcpSrv(id string, spec source.MCPServerSpec) source.MCPServer {
	return source.MCPServer{ID: id, Server: spec}
}

// TestResidualSecretCleartext_Adversarial exhaustively table-tests the fail-closed
// backstop that stands between a resolved credential and the committed canonical
// source. It exercises BOTH prongs, the post-#135 length boundary (a sub-4-char
// secret must still trip the backstop even though the re-reference floor drops it),
// the passthrough-Extra scan across MCP/LSP/project scope, and the nil guards.
//
// The VALUE-in-Extra MCP prong is also covered by
// TestResidualSecretCleartext_ExtraValue; the rows here extend it to LSP + project
// scope and cross it with the prong/length matrix. A non-empty result means
// "refuse the write"; an empty result means "safe to write".
func TestResidualSecretCleartext_Adversarial(t *testing.T) {
	const live = "livesecret" // the resolved vault value the backstop must not persist

	type testCase struct {
		name       string
		sec        fakeResolver
		against    *source.Canonical // current templated source
		ingested   *source.Canonical // re-referenced canonical about to be written
		wantRefuse bool
	}

	cases := []testCase{
		// ---- VALUE prong -------------------------------------------------------
		{
			// A live vault value moved into a field whose source counterpart is a
			// plain literal (so re-reference left it unmasked) → refuse.
			name: "value/moved-into-literal-field",
			sec:  fakeResolver{"K": live},
			against: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Type: "stdio", Command: "run", Env: map[string]string{"TOKEN": "${secret:K}"},
			})}},
			ingested: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Type: "stdio", Command: "run --token=" + live, Env: map[string]string{"TOKEN": "${secret:K}"},
			})}},
			wantRefuse: true,
		},
		{
			// The same value ALREADY present in the source counterpart is a
			// pre-existing literal, not a new leak (the !strings.Contains(src, v)
			// guard) → allow.
			name: "value/pre-existing-literal-in-source",
			sec:  fakeResolver{"K": live},
			against: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Type: "stdio", Command: "run --token=" + live, Env: map[string]string{"TOKEN": "${secret:K}"},
			})}},
			ingested: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Type: "stdio", Command: "run --token=" + live, Env: map[string]string{"TOKEN": "${secret:K}"},
			})}},
			wantRefuse: false,
		},

		// ---- SLOT prong --------------------------------------------------------
		{
			// A rotated secret: the source slot's SHAPE is retained with cleartext,
			// and the ${secret:K} placeholder is gone from the whole group → refuse.
			name: "slot/rotation-placeholder-gone",
			sec:  fakeResolver{"K": live},
			against: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Env: map[string]string{"TOKEN": "${secret:K}"},
			})}},
			ingested: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Env: map[string]string{"TOKEN": "rotated-new-cleartext"},
			})}},
			wantRefuse: true,
		},
		{
			// A secret legitimately shifted WITHIN the group keeps its placeholder
			// somewhere in the group, so the slot is not "gone" → allow.
			name: "slot/shift-placeholder-restored-elsewhere",
			sec:  fakeResolver{"K": live},
			against: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Args: []string{"${secret:K}", "--flag"},
			})}},
			ingested: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Args: []string{"--flag", "${secret:K}"},
			})}},
			wantRefuse: false,
		},
		{
			// A trim that removes the slot AND its surrounding context breaks the
			// shape (fieldRetainsRotatedSecret → false): no cleartext remains → allow.
			name: "slot/trim-removes-slot-and-context",
			sec:  fakeResolver{"K": live},
			against: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Command: "auth ${secret:K} --verbose",
			})}},
			ingested: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Command: "--verbose",
			})}},
			wantRefuse: false,
		},

		// ---- Extra passthrough (VALUE shape only, via scanExtraResidual) -------
		{
			name: "extra/mcp-leak",
			sec:  fakeResolver{"K": live},
			against: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Env: map[string]string{"E": "${secret:K}"},
			})}},
			ingested: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Env: map[string]string{"E": "${secret:K}"}, Extra: map[string]any{"authMirror": live},
			})}},
			wantRefuse: true,
		},
		{
			name: "extra/mcp-pre-existing",
			sec:  fakeResolver{"K": live},
			against: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Env: map[string]string{"E": "${secret:K}"}, Extra: map[string]any{"authMirror": live},
			})}},
			ingested: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Env: map[string]string{"E": "${secret:K}"}, Extra: map[string]any{"authMirror": live},
			})}},
			wantRefuse: false,
		},
		{
			name: "extra/lsp-leak",
			sec:  fakeResolver{"K": live},
			against: &source.Canonical{LSPServers: []source.LSPServer{{ID: "ls", Spec: source.LSPServerSpec{
				Env: map[string]string{"E": "${secret:K}"},
			}}}},
			ingested: &source.Canonical{LSPServers: []source.LSPServer{{ID: "ls", Spec: source.LSPServerSpec{
				Env: map[string]string{"E": "${secret:K}"}, Extra: map[string]any{"authMirror": live},
			}}}},
			wantRefuse: true,
		},
		{
			name: "extra/project-scope-leak",
			sec:  fakeResolver{"K": live},
			against: &source.Canonical{Project: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("psrv", source.MCPServerSpec{
				Env: map[string]string{"E": "${secret:K}"},
			})}}},
			ingested: &source.Canonical{Project: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("psrv", source.MCPServerSpec{
				Env: map[string]string{"E": "${secret:K}"}, Extra: map[string]any{"authMirror": live},
			})}}},
			wantRefuse: true,
		},
		{
			name: "extra/project-scope-pre-existing",
			sec:  fakeResolver{"K": live},
			against: &source.Canonical{Project: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("psrv", source.MCPServerSpec{
				Env: map[string]string{"E": "${secret:K}"}, Extra: map[string]any{"authMirror": live},
			})}}},
			ingested: &source.Canonical{Project: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("psrv", source.MCPServerSpec{
				Env: map[string]string{"E": "${secret:K}"}, Extra: map[string]any{"authMirror": live},
			})}}},
			wantRefuse: false,
		},

		// ---- nil guards --------------------------------------------------------
		{
			name: "nil/ingested-nil",
			sec:  fakeResolver{"K": live},
			against: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Env: map[string]string{"E": "${secret:K}"},
			})}},
			ingested:   nil,
			wantRefuse: false,
		},
		{
			name: "nil/against-nil",
			sec:  fakeResolver{"K": live},
			ingested: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Env: map[string]string{"E": live},
			})}},
			against:    nil,
			wantRefuse: false,
		},
	}

	// Length boundary (post-#135): the backstop uses backstopSecretValues (no
	// minReReferenceLen floor), so a live secret of length >= 1 moved into a
	// literal-counterpart field must still be refused, while a truly-empty value
	// (len 0) has nothing to leak and is allowed. Whitespace-only is len >= 1, so
	// it participates and, when it appears as a cleartext run, is refused.
	lens := []struct {
		name       string
		val        string
		wantRefuse bool
	}{
		{"len0-empty", "", false},
		{"len1", "a", true},
		{"len3", "abc", true},
		{"len4", "abcd", true},
		{"len-longer", "abcdefghij", true},
		{"whitespace-only", "   ", true},
	}
	for _, lc := range lens {
		cases = append(cases, testCase{
			name: "length/" + lc.name,
			sec:  fakeResolver{"K": lc.val},
			against: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Type: "stdio", Command: "run", Env: map[string]string{"E": "${secret:K}"},
			})}},
			// The leaked value is appended to the command; its source counterpart
			// ("run") never contains it, so a non-empty value trips the VALUE prong.
			ingested: &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
				Type: "stdio", Command: "run" + lc.val, Env: map[string]string{"E": "${secret:K}"},
			})}},
			wantRefuse: lc.wantRefuse,
		})
	}

	env := fakeResolver{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaks := ResidualSecretCleartext(tc.ingested, tc.against, tc.sec, env)
			if got := len(leaks) > 0; got != tc.wantRefuse {
				t.Fatalf("ResidualSecretCleartext refuse=%v (leaks=%v), want refuse=%v", got, leaks, tc.wantRefuse)
			}
		})
	}
}

// TestResidualSecretCleartext_LockedBackend pins the interaction between the
// backstop and an always-erroring (locked age vault / NopResolver) backend, the
// exact backend capture.Capture passes through (secrets.SelectBackend can return a
// locked AgeBackend). Two facts are documented:
//
//   - The VALUE prong resolves via the backend, so a locked vault yields an empty
//     detection set and the prong goes SILENT — a pure value-leak is NOT refused.
//     (This under-refusal is a known edge; it belongs to #135/#163, not to this
//     test-only pass. We pin the current behavior, we do not paper over it.)
//   - The SLOT prong never calls Resolve (it reasons off the source's ${secret:…}
//     shape), so it STILL fires on a rotated secret even under a locked backend.
//
// Neither ResidualSecretCleartext nor ReReferenceCanonical may panic.
func TestResidualSecretCleartext_LockedBackend(t *testing.T) {
	const live = "livesecret"
	locked := NopResolver{} // always errors — a locked vault
	env := EnvBackend{}

	t.Run("value-prong-silent-under-locked-backend", func(t *testing.T) {
		against := &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
			Command: "run", Env: map[string]string{"TOKEN": "${secret:K}"},
		})}}
		ingested := &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
			Command: "run --token=" + live, Env: map[string]string{"TOKEN": "${secret:K}"},
		})}}
		// With a working backend this leak WOULD be refused (see the adversarial
		// table's value/moved-into-literal-field row); with a locked backend the
		// VALUE prong cannot see the value and stays silent.
		if leaks := ResidualSecretCleartext(ingested, against, locked, env); len(leaks) != 0 {
			t.Fatalf("expected the VALUE prong to be silent under a locked backend, got %v", leaks)
		}
	})

	t.Run("slot-prong-fires-under-locked-backend", func(t *testing.T) {
		against := &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
			Env: map[string]string{"TOKEN": "${secret:K}"},
		})}}
		ingested := &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
			Env: map[string]string{"TOKEN": "rotated-cleartext"},
		})}}
		if leaks := ResidualSecretCleartext(ingested, against, locked, env); len(leaks) == 0 {
			t.Fatal("SLOT prong must still refuse a rotated secret even under a locked backend")
		}
	})

	t.Run("rereference-does-not-panic-under-locked-backend", func(t *testing.T) {
		against := &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
			Env: map[string]string{"TOKEN": "${secret:K}"},
		})}}
		ingested := &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
			Env: map[string]string{"TOKEN": live},
		})}}
		// A no-op is fine; the assertion is simply "no panic".
		ReReferenceCanonical(ingested, against, locked, env)
	})
}

// FuzzResidualSecretCleartext hunts for an input where a genuine value-leak slips
// through the backstop as "allow". It builds a one-MCP-server canonical from the
// fuzzed strings for both the source and the ingested side (varying Command +
// Args, which share the same source counterpart string), resolves one secret key
// to the fuzzed value, and asserts the SAFETY PROPERTY the backstop exists to
// guarantee:
//
//	a live vault value present verbatim in an ingested field, and absent from that
//	field's source counterpart, MUST produce a non-empty result (refuse).
//
// Extra conservative refusals never violate the property — it is a one-directional
// implication (leak ⇒ refuse). The fuzzer also drives adversarial source strings
// through fieldRetainsRotatedSecret's regex construction, which must never panic.
func FuzzResidualSecretCleartext(f *testing.F) {
	// A handful of representative seeds (no large corpus is committed).
	f.Add("run", "run --token=livesecret", "livesecret") // clear value-leak → must refuse
	f.Add("${secret:K}", "${secret:K}", "livesecret")    // re-referenced, no residual → allow
	f.Add("run", "run", "")                              // empty secret → nothing to leak
	f.Add("run", "runa", "a")                            // sub-4-char leak → must refuse (#135)
	f.Add("a.*b", "xSECRETy", "SECRET")                  // regex-special source + embedded leak
	f.Add("auth ${secret:K} --v", "--v", "livesecret")   // trim → shape broken

	env := EnvBackend{}
	f.Fuzz(func(t *testing.T, srcField, ingestedField, secretVal string) {
		sec := fakeResolver{"K": secretVal}
		// against references K (so a non-empty secretVal is a "live vault value"),
		// with a literal Command/Args pair as the potential leak sink.
		against := &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
			Type:    "stdio",
			Command: srcField,
			Args:    []string{srcField},
			Env:     map[string]string{"E": "${secret:K}"},
		})}}
		ingested := &source.Canonical{MCPServers: []source.MCPServer{mcpSrv("srv", source.MCPServerSpec{
			Type:    "stdio",
			Command: ingestedField,
			Args:    []string{ingestedField},
			Env:     map[string]string{"E": "${secret:K}"},
		})}}

		leaks := ResidualSecretCleartext(ingested, against, sec, env)

		if secretVal == "" {
			return // an empty value carries nothing to leak (excluded at build time)
		}
		// The Command and Args[0] fields both have srcField as their source
		// counterpart, so this predicts a VALUE-prong leak in each.
		leaked := strings.Contains(ingestedField, secretVal) && !strings.Contains(srcField, secretVal)
		if leaked && len(leaks) == 0 {
			t.Fatalf("SAFETY VIOLATION: live secret %q present verbatim in ingested field %q "+
				"(absent from source counterpart %q) but the backstop ALLOWED the write",
				secretVal, ingestedField, srcField)
		}
	})
}

// Package capture owns the single dest->source write-back path. apply
// substitutes ${secret:…} into a destination as cleartext and may drop
// source-only fields the rendered dest never carries; any flow that reads a
// destination back into the canonical source (import, reconcile write-back, a
// future "adopt") must invert both, in exactly one place, or it reintroduces a
// recurring class of bugs (cleartext-secret persistence, source-only field
// drift, backup bypass).
//
// Capture is that place. It re-references secrets, preserves source-only
// fields, and routes every write through internal/source/writer.go.
package capture

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/jsonkeys"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
)

// Opts tunes a Capture call.
type Opts struct {
	// Warn receives a human-readable warning when a ${secret:…} reference in
	// the current source cannot be resolved (backend unavailable / locked), so
	// re-referencing may have left cleartext in the written-back file. nil
	// discards it. This is the guard that keeps a secret from silently landing
	// in ~/.agentsync.
	Warn io.Writer
}

// Result reports what Capture wrote.
type Result struct {
	// Written lists the source-relative paths written, in write order.
	Written []string
}

// Capture persists ingested — a canonical reconstructed from a destination —
// back into the canonical source at home. It is the single dest->source path;
// both import and reconcile build a single-item canonical and call it.
//
// For every component present in ingested it, in one place:
//  0. normalizes json.Number values in the MCP/LSP Extra passthrough maps to
//     int64/float64 (normalizeExtraNumbers) so a UseNumber-decoded native
//     integer is persisted as a TOML integer, not a string;
//  1. re-references resolved secrets back to ${secret:…} against the current
//     source (secrets.ReReferenceCanonical), so a live credential apply wrote
//     into the destination is never persisted into ~/.agentsync;
//  2. preserves MCP/LSP targeting the destination doesn't fully carry: `agents`
//     is source-only (no native dest carries it) and is ALWAYS restored, so
//     write-back can't silently broaden a server's exposure; `enabled` IS carried
//     by some dests (Codex reads native `enabled` — issue #152), so the source
//     value is restored only when the ingest carried none (nil) — a real native
//     enable/disable round-trips, while a dest that omits `enabled` keeps the
//     source state (so write-back still can't silently clear it);
//  3. writes through internal/source/writer.go (never iox.AtomicWrite directly).
//
// The current source MUST load for Capture to run: re-referencing and
// source-only field preservation both read it. source.Load returns an empty
// Canonical (no error) for an empty/absent home — the first-import / "adopt a
// foreign dest item" case — so steps 1–2 run as harmless no-ops there, which is
// correct (apply only substitutes from a source ${secret:…}, so a brand-new
// item carries no secret WE resolved). But source.Load DOES error on any
// malformed file anywhere in the tree, and that is exactly when a user is most
// likely re-importing. Writing in that state would skip re-referencing and
// persist a resolved cleartext secret into ~/.agentsync (a committed dotfiles
// repo) with no warning. So Capture fails CLOSED: it refuses to write and
// surfaces the load error rather than risk leaking a credential.
func Capture(home string, ingested *source.Canonical, opts Opts) (Result, error) {
	var res Result
	if ingested == nil {
		return res, nil
	}
	normalizeExtraNumbers(ingested)

	cur, cerr := source.Load(afero.NewOsFs(), home)
	if cerr != nil {
		return res, fmt.Errorf("load canonical source to re-reference secrets: %w; "+
			"refusing to write back — writing now could persist a resolved cleartext "+
			"secret into ~/.agentsync; fix the source error and retry", cerr)
	}
	userHome := paths.HomeDir(paths.OSEnv{})
	sec := secrets.SelectBackend(cur.Config.Secrets, home, userHome)
	secrets.ReReferenceCanonical(ingested, &cur, sec, secrets.EnvBackend{})

	// Fail-closed under indeterminacy (issue #163): the value prong of the backstop
	// below builds its "live secret value -> placeholder" detection set by resolving
	// the source's ${secret:…} refs through the backend. If the backend can't resolve
	// them (vault locked / unavailable), that set is empty and the value prong is
	// BLIND — it cannot prove a resolved secret wasn't moved into a literal-
	// counterpart field. So an unresolvable ${secret:…} the source references forces
	// a refusal here rather than letting the flow fall through to a mere warning
	// (the reporting side used to carry the leak-prevention weight, which it can't).
	// Only the `secret:` kind matters: the value prong resolves via `sec`, not the
	// env backend, so an unresolvable ${env:…} does not blind it (it stays a warning).
	if blind := secretKindRefs(secrets.UnresolvedSecretRefs(&cur, sec, secrets.EnvBackend{})); len(blind) > 0 {
		return res, fmt.Errorf("refusing to write back: the secrets backend could not resolve %s that the "+
			"canonical source references, so agentsync cannot verify a resolved secret would not be persisted "+
			"as cleartext into ~/.agentsync. Unlock/restore the vault (`agentsync secrets …`) and retry, or "+
			"edit the canonical source directly", strings.Join(blind, ", "))
	}

	// Fail-closed backstop: re-reference matches by value and cannot tell a
	// moved/rotated secret from a deliberate literal edit, so it can leave a
	// resolved credential as cleartext (a secret moved into a literal-counterpart
	// field, or a rotated value the vault no longer knows). Rather than guess and
	// risk persisting a live credential into ~/.agentsync, refuse the whole
	// write-back and tell the user exactly which fields to resolve (update the
	// vault, or edit the canonical source directly).
	if leaks := secrets.ResidualSecretCleartext(ingested, &cur, sec, secrets.EnvBackend{}); len(leaks) > 0 {
		return res, fmt.Errorf("refusing to write back: a resolved secret would be persisted as "+
			"cleartext into ~/.agentsync at %s — re-reference could not restore the ${secret:…} "+
			"reference (the value was moved or rotated). Update the vault (`agentsync secrets set`) "+
			"and/or edit the canonical source directly, then retry; set AGENTSYNC_ALLOW_PLUGIN_DRIFT "+
			"is NOT a bypass here", strings.Join(leaks, ", "))
	}

	for _, m := range ingested.MCPServers {
		if existing, ok, rerr := source.ReadMCP(home, m.ID); rerr == nil && ok {
			// Agents targeting is never carried by any native dest, so restoring
			// the source value is always right. Enabled, however, IS carried by
			// some dests (Codex reads native `enabled` — issue #152), so only fall
			// back to the source value when the ingest produced none (nil); a
			// captured explicit &true/&false must survive, or a native disable is
			// silently dropped on re-import/reconcile of an already-managed server.
			m.Server.Agents = existing.Server.Agents
			if m.Server.Enabled == nil {
				m.Server.Enabled = existing.Server.Enabled
			}
		}
		if err := source.WriteMCP(home, m.ID, m); err != nil {
			return res, fmt.Errorf("write mcp %s: %w", m.ID, err)
		}
		res.Written = append(res.Written, "mcp/"+m.ID+".toml")
	}

	for _, ls := range ingested.LSPServers {
		if existing, ok, rerr := source.ReadLSP(home, ls.ID); rerr == nil && ok {
			// Same rule as MCP: Agents is source-only, but preserve Enabled only
			// when the ingest carried none, so a dest that ever reports LSP enable
			// state can round-trip it (no adapter does today; kept symmetric).
			ls.Spec.Agents = existing.Spec.Agents
			if ls.Spec.Enabled == nil {
				ls.Spec.Enabled = existing.Spec.Enabled
			}
		}
		if err := source.WriteLSP(home, ls); err != nil {
			return res, fmt.Errorf("write lsp %s: %w", ls.ID, err)
		}
		res.Written = append(res.Written, "lsp/"+ls.ID+".toml")
	}

	// Hooks have no per-entry source-only fields; write all entries for each
	// event as a unit (matching WriteHooks' file-per-event shape).
	byEvent := map[string][]source.Hook{}
	var eventOrder []string
	for _, h := range ingested.Hooks {
		// event is a map key and the hooks/<event>.toml file stem — the raw
		// machine value, not the sanitizing String().
		event := h.Event.Unverified()
		if _, seen := byEvent[event]; !seen {
			eventOrder = append(eventOrder, event)
		}
		byEvent[event] = append(byEvent[event], h)
	}
	for _, event := range eventOrder {
		if err := source.WriteHooks(home, event, byEvent[event]); err != nil {
			return res, fmt.Errorf("write hooks/%s: %w", event, err)
		}
		res.Written = append(res.Written, "hooks/"+event+".toml")
	}

	if opts.Warn != nil {
		// Unresolvable `secret:` refs already forced a refusal above, so anything
		// left here is an `env:` ref the source references but that the env backend
		// couldn't resolve. This warning describes the CURRENT SOURCE's resolvability
		// (`cur`), not the just-written file: re-referencing restores a ${…} form by
		// matching the resolved value, so a field carrying an unresolvable ref may not
		// have been re-referenced (issue #163).
		if missing := secrets.UnresolvedSecretRefs(&cur, sec, secrets.EnvBackend{}); len(missing) > 0 {
			fmt.Fprintf(opts.Warn,
				"warning: the secrets backend could not resolve %s referenced by the canonical source; "+
					"a field carrying one of these may not have been restored to its ${…} reference — "+
					"review the written-back source before committing\n",
				strings.Join(missing, ", "))
		}
	}
	return res, nil
}

// normalizeExtraNumbers rewrites every json.Number in the ingested canonical's
// MCP/LSP Extra passthrough maps (recursively through the project overlay) to
// int64/float64. Adapter ingests decode native JSON/JSONC with UseNumber so an
// unmodeled integer beyond 2^53 survives in memory — but json.Number's
// underlying type is a string, and go-toml marshals it as a TOML *string*
// (`timeout = '30'`), so persisting it unconverted through source.Write* flips
// the value's native type on the next render (30 -> "30"). Extra is the only
// TOML-persisted passthrough surface (Subagent/Command/Skill frontmatter decode
// via DecodeYAML, which already converts), so the funnel normalizes exactly
// here — before the leak backstop, which compares string values and never
// matches a number either way.
func normalizeExtraNumbers(c *source.Canonical) {
	for i := range c.MCPServers {
		if x := c.MCPServers[i].Server.Extra; x != nil {
			c.MCPServers[i].Server.Extra, _ = jsonkeys.ConvertNumbers(x).(map[string]any)
		}
	}
	for i := range c.LSPServers {
		if x := c.LSPServers[i].Spec.Extra; x != nil {
			c.LSPServers[i].Spec.Extra, _ = jsonkeys.ConvertNumbers(x).(map[string]any)
		}
	}
	if c.Project != nil {
		normalizeExtraNumbers(c.Project)
	}
}

// secretKindRefs filters UnresolvedSecretRefs output down to the `secret:` kind
// (vault-backed), which is the only kind the backstop's value prong resolves and
// therefore the only kind whose unresolvability blinds it.
func secretKindRefs(refs []string) []string {
	var out []string
	for _, r := range refs {
		if strings.HasPrefix(r, "secret:") {
			out = append(out, r)
		}
	}
	return out
}

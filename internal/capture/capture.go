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
//  1. re-references resolved secrets back to ${secret:…} against the current
//     source (secrets.ReReferenceCanonical), so a live credential apply wrote
//     into the destination is never persisted into ~/.agentsync;
//  2. preserves source-only fields the rendered destination never carries
//     (MCP/LSP agents + enabled), so write-back can't silently broaden a
//     server's exposure or clear its enablement;
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

	cur, cerr := source.Load(afero.NewOsFs(), home)
	if cerr != nil {
		return res, fmt.Errorf("load canonical source to re-reference secrets: %w; "+
			"refusing to write back — writing now could persist a resolved cleartext "+
			"secret into ~/.agentsync; fix the source error and retry", cerr)
	}
	userHome := paths.HomeDir(paths.OSEnv{})
	sec := secrets.SelectBackend(cur.Config.Secrets, home, userHome)
	secrets.ReReferenceCanonical(ingested, &cur, sec, secrets.EnvBackend{})

	for _, m := range ingested.MCPServers {
		if existing, ok, rerr := source.ReadMCP(home, m.ID); rerr == nil && ok {
			m.Server.Agents = existing.Server.Agents
			m.Server.Enabled = existing.Server.Enabled
		}
		if err := source.WriteMCP(home, m.ID, m); err != nil {
			return res, fmt.Errorf("write mcp %s: %w", m.ID, err)
		}
		res.Written = append(res.Written, "mcp/"+m.ID+".toml")
	}

	for _, ls := range ingested.LSPServers {
		if existing, ok, rerr := source.ReadLSP(home, ls.ID); rerr == nil && ok {
			ls.Spec.Agents = existing.Spec.Agents
			ls.Spec.Enabled = existing.Spec.Enabled
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
		if _, seen := byEvent[h.Event]; !seen {
			eventOrder = append(eventOrder, h.Event)
		}
		byEvent[h.Event] = append(byEvent[h.Event], h)
	}
	for _, event := range eventOrder {
		if err := source.WriteHooks(home, event, byEvent[event]); err != nil {
			return res, fmt.Errorf("write hooks/%s: %w", event, err)
		}
		res.Written = append(res.Written, "hooks/"+event+".toml")
	}

	if opts.Warn != nil {
		if missing := secrets.UnresolvedSecretRefs(&cur, sec); len(missing) > 0 {
			fmt.Fprintf(opts.Warn,
				"warning: could not re-reference secret(s) %s (secrets backend unavailable); "+
					"the written-back source file may contain cleartext — review it before committing\n",
				strings.Join(missing, ", "))
		}
	}
	return res, nil
}

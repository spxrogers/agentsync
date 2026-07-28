package secrets

import (
	"sort"

	"github.com/spxrogers/agentsync/internal/source"
)

// RefLocation identifies the canonical component a secret reference lives on.
// Kind is "mcp", "lsp", or "hook"; ID is the server id or the hook event.
type RefLocation struct {
	Kind string
	ID   string
}

// SecretRefsByComponent returns, per canonical component, the
// ${secret:…}/${env:…} REFERENCES its secret-bearing fields carry — the
// placeholder text, never a resolved value, and no backend is consulted.
//
// It is for diagnostics that must report *that* a reference resolves at a
// location (`agentsync explain <path>`) without emitting any secret material.
// Like every other secret operation it is a thin caller of walkSecretFields, so
// a field added to the one authoritative list is picked up here automatically.
//
// Each component's refs are deduped and sorted; a component with no references
// is absent from the map.
func SecretRefsByComponent(c *source.Canonical) map[RefLocation][]string {
	if c == nil {
		return nil
	}
	seen := map[RefLocation]map[string]bool{}
	walkSecretFields(c, func(loc secretFieldLoc, s string) string {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if len(m) < 3 {
				continue
			}
			switch m[1] {
			case "secret", "env":
			default:
				continue
			}
			key := RefLocation{Kind: loc.kind, ID: loc.id}
			if seen[key] == nil {
				seen[key] = map[string]bool{}
			}
			// m[0] is the whole "${secret:KEY}" placeholder — the reference as
			// authored, which is what a provenance report should echo.
			seen[key][m[0]] = true
		}
		return s
	})
	if len(seen) == 0 {
		return nil
	}
	out := make(map[RefLocation][]string, len(seen))
	for loc, refs := range seen {
		list := make([]string, 0, len(refs))
		for r := range refs {
			list = append(list, r)
		}
		sort.Strings(list)
		out[loc] = list
	}
	return out
}

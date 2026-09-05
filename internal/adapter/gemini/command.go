package gemini

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// geminiCommandFile is the TOML shape of a Gemini custom command
// (`.gemini/commands/<name>.toml`): a `prompt` (the body sent to the model) and
// an optional one-line `description` shown in /help.
type geminiCommandFile struct {
	Description string `toml:"description,omitempty"`
	Prompt      string `toml:"prompt"`
}

// renderCommands projects canonical slash commands into Gemini custom commands
// (`.gemini/commands/<name>.toml`). The canonical body becomes `prompt` and the
// `description` frontmatter carries over; any other frontmatter key (notably
// `argument-hint` and `allowed-tools`) has no Gemini field and is dropped with a
// reported Skip. (Gemini's argument placeholder is `{{args}}` rather than Claude's
// `$ARGUMENTS`/`$1`; the body is written verbatim — placeholder syntax is not
// auto-translated, a documented projection note.)
//
// A Command.Name carrying a forward-slash namespace (e.g. "git/commit", the
// invocation `/git:commit`) is projected into Gemini's subdirectory layout —
// `commands/git/commit.toml` — via filepath.FromSlash; iox.AtomicWrite creates the
// intermediate directory. This inverts ingest's WalkDir capture. NOTE: the guarantee
// is scoped to the ADAPTER round-trip (native→native via ingest+render); the flat
// canonical loader/writer cannot carry a '/'-bearing Name (source.ValidateComponentID
// rejects path separators), so a namespaced command does not survive a full
// import→canonical-source→apply cycle — see docs/capability-matrix.md.
func (a *Adapter) renderCommands(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, cmd := range c.Commands {
		cf := geminiCommandFile{
			Description: fmString(cmd.Frontmatter, "description"),
			Prompt:      cmd.Body,
		}
		if dropped := droppedKeys(cmd.Frontmatter, geminiCommandKnownKeys); len(dropped) > 0 {
			skips = append(skips, adapter.Skip{
				Component: "command",
				Name:      cmd.Name,
				Reason:    "Gemini commands are TOML (description + prompt); dropped " + strings.Join(dropped, ", "),
				Kind:      adapter.SkipReduced,
			})
		}
		body, err := toml.Marshal(cf)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal command %s: %w", cmd.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        adapter.ActionWrite,
			Path:          filepath.Join(p.CommandsDir, filepath.FromSlash(cmd.Name)+".toml"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("commands", filepath.FromSlash(cmd.Name)+".md"),
			MergeStrategy: "replace",
		})
	}
	return ops, skips, nil
}

// geminiCommandKnownKeys are the canonical command frontmatter keys with a Gemini
// TOML home. Anything else (argument-hint, allowed-tools, …) is dropped + reported.
var geminiCommandKnownKeys = map[string]bool{"description": true}

// fmString returns the string value at key in a frontmatter map, or "".
func fmString(fm map[string]any, key string) string {
	s, _ := fm[key].(string)
	return s
}

// droppedKeys returns the sorted frontmatter keys NOT in known.
func droppedKeys(fm map[string]any, known map[string]bool) []string {
	var out []string
	for k := range fm {
		if !known[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

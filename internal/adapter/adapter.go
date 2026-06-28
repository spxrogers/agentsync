// Package adapter declares the interface every per-agent adapter implements.
// The registry holds zero or more concrete implementations; the apply pipeline
// asks each registered adapter to Render a CanonicalModel into FileOps.
package adapter

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// ErrProjectRootRequired is returned by RequireProjectRoot (and thus by every
// scope-resolving adapter method — Render, Ingest, and IngestPlugins) when a
// project-scope call supplies no project root.
var ErrProjectRootRequired = errors.New("adapter: project scope requires a non-empty project root")

// RequireProjectRoot guards the adapter boundary against a project-scope call
// with an empty project root. Every adapter resolves its destinations via a
// ResolvePaths that falls through to USER-scope paths when project == "" — so a
// caller reaching an adapter with (ScopeProject, "") would SILENTLY write the
// project overlay into the user's global config (or read it from there). The
// CLI's resolveScope already guarantees a non-empty root for project scope; this
// is the belt-and-suspenders that turns a future caller's mistake into a loud
// error at the narrowest waist every read/write funnels through, instead of a
// silent wrong-scope I/O. Adapters MUST call it first thing in every method that
// resolves scope-dependent paths: Render, Ingest, and IngestPlugins.
func RequireProjectRoot(scope Scope, project string) error {
	if scope == ScopeProject && project == "" {
		return ErrProjectRootRequired
	}
	return nil
}

// Capability is a bitmask of components an adapter can produce. M1's Claude
// adapter is full-spectrum; M2's OpenCode adapter omits Hook + LSP.
type Capability uint32

const (
	CapMCP Capability = 1 << iota
	CapMemory
	CapSkill
	CapSubagent
	CapCommand
	CapHook
	CapLSP
)

// Scope distinguishes user-level vs project-level apply targets.
type Scope int

const (
	ScopeUser Scope = iota
	ScopeProject
)

func (s Scope) String() string {
	switch s {
	case ScopeProject:
		return "project"
	default:
		return "user"
	}
}

// FileOp describes one destination-side change. Action is "write" or "delete".
// Path is absolute (after AGENTSYNC_TARGET_ROOT redirection).
//
// CONTRACT — Content is ALWAYS JSON for a key-merge op, regardless of the
// destination file's on-disk format. MergeStrategy names the on-disk format the
// adapter's Apply and the render pipeline decode/encode against:
//
//	"replace" (default)   whole-file write; Content is the literal file bytes.
//	"merge-json-keys"     shared JSON   (~/.claude.json, settings.json).
//	"merge-jsonc-keys"    shared JSONC  (opencode.json; comments tolerated).
//	"merge-toml-keys"     shared TOML   (~/.codex/config.toml).
//
// For every merge-* strategy, Content is the JSON projection of the owned
// subtree ({"mcpServers": …} / {"hooks": …} / {"mcp_servers": …}) — the
// pointer-merge "currency" the pipeline reasons over (OwnedKeys synthesis,
// orphan cleanup, per-pointer state hashing, foreign-collision backup are all
// format-agnostic). Only the DESTINATION file is parsed/emitted per strategy
// (render.IsKeyMerge gates the classification; the adapter's Apply does the
// format-specific merge). A new TOML/YAML-backed agent must keep Content JSON,
// not emit the on-disk format here.
type FileOp struct {
	Action        string // "write" | "delete"
	Path          string
	Content       []byte
	Mode          uint32
	SourceID      string   // canonical source path that produced this op
	MergeStrategy string   // "replace" (default) | "merge-json-keys" | "merge-jsonc-keys" | "merge-toml-keys"
	OwnedKeys     []string // JSON pointers owned by agentsync; populated by Apply from state, not Render
}

// SkipKind classifies how much of a component was lost, so consumers never have
// to re-derive the distinction from a component-name convention. The adapter that
// constructs the Skip sets it — it is the only layer that knows whether the whole
// component was dropped or merely reduced. (It replaces the former "-frontmatter"
// component-suffix convention, which encoded the same fact in a string only the
// CLI knew how to parse.)
type SkipKind int

const (
	// SkipKindUnset is the zero value and is INVALID: every constructed Skip MUST
	// set Kind to one of the classifications below. Leaving it unset is a bug,
	// caught by two complementary guards. TestEverySkipLiteralSetsKind
	// (internal/adapter) statically parses every production adapter.Skip literal
	// under internal/ and fails if one omits Kind — reachability-independent, so a
	// skip site gated on a path that is never empty at runtime cannot hide from it.
	// TestEveryAdapterClassifiesSkips (internal/cli) is the behavioral complement:
	// it renders every registered adapter at both scopes and fails if any emitted
	// skip carries SkipKindUnset, while also pinning that both kind values are
	// exercised. Together they close the "no compile-time guard" gap a silent
	// string suffix left open.
	SkipKindUnset SkipKind = iota
	// SkipDropped — the whole component had no native target on this agent and was
	// not emitted at all (e.g. an LSP server on an agent with no LSP concept, a
	// SessionEnd hook on Codex).
	SkipDropped
	// SkipReduced — the component still rendered, minus fields the target agent has
	// no home for (e.g. a subagent's Claude-only tools/color frontmatter).
	SkipReduced
)

// String renders the kind for human notes and the report's machine surface.
func (k SkipKind) String() string {
	switch k {
	case SkipReduced:
		return "reduced"
	case SkipDropped:
		return "dropped"
	default:
		return "unset"
	}
}

// MarshalJSON emits the lowercase string form so `explain --json` carries an
// explicit "kind":"reduced"|"dropped" rather than an opaque integer.
func (k SkipKind) MarshalJSON() ([]byte, error) { return json.Marshal(k.String()) }

// Skip describes a component the adapter could not fully render. Surfaces in the
// translation report and in `apply --strict`'s exit logic. Kind distinguishes a
// whole-component drop from a field-level reduction (the component still
// rendered) and MUST be set explicitly at every construction site.
type Skip struct {
	Component string // "skill" | "subagent" | "command" | etc.
	Name      string
	Reason    string
	Kind      SkipKind
}

// Adapter is the per-agent contract.
type Adapter interface {
	Name() string
	Capabilities() Capability
	Detect() (bool, error)
	// Render projects the resolved canonical (secrets already substituted to
	// cleartext, or wrapped templated for a preview) into destination FileOps.
	// It accepts only secrets.Resolved — never a raw source.Canonical — so the
	// render egress is type-distinct from the dest->source write path.
	//
	// CONTRACT — at ScopeProject the project root MUST be non-empty. Any adapter
	// method that resolves scope-dependent destinations MUST call
	// RequireProjectRoot first thing and return ErrProjectRootRequired for an
	// empty root, so a project-scope call can never silently fall through to
	// user-scope destinations. That covers Render and Ingest here, and
	// IngestPlugins on the PluginIngester extension below; the three real
	// adapters do all of these. (A pure no-op adapter that resolves no paths has
	// nothing to fall through to.) See RequireProjectRoot.
	Render(r secrets.Resolved, scope Scope, project string) ([]FileOp, []Skip, error)
	Ingest(scope Scope, project string) (source.Canonical, error)
	// KeyMergeStrategy returns this adapter's single JSON-key-merge strategy
	// ("merge-json-keys" for claude, "merge-jsonc-keys" for opencode), or ""
	// if the adapter does not merge keys. The render layer needs it to
	// synthesize cleanup ops that remove now-orphaned owned keys from a
	// destination when the source section became empty (no render op exists
	// to carry the removal). It MUST be exact — applying the wrong strategy to
	// a JSONC file would parse it as strict JSON and clobber the file.
	KeyMergeStrategy() string
	// Apply executes ops against destinations. Adapters MUST route every
	// destination write through w.Write / w.Delete rather than calling
	// iox.AtomicWrite or os.Remove directly — w owns the foreign-collision
	// backup invariant. A forbidigo lint rule in .golangci.yml backs this
	// up at build time by failing direct iox.AtomicWrite calls outside the
	// allowed packages.
	Apply(ops []FileOp, w DestWriter) error
}

// NativeSource describes where a natively-registered marketplace is fetched
// from, in the agent's own vocabulary. `import` maps it onto an agentsync
// marketplace source. Fields mirror Claude's extraKnownMarketplaces `source`
// object; only the fields relevant to a given Type are populated.
type NativeSource struct {
	Type    string // "github" | "git" | "url" | "npm" | "file" | "directory" | …
	Repo    string // "owner/repo" for github
	URL     string // for git/url
	Path    string // for file/directory
	Ref     string // branch/tag/commit
	Package string // for npm
}

// NativeMarketplace is a marketplace registered in an agent's native config,
// as discovered by `import`. ID is the agent's own marketplace identifier (the
// "@<marketplace>" half of a plugin reference), which need not match the name
// declared inside the fetched marketplace.json.
type NativeMarketplace struct {
	ID     string
	Source NativeSource
}

// NativePlugin is a plugin recorded in an agent's native config, as discovered
// by `import`. Enabled is false for an explicitly-disabled entry.
//
// Name is untrusted.Text: a plugin author influences the name the agent persists
// in its own config, and `status`/`doctor` read it back via IngestPlugins and
// print it in their "undeclared native plugins" notes, so it must sanitize on
// display by construction (its String() runs untrusted.Sanitize). Reach the raw
// bytes via Unverified() only for non-display use — the plugins/<name>.toml stem,
// a map key, ValidateComponentID. The reflection-based TestUntrustedFieldGuard
// (internal/adapter) fails the build if a new string field here ships
// unclassified. See docs/architecture.md §7 and SECURITY.md.
//
// MarketplaceID stays a plain string: unlike Name it is not rendered into the
// status/doctor layout this hardening targets; its only display sites are
// `import`'s warn diagnostics, the same plain-string surface as the sibling
// NativeMarketplace / NativeSource fields (marketplace ids, source types). That
// whole import-diagnostics surface is a deliberate plain-string subset — promote
// these together if it is ever hardened.
type NativePlugin struct {
	Name          untrusted.Text // the "<plugin>" half of a plugin reference
	MarketplaceID string         // the marketplace it was installed from
	Enabled       bool
}

// PluginIngester is an OPTIONAL extension to Adapter: an agent that tracks
// installed plugins + marketplaces in its native config implements it so
// `import` can capture them into the canonical source. `import` type-asserts for
// it; an adapter that does not implement it simply imports no plugins.
//
// **Read-only by design — asymmetric on purpose.** PluginIngester has no
// `Render`-side counterpart, and `Adapter.Render` MUST NOT emit plugin-
// enablement or marketplace-registry metadata back into the native config. The
// invariant for every adapter (current and future) is:
//
//	import   — read enable-state + marketplace sources from native config
//	apply    — fan out the plugin's COMPONENTS (skills, MCP, commands,
//	           subagents, hooks, LSP) to the agent's native component paths
//	           via the normal Render path. Plugin identity dissolves at the
//	           projection boundary.
//
// Once a plugin's skills land at `~/.claude/skills/<name>/`, its MCP server in
// `mcpServers`, its commands in `~/.claude/commands/<name>.md`, etc., the
// consumer agent reads them as regular components — it doesn't need plugin-
// manager metadata to use them. Writing enable-state back would (1) ping-pong
// against the user's own `/plugin disable` in the agent's UI on every apply,
// (2) blur ownership between agentsync (canonical source of truth) and the
// agent's plugin manager, and (3) double-install with the agent's own plugin
// install dir.
//
// The PluginIngester interface is kept off the core Adapter because the
// canonical schema does not otherwise depend on a native plugin concept
// (OpenCode has no plugins; the Cursor adapter has them but its enable-state
// location is undocumented, so it implements no PluginIngester yet). See
// `docs/architecture.md` § "PluginIngester (read-only)" for the full rationale.
type PluginIngester interface {
	// IngestPlugins resolves scope-dependent paths (it reads the agent's native
	// config, which differs per scope), so — like Render/Ingest — it MUST call
	// RequireProjectRoot first and return ErrProjectRootRequired for a
	// project-scope call with no root, even though import only ever calls it at
	// user scope today (plugins are a user-scope concept).
	IngestPlugins(scope Scope, project string) ([]NativeMarketplace, []NativePlugin, error)
}

// VersionedDirs is an OPTIONAL extension to Adapter: an adapter that writes into
// one or more on-disk directories declares them so the apply tail can git-init and
// checkpoint those directories as a local-only rollback history (issue #118). An
// adapter with no versionable directory (e.g. noop) does not implement it.
//
// VersionRoots is READ-ONLY and user-scope-focused — it reports the directories to
// back up; it does not widen the Render/Apply contract. The unit of versioning is
// the DIRECTORY, not the agent: an adapter returns its own config dir PLUS any
// SHARED cross-agent dir it writes into (e.g. Codex and several breadth agents all
// write skills to ~/.agents/skills; OpenCode writes skills to ~/.claude/skills).
// The apply tail unions these across all enabled adapters, drops any root nested
// under another (no repo inside a repo), and de-duplicates — so a shared dir is
// versioned exactly ONCE no matter how many adapters target it.
//
// At ScopeProject it MUST return nil: project destinations live inside the user's
// own project repo, left to that repo's source control. Each returned path is
// absolute (after AGENTSYNC_TARGET_ROOT redirection), matching FileOp.Path. A deep
// agent may also write a stray top-level file outside any returned dir (Claude's
// ~/.claude.json); those are intentionally NOT versioned — agentsync never inits a
// repo at $HOME. See docs/architecture.md.
type VersionedDirs interface {
	VersionRoots(scope Scope, project string) []string
}

// NonEmptyDirs returns the non-empty arguments as a slice — a small shared helper
// for adapters assembling their VersionRoots from a Paths struct whose fields may
// be "" at a given scope.
func NonEmptyDirs(dirs ...string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// WarnEmitter is an OPTIONAL extension to Adapter: an adapter that emits
// Ingest warnings implements it to let callers redirect the stream away
// from the default (os.Stderr). Implementors are SOURCES of warnings that
// accept a sink — the name follows the PluginIngester precedent of
// describing what the implementor does, not the parameter the method
// takes. Kept off the core Adapter so adapters that emit no warnings
// aren't forced to implement a setter they'll never use; ui.WarnWriter
// type-asserts for it, so callers pass any adapter and let the structural
// match decide.
//
// Implementations MUST:
//
//   - Treat a nil writer as "reset to default" — subsequent warnings go to
//     os.Stderr (or whatever the implementor's pre-SetStderr default was),
//     not to io.Discard or any silent sink. Pinned per-adapter by
//     TestSetStderr_NilResetsToDefault tests that capture os.Stderr and
//     assert the warning lands there.
//   - Not panic on a nil writer.
//
// Configuring stderr is meant to happen BEFORE Ingest runs. Today's
// adapters snapshot the writer at Ingest entry (warn := a.stderr()), so a
// SetStderr call mid-Ingest is ignored for the remainder of that call.
// Use RouteTo-before-Ingest (the import pattern); don't depend on dynamic
// switching during a single Ingest invocation.
type WarnEmitter interface {
	SetStderr(w io.Writer)
}

// DestWriter is the single funnel for any write that targets a native agent
// destination file (~/.claude.json, ~/.claude/agents/*.md, ~/.config/opencode/*,
// etc). It enforces the foreign-collision backup invariant: a pre-existing
// destination that agentsync does not yet own is copied to
// <home>/.state/backups/<ts>/<original-path> before being overwritten.
//
// Adapters MUST use DestWriter instead of calling iox.AtomicWrite or
// os.Remove directly. The forbidigo lint rule in .golangci.yml will fail
// any direct iox.AtomicWrite call outside the allowed packages, so a new
// adapter or a new write path inside an existing adapter cannot silently
// regress the backup guarantee.
type DestWriter interface {
	// Write writes finalBytes to op.Path, after backing up any pre-existing
	// foreign content. For replace ops, finalBytes is op.Content. For merge
	// ops, the adapter performs its merge first (claude → jsonkeys.MergeKeys;
	// opencode → hujson.MergeJSONC) and passes the post-merge bytes here;
	// the writer uses op.Content (ours pre-merge) plus op.OwnedKeys to
	// detect per-key collisions for the backup decision.
	Write(op FileOp, finalBytes []byte) error

	// Delete removes op.Path. No backup — agentsync only deletes paths it
	// already owns per state. Idempotent on missing files.
	Delete(op FileOp) error
}

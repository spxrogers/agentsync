package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// newCheckCmd is `agentsync check` — the CONFIG validator. It was named
// `verify` until #200 F8, which split the overlap with `doctor` along the axis
// the two actually differ on: doctor answers "is this MACHINE ready" (paths,
// adapter detection, backend reachability, destination git state), check
// answers "is this CONFIG valid" (schema, references, scope-aware). Folding
// check into `doctor --config` was considered and declined — check is
// scope-aware and doctor is not, and a project-scope config lint has no
// business living inside a machine readiness report.
func newCheckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "validate the canonical source: schema lint + secret-reference resolvability",
		Long: `check validates your CONFIG: it schema-lints the canonical source and
resolves every ${secret:…} / ${env:…} reference it carries.

It is scope-aware — --scope project / --project <path> lints a project tree
against the inherited user secrets backend.

Its sibling is 'agentsync doctor', which validates your MACHINE instead:
PATH, home/state writability, adapter detection, the secrets backend, and
destination git-backup state. Reach for doctor when something is set up
wrong, and check when something is written wrong.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fs := afero.NewOsFs()
			// home is the USER agentsync home. It is both the user-scope source
			// root AND the anchor for relative secrets paths (identity_file /
			// file) at EVERY scope: project trees inherit the user's [secrets]
			// config and resolve against the same vault, exactly as apply does, so
			// check and apply never disagree on which key/file to read.
			home := paths.AgentsyncHome(paths.OSEnv{})
			userHome := paths.HomeDir(paths.OSEnv{})

			// Resolve scope ONCE — resolveScope may prompt interactively when cwd
			// sits inside a project tree and no scope was given, so calling it
			// twice (here and again inside a shared loader) would prompt twice.
			sc, projectRoot, err := resolveScope(cmd, noInputFlag(cmd))
			if err != nil {
				return err
			}

			// The source root the init/half-init guards below check: the project
			// tree at project scope, the user home otherwise. resolveScope already
			// guarantees a project tree's .agentsync/ DIRECTORY exists; the guards
			// add the agentsync.toml check so `check` never reports a false "ok" on
			// a half-initialized tree (source.Load tolerates a missing config).
			sourceRoot := home
			if sc == adapter.ScopeProject {
				sourceRoot = project.Home(projectRoot)
			}
			if err := requireInitializedSource(sourceRoot, sc); err != nil {
				return err
			}

			// Load the source for this scope: the user base, plus the project
			// overlay at project scope so the project's ${secret:…}/${env:…}
			// references resolve against the inherited user [secrets] config —
			// mirroring apply's merged render. check deliberately does NOT project
			// marketplace plugins; it validates the source the user authors.
			c, err := source.Load(fs, home)
			if err != nil {
				return fmt.Errorf("check: %w", err)
			}
			if sc == adapter.ScopeProject {
				pc, perr := source.Load(fs, sourceRoot)
				if perr != nil {
					return fmt.Errorf("check: load project source %s: %w", sourceRoot, perr)
				}
				// check lints the source a project-scope apply would consume, so
				// it must reject the same undeclared-agents state apply rejects.
				if err := requireProjectAgents(pc, sourceRoot); err != nil {
					return err
				}
				c = project.Merge(c, pc)
			}
			for name := range c.Config.Agents {
				if err := validateAgent(name); err != nil {
					// %q, not %s: name is a config-derived [agents.<name>] TOML key
					// and a quoted key can carry raw ESC/bidi bytes; %q escapes them
					// so a shared config can't inject terminal escapes here (matches
					// the agent-name print convention in agent.go/status.go/revert.go).
					return fmt.Errorf("agents.%q: %w", name, err)
				}
			}
			if err := secretsProblems(c.Config.Secrets, home, userHome); err != nil {
				return err
			}
			// Reference checking, two layers (issue #171):
			//   1. SHAPE — both modes. A malformed ref (${secret:} empty key,
			//      missing colon, illegal char) is flagged here because the strict
			//      resolver silently passes it through as literal text; before this
			//      ran online too, a green local `check` ("all references resolve")
			//      could contradict a red offline CI check on the same config.
			//   2. RESOLVABILITY — online (default) only: every ${secret:…}/${env:…}
			//      resolves against the live backends, catching a well-shaped-but-
			//      unresolvable name (a typo'd key, a missing vault entry). Offline
			//      (AGENTSYNC_ALLOW_OFFLINE_VERIFY=1, the documented CI path without
			//      an age key) cannot resolve, so it stops at shape.
			offline := os.Getenv("AGENTSYNC_ALLOW_OFFLINE_VERIFY") == "1"
			if bad := secrets.MalformedSecretRefs(&c); len(bad) > 0 {
				// bad is []untrusted.Text — untrusted.Join sanitizes each token
				// on display, so a config-crafted ESC/control byte in a malformed
				// ref can't inject terminal escapes here (issue #171 / #93).
				hint := ""
				if offline {
					hint = " (offline mode checks reference shape only; run without " +
						"AGENTSYNC_ALLOW_OFFLINE_VERIFY=1 to also check resolvability)"
				}
				return fmt.Errorf("check: malformed secret/env reference(s): %s%s",
					untrusted.Join(bad, ", "), hint)
			}
			if !offline {
				secBackend := secrets.SelectBackend(c.Config.Secrets, home, userHome)
				envBackend := secrets.EnvBackend{}
				if _, err := secrets.SubstituteCanonical(c, secBackend, envBackend); err != nil {
					return fmt.Errorf("check ${secret:}/${env:} resolution: %w", err)
				}
			}
			if offline {
				success(cmd, ui.EmojiSuccess, "schema valid; reference shapes valid (offline — resolvability not checked)")
			} else {
				success(cmd, ui.EmojiSuccess, "schema valid; all references resolve")
			}
			return nil
		},
	}
	markScopeAware(cmd)
	return cmd
}

// requireInitializedSource fails `check` with an actionable error when the source
// root for the chosen scope is absent or half-initialized (present but missing
// agentsync.toml). source.Load tolerates both and returns an empty model, so
// without this guard `check` would print a false "ok: schema valid" on a tree
// that was never set up. The message points at the scope-appropriate `init`.
func requireInitializedSource(root string, sc adapter.Scope) error {
	label := "agentsync home"
	initCmd := "`agentsync init`"
	if sc == adapter.ScopeProject {
		label = "project source tree"
		initCmd = "`agentsync init --scope project`"
	}
	st, err := probeSourceInit(root)
	switch st {
	case sourceInitOK:
		return nil
	case sourceInitRootMissing:
		return fmt.Errorf("%s %s does not exist; run %s first", label, root, initCmd)
	case sourceInitRootNotDir:
		return fmt.Errorf("%s %s exists but is not a directory; move it aside and run %s", label, root, initCmd)
	case sourceInitConfigMissing:
		return fmt.Errorf("%s %s is missing agentsync.toml (half-initialized); run %s", label, root, initCmd)
	case sourceInitConfigNotFile:
		return fmt.Errorf("%s %s has an agentsync.toml that is not a regular file (half-initialized); run %s",
			label, root, initCmd)
	default: // sourceInitRootUnreadable, sourceInitConfigUnreadable
		return fmt.Errorf("check: stat %s: %w", root, err)
	}
}

// secretsProblems maps the SINGLE [secrets] validator's hard failures onto
// check's one error-or-nil verdict. check does not decide what a valid
// [secrets] block is — secrets.ValidateConfig does, in the same package as the
// SelectBackend apply resolves through and folding the backend name through
// the same NormalizeBackend, so `backend = "AGE"` can no longer apply cleanly
// and fail check (issue #228). Where the validator is deliberately STRICTER
// than apply it stays so: an unrecognised backend fails here, while apply
// degrades it to NopResolver and errors only at the first ${secret:…}.
//
// `doctor` renders from this same validator now, so check and doctor can no
// longer reach different verdicts on one [secrets] block
// (TestSecretsValidationParity pins that). The `secret` subcommands — edit,
// get, set, list, remove — gate on secrets.RequireAgeVault instead: a
// deliberately DIFFERENT and narrower rule, because they manage the age vault
// itself, so `backend = "env"` is a correct refusal for them (doctor's refusal
// of the same value was the #228 bug — it only reports). What they no longer
// keep is a private idea of how a backend name is spelled: RequireAgeVault
// folds it through the same NormalizeBackend, so the `backend = "AGE"` apply
// resolves is accepted there too. Nothing in internal/cli decides what a valid
// [secrets] block is any more — internal/secrets owns both rules, and check,
// doctor and the `secret` group are its consumers.
//
// Only SeverityFail findings become an error. SeverityWarn (a vault that has
// not been created yet) and the passing tiers are for a REPORT surface to
// render: check's contract is "is this config valid", and an unwritten vault
// is not invalid config.
//
// Each failure is labelled with its [secrets] key. ValidateConfig carries the
// key in Finding.Field precisely because several messages do not name it
// themselves — most sharply the vault path, which is DEFAULTED when
// [secrets].file is unset, so an unlabelled "<path> — not readable" would
// print a path the user never wrote with no clue which key produced it.
func secretsProblems(cfg source.SecretsConfig, agentsyncHome, userHome string) error {
	var msgs []string
	for _, f := range secrets.ValidateConfig(cfg, agentsyncHome, userHome) {
		if f.Severity != secrets.SeverityFail {
			continue
		}
		// f.Message is untrusted.Text, so %s invokes its sanitizing String()
		// and a crafted [secrets].identity_file cannot inject terminal escapes
		// into this error (issue #93/#171). f.Field is a validator constant.
		msgs = append(msgs, fmt.Sprintf("%s: %s", f.Field, f.Message))
	}
	if len(msgs) == 0 {
		return nil
	}
	return fmt.Errorf("check secrets: %s", strings.Join(msgs, "; "))
}

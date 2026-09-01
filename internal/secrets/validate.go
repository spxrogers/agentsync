package secrets

import (
	"fmt"
	"os"
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// The [secrets].backend values agentsync understands. These are the exact
// strings SelectBackend switches on AFTER NormalizeBackend.
const (
	BackendAge = "age"
	BackendEnv = "env"
)

// NormalizeBackend folds a raw [secrets].backend value to the form
// SelectBackend switches on.
//
// It is strings.ToLower and NOTHING ELSE. In particular it does NOT trim
// whitespace, because SelectBackend does not either: trimming here would make a
// validator bless a `" age"` that apply then resolves as NopResolver — the
// mirror image of the bug this function exists to kill. SelectBackend and
// ValidateConfig both route through it, so the resolver apply renders through
// and the validator that gates it cannot drift on backend spelling. Backend
// names are therefore matched CASE-INSENSITIVELY on both sides: `"AGE"`,
// `"Age"` and `"age"` are one value. (issue #228)
func NormalizeBackend(b string) string { return strings.ToLower(b) }

// Severity ranks a Finding.
//
// The four tiers exist so that a report surface (`agentsync doctor`) can derive
// its whole Secrets section from this validator — including the lines that pass
// — instead of restating which fields exist, in what order, and what counts as
// healthy. Two independently hand-written copies of exactly that is how `check`
// and `doctor` came to disagree about the same block (issue #228). An
// error-or-nil surface (`agentsync check`) has one verdict to give and acts on
// SeverityFail alone.
type Severity int

const (
	// SeverityOK — the field is configured correctly.
	SeverityOK Severity = iota
	// SeverityInfo — there is nothing to check (no [secrets] block at all).
	SeverityInfo
	// SeverityWarn — usable now, but something is missing or will bite later.
	SeverityWarn
	// SeverityFail — the configuration is broken.
	SeverityFail
)

// String renders a severity for test failures and debugging. It is NOT the
// terminal rendering: a report surface picks its own glyph and colour per tier.
func (s Severity) String() string {
	switch s {
	case SeverityOK:
		return "ok"
	case SeverityInfo:
		return "info"
	case SeverityWarn:
		return "warn"
	case SeverityFail:
		return "fail"
	}
	return fmt.Sprintf("Severity(%d)", int(s))
}

// Field names the [secrets] key a Finding is about, so a caller can label or
// group findings — a padded report column, say. It is not redundant with the
// message: several messages carry no field name of their own (an OK recipient's
// "set", a bare resolved path), so a surface that drops the Field has to supply
// the key itself.
type Field string

const (
	FieldBackend      Field = "backend"
	FieldRecipient    Field = "recipient"
	FieldIdentityFile Field = "identity_file"
	FieldFile         Field = "file"
)

// Finding is one observation about the [secrets] block.
type Finding struct {
	Field    Field
	Severity Severity
	// Message is display-safe BY CONSTRUCTION: untrusted.Text sanitizes on
	// String(), which fmt's %s/%v invoke, so a [secrets].identity_file / file
	// path carrying raw ESC or bidi bytes — these are shareable dotfile values —
	// cannot inject terminal escapes into an error or a report that prints the
	// Message through fmt (issue #93/#171).
	//
	// The whole formatted message is wrapped ONCE, rather than each path
	// operand being wrapped individually. That is deliberate and stronger: a
	// *fs.PathError's own Error() re-embeds its raw path, so the per-operand
	// style leaked the path a second time through %v (see the WRAPPED-ERROR
	// HAZARD note in the untrusted package doc). Nothing here is %w-wrapped, so
	// the Text owns the entire string.
	Message untrusted.Text
}

// finding builds a Finding, wrapping the formatted message exactly once.
func finding(f Field, s Severity, format string, a ...any) Finding {
	return Finding{Field: f, Severity: s, Message: untrusted.Wrap(fmt.Sprintf(format, a...))}
}

// ValidateConfig is the SINGLE definition of a valid [secrets] block.
//
// It lives here, beside SelectBackend, because a validator that disagrees with
// the resolver it gates is worse than no validator: before #228 the [secrets]
// contract was written out seven times across internal/cli (`check`, `doctor`,
// and five `secret`-subcommand gates), and those copies disagreed with each
// other AND with apply — `backend = "env"` passed `check` and failed `doctor`,
// and `backend = "AGE"` applied cleanly while failing both. This function is
// the one definition they collapse onto.
//
// It emits AT MOST ONE finding per Field, in backend → recipient →
// identity_file → file order, so a caller can render one line per field in a
// stable order. agentsyncHome anchors relative identity/vault paths and
// userHome expands ${env:HOME} / a leading ~ — the same two anchors
// SelectBackend uses, passed the same way (the USER agentsync home at every
// scope, since a project tree inherits the user's vault).
//
// It is built for two consumption shapes: an error-or-nil surface errors on the
// SeverityFail findings and ignores the rest; a report surface renders every
// finding as a line and counts the SeverityFail ones as issues.
func ValidateConfig(cfg source.SecretsConfig, agentsyncHome, userHome string) []Finding {
	switch NormalizeBackend(cfg.Backend) {
	case "":
		return []Finding{finding(FieldBackend, SeverityInfo, "not configured (skip — no [secrets] block)")}
	case BackendEnv:
		// The env backend keeps no vault and no identity, so recipient /
		// identity_file / file are not its business — nothing else to check.
		return []Finding{finding(FieldBackend, SeverityOK, "env (${secret:…} resolves from the environment)")}
	case BackendAge:
		// fall through
	default:
		// Deliberately STRICTER than apply, which degrades an unrecognised
		// backend to NopResolver and only fails later, at the first
		// ${secret:…}, with "no secrets backend configured". Catching the typo
		// here is the whole point of a validator.
		return []Finding{finding(FieldBackend, SeverityFail,
			"unsupported backend %q (want %q or %q)", cfg.Backend, BackendAge, BackendEnv)}
	}

	return []Finding{
		finding(FieldBackend, SeverityOK, "%s", BackendAge),
		validateRecipient(cfg),
		validateIdentity(cfg, agentsyncHome, userHome),
		validateAgeFile(cfg, agentsyncHome, userHome),
	}
}

// validateRecipient checks [secrets].recipient.
//
// Note that the age READ path never uses it — SelectBackend builds an
// AgeBackend from the vault and identity paths alone, and decryption needs the
// identity. recipient is required by the WRITE path: `secret set`, `secret
// edit` and `secret remove` all re-encrypt through Encrypt(plain,
// cfg.Recipient, …). An age config without one is a vault you can read and
// never update, which is broken, not valid — so this stays a hard failure.
func validateRecipient(cfg source.SecretsConfig) Finding {
	if cfg.Recipient == "" {
		return finding(FieldRecipient, SeverityFail,
			"[secrets].recipient is required for backend = %q", BackendAge)
	}
	return finding(FieldRecipient, SeverityOK, "set")
}

// validateIdentity checks [secrets].identity_file, resolving it exactly as
// apply does (ResolveIdentityFile) and gating it with the same
// CheckIdentityPermissions apply uses — which honours
// AGENTSYNC_AGE_SKIP_PERM_CHECK=1 and the Windows ACL caveat.
func validateIdentity(cfg source.SecretsConfig, agentsyncHome, userHome string) Finding {
	if cfg.IdentityFile == "" {
		return finding(FieldIdentityFile, SeverityFail,
			"[secrets].identity_file is required for backend = %q", BackendAge)
	}
	path := ResolveIdentityFile(cfg, agentsyncHome, userHome)
	info, err := os.Stat(path)
	if err != nil {
		return finding(FieldIdentityFile, SeverityFail, "%s — not readable (%v)", path, err)
	}
	if permErr := CheckIdentityPermissions(path); permErr != nil {
		return finding(FieldIdentityFile, SeverityFail,
			"%s — too permissive (%v); chmod 600 (or set %s=1)", path, info.Mode().Perm(), SkipPermCheckEnv)
	}
	return finding(FieldIdentityFile, SeverityOK, "ok (%s)", path)
}

// validateAgeFile checks the encrypted vault's location.
//
// An ABSENT vault is only a warning: it is legitimate on a fresh install where
// the user has not run `secret set` yet. Any OTHER stat failure is a hard
// failure — a path that is blocked rather than missing (a non-directory parent,
// a permission denial) is not "not yet created", and reporting it as such is
// what let doctor exit 0 on a config `check` exits 1 on (issue #228).
func validateAgeFile(cfg source.SecretsConfig, agentsyncHome, userHome string) Finding {
	path := ResolveAgeFile(cfg, agentsyncHome, userHome)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return finding(FieldFile, SeverityWarn,
				"%s — not yet created (run `agentsync secret edit` to author)", path)
		}
		return finding(FieldFile, SeverityFail, "%s — not readable (%v)", path, err)
	}
	return finding(FieldFile, SeverityOK, "%s", path)
}

// VaultAccess says which age-vault operation a `secret` subcommand performs.
type VaultAccess int

const (
	// VaultRead only reads the vault (`secret get`, `secret list`).
	VaultRead VaultAccess = iota
	// VaultWrite re-encrypts the vault (`secret set`, `secret edit`,
	// `secret remove`), decrypting it first when one exists, so it
	// additionally needs a recipient. It does NOT always read: against a
	// not-yet-created vault these commands write without decrypting anything.
	VaultWrite
)

// RequireAgeVault reports why cfg cannot serve op, or nil.
//
// This is a DIFFERENT rule from ValidateConfig, deliberately: the `secret`
// subcommands manage the age-encrypted vault specifically, so `backend = "env"`
// is a legitimate refusal for them even though it is a perfectly valid
// configuration for apply. What they must NOT do is invent their own idea of
// how a backend name is spelled — five hand-written copies of
// `cfg.Backend != "age"` refused a `backend = "AGE"` that apply resolves
// cleanly (issue #228). Routing through NormalizeBackend fixes all five at
// once, so these commands accept an age backend in any casing.
//
// op is the command as the user types it today ("secret set", "secret edit"),
// so a refusal names a live command rather than a retired spelling — two of the
// copies this replaces still said `secrets edit` / `secrets set`, renamed to the
// singular group with no alias in #200 F4.
func RequireAgeVault(cfg source.SecretsConfig, op string, access VaultAccess) error {
	if NormalizeBackend(cfg.Backend) != BackendAge {
		// %q escapes control bytes, so a config-derived backend name cannot
		// smuggle a terminal escape through this message.
		return fmt.Errorf("%s manages the age-encrypted vault; set backend = %q in agentsync.toml [secrets] (found %q)",
			op, BackendAge, cfg.Backend)
	}
	// Every one of these commands decrypts the vault when one exists, and
	// decryption needs the identity — and a backend = "age" config without an
	// identity_file is invalid anyway (ValidateConfig fails it). Refusing here
	// names the field: without it the failure surfaced deep inside Decrypt as
	// `read identity : no such file or directory`, and against a not-yet-created
	// vault it did not surface at all — `secret list` printed "(vault is empty)".
	if cfg.IdentityFile == "" {
		return fmt.Errorf("%s requires [secrets].identity_file in agentsync.toml", op)
	}
	// Only the re-encrypting operations need a recipient — Encrypt parses it as
	// an X25519 recipient and an empty one fails there.
	if access == VaultWrite && cfg.Recipient == "" {
		return fmt.Errorf("%s requires [secrets].recipient in agentsync.toml", op)
	}
	return nil
}

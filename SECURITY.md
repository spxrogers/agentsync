# Security Policy

## Reporting a vulnerability

Please report security issues **privately** via GitHub's "Report a
vulnerability" button under the repository's **Security** tab
(Security Advisories), rather than opening a public issue. We aim to
acknowledge reports within a few days.

When reporting, include the affected version (`agentsync --version`), repro
steps, and impact.

## Scope and threat model

agentsync reads and writes coding-agent configuration on a single machine and
can resolve secrets into native config files. Areas of particular interest:

- **age-encrypted secrets** (`internal/secrets`): the age identity file must
  be `0600`; agentsync refuses to read a group/other-readable identity unless
  `AGENTSYNC_AGE_SKIP_PERM_CHECK=1`. agentsync never writes decrypted secret
  values to durable storage and redacts resolved `${secret:...}` values in
  `agentsync diff`.
- **Untrusted marketplaces / plugins**: a marketplace or plugin you add is
  treated as untrusted input. The npm/relative fetchers reject every symlink;
  the git fetcher allows a symlink only when its resolved target stays inside the
  fetched tree (an escaping link — `skills/x -> /etc` — is refused, as is an
  unresolvable one), so a plugin's legitimate in-tree link is kept without
  letting a read escape the cache. Fetchers also cap decompressed tarball size
  and bound manifest-listed component paths and names to the plugin cache. Each
  installed plugin is pinned with a content hash over its *entire* cache tree
  (every projected component body, not just `plugin.json`; a cached symlink is
  hashed by its target path), so a tampered or re-uploaded body — or a swapped
  link target — is detected at apply rather than silently consumed. A plugin's
  id, version, and the component names it supplies are also untrusted *display*
  input: agentsync sanitizes plugin- and native-config-derived metadata with
  `ui.Sanitize` at every display boundary before it reaches the terminal,
  stripping C0/C1 control bytes (ESC, CR, LF, …) so a hostile plugin cannot
  smuggle terminal escape sequences (recoloring the screen, spoofing rows, or
  setting the window title) into agentsync's own output. `ui.Sanitize` also
  strips the printable-but-deceptive format runes: the explicit Unicode bidi
  controls (U+202A–U+202E, U+2066–U+2069 — the "Trojan Source" / CVE-2021-42574
  class that can visually reorder a plugin id to read as a trusted name) and the
  zero-width / invisible runes (U+200B–U+200D, U+FEFF) that can hide or pad a
  name. Ordinary right-to-left scripts (Arabic, Hebrew) and CJK are preserved
  byte-for-byte — only the explicit override/isolate controls an attacker would
  inject are removed, never the implicit direction of legitimate letters.
  Display *width* is explicitly out of scope: combining marks and wide-width
  runes can still skew agentsync's rune-counted column alignment, a purely
  cosmetic limitation (documented on `ui.Pad`), not a spoofing vector. The
  invariant — not a fixed list of commands — is that any terminal rendering of
  such metadata is sanitized at the print boundary (today that spans `explain`,
  `apply`, `plugin`, `marketplace`, `update`, `status`, and `doctor`).
  `explain --json` keeps ids raw (a machine contract where the consumer owns
  escaping).
- **Destination writes**: writes are atomic and refuse to clobber symlinked
  destinations by default; pre-existing foreign files are backed up before
  overwrite.

## Sensitive files

Do not commit your age identity file, decrypted secrets, or
`~/.agentsync/.state/` to a public repository. `agentsync secrets set
--stdin` keeps secret values off `argv`, shell history, and process listings.

## Supported versions

Until the first stable (`v1.0.0`) release, only the latest tagged version is
supported for security fixes.

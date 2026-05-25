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
  treated as untrusted input. Fetchers reject symlinks (npm/relative/git),
  cap decompressed tarball size, and bound manifest-listed component paths and
  names to the plugin cache. Each installed plugin is pinned with a content
  hash over its *entire* cache tree (every projected component body, not just
  `plugin.json`), so a tampered or re-uploaded body is detected at apply rather
  than silently consumed.
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

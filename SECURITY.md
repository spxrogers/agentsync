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
  id and the component names it supplies are also untrusted *display* input:
  agentsync sanitizes them (`ui.Sanitize`) on the surfaces that render them to
  the terminal — `explain`, the translation report `apply` prints, and the
  `plugin install` status line — stripping C0/C1 control bytes (ESC, CR, LF, …)
  so a hostile plugin cannot smuggle terminal escape sequences (recoloring the
  screen, spoofing rows, or setting the window title) into agentsync's own
  output. `explain --json` keeps ids raw (a machine contract where the consumer
  owns escaping).
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

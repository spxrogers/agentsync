# agentsync M6 — Secrets (env + age)

> Conventions in [`overview`](2026-05-04-agentsync-v1.0-overview.md). Builds on M0–M5.

**Goal:** age-encrypted file at `~/.agentsync/secrets/secrets.age`; `${secret:foo.bar}` resolution at apply-time; `secrets edit/get/set` commands; in-memory cleartext only — never persisted to disk except via the user's $EDITOR's tmp file.

**Architecture:** New `internal/secrets` package. `Resolver` interface so the value-substitution callsite is independent of backend (env, age, future 1Password). `Resolver.Resolve("github.token")` returns the cleartext for substitution. apply pipeline walks `source.Canonical`'s string-valued fields and replaces `${secret:foo.bar}` and `${env:FOO}` references in-place before passing to adapters.

**Tech stack:** `filippo.io/age` (vendored library; no `age` CLI required).

---

## Files

```
NEW:
internal/secrets/
├── secrets.go         # Resolver interface, errors
├── env.go             # EnvBackend (resolves ${env:FOO})
├── age.go             # AgeBackend (encrypts/decrypts secrets.age)
└── *_test.go

internal/cli/
├── secrets.go         # secrets edit/get/set
└── secrets_test.go

MODIFIED:
internal/render/pipeline.go     # walk canonical, substitute ${secret:...} / ${env:...}
internal/source/loader.go       # secrets backend hint loaded from agentsync.toml
```

---

## Task 1: `Resolver` + EnvBackend

```go
// Package secrets resolves ${secret:foo.bar} and ${env:FOO} references at
// apply-time. The active backend is selected from agentsync.toml [secrets]
// `backend` field (env|age).
package secrets

import (
    "fmt"
    "regexp"
    "strings"
)

// Resolver returns the cleartext value for a key like "github.token". An
// unknown key returns an error.
type Resolver interface {
    Resolve(key string) (string, error)
}

// SubstituteRefs walks s and replaces ${secret:dotted.key} and ${env:NAME}
// references. Unknown references are left as-is and reported in the
// returned []string of unresolved markers (caller decides whether to error).
func SubstituteRefs(s string, secrets Resolver, env Resolver) (string, []string, error) {
    var unresolved []string
    out := re.ReplaceAllStringFunc(s, func(m string) string {
        sub := re.FindStringSubmatch(m)
        if len(sub) < 3 {
            return m
        }
        kind, key := sub[1], sub[2]
        var r Resolver
        switch kind {
        case "secret":
            r = secrets
        case "env":
            r = env
        default:
            unresolved = append(unresolved, m)
            return m
        }
        v, err := r.Resolve(key)
        if err != nil {
            unresolved = append(unresolved, m)
            return m
        }
        return v
    })
    return out, unresolved, nil
}

var re = regexp.MustCompile(`\$\{(secret|env):([A-Za-z0-9._-]+)\}`)

// EnvBackend resolves ${env:NAME} via os.Getenv(NAME). Used both as the env
// resolver in SubstituteRefs and as the "backend = env" mode where secrets
// are also stored as env vars (e.g. by direnv / 1Password CLI).
type EnvBackend struct{}

func (EnvBackend) Resolve(key string) (string, error) {
    v := osGetenv(key)
    if v == "" {
        return "", fmt.Errorf("env var %q not set", key)
    }
    return v, nil
}

// indirection so tests can inject; real impl reads os.Getenv
var osGetenv = func(k string) string {
    return _osLookupEnv(k)
}
```

(`_osLookupEnv` is `os.Getenv`; indirection makes it testable.)

- [ ] **Test SubstituteRefs**

```go
func TestSubstituteRefs_SecretsAndEnv(t *testing.T) {
    secrets := mapBackend{"github.token": "ghp_abc"}
    env := mapBackend{"HOME": "/Users/x"}
    got, unresolved, _ := secrets_pkg.SubstituteRefs(
        "token=${secret:github.token}; home=${env:HOME}; ?=${secret:missing}",
        secrets, env)
    if !strings.Contains(got, "token=ghp_abc") {
        t.Fatalf("substitution failed: %s", got)
    }
    if !strings.Contains(got, "home=/Users/x") {
        t.Fatalf("env substitution failed: %s", got)
    }
    if len(unresolved) != 1 {
        t.Fatalf("expected 1 unresolved, got %v", unresolved)
    }
}
```

(`mapBackend` is a small test fake type implementing `Resolver`.)

Commit.

---

## Task 2: AgeBackend

```bash
go get filippo.io/age@latest
```

```go
// AgeBackend reads ~/.agentsync/secrets/secrets.age, decrypts using identity
// file specified in agentsync.toml [secrets].identity_file, parses as TOML,
// and resolves dotted keys.
package secrets

import (
    "fmt"
    "io"
    "os"
    "strings"

    "filippo.io/age"
    "github.com/pelletier/go-toml/v2"
)

type AgeBackend struct {
    AgeFile      string // path to secrets.age
    IdentityFile string // path to age identity (private key)
    cache        map[string]string
}

func NewAgeBackend(ageFile, identityFile string) *AgeBackend {
    return &AgeBackend{AgeFile: ageFile, IdentityFile: identityFile}
}

func (b *AgeBackend) load() error {
    if b.cache != nil {
        return nil
    }
    idData, err := os.ReadFile(b.IdentityFile)
    if err != nil {
        return fmt.Errorf("read identity %s: %w", b.IdentityFile, err)
    }
    ids, err := age.ParseIdentities(strings.NewReader(string(idData)))
    if err != nil {
        return fmt.Errorf("parse age identity: %w", err)
    }
    encFile, err := os.Open(b.AgeFile)
    if err != nil {
        return fmt.Errorf("open age file %s: %w", b.AgeFile, err)
    }
    defer encFile.Close()
    rd, err := age.Decrypt(encFile, ids...)
    if err != nil {
        return fmt.Errorf("decrypt %s: %w", b.AgeFile, err)
    }
    raw, err := io.ReadAll(rd)
    if err != nil {
        return fmt.Errorf("read decrypted: %w", err)
    }
    var top map[string]any
    if err := toml.Unmarshal(raw, &top); err != nil {
        return fmt.Errorf("parse decrypted as TOML: %w", err)
    }
    b.cache = flatten("", top)
    return nil
}

func (b *AgeBackend) Resolve(dottedKey string) (string, error) {
    if err := b.load(); err != nil {
        return "", err
    }
    v, ok := b.cache[dottedKey]
    if !ok {
        return "", fmt.Errorf("secret %q not found", dottedKey)
    }
    return v, nil
}

func flatten(prefix string, m map[string]any) map[string]string {
    out := map[string]string{}
    for k, v := range m {
        key := k
        if prefix != "" {
            key = prefix + "." + k
        }
        switch vv := v.(type) {
        case map[string]any:
            for kk, vvv := range flatten(key, vv) {
                out[kk] = vvv
            }
        case string:
            out[key] = vv
        default:
            out[key] = fmt.Sprint(vv)
        }
    }
    return out
}

// Encrypt writes plaintext as TOML, encrypted to recipient.
func Encrypt(plaintext []byte, recipient string, dest string) error {
    rec, err := age.ParseX25519Recipient(recipient)
    if err != nil {
        return fmt.Errorf("parse age recipient: %w", err)
    }
    f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
    if err != nil {
        return err
    }
    defer f.Close()
    w, err := age.Encrypt(f, rec)
    if err != nil {
        return fmt.Errorf("init age encrypt: %w", err)
    }
    if _, err := w.Write(plaintext); err != nil {
        return err
    }
    return w.Close()
}
```

- [ ] **Test round-trip**

```go
func TestAgeRoundTrip(t *testing.T) {
    tmp := t.TempDir()
    // generate identity
    id, err := age.GenerateX25519Identity()
    if err != nil { t.Fatal(err) }
    idPath := filepath.Join(tmp, "id.txt")
    _ = os.WriteFile(idPath, []byte(id.String()), 0o600)
    rec := id.Recipient().String()

    plain := []byte(`[github]
token = "ghp_abc"
[linear]
api_key = "lin_xyz"
`)
    agePath := filepath.Join(tmp, "secrets.age")
    if err := secrets_pkg.Encrypt(plain, rec, agePath); err != nil {
        t.Fatal(err)
    }

    b := secrets_pkg.NewAgeBackend(agePath, idPath)
    if v, _ := b.Resolve("github.token"); v != "ghp_abc" {
        t.Fatalf("github.token = %q", v)
    }
    if v, _ := b.Resolve("linear.api_key"); v != "lin_xyz" {
        t.Fatalf("linear.api_key = %q", v)
    }
}
```

Commit.

---

## Task 3: `secrets edit/get/set`

`internal/cli/secrets.go`:

```go
agentsync secrets edit            # decrypt -> tmp -> $EDITOR -> re-encrypt
agentsync secrets get <key>       # print one value
agentsync secrets set <key>=<val> # mutate one value (decrypt -> patch -> re-encrypt)
```

Implementation:

```go
func newSecretsCmd() *cobra.Command {
    sec := &cobra.Command{Use: "secrets", Short: "manage age-encrypted secrets"}
    sec.AddCommand(
        &cobra.Command{Use: "edit", RunE: secretsEdit},
        &cobra.Command{Use: "get <key>", Args: cobra.ExactArgs(1), RunE: secretsGet},
        &cobra.Command{Use: "set <key=value>", Args: cobra.ExactArgs(1), RunE: secretsSet},
    )
    return sec
}

func secretsEdit(cmd *cobra.Command, _ []string) error {
    // 1. Resolve agePath + identityPath + recipient from agentsync.toml
    // 2. Read existing or create empty plaintext
    // 3. Write to tmp file in os.TempDir() (RAM-backed on macOS)
    // 4. Run os.Getenv("EDITOR") on the tmp file (default to vi)
    // 5. Read the edited bytes back; encrypt to agePath atomically via iox.AtomicWrite (after writing tmp)
    // 6. Remove the cleartext tmp file
    return nil
}
```

(Each function is straightforward boilerplate — engineer fills in the explicit reads/writes following the patterns in M0/M1.)

Commit.

---

## Task 4: Wire substitution into apply

In `internal/render/pipeline.go`, before each adapter's Render is called, walk `source.Canonical` and replace `${secret:...}` / `${env:...}` strings via `secrets.SubstituteRefs`:

```go
// in Plan(), after canonical loaded:
backend := secrets.SelectBackend(c.Config.Secrets) // returns AgeBackend or EnvBackend per [secrets].backend
env := secrets.EnvBackend{}
if err := substituteCanonicalSecrets(&c, backend, env); err != nil {
    return Plan{}, err
}
```

`substituteCanonicalSecrets` walks `c.MCPServers[*].Server.Env`, `Args`, `Command`, `Headers`, etc., calling `SubstituteRefs` on each string. Unresolved refs return an error (apply blocks; user sees which secret is missing).

- [ ] **Implement** `substituteCanonicalSecrets`. Test that an MCP env value `${secret:github.token}` gets resolved before adapter Render sees it. Commit.

---

## Task 5: Integration test

```go
func TestIntegration_M6_AgeSecretsResolveOnApply(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

    _, _ = runCLI(t, env, "init")
    _, _ = runCLI(t, env, "agent", "add", "claude")

    // generate age key
    id, _ := age.GenerateX25519Identity()
    idPath := filepath.Join(tmp, ".config", "agentsync", "age.key")
    _ = os.MkdirAll(filepath.Dir(idPath), 0o755)
    _ = os.WriteFile(idPath, []byte(id.String()), 0o600)

    // configure agentsync.toml [secrets]
    cfg := filepath.Join(tmp, ".agentsync", "agentsync.toml")
    body, _ := os.ReadFile(cfg)
    body = append(body, []byte(fmt.Sprintf(`
[secrets]
backend       = "age"
file          = "secrets/secrets.age"
recipient     = "%s"
identity_file = "%s"
`, id.Recipient().String(), idPath))...)
    _ = os.WriteFile(cfg, body, 0o644)

    // write secrets.age
    plain := []byte(`[github]
token = "ghp_abc"
`)
    _ = secrets_pkg.Encrypt(plain, id.Recipient().String(),
        filepath.Join(tmp, ".agentsync", "secrets", "secrets.age"))

    // mcp file with ${secret:...}
    _ = os.WriteFile(filepath.Join(tmp, ".agentsync", "mcp", "github.toml"), []byte(`
[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
[server.env]
GITHUB_TOKEN = "${secret:github.token}"
`), 0o644)

    if _, err := runCLI(t, env, "apply"); err != nil {
        t.Fatal(err)
    }

    // verify .claude.json has the literal token
    body, _ = os.ReadFile(filepath.Join(tmp, ".claude.json"))
    if !strings.Contains(string(body), "ghp_abc") {
        t.Fatalf("token not substituted: %s", body)
    }
    // verify cleartext is NOT in the agentsync repo
    repoBytes, _ := os.ReadFile(filepath.Join(tmp, ".agentsync", "mcp", "github.toml"))
    if strings.Contains(string(repoBytes), "ghp_abc") {
        t.Fatalf("token leaked into source repo: %s", repoBytes)
    }
}
```

Commit.

---

## Done When

- `~/.agentsync/secrets/secrets.age` decrypts via `filippo.io/age` library (no `age` CLI required).
- `${secret:dotted.key}` references in canonical (MCP env, hook commands, etc.) resolve to cleartext at apply-time, never persisted in canonical.
- `secrets edit` round-trip preserves data; `get`/`set` work non-interactively.
- Apply errors when a `${secret:...}` reference can't be resolved (missing key) — fail-loud, never silent.
- README documents age-key backup discipline (lose the key = lose all secrets); test the README's quickstart commands work.
- CI green on linux/macos/windows (age library is pure Go; cross-platform fine).

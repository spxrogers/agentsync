## Summary

<!-- What does this change do, and why? Link any related issue. -->

## Type of change

- [ ] Bug fix
- [ ] New feature / enhancement
- [ ] Refactor (no behavior change)
- [ ] Docs
- [ ] Tests / CI / tooling

## Test plan

<!-- How did you verify this? Note any new tests. -->

- [ ] `just test-release` is green (the release bar)
- [ ] `just lint` is clean

## Checklist

- [ ] Conventional commit messages with a scope (e.g. `fix(secrets): …`).
- [ ] Tests added/updated for the behavior changed.
- [ ] If this touches `internal/secrets`, `internal/capture`, or any
      `source.Write*` path, I've re-read the secret-handling invariants in
      `CLAUDE.md` / `SECURITY.md` and not weakened them.
- [ ] Docs updated if behavior, CLI surface, or capability coverage changed.

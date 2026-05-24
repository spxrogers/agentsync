# Decision records

Chronological record of design decisions that **change or depart from** the
original specs under `docs/superpowers/specs/`. The specs capture the v1.0
design intent; this directory captures where the shipped behaviour
deliberately diverged, and why — so the canonical timeline is recoverable
without spelunking git history.

One file per decision: `YYYY-MM-DD-short-slug.md`, date-stamped with the day it
was decided (the slug disambiguates multiple decisions on one day). Each records
**Status**, **Context**, **Decision**, **Departure from spec** (when
applicable), and **Consequences**. Superseding a decision means adding a new
record that references the old one — records are append-only history, not living
docs.

| Date | Decision |
|------|----------|
| [2026-05-24](2026-05-24-strict-flag-conflict-policy.md) | `strict` flag is a conflict policy on a plugin.json+entry union |

# `entire adopt` Command

`entire adopt` lets one developer share Entire with their team. It is a
repository-level action (distinct from `entire enable`, which is per-developer).
It writes a few small, committed files and stages them — it never edits the
team's README or build system, and never runs anything on a teammate's machine
without an explicit prompt.

## How it works

```
Champion (already using Entire)        Teammate
──────────────────────────────        ────────────────────────────────
entire adopt ── commits ──►  .entire/config
                             .githooks/
                                   │  git push
                                   ▼
                             teammate clones / pulls
                                   │
                      ┌────────────┴─────────────┐
                      │ has Entire installed?     │
               yes ◄──┤                           ├──► no
               │      └───────────────────────────┘     │
   committed hook runs                            committed hook runs
   `entire adopt --check`:                        the install one-liner:
   "This repo uses Entire. Set up? [Y/n]"         "Install Entire? [Y/n]"
        │ yes                                           │ yes
        ▼                                               ▼
   entire enable                                  curl …install.sh | bash
                                                        && entire enable
```

## What it writes

```
.entire/config               marker that says "this repo uses Entire" (committed)
.githooks/entire-adopt-check shared POSIX script: asks a teammate once if they want Entire
.githooks/post-checkout      thin wrapper → entire-adopt-check (runs after clone + checkout)
.githooks/post-merge         thin wrapper → entire-adopt-check (runs after `git pull`)
```

`.entire/config` is small, human-readable JSON:

```json
{ "adopted": true, "repo": "acme/backend", "docs_url": "https://docs.entire.io/overview" }
```

The command is idempotent: once `.entire/config` exists it is a no-op unless run
with `--force`. Files are staged, not committed, so the developer reviews them
before pushing.

## How teammates get prompted

Git cannot run code from a fresh clone, so adoption can only *offer*, never
force. The committed hooks fire for teammates whose repo uses
`git config core.hooksPath .githooks`, and behave safely:

- they never exit non-zero (so they can't block a git command);
- they only prompt when a real terminal is attached (silent in CI/scripts);
- they ask at most once per clone.

When the hook runs it checks whether `entire` is installed:

- **installed** → delegates to the hidden `entire adopt --check`, which shows
  the prompt and, on yes, runs `entire enable`;
- **not installed** → offers to install it (`curl …install.sh | bash`) and only
  then runs `entire enable`. It never runs `entire enable` without Entire present.

## One-time state

The teammate's answer is stored at `<git-common-dir>/entire/prompt-seen`
(`accepted` / `declined` / `deferred`). It lives under `.git/`, so it is never
committed, is unique per clone, and survives future pulls — guaranteeing a
teammate is asked at most once. `deferred` (written when we couldn't ask, e.g.
in CI) is not final, so a real terminal can still ask later.

## Key files

- `cmd/entire/cli/adopt.go` — the `entire adopt` command (incl. hidden `--check`) + committed hook scripts.
- `cmd/entire/cli/adopt_prompt.go` — the teammate prompt run by `entire adopt --check`.
- `cmd/entire/cli/adopt/state.go` — the marker + one-time-state helpers (no UI).

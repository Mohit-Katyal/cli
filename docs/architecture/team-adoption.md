# Team Adoption (`entire adopt`) and Onboarding

This document describes the lightweight, consent-based flow that lets one
developer ("the champion") roll Entire out to their team, and gets teammates
onboarded at most once — without ever silently installing software or relying on
language-specific tooling.

## Goals and constraints

The flow is shaped by hard constraints, the most important of which is a
property of Git itself:

1. **Git cannot auto-run repo code after a teammate pulls/clones.** A freshly
   cloned repository cannot execute anything on a teammate's machine until that
   teammate takes an action. This is a security feature of Git, not something we
   can work around.
2. **Never silently install software** on a teammate's machine. Every install or
   setup is gated behind an explicit `[Y/n]`.
3. **Ask at most once.** A developer who declines is never asked again.
4. **Feel native to Git** and work across any repo type (Node, Python, Go, Rust,
   Java, mixed, monorepo). No build-system edits, no Makefile dependency.
5. **Repository-level and developer-level setup are separate concepts.**

## The three commands

| Command | Layer | Commits files? | What it does |
|---|---|---|---|
| `entire adopt` | repository | **yes** (staged) | Writes the committed adoption artifacts so teammates can be onboarded. Run once by the champion. |
| `entire enable` | developer | no | Existing command. Configures local git hooks + `.entire` integration for this developer in this clone. **Unchanged by this feature.** |
| `entire onboard --check` | internal | no | Hidden. The one-time teammate prompt, invoked by the committed git hooks. Safe to run by hand. |

There is intentionally **no `entire setup` command** — `entire enable` already
performs the developer-level per-repo integration.

## End-to-end flow

```
Champion (already using Entire)                Teammate
───────────────────────────────               ────────────────────────────────
uses Entire; after N enabled commits
 ─ gated post-run nudge ─►
   "Enable Entire for your team? [Y/n]"
        │ yes
        ▼
   entire adopt  ── commits ──►  .entire/config
                                 .githooks/…
                                 README section
                                       │
                                  git push
                                       │
                                       ▼
                                 teammate clones / pulls
                                       │
                          ┌────────────┴─────────────┐
                          │ has Entire?               │
                   yes ◄──┤                           ├──► no
                   │      └───────────────────────────┘     │
   gated post-run nudge or committed hook:           committed hook (if active) /
   "This repo uses Entire. Set up? [Y/n]"            README one-liner:
        │ yes                                        "Install Entire? [Y/n]"
        ▼                                                 │ yes
   entire enable                                          ▼
                                                    curl …install.sh | bash
                                                          && entire enable
```

### Why two prompt "surfaces"

Because Git can't auto-run repo code, the onboarding prompt has to come from one
of two places, depending on whether the teammate already has Entire:

1. **The gated root post-run check** (`maybePromptOnboarding`, wired into the
   root command's `PersistentPostRun`). This reaches developers who *already*
   have the `entire` binary. After any successful, non-hidden command it does at
   most two cheap stats and exits unless a one-time prompt is genuinely due. It
   was placed in `PersistentPostRun` (not a new `PersistentPreRunE`) because a
   root `PersistentPreRunE` would be shadowed by the `session`, `agent`, and
   `checkpoint` group commands; the existing `PersistentPostRun` only fires for
   user-facing commands and runs after the command succeeds.
2. **The committed `.githooks/`** scripts. Plain POSIX `sh`, they handle the
   case where a teammate has set `git config core.hooksPath .githooks` (a common
   org convention). They detect whether `entire` is installed:
   - installed → delegate to `entire onboard --check`;
   - not installed → offer to run the installer (`curl …install.sh | bash`), and
     only run `entire enable` *after* a successful install. They never call
     `entire enable` on a machine without Entire.

For a teammate with **no Entire and no `core.hooksPath`**, Git permits nothing
automatic — the README install one-liner (added by `adopt`) is the entry point.

## The two nudges (gated post-run check)

`maybePromptOnboarding` routes to at most one of two one-time prompts:

- **Champion** — Entire is set up *and enabled* here, the repo is **not** adopted,
  and the developer has made at least `threshold` commits with Entire enabled
  (the value signal). Offers `entire adopt`.
- **Teammate** — the repo **is** adopted but Entire isn't set up here. Offers to
  install (if needed) and run `entire enable`.

Both are skipped for lifecycle/auth commands (see `onboardingNudgeSkip`), in
non-interactive contexts (`interactive.CanPromptInteractively()` is false in CI,
pipes, agent subprocesses, and `go test`), and once a final answer is recorded.

### Champion value-moment threshold

The champion prompt only fires after the developer has made enough commits with
Entire enabled, so it lands when they've found value rather than on day one.

- The counter lives at `<git-common-dir>/entire/commit-count` and is incremented
  best-effort by the `post-commit` git hook (which already no-ops when Entire is
  disabled, so it counts exactly "commits made with Entire enabled").
- The threshold is **adjustable** via `.entire/settings.json`:

  ```json
  { "adopt_prompt_after_commits": 5 }
  ```

  - unset / `0` → default (`settings.DefaultAdoptPromptAfterCommits`, currently 3)
  - positive `N` → prompt after `N` enabled commits
  - negative → champion prompt disabled entirely

  Resolved via `(*EntireSettings).AdoptPromptThreshold()`.

## Files written by `entire adopt` (committed)

```
.entire/config            adoption marker (JSON)
.githooks/entire-onboarding   shared POSIX onboarding script
.githooks/post-checkout       thin wrapper → entire-onboarding (fires after clone+checkout)
.githooks/post-merge          thin wrapper → entire-onboarding (fires after pull)
README.md                 marker-delimited "## Entire" section with a cross-platform install line
```

`.entire/config` is small, human-readable, and language-agnostic:

```json
{
  "adopted": true,
  "repo": "acme/backend",
  "docs_url": "https://docs.entire.io/overview",
  "min_version": "0.7.x",
  "adopted_at": "2026-06-20T00:00:00Z"
}
```

`entire adopt` is idempotent (the README section is updated in place between its
markers; re-running without `--force` is a no-op once `.entire/config` exists)
and **stages** the files rather than committing them, so the champion reviews and
commits.

The committed hooks are defensive by construction: they never exit non-zero (so
they can never block a git operation), only prompt when `/dev/tty` is readable,
and write their own one-time state when Entire is absent.

## Onboarding state (local, never committed)

All per-developer state lives under the git common dir so it is unique per
clone, survives future pulls, and is never committed:

| File | Meaning |
|---|---|
| `<git-common-dir>/entire/prompt-seen` | teammate answer: `accepted` / `declined` / `deferred` (`v1 <status> <rfc3339>`) |
| `<git-common-dir>/entire/adopt-asked` | champion has been asked (presence = asked) |
| `<git-common-dir>/entire/commit-count` | count of commits made with Entire enabled |

`deferred` is written when a prompt was due but we couldn't ask (non-interactive)
— it is *not* final, so a real terminal can still ask later. A `declined`/
`accepted` answer, or a recorded `adopt-asked`, is final.

## Installation across machines

The teammate "install Entire" path reuses the canonical installer:

- macOS / Linux: `curl -fsSL https://entire.io/install.sh | bash` (the README
  one-liner chains `&& entire enable`).
- Windows / other: install from the releases page, then `entire enable`. (Mirrors
  the platform handling already used by the version-check auto-updater.)

`entire enable` is strictly a *post-install* step — the installer is always the
bootstrap, and the committed hooks check `command -v entire` before ever invoking
`entire enable`.

## Key files

- `cmd/entire/cli/onboarding/onboarding.go` — UI-free state: adoption marker,
  `prompt-seen` / `adopt-asked` / `commit-count`, repo-slug helper.
- `cmd/entire/cli/adopt.go` — `entire adopt`; committed hook + README templates.
- `cmd/entire/cli/onboard.go` — hidden `entire onboard --check`; the champion /
  teammate routing (`maybePromptOnboarding`).
- `cmd/entire/cli/hooks_git_cmd.go` — increments the commit counter in
  `post-commit`.
- `cmd/entire/cli/settings/settings.go` — `AdoptPromptAfterCommits` +
  `AdoptPromptThreshold()`.
- `cmd/entire/cli/root.go` — registers `adopt` / `onboard`; calls the nudge from
  `PersistentPostRun`.

## Anti-features (do NOT add)

- **Do not** change the global `git config core.hooksPath`. It is invasive and
  was explicitly rejected; the gated post-run check covers already-installed
  users without touching global git config.
- **Do not** auto-install or auto-enable without an explicit prompt.
- **Do not** add a separate `entire setup` command — `entire enable` is the
  developer-level command.
- **Do not** make any onboarding hook able to fail a git operation.

package cli

// This file implements `entire adopt`: a one-time, repository-level command a
// developer runs to share Entire with their team.
//
// All it does is write a few small, committed files and stage them:
//
//   .entire/config               a marker that says "this repo uses Entire"
//   .githooks/entire-adopt-check a tiny shell script that, for teammates who use
//                                .githooks, asks once if they want to use Entire
//   .githooks/post-checkout      \ thin wrappers that run the script above after
//   .githooks/post-merge         / a clone+checkout and after a pull
//
// It deliberately does NOT touch the team's README or build system, and it never
// runs anything on a teammate's machine without an explicit prompt. The files are
// staged (not committed) so the developer reviews them before pushing.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/adopt"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/versioninfo"
	"github.com/spf13/cobra"
)

// adoptHookName is the shared script the committed git hooks call. It lives in
// one place so the wrappers stay trivial.
const adoptHookName = "entire-adopt-check"

// committedHookWrappers are the git hooks `entire adopt` writes. Each is a thin
// wrapper around adoptHookName. post-checkout fires after a clone + checkout;
// post-merge fires after `git pull` — the two natural "I just got this repo"
// moments.
var committedHookWrappers = []string{"post-checkout", "post-merge"}

func newAdoptCmd() *cobra.Command {
	var (
		force bool
		check bool
	)
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Adopt Entire for your whole team in this repository",
		Long: `Share Entire with your team by committing a few small files.

This is a repository-level action (separate from per-developer 'entire enable').
It adds committed, language-agnostic files:

  .entire/config            a marker that this repo uses Entire
  .githooks/                lightweight hooks that ask teammates once (committed)

It does not modify your README or build system, and works with any repo type
(Node, Python, Go, Rust, Java, mixed, monorepo). Nothing runs on a teammate's
machine without their consent.

The files are staged but not committed — review them, then commit and push.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			// --check is the hidden, teammate-facing mode the committed git hooks
			// invoke (`entire adopt --check`). It only ever offers — it never
			// writes the adoption files.
			if check {
				runAdoptCheck(cmd)
				return nil
			}

			// `entire adopt` only makes sense inside a git repo.
			repoRoot, err := paths.WorktreeRoot(ctx)
			if err != nil {
				cmd.SilenceUsage = true
				fmt.Fprintln(cmd.ErrOrStderr(), "Not a git repository. Run 'entire adopt' from within a git repository.")
				return NewSilentError(errors.New("not a git repository"))
			}

			// Idempotent: if the marker already exists we're done, unless the
			// developer explicitly wants to refresh the files with --force.
			if adopt.RepoIsAdopted(repoRoot) && !force {
				fmt.Fprintln(out, "This repository already uses Entire (.entire/config exists).")
				fmt.Fprintln(out, "Re-run with --force to refresh the files.")
				return nil
			}

			written, err := runAdopt(ctx, repoRoot)
			if err != nil {
				cmd.SilenceUsage = true
				return err
			}

			fmt.Fprintln(out, "Adopted Entire for this repository. Staged files:")
			for _, f := range written {
				fmt.Fprintf(out, "  %s\n", f)
			}
			fmt.Fprintln(out, "\nReview, then commit and push to share with your team:")
			fmt.Fprintln(out, "  git commit -m \"Adopt Entire\"")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing files")
	// --check is for the committed git hooks, not humans.
	cmd.Flags().BoolVar(&check, "check", false, "Run the one-time teammate prompt (used by git hooks)")
	_ = cmd.Flags().MarkHidden("check") //nolint:errcheck // flag is defined just above; MarkHidden cannot fail here
	return cmd
}

// runAdopt writes the committed adoption files and stages them. It returns the
// repo-relative paths it wrote, in the order they were created (for display).
func runAdopt(ctx context.Context, repoRoot string) ([]string, error) {
	var written []string

	// 1. The marker. We record a best-effort repo slug and the version that
	//    adopted, purely for humans reading the file.
	cfg := &adopt.Config{
		Adopted:    true,
		Repo:       adopt.RepoSlug(ctx, repoRoot),
		DocsURL:    adopt.DefaultDocsURL,
		MinVersion: versioninfo.Version,
		AdoptedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := adopt.WriteConfig(repoRoot, cfg); err != nil {
		return nil, fmt.Errorf("write adoption marker: %w", err)
	}
	written = append(written, adopt.ConfigRelPath)

	// 2. The committed git hooks.
	hooks, err := writeCommittedHooks(repoRoot, cfg.DocsURL)
	if err != nil {
		return nil, err
	}
	written = append(written, hooks...)

	// 3. Stage everything so the developer just has to commit. Best-effort:
	//    never fail adoption because staging hit a snag (e.g. an index lock) —
	//    they can always `git add` manually.
	stageFiles(ctx, repoRoot, written)

	return written, nil
}

// writeCommittedHooks writes the shared adopt-check script plus one wrapper per
// managed hook, all executable. Returns the repo-relative paths written.
func writeCommittedHooks(repoRoot, docsURL string) ([]string, error) {
	hooksDir := filepath.Join(repoRoot, ".githooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil { //nolint:gosec // committed, world-readable hooks dir
		return nil, fmt.Errorf("create .githooks: %w", err)
	}

	var written []string

	// The shared script carries the real logic. We bake the docs URL in so the
	// hook has no runtime dependency on anything but git + a POSIX shell.
	script := strings.ReplaceAll(adoptHookScript, "{{DOCS_URL}}", docsURL)
	if err := os.WriteFile(filepath.Join(hooksDir, adoptHookName), []byte(script), 0o755); err != nil { //nolint:gosec // git hooks must be executable
		return nil, fmt.Errorf("write .githooks/%s: %w", adoptHookName, err)
	}
	written = append(written, ".githooks/"+adoptHookName)

	for _, name := range committedHookWrappers {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(hookWrapperScript), 0o755); err != nil { //nolint:gosec // git hooks must be executable
			return nil, fmt.Errorf("write .githooks/%s: %w", name, err)
		}
		written = append(written, ".githooks/"+name)
	}
	return written, nil
}

// stageFiles runs `git add` for the given paths. Best-effort by design (see
// runAdopt) — the error is intentionally ignored.
func stageFiles(ctx context.Context, repoRoot string, relPaths []string) {
	args := append([]string{"add", "--"}, relPaths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	_ = cmd.Run() //nolint:errcheck // staging is best-effort; developer can stage manually
}

// adoptHookScript is the shared POSIX hook written to
// .githooks/entire-adopt-check. It is intentionally defensive: it never exits
// non-zero (so it can never block a git command), only acts when a real terminal
// is attached, asks at most once per clone, and never installs anything without
// an explicit yes.
//
// Note: committed hooks only run for teammates whose repo uses
// `git config core.hooksPath .githooks`. That's the only safe, language-agnostic
// way for a repo to nudge teammates — git will not auto-run code from a clone.
const adoptHookScript = `#!/bin/sh
# Entire adopt hook (committed by 'entire adopt').
#
# Safe & lightweight by design:
#   * never blocks or fails a git operation
#   * only prompts when a real terminal is available (silent in CI/scripts)
#   * asks at most once per clone, then never again
#   * never installs software without explicit consent
#
# Active only when this repo uses 'git config core.hooksPath .githooks'.

# Must have a terminal to prompt on, otherwise do nothing.
[ -r /dev/tty ] || exit 0

# Only act in repositories that have adopted Entire.
[ -f .entire/config ] || exit 0

git_common_dir=$(git rev-parse --git-common-dir 2>/dev/null) || exit 0
state_dir="$git_common_dir/entire"
state_file="$state_dir/prompt-seen"

# Already asked in this clone: nothing to do.
[ -f "$state_file" ] && exit 0

if command -v entire >/dev/null 2>&1; then
	# Entire is installed: let the CLI own the prompt and the state file.
	entire adopt --check </dev/tty >/dev/tty 2>&1 || true
	exit 0
fi

# Entire is NOT installed: offer to install it (never silently).
mkdir -p "$state_dir" 2>/dev/null || exit 0
now=$(date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)
{
	printf '\n─────────────────────────────\n'
	printf 'This repository uses Entire.\n\n'
	printf 'Entire automatically captures coding progress and creates checkpoints\n'
	printf 'so teammates can see work as it evolves.\n\n'
	printf 'Learn more: {{DOCS_URL}}\n\n'
	printf 'Install Entire now? [Y/n] '
} >/dev/tty 2>&1
answer=""
read answer </dev/tty 2>/dev/null || answer=""
printf '─────────────────────────────\n' >/dev/tty 2>&1

case "$answer" in
	[Nn]*)
		printf 'v1 declined %s\n' "$now" >"$state_file" 2>/dev/null || true
		;;
	*)
		if command -v curl >/dev/null 2>&1; then
			curl -fsSL https://entire.io/install.sh | bash </dev/tty >/dev/tty 2>&1 || true
		else
			printf 'curl not found. Install Entire manually: https://github.com/entireio/cli/releases\n' >/dev/tty 2>&1
		fi
		if command -v entire >/dev/null 2>&1; then
			entire enable </dev/tty >/dev/tty 2>&1 || true
			printf 'v1 accepted %s\n' "$now" >"$state_file" 2>/dev/null || true
		fi
		# If install did not complete, leave state unset so we can offer again later.
		;;
esac
exit 0
`

// hookWrapperScript is what each managed hook (post-checkout, post-merge)
// contains: it just execs the shared script above.
const hookWrapperScript = `#!/bin/sh
# Entire adopt hook wrapper (committed by 'entire adopt').
hook_dir=$(dirname "$0")
[ -x "$hook_dir/` + adoptHookName + `" ] || exit 0
exec "$hook_dir/` + adoptHookName + `" "$@"
`

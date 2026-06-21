package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/entireio/cli/cmd/entire/cli/onboarding"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/versioninfo"
	"github.com/spf13/cobra"
)

// README markers delimit the Entire section so `entire adopt` can update it
// idempotently without disturbing the rest of the file.
const (
	readmeStartMarker = "<!-- entire:onboarding:start -->"
	readmeEndMarker   = "<!-- entire:onboarding:end -->"
)

// onboardingShellName is the shared POSIX script the committed git hooks call.
const onboardingShellName = "entire-onboarding"

// committedHookWrappers are the git hooks `entire adopt` installs into
// .githooks/. They are thin wrappers that delegate to onboardingShellName so
// the logic lives in exactly one place. post-checkout fires after a clone +
// checkout; post-merge fires after `git pull`.
var committedHookWrappers = []string{"post-checkout", "post-merge"}

func newAdoptCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Adopt Entire for your whole team in this repository",
		Long: `Adopt Entire at the repository level so teammates are gently onboarded.

This is a repository-level action (separate from per-developer setup). It adds a
small set of committed, language-agnostic files:

  .entire/config            an adoption marker (committed)
  .githooks/                lightweight onboarding hooks (committed)
  README.md                 an "## Entire" section with a cross-platform install line

It never modifies your build system and works with any repo type (Node, Python,
Go, Rust, Java, mixed, monorepo). Nothing runs on a teammate's machine without
their consent: they are asked at most once whether they'd like to use Entire.

The files are staged but not committed — review them, then commit and push to
share with your team.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			repoRoot, err := paths.WorktreeRoot(ctx)
			if err != nil {
				cmd.SilenceUsage = true
				fmt.Fprintln(cmd.ErrOrStderr(), "Not a git repository. Run 'entire adopt' from within a git repository.")
				return NewSilentError(errors.New("not a git repository"))
			}

			if onboarding.RepoIsAdopted(repoRoot) && !force {
				fmt.Fprintln(out, "This repository is already adopted (.entire/config exists).")
				fmt.Fprintln(out, "Re-run with --force to refresh the onboarding files.")
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
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing onboarding files")
	return cmd
}

// runAdopt writes all committed adoption artifacts and stages them. It returns
// the list of repo-relative paths that were written (for display).
func runAdopt(ctx context.Context, repoRoot string) ([]string, error) {
	var written []string

	cfg := &onboarding.Config{
		Adopted:    true,
		Repo:       onboarding.RepoSlug(ctx, repoRoot),
		DocsURL:    onboarding.DefaultDocsURL,
		MinVersion: versioninfo.Version,
		AdoptedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := onboarding.WriteConfig(repoRoot, cfg); err != nil {
		return nil, fmt.Errorf("write adoption marker: %w", err)
	}
	written = append(written, onboarding.ConfigRelPath)

	hooks, err := writeCommittedHooks(repoRoot, cfg.DocsURL)
	if err != nil {
		return nil, err
	}
	written = append(written, hooks...)

	readmeRel, err := updateReadme(repoRoot)
	if err != nil {
		return nil, err
	}
	written = append(written, readmeRel)

	// Stage the files so the champion just has to commit. Best-effort: never
	// fail adoption because staging hit a snag (e.g. partial index lock).
	stageFiles(ctx, repoRoot, written)

	return written, nil
}

// writeCommittedHooks writes the shared onboarding script plus the per-hook
// wrappers into .githooks/, all executable. Returns repo-relative paths.
func writeCommittedHooks(repoRoot, docsURL string) ([]string, error) {
	hooksDir := filepath.Join(repoRoot, ".githooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil { //nolint:gosec // committed, world-readable hooks dir
		return nil, fmt.Errorf("create .githooks: %w", err)
	}

	var written []string

	script := strings.ReplaceAll(onboardingShellScript, "{{DOCS_URL}}", docsURL)
	if err := os.WriteFile(filepath.Join(hooksDir, onboardingShellName), []byte(script), 0o755); err != nil { //nolint:gosec // git hooks must be executable
		return nil, fmt.Errorf("write .githooks/%s: %w", onboardingShellName, err)
	}
	written = append(written, ".githooks/"+onboardingShellName)

	for _, name := range committedHookWrappers {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(hookWrapperScript), 0o755); err != nil { //nolint:gosec // git hooks must be executable
			return nil, fmt.Errorf("write .githooks/%s: %w", name, err)
		}
		written = append(written, ".githooks/"+name)
	}
	return written, nil
}

// updateReadme inserts or refreshes the Entire onboarding section in README.md,
// preserving the rest of the file. Creates README.md if absent.
func updateReadme(repoRoot string) (string, error) {
	readmePath := filepath.Join(repoRoot, "README.md")
	existing, err := os.ReadFile(readmePath) //nolint:gosec // path is repo root + constant
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read README.md: %w", err)
	}

	section := readmeStartMarker + "\n" + readmeSection + "\n" + readmeEndMarker

	var next string
	switch {
	case len(existing) == 0:
		next = section + "\n"
	case strings.Contains(string(existing), readmeStartMarker) && strings.Contains(string(existing), readmeEndMarker):
		next = replaceBetween(string(existing), readmeStartMarker, readmeEndMarker, section)
	default:
		body := strings.TrimRight(string(existing), "\n")
		next = body + "\n\n" + section + "\n"
	}

	if err := os.WriteFile(readmePath, []byte(next), 0o644); err != nil { //nolint:gosec // README is world-readable
		return "", fmt.Errorf("write README.md: %w", err)
	}
	return "README.md", nil
}

// replaceBetween replaces the text from start..end (inclusive of both markers)
// with replacement. Callers guarantee both markers are present.
func replaceBetween(s, start, end, replacement string) string {
	i := strings.Index(s, start)
	j := strings.Index(s, end)
	if i < 0 || j < 0 || j < i {
		return s
	}
	j += len(end)
	return s[:i] + replacement + s[j:]
}

func stageFiles(ctx context.Context, repoRoot string, relPaths []string) {
	args := append([]string{"add", "--"}, relPaths...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	_ = cmd.Run() //nolint:errcheck // staging is best-effort; champion can stage manually
}

// onboardingShellScript is the shared POSIX hook logic written to
// .githooks/entire-onboarding. It is intentionally defensive: it never exits
// non-zero (so it can never block git), only acts with a usable terminal, fires
// at most once per clone, and never installs anything without explicit consent.
const onboardingShellScript = `#!/bin/sh
# Entire onboarding hook (committed by 'entire adopt').
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

# Only act in repositories that have been adopted.
[ -f .entire/config ] || exit 0

git_common_dir=$(git rev-parse --git-common-dir 2>/dev/null) || exit 0
state_dir="$git_common_dir/entire"
state_file="$state_dir/prompt-seen"

# Already asked in this clone: nothing to do.
[ -f "$state_file" ] && exit 0

if command -v entire >/dev/null 2>&1; then
	# Entire is installed: let the CLI own the prompt and the state file.
	entire onboard --check </dev/tty >/dev/tty 2>&1 || true
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

// hookWrapperScript delegates to the shared onboarding script. Each managed hook
// (post-checkout, post-merge) is an identical wrapper.
const hookWrapperScript = `#!/bin/sh
# Entire onboarding hook wrapper (committed by 'entire adopt').
hook_dir=$(dirname "$0")
[ -x "$hook_dir/` + onboardingShellName + `" ] || exit 0
exec "$hook_dir/` + onboardingShellName + `" "$@"
`

// readmeSection is the human-facing onboarding blurb inserted into README.md.
const readmeSection = `## Entire

This repository uses [Entire](https://docs.entire.io/overview). Entire automatically
captures coding progress and creates checkpoints so teammates can see work as it evolves.

**Set up Entire (macOS / Linux):**

` + "```sh" + `
curl -fsSL https://entire.io/install.sh | bash && entire enable
` + "```" + `

**Windows / other platforms:** install from https://github.com/entireio/cli/releases, then run ` + "`entire enable`" + `.

You'll be asked once whether you'd like to use Entire. You can decline, and you won't be asked again.`

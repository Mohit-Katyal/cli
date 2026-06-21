package cli

import (
	"fmt"
	"io"

	"charm.land/huh/v2"

	"github.com/entireio/cli/cmd/entire/cli/interactive"
	"github.com/entireio/cli/cmd/entire/cli/onboarding"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/settings"
	"github.com/spf13/cobra"
)

// onboardingNudgeSkip lists command names for which the post-run nudge must
// never fire — lifecycle/auth commands where an unrelated prompt would be
// confusing or redundant. The bare root command ("entire") is skipped because
// it runs the setup flow itself; "enable" is skipped so the champion isn't
// asked to adopt in the same breath as enabling.
var onboardingNudgeSkip = map[string]bool{
	"entire":    true,
	"enable":    true,
	"disable":   true,
	"adopt":     true,
	"onboard":   true,
	"configure": true,
	"clean":     true,
	"reset":     true,
	"login":     true,
	"logout":    true,
	"version":   true,
	"help":      true,
}

// newOnboardCmd registers the hidden `entire onboard --check`. It is invoked by
// the committed .githooks/ scripts (and is safe to run by hand). It runs the
// teammate-facing onboarding check. Without --check it just prints help.
func newOnboardCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:    "onboard",
		Short:  "One-time Entire onboarding check (internal)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !check {
				return cmd.Help()
			}
			runTeammateOnboarding(cmd)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "Run the one-time onboarding check used by git hooks")
	return cmd
}

// maybePromptOnboarding is the gated nudge wired into the root
// PersistentPostRun. It runs after a successful, non-hidden command and routes
// to at most one of two one-time prompts:
//
//   - champion: this developer uses Entire here, but the repo is not adopted →
//     offer to run `entire adopt` so teammates can be onboarded.
//   - teammate: the repo is adopted, but this developer is not set up → offer
//     to install (if needed) and run `entire enable`.
//
// It never returns an error and never disrupts the command the user ran. The
// common case (an ordinary repo where Entire is neither adopted nor set up)
// exits after two cheap stats.
func maybePromptOnboarding(cmd *cobra.Command) {
	if cmd == nil || onboardingNudgeSkip[cmd.Name()] {
		return
	}
	ctx := cmd.Context()

	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return // not in a git repo
	}

	if onboarding.RepoIsAdopted(repoRoot) {
		if settings.IsSetUpAny(ctx) {
			return // adopted repo where this developer already engaged
		}
		runTeammateOnboarding(cmd)
		return
	}

	// Not adopted: only relevant to a developer actively using Entire here.
	if !settings.IsSetUp(ctx) {
		return // Entire isn't set up in this repo; nothing to suggest
	}
	s, err := LoadEntireSettings(ctx)
	if err != nil || !s.Enabled {
		return // set up but disabled (or unreadable) — respect that
	}
	runChampionAdoptPrompt(cmd, repoRoot, s.AdoptPromptThreshold())
}

// runChampionAdoptPrompt offers to adopt Entire for the team. It fires only
// after the developer has made at least `threshold` commits with Entire enabled
// here (the value signal), at most once per clone, and only interactively. A
// negative threshold disables the prompt entirely.
func runChampionAdoptPrompt(cmd *cobra.Command, repoRoot string, threshold int) {
	ctx := cmd.Context()

	if threshold < 0 {
		return // explicitly disabled via settings
	}
	if asked, _ := onboarding.AdoptAsked(ctx); asked { //nolint:errcheck // best-effort: a read error means "ask"
		return
	}
	if count, _ := onboarding.CommitCount(ctx); count < threshold { //nolint:errcheck // best-effort: a read error counts as 0
		return // not enough Entire usage yet to suggest team adoption
	}
	if !interactive.CanPromptInteractively() {
		return // ask later in a real terminal; don't burn the one-time prompt
	}

	out := cmd.OutOrStdout()
	slug := onboarding.RepoSlug(ctx, repoRoot)
	fmt.Fprintf(out, "\nYou're using Entire in %s.\n\n", slug)
	fmt.Fprintln(out, "Want to enable Entire for the rest of your team?")
	fmt.Fprintln(out, "This adds lightweight onboarding files so teammates are asked once")
	fmt.Fprintln(out, "whether they'd like to use Entire.")

	confirmed := true
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Enable Entire for your team?").
				Affirmative("Yes").
				Negative("No").
				Value(&confirmed),
		),
	)
	if err := form.Run(); err != nil {
		return // cancelled: ask again next time
	}

	// They gave a definitive answer; don't ask again regardless of choice.
	_ = onboarding.MarkAdoptAsked(ctx) //nolint:errcheck // best-effort marker

	if !confirmed {
		fmt.Fprintln(out, "No problem — run 'entire adopt' whenever you're ready.")
		return
	}

	written, err := runAdopt(ctx, repoRoot)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Could not adopt Entire: %v\n", err)
		return
	}
	fmt.Fprintln(out, "\nAdded onboarding files (staged):")
	for _, f := range written {
		fmt.Fprintf(out, "  %s\n", f)
	}
	fmt.Fprintln(out, "\nCommit and push to share with your team:")
	fmt.Fprintln(out, "  git commit -m \"Adopt Entire\"")
}

// runTeammateOnboarding performs the one-time teammate prompt: an adopted repo
// where this developer is not yet set up. On "yes" it runs the enable flow.
func runTeammateOnboarding(cmd *cobra.Command) {
	ctx := cmd.Context()

	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return
	}
	if !onboarding.RepoIsAdopted(repoRoot) {
		return
	}
	if settings.IsSetUpAny(ctx) {
		return
	}

	status, _ := onboarding.ReadStatus(ctx) //nolint:errcheck // best-effort: a read error means "ask"
	if onboarding.Answered(status) {
		return
	}

	if !interactive.CanPromptInteractively() {
		// Can't ask now (CI, piped, agent subprocess). Record a non-final
		// "deferred" marker only if nothing recorded yet, so a real terminal
		// can still ask later.
		if status == onboarding.StatusUnseen {
			_ = onboarding.WriteStatus(ctx, onboarding.StatusDeferred) //nolint:errcheck // best-effort marker
		}
		return
	}

	out := cmd.OutOrStdout()
	printOnboardingBanner(out, onboarding.RepoSlug(ctx, repoRoot))

	confirmed := true
	form := NewAccessibleForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Set up Entire?").
				Affirmative("Yes").
				Negative("No").
				Value(&confirmed),
		),
	)
	if err := form.Run(); err != nil {
		return // cancelled: leave unset so Ctrl+C isn't a silent opt-out
	}

	if !confirmed {
		_ = onboarding.WriteStatus(ctx, onboarding.StatusDeclined) //nolint:errcheck // best-effort marker
		fmt.Fprintln(out, "No problem — you won't be asked again. Run 'entire enable' anytime.")
		return
	}

	if err := runSetupFlow(ctx, out, EnableOptions{}); err != nil {
		// Setup failed; don't burn the prompt — let them try again next time.
		fmt.Fprintf(cmd.ErrOrStderr(), "Entire setup did not complete: %v\n", err)
		return
	}
	_ = onboarding.WriteStatus(ctx, onboarding.StatusAccepted) //nolint:errcheck // best-effort marker
}

// printOnboardingBanner renders the bordered teammate-facing introduction.
func printOnboardingBanner(w io.Writer, slug string) {
	const rule = "─────────────────────────────"
	fmt.Fprintf(w, "\n%s\n", rule)
	if slug != "" && slug != "this repository" {
		fmt.Fprintf(w, "%s uses Entire.\n\n", slug)
	} else {
		fmt.Fprintf(w, "This repository uses Entire.\n\n")
	}
	fmt.Fprintln(w, "Entire automatically captures coding progress and creates checkpoints")
	fmt.Fprintln(w, "so teammates can see work as it evolves.")
	fmt.Fprintf(w, "\nLearn more:\n%s\n", onboarding.DefaultDocsURL)
	fmt.Fprintf(w, "%s\n", rule)
}

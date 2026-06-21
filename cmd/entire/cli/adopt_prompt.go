package cli

// This file implements the teammate side of adoption: the one-time
// "this repo uses Entire — want to set it up?" prompt.
//
// It runs as the hidden `entire adopt --check`, which the committed .githooks/
// scripts call. It is deliberately a no-op unless ALL of these hold: the repo
// has been adopted, this developer hasn't set Entire up here, they haven't
// already answered, and we have a real terminal to ask on. On "yes" it runs the
// normal enable flow.

import (
	"fmt"
	"io"

	"charm.land/huh/v2"

	"github.com/entireio/cli/cmd/entire/cli/adopt"
	"github.com/entireio/cli/cmd/entire/cli/interactive"
	"github.com/entireio/cli/cmd/entire/cli/paths"
	"github.com/entireio/cli/cmd/entire/cli/settings"
	"github.com/spf13/cobra"
)

// runAdoptCheck performs the one-time teammate prompt: in a repo that has been
// adopted, where this developer hasn't set Entire up yet, ask once whether they
// want to. On "yes" it runs the enable flow. It never returns an error and never
// disrupts whatever the caller was doing.
func runAdoptCheck(cmd *cobra.Command) {
	ctx := cmd.Context()

	// Only act inside a git repo that has actually adopted Entire.
	repoRoot, err := paths.WorktreeRoot(ctx)
	if err != nil {
		return
	}
	if !adopt.RepoIsAdopted(repoRoot) {
		return
	}
	// If Entire is already set up here, there's nothing to ask.
	if settings.IsSetUpAny(ctx) {
		return
	}

	// Ask at most once per clone.
	status, _ := adopt.ReadStatus(ctx) //nolint:errcheck // best-effort: a read error means "ask"
	if adopt.Answered(status) {
		return
	}

	if !interactive.CanPromptInteractively() {
		// Can't ask now (CI, piped, agent subprocess). Record a non-final
		// "deferred" marker only if nothing is recorded yet, so a real terminal
		// can still ask later.
		if status == adopt.StatusUnseen {
			_ = adopt.WriteStatus(ctx, adopt.StatusDeferred) //nolint:errcheck // best-effort marker
		}
		return
	}

	out := cmd.OutOrStdout()
	printAdoptBanner(out, adopt.RepoSlug(ctx, repoRoot))

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
		_ = adopt.WriteStatus(ctx, adopt.StatusDeclined) //nolint:errcheck // best-effort marker
		fmt.Fprintln(out, "No problem — you won't be asked again. Run 'entire enable' anytime.")
		return
	}

	if err := runSetupFlow(ctx, out, EnableOptions{}); err != nil {
		// Setup failed; don't burn the prompt — let them try again next time.
		fmt.Fprintf(cmd.ErrOrStderr(), "Entire setup did not complete: %v\n", err)
		return
	}
	_ = adopt.WriteStatus(ctx, adopt.StatusAccepted) //nolint:errcheck // best-effort marker
}

// printAdoptBanner renders the bordered, teammate-facing introduction.
func printAdoptBanner(w io.Writer, slug string) {
	const rule = "─────────────────────────────"
	fmt.Fprintf(w, "\n%s\n", rule)
	if slug != "" && slug != "this repository" {
		fmt.Fprintf(w, "%s uses Entire.\n\n", slug)
	} else {
		fmt.Fprintf(w, "This repository uses Entire.\n\n")
	}
	fmt.Fprintln(w, "Entire automatically captures coding progress and creates checkpoints")
	fmt.Fprintln(w, "so teammates can see work as it evolves.")
	fmt.Fprintf(w, "\nLearn more:\n%s\n", adopt.DefaultDocsURL)
	fmt.Fprintf(w, "%s\n", rule)
}

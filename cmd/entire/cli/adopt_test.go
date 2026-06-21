package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/onboarding"
	"github.com/entireio/cli/cmd/entire/cli/settings"
	"github.com/entireio/cli/cmd/entire/cli/testutil"
	"github.com/spf13/cobra"
)

func TestRunAdopt_CreatesCommittedArtifacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testutil.InitRepo(t, dir)

	written, err := runAdopt(context.Background(), dir)
	if err != nil {
		t.Fatalf("runAdopt: %v", err)
	}

	wantFiles := []string{
		".entire/config",
		".githooks/" + onboardingShellName,
		".githooks/post-checkout",
		".githooks/post-merge",
		"README.md",
	}
	for _, rel := range wantFiles {
		if _, statErr := os.Stat(filepath.Join(dir, rel)); statErr != nil {
			t.Errorf("expected %s to exist: %v", rel, statErr)
		}
	}
	if len(written) != len(wantFiles) {
		t.Errorf("runAdopt reported %d files, want %d (%v)", len(written), len(wantFiles), written)
	}

	if !onboarding.RepoIsAdopted(dir) {
		t.Fatal("repo should be adopted after runAdopt")
	}
	cfg, err := onboarding.ReadConfig(dir)
	if err != nil || !cfg.Adopted {
		t.Fatalf("ReadConfig adopted = %+v, %v", cfg, err)
	}

	// Hooks must be executable so they actually run as git hooks.
	for _, name := range append([]string{onboardingShellName}, committedHookWrappers...) {
		info, statErr := os.Stat(filepath.Join(dir, ".githooks", name))
		if statErr != nil {
			t.Fatalf("stat hook %s: %v", name, statErr)
		}
		if info.Mode()&0o100 == 0 {
			t.Errorf("hook %s is not executable (mode %v)", name, info.Mode())
		}
	}

	// README carries the cross-platform install path for teammates with no CLI.
	readme := readFile(t, filepath.Join(dir, "README.md"))
	for _, want := range []string{readmeStartMarker, readmeEndMarker, "install.sh | bash", "entire enable"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README missing %q", want)
		}
	}
}

func TestUpdateReadme_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if _, err := updateReadme(dir); err != nil {
		t.Fatalf("updateReadme #1: %v", err)
	}
	if _, err := updateReadme(dir); err != nil {
		t.Fatalf("updateReadme #2: %v", err)
	}

	readme := readFile(t, filepath.Join(dir, "README.md"))
	if n := strings.Count(readme, readmeStartMarker); n != 1 {
		t.Fatalf("expected exactly 1 start marker, got %d", n)
	}
	if n := strings.Count(readme, readmeEndMarker); n != 1 {
		t.Fatalf("expected exactly 1 end marker, got %d", n)
	}
}

func TestUpdateReadme_PreservesExistingContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	original := "# My Project\n\nSome important docs.\n"
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := updateReadme(dir); err != nil {
		t.Fatalf("updateReadme: %v", err)
	}

	readme := readFile(t, filepath.Join(dir, "README.md"))
	if !strings.Contains(readme, "# My Project") || !strings.Contains(readme, "Some important docs.") {
		t.Fatal("updateReadme dropped existing README content")
	}
	if !strings.Contains(readme, readmeStartMarker) {
		t.Fatal("updateReadme did not add the Entire section")
	}
}

func TestReplaceBetween(t *testing.T) {
	t.Parallel()
	got := replaceBetween("a START old END b", "START", "END", "START new END")
	if want := "a START new END b"; got != want {
		t.Fatalf("replaceBetween = %q, want %q", got, want)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// newOnboardTestCmd builds a minimal cobra command with a discardable output
// and a real context for exercising the nudge logic.
func newOnboardTestCmd(name string) *cobra.Command {
	c := &cobra.Command{Use: name}
	c.SetContext(context.Background())
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	return c
}

func TestTeammateOnboarding_DeferredWhenNonInteractive(t *testing.T) {
	// Uses CWD-based git/settings resolution; not parallel.
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	// Adopted, but this developer is not set up here.
	mustWriteConfig(t, dir)

	runTeammateOnboarding(newOnboardTestCmd("status"))

	if s := readStatus(t); s != onboarding.StatusDeferred {
		t.Fatalf("status = %q, want deferred (non-interactive should defer)", s)
	}
}

func TestTeammateOnboarding_NoopWhenNotAdopted(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)

	runTeammateOnboarding(newOnboardTestCmd("status"))

	if s := readStatus(t); s != onboarding.StatusUnseen {
		t.Fatalf("status = %q, want unseen (non-adopted repo must be a no-op)", s)
	}
}

func TestTeammateOnboarding_NoopWhenAlreadyAnswered(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	mustWriteConfig(t, dir)
	if err := onboarding.WriteStatus(context.Background(), onboarding.StatusDeclined); err != nil {
		t.Fatal(err)
	}

	runTeammateOnboarding(newOnboardTestCmd("status"))

	if s := readStatus(t); s != onboarding.StatusDeclined {
		t.Fatalf("status = %q, want declined unchanged", s)
	}
}

func TestMaybePromptOnboarding_SkipsLifecycleCommands(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	mustWriteConfig(t, dir)

	// A skip-listed command must not even record a deferred marker.
	maybePromptOnboarding(newOnboardTestCmd("login"))
	if s := readStatus(t); s != onboarding.StatusUnseen {
		t.Fatalf("status = %q after skip-listed command, want unseen", s)
	}

	// A non-skip-listed command goes through the teammate gate (defers).
	maybePromptOnboarding(newOnboardTestCmd("status"))
	if s := readStatus(t); s != onboarding.StatusDeferred {
		t.Fatalf("status = %q after normal command, want deferred", s)
	}
}

func TestMaybePromptOnboarding_ChampionDefersWhenNonInteractive(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	// Developer is using Entire here, but the repo is NOT adopted yet, and has
	// reached the default commit threshold (value signal met).
	mustEnableRepo(t, dir)
	ctx := context.Background()
	for range settings.DefaultAdoptPromptAfterCommits {
		if err := onboarding.IncrementCommitCount(ctx); err != nil {
			t.Fatal(err)
		}
	}

	maybePromptOnboarding(newOnboardTestCmd("status"))

	// Threshold met but non-interactive: must not adopt and must not burn the
	// one-time prompt.
	if onboarding.RepoIsAdopted(dir) {
		t.Fatal("champion path adopted the repo non-interactively")
	}
	if asked := adoptAsked(t); asked {
		t.Fatal("champion prompt marked asked non-interactively; should defer")
	}
}

func TestMaybePromptOnboarding_NoopInUnrelatedRepo(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	// Not adopted and Entire not set up: neither path applies.
	maybePromptOnboarding(newOnboardTestCmd("status"))

	if onboarding.RepoIsAdopted(dir) {
		t.Fatal("unexpectedly adopted")
	}
	if s := readStatus(t); s != onboarding.StatusUnseen {
		t.Fatalf("status = %q, want unseen", s)
	}
}

func readStatus(t *testing.T) onboarding.Status {
	t.Helper()
	s, err := onboarding.ReadStatus(context.Background())
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	return s
}

func adoptAsked(t *testing.T) bool {
	t.Helper()
	asked, err := onboarding.AdoptAsked(context.Background())
	if err != nil {
		t.Fatalf("AdoptAsked: %v", err)
	}
	return asked
}

func mustWriteConfig(t *testing.T, dir string) {
	t.Helper()
	if err := onboarding.WriteConfig(dir, &onboarding.Config{Adopted: true, DocsURL: onboarding.DefaultDocsURL}); err != nil {
		t.Fatal(err)
	}
}

// mustEnableRepo writes a minimal committed settings file so the repo reports as
// set up and enabled for this developer (without running the full enable flow).
func mustEnableRepo(t *testing.T, dir string) {
	t.Helper()
	entireDir := filepath.Join(dir, ".entire")
	if err := os.MkdirAll(entireDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entireDir, "settings.json"), []byte(`{"enabled":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

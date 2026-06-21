package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/adopt"
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

	// adopt writes the marker plus the committed hooks — and nothing else (it
	// must not touch the team's README or any other file).
	wantFiles := []string{
		".entire/config",
		".githooks/" + adoptHookName,
		".githooks/post-checkout",
		".githooks/post-merge",
	}
	for _, rel := range wantFiles {
		if _, statErr := os.Stat(filepath.Join(dir, rel)); statErr != nil {
			t.Errorf("expected %s to exist: %v", rel, statErr)
		}
	}
	if len(written) != len(wantFiles) {
		t.Errorf("runAdopt reported %d files, want %d (%v)", len(written), len(wantFiles), written)
	}

	// adopt must not create or modify a README.
	if _, statErr := os.Stat(filepath.Join(dir, "README.md")); statErr == nil {
		t.Error("adopt should not create a README.md")
	}

	if !adopt.RepoIsAdopted(dir) {
		t.Fatal("repo should be adopted after runAdopt")
	}
	cfg, err := adopt.ReadConfig(dir)
	if err != nil || !cfg.Adopted {
		t.Fatalf("ReadConfig adopted = %+v, %v", cfg, err)
	}

	// Hooks must be executable so they actually run as git hooks.
	for _, name := range append([]string{adoptHookName}, committedHookWrappers...) {
		info, statErr := os.Stat(filepath.Join(dir, ".githooks", name))
		if statErr != nil {
			t.Fatalf("stat hook %s: %v", name, statErr)
		}
		if info.Mode()&0o100 == 0 {
			t.Errorf("hook %s is not executable (mode %v)", name, info.Mode())
		}
	}
}

// newAdoptTestCmd builds a minimal cobra command with a discardable output
// and a real context for exercising the nudge logic.
func newAdoptTestCmd(name string) *cobra.Command {
	c := &cobra.Command{Use: name}
	c.SetContext(context.Background())
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	return c
}

func TestAdoptCheck_DeferredWhenNonInteractive(t *testing.T) {
	// Uses CWD-based git/settings resolution; not parallel.
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	// Adopted, but this developer is not set up here.
	mustWriteConfig(t, dir)

	runAdoptCheck(newAdoptTestCmd("status"))

	if s := readStatus(t); s != adopt.StatusDeferred {
		t.Fatalf("status = %q, want deferred (non-interactive should defer)", s)
	}
}

func TestAdoptCheck_NoopWhenNotAdopted(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)

	runAdoptCheck(newAdoptTestCmd("status"))

	if s := readStatus(t); s != adopt.StatusUnseen {
		t.Fatalf("status = %q, want unseen (non-adopted repo must be a no-op)", s)
	}
}

func TestAdoptCheck_NoopWhenAlreadyAnswered(t *testing.T) {
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	mustWriteConfig(t, dir)
	if err := adopt.WriteStatus(context.Background(), adopt.StatusDeclined); err != nil {
		t.Fatal(err)
	}

	runAdoptCheck(newAdoptTestCmd("status"))

	if s := readStatus(t); s != adopt.StatusDeclined {
		t.Fatalf("status = %q, want declined unchanged", s)
	}
}

func readStatus(t *testing.T) adopt.Status {
	t.Helper()
	s, err := adopt.ReadStatus(context.Background())
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	return s
}

func mustWriteConfig(t *testing.T, dir string) {
	t.Helper()
	if err := adopt.WriteConfig(dir, &adopt.Config{Adopted: true, DocsURL: adopt.DefaultDocsURL}); err != nil {
		t.Fatal(err)
	}
}

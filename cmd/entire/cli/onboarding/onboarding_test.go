package onboarding

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/entireio/cli/cmd/entire/cli/testutil"
)

func TestSlugFromRemote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"ssh scp github", "git@github.com:acme/backend.git", "acme/backend"},
		{"https github .git", "https://github.com/acme/backend.git", "acme/backend"},
		{"https github no suffix", "https://github.com/acme/backend", "acme/backend"},
		{"ssh url", "ssh://git@github.com/acme/backend.git", "acme/backend"},
		{"gitlab nested", "git@gitlab.com:group/sub/repo.git", "sub/repo"},
		{"trailing slash", "https://example.com/owner/repo/", "owner/repo"},
		{"self-hosted port", "ssh://git@git.internal:2222/team/app.git", "team/app"},
		{"empty", "", ""},
		{"not a url", "not-a-url", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := slugFromRemote(tc.in); got != tc.want {
				t.Fatalf("slugFromRemote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAnswered(t *testing.T) {
	t.Parallel()
	cases := map[Status]bool{
		StatusUnseen:   false,
		StatusDeferred: false,
		StatusAccepted: true,
		StatusDeclined: true,
	}
	for s, want := range cases {
		if got := Answered(s); got != want {
			t.Errorf("Answered(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestConfig_RoundTripAndDetect(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	if RepoIsAdopted(dir) {
		t.Fatal("fresh dir should not be adopted")
	}

	want := &Config{Adopted: true, Repo: "acme/backend", DocsURL: DefaultDocsURL, MinVersion: "1.2.3"}
	if err := WriteConfig(dir, want); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if !RepoIsAdopted(dir) {
		t.Fatal("dir should be adopted after WriteConfig")
	}

	got, err := ReadConfig(dir)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if !got.Adopted || got.Repo != want.Repo || got.DocsURL != want.DocsURL || got.MinVersion != want.MinVersion {
		t.Fatalf("ReadConfig = %+v, want %+v", got, want)
	}
}

func TestRepoIsAdopted_DirIsNotMarker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Create a directory at the marker path; it must not count as adopted.
	if err := os.MkdirAll(ConfigPath(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if RepoIsAdopted(dir) {
		t.Fatal("a directory at the marker path must not count as adopted")
	}
}

func TestCommitCount_Increment(t *testing.T) {
	// Uses git-common-dir resolution from CWD, so cannot run in parallel.
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	ctx := context.Background()

	if n, err := CommitCount(ctx); err != nil || n != 0 {
		t.Fatalf("initial CommitCount = %d, %v; want 0, nil", n, err)
	}
	for i := 1; i <= 3; i++ {
		if err := IncrementCommitCount(ctx); err != nil {
			t.Fatalf("IncrementCommitCount: %v", err)
		}
		if n, err := CommitCount(ctx); err != nil || n != i {
			t.Fatalf("after %d increments CommitCount = %d, %v; want %d, nil", i, n, err, i)
		}
	}

	// A corrupt counter reads as 0 rather than erroring.
	path := filepath.Join(dir, ".git", "entire", "commit-count")
	if err := os.WriteFile(path, []byte("not-a-number"), 0o644); err != nil {
		t.Fatal(err)
	}
	if n, err := CommitCount(ctx); err != nil || n != 0 {
		t.Fatalf("corrupt CommitCount = %d, %v; want 0, nil", n, err)
	}
}

func TestStatus_RoundTrip(t *testing.T) {
	// Uses git-common-dir resolution from CWD, so cannot run in parallel.
	dir := t.TempDir()
	testutil.InitRepo(t, dir)
	t.Chdir(dir)
	ctx := context.Background()

	if s, err := ReadStatus(ctx); err != nil || s != StatusUnseen {
		t.Fatalf("ReadStatus initial = %q, %v; want unseen, nil", s, err)
	}

	if err := WriteStatus(ctx, StatusAccepted); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	if s, err := ReadStatus(ctx); err != nil || s != StatusAccepted {
		t.Fatalf("ReadStatus after write = %q, %v; want accepted, nil", s, err)
	}

	// State lives under .git/, never in the work tree.
	if _, err := os.Stat(filepath.Join(dir, ".git", "entire", "prompt-seen")); err != nil {
		t.Fatalf("expected prompt-seen under .git/entire: %v", err)
	}

	// Overwriting with a final answer is respected.
	if err := WriteStatus(ctx, StatusDeclined); err != nil {
		t.Fatalf("WriteStatus declined: %v", err)
	}
	if s, err := ReadStatus(ctx); err != nil || s != StatusDeclined {
		t.Fatalf("ReadStatus = %q, %v; want declined, nil", s, err)
	}
}

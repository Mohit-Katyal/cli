// Package onboarding implements the lightweight, consent-based team-adoption
// flow for the Entire CLI.
//
// Two concepts are kept strictly separate:
//
//   - Repository adoption ("entire adopt") writes committed artifacts that
//     advertise that a repo uses Entire: the .entire/config marker, committed
//     .githooks/ scripts, and a README section. This package owns reading and
//     writing the .entire/config marker.
//
//   - Per-developer onboarding state ("have we already asked this person?") is
//     stored OUTSIDE the work tree, under <git-common-dir>/entire/prompt-seen,
//     so it is never committed, survives future pulls, and is unique per clone.
//
// This package is intentionally UI-free and dependency-light (stdlib + git via
// exec only) so it can be imported anywhere in the CLI without import cycles.
// All interactive prompting lives in the cli package.
package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ConfigRelPath is the committed adoption marker, relative to the work-tree
// root. Its presence is what makes a repository "adopted".
const ConfigRelPath = ".entire/config"

// DefaultDocsURL is the learn-more link shown to teammates during onboarding.
const DefaultDocsURL = "https://docs.entire.io/overview"

// Status records whether the current developer has already been asked, in this
// clone, whether they want to onboard. It is persisted as the first token of
// the prompt-seen file (format: "v1 <status> <rfc3339>").
type Status string

const (
	// StatusUnseen means the developer has never been asked in this clone.
	StatusUnseen Status = ""
	// StatusAccepted means the developer accepted and was set up.
	StatusAccepted Status = "accepted"
	// StatusDeclined means the developer declined; never ask again.
	StatusDeclined Status = "declined"
	// StatusDeferred means we could not ask (non-interactive context). We are
	// allowed to ask again later in an interactive session.
	StatusDeferred Status = "deferred"
)

// stateVersion lets us deliberately re-ask on a future major onboarding change
// without re-asking people who already answered the current version.
const stateVersion = "v1"

// Config is the committed .entire/config adoption marker. It is intentionally
// small, human-readable, and language-agnostic — it touches no build system.
type Config struct {
	Adopted    bool   `json:"adopted"`
	Repo       string `json:"repo,omitempty"`
	DocsURL    string `json:"docs_url,omitempty"`
	MinVersion string `json:"min_version,omitempty"`
	AdoptedAt  string `json:"adopted_at,omitempty"`
}

// ConfigPath returns the absolute path to the adoption marker for the given
// work-tree root.
func ConfigPath(repoRoot string) string {
	return filepath.Join(repoRoot, ConfigRelPath)
}

// RepoIsAdopted reports whether the work tree at repoRoot contains a committed
// adoption marker. It is deliberately a cheap stat so it is safe to call on a
// hot path (e.g. the root PersistentPostRun nudge).
func RepoIsAdopted(repoRoot string) bool {
	if repoRoot == "" {
		return false
	}
	info, err := os.Stat(ConfigPath(repoRoot))
	return err == nil && !info.IsDir()
}

// ReadConfig loads the adoption marker. A missing file is reported via
// errors.Is(err, fs.ErrNotExist) on the returned error.
func ReadConfig(repoRoot string) (*Config, error) {
	data, err := os.ReadFile(ConfigPath(repoRoot))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ConfigRelPath, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ConfigRelPath, err)
	}
	return &cfg, nil
}

// WriteConfig writes the adoption marker, creating the .entire/ directory if
// needed. The file is pretty-printed JSON so it reviews cleanly in a PR.
func WriteConfig(repoRoot string, cfg *Config) error {
	dir := filepath.Dir(ConfigPath(repoRoot))
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // .entire/ is a normal, world-readable project dir
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode adoption config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(ConfigPath(repoRoot), data, 0o644); err != nil { //nolint:gosec // committed marker is meant to be world-readable
		return fmt.Errorf("write %s: %w", ConfigRelPath, err)
	}
	return nil
}

// promptSeenPath returns <git-common-dir>/entire/prompt-seen. The git common
// dir is used (not the work-tree .git) so the answer is shared across linked
// worktrees of the same clone but never committed.
func promptSeenPath(ctx context.Context) (string, error) {
	common, err := gitCommonDir(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "entire", "prompt-seen"), nil
}

// ReadStatus returns the developer's onboarding answer for this clone, or
// StatusUnseen if they have not been asked yet.
func ReadStatus(ctx context.Context) (Status, error) {
	path, err := promptSeenPath(ctx)
	if err != nil {
		return StatusUnseen, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path derived from trusted git-common-dir
	if err != nil {
		if os.IsNotExist(err) {
			return StatusUnseen, nil
		}
		return StatusUnseen, fmt.Errorf("read onboarding state: %w", err)
	}
	fields := strings.Fields(string(data))
	// Format: "v1 <status> <rfc3339>". Be lenient about extra/missing trailing
	// fields so a hand-edited or future-format file never crashes a git command.
	if len(fields) >= 2 {
		return Status(fields[1]), nil
	}
	if len(fields) == 1 {
		// Tolerate a bare status token written by the shell hook fallback.
		return Status(fields[0]), nil
	}
	return StatusUnseen, nil
}

// WriteStatus records the developer's onboarding answer for this clone.
func WriteStatus(ctx context.Context, s Status) error {
	path, err := promptSeenPath(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // under .git/, per-user local state
		return fmt.Errorf("create onboarding state dir: %w", err)
	}
	line := fmt.Sprintf("%s %s %s\n", stateVersion, s, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil { //nolint:gosec // under .git/, per-user local state
		return fmt.Errorf("write onboarding state: %w", err)
	}
	return nil
}

// Answered reports whether the developer has given a final answer (accepted or
// declined) in this clone. A deferred state is not final — we may ask again.
func Answered(s Status) bool {
	return s == StatusAccepted || s == StatusDeclined
}

// adoptAskedPath returns <git-common-dir>/entire/adopt-asked: the marker that
// records we already asked this developer (the repo champion) whether to adopt
// Entire for the whole team. Kept separate from prompt-seen so the champion and
// teammate prompts never collide.
func adoptAskedPath(ctx context.Context) (string, error) {
	common, err := gitCommonDir(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "entire", "adopt-asked"), nil
}

// AdoptAsked reports whether the champion has already been asked, in this clone,
// to adopt Entire for the team.
func AdoptAsked(ctx context.Context) (bool, error) {
	path, err := adoptAskedPath(ctx)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("stat adopt-asked state: %w", err)
}

// MarkAdoptAsked records that the champion has been asked (so we never nag).
func MarkAdoptAsked(ctx context.Context) error {
	path, err := adoptAskedPath(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // under .git/, per-user local state
		return fmt.Errorf("create onboarding state dir: %w", err)
	}
	line := fmt.Sprintf("%s %s\n", stateVersion, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil { //nolint:gosec // under .git/, per-user local state
		return fmt.Errorf("write adopt-asked state: %w", err)
	}
	return nil
}

// commitCountPath returns <git-common-dir>/entire/commit-count, a per-clone
// tally of commits made while Entire was enabled here. It is the "value signal"
// that gates the champion adoption prompt.
func commitCountPath(ctx context.Context) (string, error) {
	common, err := gitCommonDir(ctx)
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "entire", "commit-count"), nil
}

// CommitCount returns how many commits have been made with Entire enabled in
// this clone. A missing counter reads as 0.
func CommitCount(ctx context.Context) (int, error) {
	path, err := commitCountPath(ctx)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path derived from trusted git-common-dir
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read commit count: %w", err)
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if convErr != nil {
		return 0, nil //nolint:nilerr // tolerate a corrupt counter by treating it as 0
	}
	return n, nil
}

// IncrementCommitCount bumps the per-clone commit tally by one. It is called
// best-effort from the post-commit hook and must never disrupt a commit.
func IncrementCommitCount(ctx context.Context) error {
	path, err := commitCountPath(ctx)
	if err != nil {
		return err
	}
	current, _ := CommitCount(ctx)                                 //nolint:errcheck // a read error resets to 0, which is safe
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // under .git/, per-user local state
		return fmt.Errorf("create onboarding state dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(current+1)+"\n"), 0o644); err != nil { //nolint:gosec // under .git/, per-user local state
		return fmt.Errorf("write commit count: %w", err)
	}
	return nil
}

// RepoSlug returns a best-effort "owner/repo" identity for the work tree, used
// only for display in prompts. It never errors: on any failure it falls back to
// the work-tree directory's base name, and finally to "this repository".
func RepoSlug(ctx context.Context, repoRoot string) string {
	out, err := runGit(ctx, repoRoot, "remote", "get-url", "origin")
	if err == nil {
		if slug := slugFromRemote(strings.TrimSpace(out)); slug != "" {
			return slug
		}
	}
	if base := filepath.Base(repoRoot); base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}
	return "this repository"
}

// slugFromRemote extracts "owner/repo" from common git remote URL shapes
// (https, ssh scp-like, and ssh:// URLs), trimming any trailing ".git". It is
// host-agnostic so it works for GitHub, GitLab, Bitbucket, self-hosted, etc.
func slugFromRemote(remote string) string {
	if remote == "" {
		return ""
	}
	s := remote
	// scp-like syntax: git@host:owner/repo.git
	if i := strings.Index(s, "@"); i >= 0 {
		if j := strings.Index(s[i:], ":"); j >= 0 {
			s = s[i+j+1:]
		}
	}
	// URL syntax: scheme://host/owner/repo.git — keep everything after host.
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			s = rest[j+1:]
		}
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), ".git")
	s = strings.Trim(s, "/")
	// Reduce to the final two path segments (owner/repo).
	parts := strings.Split(s, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return ""
}

// gitCommonDir resolves the shared .git directory (correct under linked
// worktrees) as an absolute path.
func gitCommonDir(ctx context.Context) (string, error) {
	out, err := runGit(ctx, "", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(out)
	if dir == "" {
		return "", errors.New("empty git common dir")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve git common dir: %w", err)
	}
	return abs, nil
}

// runGit runs a git command (optionally in dir) and returns stdout. It sets
// GIT_TERMINAL_PROMPT=0 so it can never block on credentials from a hook.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

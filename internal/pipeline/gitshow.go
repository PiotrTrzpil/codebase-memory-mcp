package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"time"

	"golang.org/x/sync/errgroup"
)

// gitShowTimeout is the per-file timeout for individual git show calls.
const gitShowTimeout = 10 * time.Second

// OldFileContents retrieves file contents from the base version for a given diff scope.
// Returns a map of relative path -> file content bytes.
//
// Ref selection by scope:
//   - "staged" / "unstaged" / "all": reads from HEAD (pre-change version)
//   - "branch": reads from baseBranch (defaults to "main" when empty)
//
// Added files (Status == "A") are omitted — they have no old version.
// Renamed files are keyed by their OldPath.
// If git show fails for an individual file (e.g. binary, submodule), a warning is
// logged and that file is skipped; the rest of the map is still returned.
func OldFileContents(repoPath string, scope DiffScope, baseBranch string, files []ChangedFile) (map[string][]byte, error) {
	ref := oldRef(scope, baseBranch)

	// Collect files that need fetching; skip Added files (no old version exists).
	type job struct {
		mapKey  string // key in the returned map
		gitPath string // path as it appears in the git tree at ref
	}
	var jobs []job
	for _, f := range files {
		if f.Status == "A" {
			continue
		}
		j := job{mapKey: f.Path, gitPath: f.Path}
		if f.OldPath != "" {
			// Renamed: the old tree knows this file by its OldPath.
			j.mapKey = f.OldPath
			j.gitPath = f.OldPath
		}
		jobs = append(jobs, j)
	}

	if len(jobs) == 0 {
		return map[string][]byte{}, nil
	}

	// Locate git once; fail fast if not available.
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("git not found in PATH: install git to use semantic_diff")
	}

	// Bounded parallel git show calls: at most NumCPU concurrent goroutines.
	maxWorkers := runtime.NumCPU()
	if maxWorkers < 1 {
		maxWorkers = 1
	}

	type fetchResult struct {
		mapKey  string
		content []byte // nil means skip (error or missing)
	}

	resultsCh := make(chan fetchResult, len(jobs))
	sem := make(chan struct{}, maxWorkers)

	g, ctx := errgroup.WithContext(context.Background())
	_ = ctx // context passed through for potential future cancellation

	for _, j := range jobs {
		j := j // capture
		g.Go(func() error {
			sem <- struct{}{}
			defer func() { <-sem }()

			content, showErr := runGitShow(gitBin, repoPath, ref, j.gitPath)
			if showErr != nil {
				slog.Warn("gitshow.skip", "ref", ref, "path", j.gitPath, "err", showErr)
				resultsCh <- fetchResult{mapKey: j.mapKey, content: nil}
				return nil // do not abort the group
			}
			resultsCh <- fetchResult{mapKey: j.mapKey, content: content}
			return nil
		})
	}

	// Wait for all goroutines, then close the channel.
	if err := g.Wait(); err != nil {
		// Unreachable: goroutines never return a non-nil error above.
		return nil, fmt.Errorf("OldFileContents: %w", err)
	}
	close(resultsCh)

	out := make(map[string][]byte, len(jobs))
	for r := range resultsCh {
		if r.content != nil {
			out[r.mapKey] = r.content
		}
	}
	return out, nil
}

// oldRef maps a DiffScope to the git ref that represents the "before" state.
func oldRef(scope DiffScope, baseBranch string) string {
	switch scope {
	case DiffBranch:
		if baseBranch == "" {
			return "main"
		}
		return baseBranch
	case DiffCommits:
		// baseBranch acts as fromRef (the "before" commit).
		if baseBranch == "" {
			return "HEAD~1"
		}
		return baseBranch
	default:
		// staged, unstaged, all → HEAD is the committed baseline.
		return "HEAD"
	}
}

// runGitShow executes `git show <ref>:<path>` with a per-call timeout and returns the
// raw file bytes. The caller is responsible for logging / skipping on error.
func runGitShow(gitBin, repoPath, ref, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitShowTimeout)
	defer cancel()

	arg := ref + ":" + path
	cmd := exec.CommandContext(ctx, gitBin, "show", arg)
	cmd.Dir = repoPath

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git show %s: %w", arg, err)
	}
	return out, nil
}

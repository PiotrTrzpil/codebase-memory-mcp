package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// initTestRepo creates a temporary git repository with an initial commit containing
// the given files (map of relative path -> content). Returns the repo path.
func initTestRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")

	for relPath, content := range files {
		full := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
		run("add", relPath)
	}

	run("commit", "-m", "initial commit")
	return dir
}

// commitFiles adds a new commit with the given file changes on top of the existing repo.
func commitFiles(t *testing.T, repoPath string, files map[string]string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for relPath, content := range files {
		full := filepath.Join(repoPath, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
		run("add", relPath)
	}
	run("commit", "-m", "update")
}

// TestOldFileContents_Modified verifies that a modified file's old content is returned.
func TestOldFileContents_Modified(t *testing.T) {
	repoPath := initTestRepo(t, map[string]string{
		"main.go": "package main\n\nfunc Hello() string { return \"old\" }\n",
	})

	// Modify the file (not yet committed — we want HEAD content).
	if err := os.WriteFile(filepath.Join(repoPath, "main.go"), []byte("package main\n\nfunc Hello() string { return \"new\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := []ChangedFile{{Status: "M", Path: "main.go"}}
	got, err := OldFileContents(repoPath, DiffUnstaged, "", files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	content, ok := got["main.go"]
	if !ok {
		t.Fatal("missing key main.go")
	}
	if string(content) != "package main\n\nfunc Hello() string { return \"old\" }\n" {
		t.Errorf("unexpected content: %q", string(content))
	}
}

// TestOldFileContents_Added verifies that added files are omitted (no old version).
func TestOldFileContents_Added(t *testing.T) {
	repoPath := initTestRepo(t, map[string]string{
		"existing.go": "package main\n",
	})

	files := []ChangedFile{
		{Status: "A", Path: "new_file.go"},
		{Status: "M", Path: "existing.go"},
	}
	got, err := OldFileContents(repoPath, DiffUnstaged, "", files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, exists := got["new_file.go"]; exists {
		t.Error("added file should not appear in old contents")
	}
	if _, exists := got["existing.go"]; !exists {
		t.Error("modified file should appear in old contents")
	}
}

// TestOldFileContents_Deleted verifies that deleted files return their old content.
func TestOldFileContents_Deleted(t *testing.T) {
	repoPath := initTestRepo(t, map[string]string{
		"todelete.go": "package main\n\nfunc OldFunc() {}\n",
	})

	// Delete the file on disk.
	if err := os.Remove(filepath.Join(repoPath, "todelete.go")); err != nil {
		t.Fatal(err)
	}

	files := []ChangedFile{{Status: "D", Path: "todelete.go"}}
	got, err := OldFileContents(repoPath, DiffUnstaged, "", files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, ok := got["todelete.go"]
	if !ok {
		t.Fatal("deleted file should appear in old contents (keyed by Path)")
	}
	if string(content) != "package main\n\nfunc OldFunc() {}\n" {
		t.Errorf("unexpected content: %q", string(content))
	}
}

// TestOldFileContents_Renamed verifies that renamed files are keyed by OldPath.
func TestOldFileContents_Renamed(t *testing.T) {
	repoPath := initTestRepo(t, map[string]string{
		"old_name.go": "package main\n\nfunc RenamedFunc() {}\n",
	})

	// Simulate a rename on disk (git rename not committed yet; we test HEAD content).
	files := []ChangedFile{
		{Status: "R", Path: "new_name.go", OldPath: "old_name.go"},
	}
	got, err := OldFileContents(repoPath, DiffUnstaged, "", files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, exists := got["new_name.go"]; exists {
		t.Error("renamed file should be keyed by OldPath, not new Path")
	}
	content, ok := got["old_name.go"]
	if !ok {
		t.Fatal("renamed file should appear keyed by OldPath")
	}
	if string(content) != "package main\n\nfunc RenamedFunc() {}\n" {
		t.Errorf("unexpected content: %q", string(content))
	}
}

// TestOldFileContents_BranchScope verifies that branch scope uses the base branch ref.
func TestOldFileContents_BranchScope(t *testing.T) {
	repoPath := initTestRepo(t, map[string]string{
		"lib.go": "package lib\n\nfunc Base() {}\n",
	})

	// Create a feature branch and add a commit.
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("checkout", "-b", "feature")
	commitFiles(t, repoPath, map[string]string{
		"lib.go": "package lib\n\nfunc Base() {}\n\nfunc New() {}\n",
	})

	// From the feature branch, DiffBranch against "main" should return the main version.
	files := []ChangedFile{{Status: "M", Path: "lib.go"}}
	got, err := OldFileContents(repoPath, DiffBranch, "main", files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, ok := got["lib.go"]
	if !ok {
		t.Fatal("expected lib.go in result")
	}
	if string(content) != "package lib\n\nfunc Base() {}\n" {
		t.Errorf("unexpected content from base branch: %q", string(content))
	}
}

// TestOldFileContents_EmptyInput verifies that an empty file list returns an empty map.
func TestOldFileContents_EmptyInput(t *testing.T) {
	repoPath := initTestRepo(t, map[string]string{"dummy.go": "package main\n"})

	got, err := OldFileContents(repoPath, DiffUnstaged, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %d entries", len(got))
	}
}

// TestOldFileContents_AllAddedSkipsGitShow verifies no git show is attempted when
// all files are Added (avoids errors even with a valid repo).
func TestOldFileContents_AllAddedSkipsGitShow(t *testing.T) {
	repoPath := initTestRepo(t, map[string]string{"seed.go": "package main\n"})

	files := []ChangedFile{
		{Status: "A", Path: "brand_new.go"},
		{Status: "A", Path: "also_new.go"},
	}
	got, err := OldFileContents(repoPath, DiffUnstaged, "", files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map for all-added files, got %d entries", len(got))
	}
}

// TestOldFileContents_MissingFileSkipped verifies that a file missing from HEAD is
// skipped gracefully (warning logged, no error returned).
func TestOldFileContents_MissingFileSkipped(t *testing.T) {
	repoPath := initTestRepo(t, map[string]string{
		"real.go": "package main\n",
	})

	// "ghost.go" was never committed — git show HEAD:ghost.go will fail.
	files := []ChangedFile{
		{Status: "M", Path: "ghost.go"},
		{Status: "M", Path: "real.go"},
	}
	got, err := OldFileContents(repoPath, DiffUnstaged, "", files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ghost.go should be absent; real.go should be present.
	if _, exists := got["ghost.go"]; exists {
		t.Error("ghost.go should have been skipped")
	}
	if _, exists := got["real.go"]; !exists {
		t.Error("real.go should be present despite ghost.go failure")
	}
}

// TestOldRef verifies ref selection per scope.
func TestOldRef(t *testing.T) {
	tests := []struct {
		scope      DiffScope
		baseBranch string
		want       string
	}{
		{DiffUnstaged, "", "HEAD"},
		{DiffStaged, "", "HEAD"},
		{DiffAll, "", "HEAD"},
		{DiffBranch, "main", "main"},
		{DiffBranch, "develop", "develop"},
		{DiffBranch, "", "main"},    // defaults to "main" when empty
		{DiffCommits, "v1.2.0", "v1.2.0"},
		{DiffCommits, "", "HEAD~1"}, // defaults to HEAD~1 when empty
	}
	for _, tt := range tests {
		got := oldRef(tt.scope, tt.baseBranch)
		if got != tt.want {
			t.Errorf("oldRef(%q, %q) = %q, want %q", tt.scope, tt.baseBranch, got, tt.want)
		}
	}
}

// TestOldFileContents_CommitsScope verifies that DiffCommits scope reads old content
// from the fromRef (baseBranch) commit, not from HEAD.
func TestOldFileContents_CommitsScope(t *testing.T) {
	// First commit: initial content.
	repoPath := initTestRepo(t, map[string]string{
		"service.go": "package main\n\nfunc OldImpl() {}\n",
	})

	// Second commit: updated content.
	commitFiles(t, repoPath, map[string]string{
		"service.go": "package main\n\nfunc NewImpl() {}\n",
	})

	// DiffCommits with baseBranch=HEAD~1 should return the content at HEAD~1.
	files := []ChangedFile{{Status: "M", Path: "service.go"}}
	got, err := OldFileContents(repoPath, DiffCommits, "HEAD~1", files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, ok := got["service.go"]
	if !ok {
		t.Fatal("expected service.go in result")
	}
	if string(content) != "package main\n\nfunc OldImpl() {}\n" {
		t.Errorf("unexpected content from fromRef: %q", string(content))
	}
}

// TestOldFileContents_CommitsScope_DefaultRef verifies that empty baseBranch defaults
// to HEAD~1 for DiffCommits scope.
func TestOldFileContents_CommitsScope_DefaultRef(t *testing.T) {
	repoPath := initTestRepo(t, map[string]string{
		"config.go": "package main\n\nconst Version = \"1.0\"\n",
	})

	commitFiles(t, repoPath, map[string]string{
		"config.go": "package main\n\nconst Version = \"2.0\"\n",
	})

	// Empty baseBranch → defaults to HEAD~1.
	files := []ChangedFile{{Status: "M", Path: "config.go"}}
	got, err := OldFileContents(repoPath, DiffCommits, "", files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content, ok := got["config.go"]
	if !ok {
		t.Fatal("expected config.go in result")
	}
	if string(content) != "package main\n\nconst Version = \"1.0\"\n" {
		t.Errorf("unexpected content: %q", string(content))
	}
}

// TestOldFileContents_MultipleFiles verifies parallel fetching of multiple files.
func TestOldFileContents_MultipleFiles(t *testing.T) {
	repoPath := initTestRepo(t, map[string]string{
		"a.go": "package a\n\nfunc A() {}\n",
		"b.go": "package b\n\nfunc B() {}\n",
		"c.go": "package c\n\nfunc C() {}\n",
	})

	// Modify all three files on disk.
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		_ = os.WriteFile(filepath.Join(repoPath, name), []byte("// modified\n"), 0o644)
	}

	files := []ChangedFile{
		{Status: "M", Path: "a.go"},
		{Status: "M", Path: "b.go"},
		{Status: "M", Path: "c.go"},
	}
	got, err := OldFileContents(repoPath, DiffUnstaged, "", files)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}
	expected := map[string]string{
		"a.go": "package a\n\nfunc A() {}\n",
		"b.go": "package b\n\nfunc B() {}\n",
		"c.go": "package c\n\nfunc C() {}\n",
	}
	for path, want := range expected {
		if string(got[path]) != want {
			t.Errorf("%s: got %q, want %q", path, string(got[path]), want)
		}
	}
}

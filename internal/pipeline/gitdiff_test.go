package pipeline

import (
	"strings"
	"testing"
)

func TestParseNameStatusOutput(t *testing.T) {
	input := "M\tinternal/store/nodes.go\nA\tnew_file.go\nD\told_file.go\nR100\tsrc/old.go\tsrc/new.go\n"

	files := ParseNameStatusOutput(input)

	if len(files) != 4 {
		t.Fatalf("expected 4 files, got %d", len(files))
	}

	tests := []struct {
		idx     int
		status  string
		path    string
		oldPath string
	}{
		{0, "M", "internal/store/nodes.go", ""},
		{1, "A", "new_file.go", ""},
		{2, "D", "old_file.go", ""},
		{3, "R", "src/new.go", "src/old.go"},
	}

	for _, tt := range tests {
		f := files[tt.idx]
		if f.Status != tt.status {
			t.Errorf("[%d] status = %q, want %q", tt.idx, f.Status, tt.status)
		}
		if f.Path != tt.path {
			t.Errorf("[%d] path = %q, want %q", tt.idx, f.Path, tt.path)
		}
		if f.OldPath != tt.oldPath {
			t.Errorf("[%d] oldPath = %q, want %q", tt.idx, f.OldPath, tt.oldPath)
		}
	}
}

func TestParseNameStatusOutput_FiltersUntrackable(t *testing.T) {
	input := "M\tpackage-lock.json\nM\tsrc/main.go\nM\tvendor/lib.go\n"
	files := ParseNameStatusOutput(input)

	if len(files) != 1 {
		t.Fatalf("expected 1 trackable file, got %d", len(files))
	}
	if files[0].Path != "src/main.go" {
		t.Errorf("expected src/main.go, got %s", files[0].Path)
	}
}

func TestParseHunksOutput(t *testing.T) {
	input := `diff --git a/main.go b/main.go
index abc1234..def5678 100644
--- a/main.go
+++ b/main.go
@@ -10,3 +10,5 @@ func main() {
+	newLine1()
+	newLine2()
@@ -50,0 +52,2 @@ func helper() {
+	another()
+	line()
diff --git a/binary.png b/binary.png
Binary files a/binary.png and b/binary.png differ
diff --git a/utils.go b/utils.go
--- a/utils.go
+++ b/utils.go
@@ -1 +1 @@ package utils
-old
+new
`

	hunks := ParseHunksOutput(input)

	if len(hunks) != 3 {
		t.Fatalf("expected 3 hunks, got %d", len(hunks))
	}

	// First hunk: main.go @@ -10,3 +10,5 @@
	if hunks[0].Path != "main.go" {
		t.Errorf("hunk 0 path = %q", hunks[0].Path)
	}
	if hunks[0].StartLine != 10 || hunks[0].EndLine != 14 {
		t.Errorf("hunk 0 range = %d-%d, want 10-14", hunks[0].StartLine, hunks[0].EndLine)
	}

	// Second hunk: main.go @@ -50,0 +52,2 @@
	if hunks[1].Path != "main.go" {
		t.Errorf("hunk 1 path = %q", hunks[1].Path)
	}
	if hunks[1].StartLine != 52 || hunks[1].EndLine != 53 {
		t.Errorf("hunk 1 range = %d-%d, want 52-53", hunks[1].StartLine, hunks[1].EndLine)
	}

	// Third hunk: utils.go @@ -1 +1 @@
	if hunks[2].Path != "utils.go" {
		t.Errorf("hunk 2 path = %q", hunks[2].Path)
	}
	if hunks[2].StartLine != 1 || hunks[2].EndLine != 1 {
		t.Errorf("hunk 2 range = %d-%d, want 1-1", hunks[2].StartLine, hunks[2].EndLine)
	}
}

func TestParseHunksOutput_NoNewlineMarker(t *testing.T) {
	input := `diff --git a/file.go b/file.go
--- a/file.go
+++ b/file.go
@@ -5,2 +5,3 @@ func foo() {
+	bar()
\ No newline at end of file
`
	hunks := ParseHunksOutput(input)
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}
	if hunks[0].StartLine != 5 || hunks[0].EndLine != 7 {
		t.Errorf("range = %d-%d, want 5-7", hunks[0].StartLine, hunks[0].EndLine)
	}
}

func TestParseRange(t *testing.T) {
	tests := []struct {
		input     string
		wantStart int
		wantCount int
	}{
		{"10,5", 10, 5},
		{"10", 10, 1},
		{"52,2", 52, 2},
		{"1,0", 1, 0},
	}
	for _, tt := range tests {
		start, count := parseRange(tt.input)
		if start != tt.wantStart || count != tt.wantCount {
			t.Errorf("parseRange(%q) = (%d, %d), want (%d, %d)", tt.input, start, count, tt.wantStart, tt.wantCount)
		}
	}
}

func TestParseHunksOutput_ModeChange(t *testing.T) {
	input := `diff --git a/script.sh b/script.sh
old mode 100644
new mode 100755
`
	hunks := ParseHunksOutput(input)
	if len(hunks) != 0 {
		t.Fatalf("expected 0 hunks for mode-only change, got %d", len(hunks))
	}
}

func TestGitNotFound(t *testing.T) {
	// Override PATH to ensure git can't be found
	t.Setenv("PATH", t.TempDir())

	_, err := runGit(t.TempDir(), []string{"status"})
	if err == nil {
		t.Fatal("expected error when git is not found")
	}
	if !strings.Contains(err.Error(), "git not found in PATH") {
		t.Errorf("expected 'git not found in PATH' error, got: %v", err)
	}
}

func TestParseHunksOutput_Deletion(t *testing.T) {
	// Deletion hunks have +start,0 — should still produce a valid hunk at start line
	input := `diff --git a/file.go b/file.go
--- a/file.go
+++ b/file.go
@@ -10,3 +10,0 @@ func foo() {
`
	hunks := ParseHunksOutput(input)
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}
	// count=0, so endLine = start + 0 - 1 = 9, but we clamp to start
	if hunks[0].StartLine != 10 {
		t.Errorf("start = %d, want 10", hunks[0].StartLine)
	}
}

// TestBuildDiffArgs_Commits verifies that DiffCommits produces the correct git diff range.
func TestBuildDiffArgs_Commits(t *testing.T) {
	tests := []struct {
		name       string
		baseBranch string
		toRef      string
		wantArgs   []string
	}{
		{
			name:       "explicit refs",
			baseBranch: "v1.2.0",
			toRef:      "v1.3.0",
			wantArgs:   []string{"diff", "v1.2.0...v1.3.0"},
		},
		{
			name:       "empty toRef defaults to HEAD",
			baseBranch: "v1.0.0",
			toRef:      "",
			wantArgs:   []string{"diff", "v1.0.0...HEAD"},
		},
		{
			name:       "empty baseBranch defaults to HEAD~1",
			baseBranch: "",
			toRef:      "v2.0.0",
			wantArgs:   []string{"diff", "HEAD~1...v2.0.0"},
		},
		{
			name:       "both empty defaults to HEAD~1...HEAD",
			baseBranch: "",
			toRef:      "",
			wantArgs:   []string{"diff", "HEAD~1...HEAD"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildDiffArgs(DiffCommits, tt.baseBranch, tt.toRef)
			if len(got) != len(tt.wantArgs) {
				t.Fatalf("buildDiffArgs args = %v, want %v", got, tt.wantArgs)
			}
			for i, want := range tt.wantArgs {
				if got[i] != want {
					t.Errorf("arg[%d] = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

// TestBuildDiffArgs_ExistingScopesUnchanged verifies backward compat — existing scopes
// produce the same args regardless of toRef.
func TestBuildDiffArgs_ExistingScopesUnchanged(t *testing.T) {
	tests := []struct {
		scope    DiffScope
		base     string
		wantArgs []string
	}{
		{DiffUnstaged, "", []string{"diff"}},
		{DiffStaged, "", []string{"diff", "--cached"}},
		{DiffAll, "", []string{"diff", "HEAD"}},
		{DiffBranch, "main", []string{"diff", "main...HEAD"}},
		{DiffBranch, "", []string{"diff", "main...HEAD"}}, // defaults to "main"
	}

	for _, tt := range tests {
		got := buildDiffArgs(tt.scope, tt.base, "")
		if len(got) != len(tt.wantArgs) {
			t.Errorf("scope %q: got %v, want %v", tt.scope, got, tt.wantArgs)
			continue
		}
		for i, want := range tt.wantArgs {
			if got[i] != want {
				t.Errorf("scope %q arg[%d] = %q, want %q", tt.scope, i, got[i], want)
			}
		}
	}
}

// TestParseGitDiffFiles_CommitRange verifies ParseGitDiffFiles with DiffCommits scope
// using a real two-commit test repository.
func TestParseGitDiffFiles_CommitRange(t *testing.T) {
	// First commit: initial file.
	repoPath := initTestRepo(t, map[string]string{
		"main.go": "package main\n\nfunc Hello() string { return \"v1\" }\n",
	})

	// Second commit: modify main.go, add new.go.
	commitFiles(t, repoPath, map[string]string{
		"main.go": "package main\n\nfunc Hello() string { return \"v2\" }\n",
		"new.go":  "package main\n\nfunc New() {}\n",
	})

	// Diff between HEAD~1 (first commit) and HEAD (second commit).
	files, err := ParseGitDiffFiles(repoPath, DiffCommits, "HEAD~1", "HEAD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) < 1 {
		t.Fatalf("expected at least 1 changed file, got %d", len(files))
	}

	// Build a map for easy lookup.
	byPath := make(map[string]ChangedFile)
	for _, f := range files {
		byPath[f.Path] = f
	}

	if f, ok := byPath["main.go"]; !ok {
		t.Error("expected main.go to be listed as changed")
	} else if f.Status != "M" {
		t.Errorf("main.go status = %q, want M", f.Status)
	}

	if f, ok := byPath["new.go"]; !ok {
		t.Error("expected new.go to be listed as added")
	} else if f.Status != "A" {
		t.Errorf("new.go status = %q, want A", f.Status)
	}
}

// TestParseGitDiffFiles_CommitRange_DefaultRefs verifies that empty refs default correctly.
func TestParseGitDiffFiles_CommitRange_DefaultRefs(t *testing.T) {
	repoPath := initTestRepo(t, map[string]string{
		"a.go": "package main\n",
	})

	commitFiles(t, repoPath, map[string]string{
		"a.go": "package main\n\n// updated\n",
	})

	// Empty baseBranch → HEAD~1, empty toRef → HEAD.
	files, err := ParseGitDiffFiles(repoPath, DiffCommits, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(files) != 1 {
		t.Fatalf("expected 1 changed file, got %d", len(files))
	}
	if files[0].Path != "a.go" {
		t.Errorf("expected a.go, got %q", files[0].Path)
	}
}

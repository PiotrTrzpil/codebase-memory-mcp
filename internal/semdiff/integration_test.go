package semdiff_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/cbm"
	"github.com/DeusData/codebase-memory-mcp/internal/lang"
	"github.com/DeusData/codebase-memory-mcp/internal/pipeline"
	"github.com/DeusData/codebase-memory-mcp/internal/semdiff"
)

// ---------------------------------------------------------------------------
// Helpers: temp git repo management
// ---------------------------------------------------------------------------

func initRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init")
	git(t, dir, "config", "user.email", "test@test.com")
	git(t, dir, "config", "user.name", "Test")
	writeFiles(t, dir, files)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-m", "initial")
	return dir
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// extractDefs parses source and returns definitions, using the provided language.
func extractDefs(t *testing.T, source, relPath string, language lang.Language) []cbm.Definition {
	t.Helper()
	cbm.Init()
	fr, err := cbm.ExtractFile([]byte(source), language, "test", relPath)
	if err != nil {
		t.Fatalf("ExtractFile(%s): %v", relPath, err)
	}
	return fr.Definitions
}

// runSemanticDiff is the core integration helper. It:
// 1. Gets changed files from git diff
// 2. Fetches old content via git show
// 3. Extracts old + new definitions with tree-sitter
// 4. Runs semdiff.Diff per file
// 5. Runs semdiff.ClassifyBreaking across all changes
// Returns the full result.
func runSemanticDiff(t *testing.T, repoPath string, scope pipeline.DiffScope, baseBranch string) (
	allChanges []semdiff.SymbolChange,
	breakingChanges []semdiff.SymbolChange,
	fileSummaries []semdiff.FileChangeSummary,
) {
	t.Helper()

	changedFiles, err := pipeline.ParseGitDiffFiles(repoPath, scope, baseBranch, "")
	if err != nil {
		t.Fatalf("ParseGitDiffFiles: %v", err)
	}
	if len(changedFiles) == 0 {
		return nil, nil, nil
	}

	oldContents, err := pipeline.OldFileContents(repoPath, scope, baseBranch, changedFiles)
	if err != nil {
		t.Fatalf("OldFileContents: %v", err)
	}

	oldDefMap := make(map[string]cbm.Definition)
	newDefMap := make(map[string]cbm.Definition)

	for _, f := range changedFiles {
		var oldDefs []cbm.Definition
		oldKey := f.Path
		if f.OldPath != "" {
			oldKey = f.OldPath
		}
		if oldContent, ok := oldContents[oldKey]; ok && len(oldContent) > 0 {
			language, ok := languageForFile(oldKey)
			if ok {
				oldDefs = extractDefsFromContent(t, oldContent, oldKey, language)
				for _, d := range oldDefs {
					oldDefMap[d.QualifiedName] = d
				}
			}
		}

		var newDefs []cbm.Definition
		if f.Status != "D" {
			newContent, err := os.ReadFile(filepath.Join(repoPath, f.Path))
			if err != nil {
				t.Fatalf("read new %s: %v", f.Path, err)
			}
			language, ok := languageForFile(f.Path)
			if ok {
				newDefs = extractDefsFromContent(t, newContent, f.Path, language)
				for _, d := range newDefs {
					newDefMap[d.QualifiedName] = d
				}
			}
		}

		changes := semdiff.Diff(oldDefs, newDefs, f.Path, f.Status)
		fs := semdiff.FileChangeSummary{
			Path:    f.Path,
			Status:  f.Status,
			OldPath: f.OldPath,
			Changes: changes,
		}
		fileSummaries = append(fileSummaries, fs)
		allChanges = append(allChanges, changes...)
	}

	breakingChanges = semdiff.ClassifyBreaking(allChanges, oldDefMap, newDefMap)
	return allChanges, breakingChanges, fileSummaries
}

func extractDefsFromContent(t *testing.T, content []byte, relPath string, language lang.Language) []cbm.Definition {
	t.Helper()
	cbm.Init()
	fr, err := cbm.ExtractFile(content, language, "test", relPath)
	if err != nil {
		t.Logf("warning: ExtractFile(%s) failed: %v", relPath, err)
		return nil
	}
	if fr == nil {
		return nil
	}
	return fr.Definitions
}

func languageForFile(path string) (lang.Language, bool) {
	base := filepath.Base(path)
	if l, ok := lang.LanguageForFilename(base); ok {
		return l, true
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return "", false
	}
	return lang.LanguageForExtension(ext)
}

func findChangeByName(changes []semdiff.SymbolChange, name string) (semdiff.SymbolChange, bool) {
	for _, c := range changes {
		if c.Name == name {
			return c, true
		}
	}
	return semdiff.SymbolChange{}, false
}

func findDelta(deltas []semdiff.FieldDelta, field string) (semdiff.FieldDelta, bool) {
	for _, d := range deltas {
		if d.Field == field {
			return d, true
		}
	}
	return semdiff.FieldDelta{}, false
}

// ---------------------------------------------------------------------------
// Integration tests: Go
// ---------------------------------------------------------------------------

func TestIntegration_Go_SignatureChange(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"main.go": `package main

func ProcessOrder(id string) error {
	return nil
}

func helper() {
	ProcessOrder("123")
}
`,
	})

	// Modify: add a parameter to ProcessOrder
	writeFiles(t, repo, map[string]string{
		"main.go": `package main

func ProcessOrder(id string, priority int) error {
	return nil
}

func helper() {
	ProcessOrder("123", 1)
}
`,
	})

	changes, breaking, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	// Should detect ProcessOrder signature change
	c, ok := findChangeByName(changes, "ProcessOrder")
	if !ok {
		t.Fatal("ProcessOrder not found in changes")
	}
	if c.Kind != semdiff.SignatureChanged {
		t.Errorf("expected SignatureChanged, got %s", c.Kind)
	}
	// Check that at least one signature-related delta exists (param_types or signature)
	_, hasParams := findDelta(c.Deltas, "param_types")
	_, hasSig := findDelta(c.Deltas, "signature")
	if !hasParams && !hasSig {
		t.Errorf("expected param_types or signature delta, got deltas: %+v", c.Deltas)
	}
	if c.FilePath != "main.go" {
		t.Errorf("expected FilePath=main.go, got %s", c.FilePath)
	}

	// ProcessOrder is exported → should be breaking
	if len(breaking) == 0 {
		t.Fatal("expected breaking changes")
	}
	found := false
	for _, b := range breaking {
		if b.Name == "ProcessOrder" {
			found = true
		}
	}
	if !found {
		t.Error("ProcessOrder should be in breaking changes")
	}
}

func TestIntegration_Go_AddedAndRemovedFunctions(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"svc.go": `package main

func OldFunc(a int, b int, c int) (string, error) {
	return "old", nil
}

func StableFunc() int {
	return 42
}
`,
	})

	// Remove OldFunc, add NewFunc (very different signature), keep StableFunc
	writeFiles(t, repo, map[string]string{
		"svc.go": `package main

func StableFunc() int {
	return 42
}

func NewFunc() bool {
	return true
}
`,
	})

	changes, _, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	// OldFunc should be removed (or renamed to NewFunc if fuzzy matched)
	removed, removedOK := findChangeByName(changes, "OldFunc")
	added, addedOK := findChangeByName(changes, "NewFunc")

	if removedOK {
		// Separate remove/add
		if removed.Kind != semdiff.Removed {
			t.Errorf("OldFunc: expected Removed, got %s", removed.Kind)
		}
		if !addedOK {
			t.Fatal("NewFunc not found in changes")
		}
		if added.Kind != semdiff.Added {
			t.Errorf("NewFunc: expected Added, got %s", added.Kind)
		}
	} else if addedOK && added.Kind == semdiff.Renamed {
		// Fuzzy matched as rename — also acceptable
		if added.OldName != "OldFunc" {
			t.Errorf("expected OldName=OldFunc for rename, got %s", added.OldName)
		}
	} else {
		t.Fatal("expected either OldFunc removed + NewFunc added, or NewFunc renamed from OldFunc")
	}

	// StableFunc unchanged — should not appear
	if _, ok := findChangeByName(changes, "StableFunc"); ok {
		t.Error("StableFunc should not appear in changes (unchanged)")
	}
}

func TestIntegration_Go_NewFile(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"existing.go": `package main

func Existing() {}
`,
	})

	// Add a brand new file
	writeFiles(t, repo, map[string]string{
		"new_service.go": `package main

func CreateUser(name string) error {
	return nil
}

func DeleteUser(id int) error {
	return nil
}
`,
	})
	git(t, repo, "add", "new_service.go")

	changes, _, summaries := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	// Should detect 2 added functions in the new file
	addedCount := 0
	for _, c := range changes {
		if c.Kind == semdiff.Added && c.FilePath == "new_service.go" {
			addedCount++
		}
	}
	if addedCount != 2 {
		t.Errorf("expected 2 added functions in new file, got %d", addedCount)
	}

	// File summary should show the new file
	found := false
	for _, fs := range summaries {
		if fs.Path == "new_service.go" {
			found = true
			if fs.Status != "A" {
				t.Errorf("expected status A, got %s", fs.Status)
			}
		}
	}
	if !found {
		t.Error("new_service.go not found in file summaries")
	}
}

func TestIntegration_Go_DeletedFile(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"main.go":    `package main; func Main() {}`,
		"todelete.go": `package main

func Alpha() string { return "a" }
func Beta() int { return 1 }
`,
	})

	os.Remove(filepath.Join(repo, "todelete.go"))
	git(t, repo, "add", "todelete.go")

	changes, _, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	removedCount := 0
	for _, c := range changes {
		if c.Kind == semdiff.Removed && c.FilePath == "todelete.go" {
			removedCount++
		}
	}
	if removedCount != 2 {
		t.Errorf("expected 2 removed functions from deleted file, got %d", removedCount)
	}
}

func TestIntegration_Go_BodyOnlyChange(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"calc.go": `package main

func Calculate(x int) int {
	return x * 2
}
`,
	})

	// Change body but not signature
	writeFiles(t, repo, map[string]string{
		"calc.go": `package main

func Calculate(x int) int {
	result := x * 2
	if result > 100 {
		result = 100
	}
	return result
}
`,
	})

	changes, breaking, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	c, ok := findChangeByName(changes, "Calculate")
	if !ok {
		t.Fatal("Calculate not found in changes")
	}
	if c.Kind != semdiff.BodyChanged {
		t.Errorf("expected BodyChanged, got %s", c.Kind)
	}

	// Body-only change on exported function is NOT breaking
	for _, b := range breaking {
		if b.Name == "Calculate" {
			t.Error("body-only change should not be breaking")
		}
	}
}

func TestIntegration_Go_VisibilityChange(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"api.go": `package main

func PublicAPI() string {
	return "public"
}
`,
	})

	// Unexport by renaming to lowercase
	writeFiles(t, repo, map[string]string{
		"api.go": `package main

func publicAPI() string {
	return "public"
}
`,
	})

	changes, _, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	// PublicAPI removed, publicAPI added (different qualified names)
	if len(changes) < 1 {
		t.Fatal("expected changes for visibility modification")
	}

	// At minimum we should see removal of PublicAPI and addition of publicAPI
	hasRemoved := false
	hasAdded := false
	for _, c := range changes {
		if c.Name == "PublicAPI" && c.Kind == semdiff.Removed {
			hasRemoved = true
		}
		if c.Name == "publicAPI" && c.Kind == semdiff.Added {
			hasAdded = true
		}
		// Or it could be detected as a rename
		if c.Kind == semdiff.Renamed {
			hasRemoved = true
			hasAdded = true
		}
	}
	if !hasRemoved || !hasAdded {
		t.Errorf("expected to see visibility change reflected in changes: %+v", changes)
	}
}

func TestIntegration_Go_ReturnTypeChange(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"handler.go": `package main

func Handle(req string) string {
	return req
}
`,
	})

	writeFiles(t, repo, map[string]string{
		"handler.go": `package main

func Handle(req string) (string, error) {
	return req, nil
}
`,
	})

	changes, breaking, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	c, ok := findChangeByName(changes, "Handle")
	if !ok {
		t.Fatal("Handle not found in changes")
	}
	if c.Kind != semdiff.SignatureChanged {
		t.Errorf("expected SignatureChanged, got %s", c.Kind)
	}

	// Check return_type delta
	d, ok := findDelta(c.Deltas, "return_type")
	if !ok {
		t.Fatal("expected return_type delta")
	}
	if d.Old == d.New {
		t.Error("return_type old and new should differ")
	}

	// Exported, signature changed → breaking
	if len(breaking) == 0 {
		t.Error("expected Handle to be a breaking change")
	}
}

// ---------------------------------------------------------------------------
// Integration tests: TypeScript
// ---------------------------------------------------------------------------

func TestIntegration_TypeScript_FunctionChange(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"service.ts": `
export function processOrder(id: string): void {
  console.log(id);
}

function internalHelper(): number {
  return 42;
}
`,
	})

	writeFiles(t, repo, map[string]string{
		"service.ts": `
export function processOrder(id: string, priority: number): Promise<void> {
  console.log(id, priority);
}

function internalHelper(): number {
  return 42;
}
`,
	})

	changes, breaking, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	// processOrder should have signature changes
	c, ok := findChangeByName(changes, "processOrder")
	if !ok {
		t.Fatal("processOrder not found in changes")
	}
	if c.Kind != semdiff.SignatureChanged {
		t.Errorf("expected SignatureChanged, got %s", c.Kind)
	}
	if c.Summary == "" {
		t.Error("expected non-empty summary")
	}

	// internalHelper unchanged — should not appear
	if _, ok := findChangeByName(changes, "internalHelper"); ok {
		t.Error("internalHelper should not appear (unchanged)")
	}

	// processOrder is exported → breaking
	found := false
	for _, b := range breaking {
		if b.Name == "processOrder" {
			found = true
		}
	}
	if !found {
		t.Error("processOrder should be breaking (exported + signature changed)")
	}
}

func TestIntegration_TypeScript_ClassChanges(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"model.ts": `
export class User {
  name: string;

  constructor(name: string) {
    this.name = name;
  }

  greet(): string {
    return "Hello " + this.name;
  }
}
`,
	})

	writeFiles(t, repo, map[string]string{
		"model.ts": `
export class User {
  name: string;
  email: string;

  constructor(name: string, email: string) {
    this.name = name;
    this.email = email;
  }

  greet(): string {
    return "Hello " + this.name;
  }

  toJSON(): object {
    return { name: this.name, email: this.email };
  }
}
`,
	})

	changes, _, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	// Should detect at least the constructor signature change and toJSON addition
	if len(changes) == 0 {
		t.Fatal("expected changes for class modifications")
	}

	// Look for added method
	hasNewMethod := false
	for _, c := range changes {
		if c.Name == "toJSON" && c.Kind == semdiff.Added {
			hasNewMethod = true
		}
	}
	if !hasNewMethod {
		t.Error("expected toJSON to be detected as added")
	}
}

// ---------------------------------------------------------------------------
// Integration tests: Python
// ---------------------------------------------------------------------------

func TestIntegration_Python_FunctionChange(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"app.py": `
def process_data(data):
    return data

def helper():
    return 42
`,
	})

	writeFiles(t, repo, map[string]string{
		"app.py": `
def process_data(data, validate=True):
    if validate:
        check(data)
    return data

def helper():
    return 42

def new_function(x, y):
    return x + y
`,
	})

	changes, _, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	// process_data: signature or body changed
	c, ok := findChangeByName(changes, "process_data")
	if !ok {
		t.Fatal("process_data not found in changes")
	}
	if c.Kind != semdiff.SignatureChanged && c.Kind != semdiff.BodyChanged {
		t.Errorf("expected SignatureChanged or BodyChanged for process_data, got %s", c.Kind)
	}

	// new_function: added
	nf, ok := findChangeByName(changes, "new_function")
	if !ok {
		t.Fatal("new_function not found in changes")
	}
	if nf.Kind != semdiff.Added {
		t.Errorf("expected Added, got %s", nf.Kind)
	}

	// helper: unchanged
	if _, ok := findChangeByName(changes, "helper"); ok {
		t.Error("helper should not appear (unchanged)")
	}
}

// ---------------------------------------------------------------------------
// Integration tests: Branch scope
// ---------------------------------------------------------------------------

func TestIntegration_BranchDiff(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"lib.go": `package lib

func BaseFunc() int {
	return 1
}
`,
	})

	// Create feature branch
	git(t, repo, "checkout", "-b", "feature")

	// Make changes on feature branch
	writeFiles(t, repo, map[string]string{
		"lib.go": `package lib

func BaseFunc() int {
	return 1
}

func FeatureFunc(x string) error {
	return nil
}
`,
	})
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "add feature")

	changes, _, _ := runSemanticDiff(t, repo, pipeline.DiffBranch, "main")

	// Should see FeatureFunc added
	c, ok := findChangeByName(changes, "FeatureFunc")
	if !ok {
		t.Fatal("FeatureFunc not found in branch diff")
	}
	if c.Kind != semdiff.Added {
		t.Errorf("expected Added, got %s", c.Kind)
	}

	// BaseFunc should NOT appear (unchanged between branches)
	if _, ok := findChangeByName(changes, "BaseFunc"); ok {
		t.Error("BaseFunc should not appear in branch diff (unchanged)")
	}
}

// ---------------------------------------------------------------------------
// Integration tests: Commit range scope
// ---------------------------------------------------------------------------

func TestIntegration_CommitRange(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"svc.go": `package main

func Version1() string {
	return "v1"
}
`,
	})

	// Tag v1
	git(t, repo, "tag", "v1")

	// Commit 2: modify + add
	writeFiles(t, repo, map[string]string{
		"svc.go": `package main

func Version1() (string, error) {
	return "v1", nil
}

func Version2() string {
	return "v2"
}
`,
	})
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-m", "v2 changes")
	git(t, repo, "tag", "v2")

	// Diff between v1 and v2
	changedFiles, err := pipeline.ParseGitDiffFiles(repo, pipeline.DiffCommits, "v1", "v2")
	if err != nil {
		t.Fatalf("ParseGitDiffFiles: %v", err)
	}
	if len(changedFiles) == 0 {
		t.Fatal("expected changed files between v1 and v2")
	}

	oldContents, err := pipeline.OldFileContents(repo, pipeline.DiffCommits, "v1", changedFiles)
	if err != nil {
		t.Fatalf("OldFileContents: %v", err)
	}

	oldDefMap := make(map[string]cbm.Definition)
	newDefMap := make(map[string]cbm.Definition)
	var allChanges []semdiff.SymbolChange

	for _, f := range changedFiles {
		var oldDefs, newDefs []cbm.Definition

		if oldContent, ok := oldContents[f.Path]; ok {
			l, ok := languageForFile(f.Path)
			if ok {
				oldDefs = extractDefsFromContent(t, oldContent, f.Path, l)
				for _, d := range oldDefs {
					oldDefMap[d.QualifiedName] = d
				}
			}
		}

		newContent, _ := os.ReadFile(filepath.Join(repo, f.Path))
		l, ok := languageForFile(f.Path)
		if ok {
			newDefs = extractDefsFromContent(t, newContent, f.Path, l)
			for _, d := range newDefs {
				newDefMap[d.QualifiedName] = d
			}
		}

		changes := semdiff.Diff(oldDefs, newDefs, f.Path, f.Status)
		allChanges = append(allChanges, changes...)
	}

	breaking := semdiff.ClassifyBreaking(allChanges, oldDefMap, newDefMap)

	// Version1: signature changed (return type)
	c, ok := findChangeByName(allChanges, "Version1")
	if !ok {
		t.Fatal("Version1 not found in commit range diff")
	}
	if c.Kind != semdiff.SignatureChanged {
		t.Errorf("expected SignatureChanged, got %s", c.Kind)
	}

	// Version2: added
	c2, ok := findChangeByName(allChanges, "Version2")
	if !ok {
		t.Fatal("Version2 not found in commit range diff")
	}
	if c2.Kind != semdiff.Added {
		t.Errorf("expected Added, got %s", c2.Kind)
	}

	// Version1 is exported + signature changed → breaking
	found := false
	for _, b := range breaking {
		if b.Name == "Version1" {
			found = true
		}
	}
	if !found {
		t.Error("Version1 should be a breaking change")
	}
}

// ---------------------------------------------------------------------------
// Integration tests: Multiple files in one diff
// ---------------------------------------------------------------------------

func TestIntegration_MultipleFiles(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"auth.go": `package main

func Login(user string) error {
	return nil
}
`,
		"db.go": `package main

func Query(sql string) error {
	return nil
}
`,
		"utils.go": `package main

func FormatDate() string {
	return "2024-01-01"
}
`,
	})

	// Modify auth.go, delete db.go, add new.go, keep utils.go
	writeFiles(t, repo, map[string]string{
		"auth.go": `package main

func Login(user, password string) (string, error) {
	return "token", nil
}
`,
		"new.go": `package main

func NewFeature() bool {
	return true
}
`,
	})
	os.Remove(filepath.Join(repo, "db.go"))
	git(t, repo, "add", "-A")

	changes, breaking, summaries := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	// auth.go: Login signature changed
	login, ok := findChangeByName(changes, "Login")
	if !ok {
		t.Fatal("Login not found")
	}
	if login.Kind != semdiff.SignatureChanged {
		t.Errorf("Login: expected SignatureChanged, got %s", login.Kind)
	}

	// db.go: Query removed
	query, ok := findChangeByName(changes, "Query")
	if !ok {
		t.Fatal("Query not found")
	}
	if query.Kind != semdiff.Removed {
		t.Errorf("Query: expected Removed, got %s", query.Kind)
	}

	// new.go: NewFeature added
	nf, ok := findChangeByName(changes, "NewFeature")
	if !ok {
		t.Fatal("NewFeature not found")
	}
	if nf.Kind != semdiff.Added {
		t.Errorf("NewFeature: expected Added, got %s", nf.Kind)
	}

	// utils.go: FormatDate unchanged — should not appear
	if _, ok := findChangeByName(changes, "FormatDate"); ok {
		t.Error("FormatDate should not appear (unchanged)")
	}

	// Breaking: Login (exported, signature changed) and Query (exported, removed)
	if len(breaking) < 2 {
		t.Errorf("expected at least 2 breaking changes, got %d", len(breaking))
	}

	// File summaries should cover the changed files
	if len(summaries) < 3 {
		t.Errorf("expected at least 3 file summaries, got %d", len(summaries))
	}
}

// ---------------------------------------------------------------------------
// Integration tests: No changes
// ---------------------------------------------------------------------------

func TestIntegration_NoChanges(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"main.go": `package main

func Main() {}
`,
	})

	// No modifications — clean working tree
	changes, breaking, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	if len(changes) != 0 {
		t.Errorf("expected 0 changes for clean repo, got %d", len(changes))
	}
	if len(breaking) != 0 {
		t.Errorf("expected 0 breaking for clean repo, got %d", len(breaking))
	}
}

// ---------------------------------------------------------------------------
// Integration tests: Summaries are human-readable
// ---------------------------------------------------------------------------

func TestIntegration_SummaryStrings(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"api.go": `package main

func Fetch(url string) string {
	return ""
}
`,
	})

	writeFiles(t, repo, map[string]string{
		"api.go": `package main

func Fetch(url string, timeout int) (string, error) {
	return "", nil
}
`,
	})

	changes, _, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	c, ok := findChangeByName(changes, "Fetch")
	if !ok {
		t.Fatal("Fetch not found")
	}

	// Summary should be non-empty and mention the function name
	if c.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if !strings.Contains(c.Summary, "Fetch") {
		t.Errorf("summary should mention function name: %s", c.Summary)
	}

	// Summary should mention what changed (param or return type)
	if !strings.Contains(c.Summary, "param") && !strings.Contains(c.Summary, "return") && !strings.Contains(c.Summary, "signature") {
		t.Errorf("summary should describe the change: %s", c.Summary)
	}
}

// ---------------------------------------------------------------------------
// Integration tests: Non-code files ignored
// ---------------------------------------------------------------------------

func TestIntegration_NonCodeFilesIgnored(t *testing.T) {
	repo := initRepo(t, map[string]string{
		"main.go":   `package main; func Main() {}`,
		"README.md": `# Hello`,
		"data.json": `{"key": "value"}`,
	})

	writeFiles(t, repo, map[string]string{
		"README.md": `# Updated readme`,
		"data.json": `{"key": "new_value"}`,
	})

	changes, _, _ := runSemanticDiff(t, repo, pipeline.DiffAll, "")

	// JSON files produce no definitions. Markdown may produce Section definitions
	// via tree-sitter — that's expected behavior (tree-sitter supports markdown).
	// The key check is that these don't produce spurious Function/Class/Method changes.
	for _, c := range changes {
		if c.FilePath == "data.json" {
			t.Errorf("JSON file should not produce symbol changes: %+v", c)
		}
	}
}

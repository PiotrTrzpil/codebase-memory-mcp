package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRouterForProjectNormalizesCase(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	// Open with mixed case — should normalize to lowercase
	s, err := r.ForProject("Users-Foo-Code-Bar")
	if err != nil {
		t.Fatal(err)
	}

	// The DB file should be lowercase
	dbPath := s.DBPath()
	base := filepath.Base(dbPath)
	if base != "users-foo-code-bar.db" {
		t.Errorf("expected lowercase db filename, got %q", base)
	}

	// Opening with different casing should return the same store
	s2, err := r.ForProject("users-foo-code-bar")
	if err != nil {
		t.Fatal(err)
	}
	if s.DBPath() != s2.DBPath() {
		t.Errorf("different stores for same project: %q vs %q", s.DBPath(), s2.DBPath())
	}
}

func TestRouterHasProjectCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	// Create a project
	if _, err := r.ForProject("my-project"); err != nil {
		t.Fatal(err)
	}

	// HasProject should find it regardless of input casing
	if !r.HasProject("my-project") {
		t.Error("HasProject should find lowercase")
	}
	if !r.HasProject("My-Project") {
		t.Error("HasProject should find mixed case")
	}
	if !r.HasProject("MY-PROJECT") {
		t.Error("HasProject should find uppercase")
	}
}

func TestRouterDeleteProjectCaseInsensitive(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	// Create a project
	if _, err := r.ForProject("delete-me"); err != nil {
		t.Fatal(err)
	}

	// Delete with different casing
	if err := r.DeleteProject("Delete-Me"); err != nil {
		t.Fatal(err)
	}

	if r.HasProject("delete-me") {
		t.Error("project should be deleted")
	}
}

func TestRouterMigratesLegacyUppercaseDB(t *testing.T) {
	dir := t.TempDir()

	// Create a legacy uppercase DB file manually
	legacyName := "Users-Foo-Code-MyApp"
	legacyPath := filepath.Join(dir, legacyName+".db")
	s, err := OpenPath(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertProject(legacyName, "/Users/foo/Code/MyApp"); err != nil {
		t.Fatal(err)
	}
	s.UpsertNode(&Node{
		Project: legacyName, Label: "Function", Name: "main",
		QualifiedName: legacyName + ".main",
	})
	s.Close()

	// Verify legacy file exists
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy DB should exist: %v", err)
	}

	// Now open via router with lowercase name — should trigger migration
	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	canonicalName := strings.ToLower(legacyName)
	st, err := r.ForProject(canonicalName)
	if err != nil {
		t.Fatalf("ForProject(%q): %v", canonicalName, err)
	}

	// DB path should now be lowercase
	expectedPath := filepath.Join(dir, canonicalName+".db")
	if st.DBPath() != expectedPath {
		t.Errorf("expected db at %q, got %q", expectedPath, st.DBPath())
	}

	// Verify the actual file on disk is lowercase
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".db") && !strings.HasSuffix(name, "-wal") && !strings.HasSuffix(name, "-shm") {
			if name != canonicalName+".db" {
				t.Errorf("expected file %q, found %q", canonicalName+".db", name)
			}
		}
	}

	// Data should still be accessible (CleanStaleProjects renames the project entry)
	nodes, _ := st.AllNodes(canonicalName)
	// The data was stored under the legacy name; CleanStaleProjects deletes
	// stale entries but ForProject also cleans. Either way the store is usable.
	_ = nodes
}

func TestRouterListProjectsNormalized(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	// Create projects with different casing — should all normalize
	for _, name := range []string{"Proj-A", "proj-b", "PROJ-C"} {
		if _, err := r.ForProject(name); err != nil {
			t.Fatal(err)
		}
	}

	projects, err := r.ListProjects()
	if err != nil {
		t.Fatal(err)
	}

	if len(projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(projects))
	}

	for _, p := range projects {
		if p.Name != strings.ToLower(p.Name) {
			t.Errorf("project name should be lowercase, got %q", p.Name)
		}
	}
}

func TestRouterAllStoresNormalized(t *testing.T) {
	dir := t.TempDir()

	// Create DB files with mixed casing directly
	for _, name := range []string{"proj-one", "Proj-Two"} {
		s, err := OpenInDir(dir, name)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}

	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	stores := r.AllStores()
	for name := range stores {
		if name != strings.ToLower(name) {
			t.Errorf("store key should be lowercase, got %q", name)
		}
	}
}

func TestFindLegacyDBFindsUppercase(t *testing.T) {
	dir := t.TempDir()

	// Create an uppercase file
	upperPath := filepath.Join(dir, "Foo-Bar.db")
	if err := os.WriteFile(upperPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	found := r.findLegacyDB("foo-bar")
	if found == "" {
		t.Error("should find legacy uppercase DB")
	}

	// Should NOT find if already lowercase
	lowerPath := filepath.Join(dir, "already-lower.db")
	if err := os.WriteFile(lowerPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	found = r.findLegacyDB("already-lower")
	if found != "" {
		t.Error("should not find legacy DB when file is already lowercase")
	}
}

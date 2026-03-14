package store

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ProjectInfo holds metadata about a discovered project database.
type ProjectInfo struct {
	Name     string
	DBPath   string
	RootPath string
}

// StoreRouter manages per-project SQLite databases.
// Each project gets its own .db file in the cache directory.
type StoreRouter struct {
	dir    string            // ~/.cache/codebase-memory-mcp/
	stores map[string]*Store // project name → open Store (lazy)
	mu     sync.Mutex
}

// NewRouter creates a StoreRouter, ensuring the cache directory exists.
// Runs migration from single-DB layout if needed.
func NewRouter() (*StoreRouter, error) {
	dir, err := cacheDir()
	if err != nil {
		return nil, err
	}

	r := &StoreRouter{
		dir:    dir,
		stores: make(map[string]*Store),
	}

	// Run one-time migration from single DB to per-project DBs
	if err := r.migrate(); err != nil {
		slog.Warn("router.migrate.err", "err", err)
	}

	return r, nil
}

// NewRouterWithDir creates a StoreRouter using a custom directory (for testing).
// No migration is run.
func NewRouterWithDir(dir string) (*StoreRouter, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	return &StoreRouter{
		dir:    dir,
		stores: make(map[string]*Store),
	}, nil
}

// ForProject returns the Store for the given project, opening it lazily.
func (r *StoreRouter) ForProject(name string) (*Store, error) {
	if name == "*" || name == "all" {
		return nil, fmt.Errorf("invalid project name: %q", name)
	}
	name = strings.ToLower(name)
	r.mu.Lock()
	defer r.mu.Unlock()

	if s, ok := r.stores[name]; ok {
		return s, nil
	}

	// Migrate legacy uppercase DB file to lowercase if needed.
	r.migrateDBCase(name)

	s, err := OpenInDir(r.dir, name)
	if err != nil {
		return nil, fmt.Errorf("open store %q: %w", name, err)
	}

	// Clean up stale project entries with different casing. On case-insensitive
	// filesystems (macOS/Windows), different casings of the same path open the
	// same .db file, creating duplicate project entries within it.
	if cleaned, cleanErr := s.CleanStaleProjects(name); cleanErr == nil && cleaned > 0 {
		slog.Info("router.clean_stale", "project", name, "removed", cleaned)
	}

	r.stores[name] = s
	return s, nil
}

// AllStores opens all .db files in the cache dir and returns a name→Store map.
func (r *StoreRouter) AllStores() map[string]*Store {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		slog.Warn("router.all_stores.readdir", "err", err)
		r.mu.Lock()
		defer r.mu.Unlock()
		result := make(map[string]*Store, len(r.stores))
		for k, v := range r.stores {
			result[k] = v
		}
		return result
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".db")
		if name == "codebase-memory" {
			continue // skip legacy single DB
		}
		// Normalize: open via ForProject which handles case migration
		canonical := strings.ToLower(name)
		r.mu.Lock()
		_, alreadyOpen := r.stores[canonical]
		r.mu.Unlock()
		if alreadyOpen {
			continue
		}
		// ForProject handles locking internally
		if _, err := r.ForProject(canonical); err != nil {
			slog.Warn("router.all_stores.open", "project", canonical, "err", err)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]*Store, len(r.stores))
	for k, v := range r.stores {
		result[k] = v
	}
	return result
}

// ListProjects scans .db files and queries each for metadata.
func (r *StoreRouter) ListProjects() ([]*ProjectInfo, error) {
	// Use AllStores to ensure case migration happens
	stores := r.AllStores()

	result := make([]*ProjectInfo, 0, len(stores))
	for name, s := range stores {
		info := &ProjectInfo{
			Name:   name,
			DBPath: filepath.Join(r.dir, name+".db"),
		}

		projects, listErr := s.ListProjects()
		if listErr == nil && len(projects) > 0 {
			info.RootPath = projects[0].RootPath
		}

		result = append(result, info)
	}
	return result, nil
}

// DeleteProject closes the Store connection and removes the .db + WAL/SHM files.
func (r *StoreRouter) DeleteProject(name string) error {
	name = strings.ToLower(name)
	r.mu.Lock()
	defer r.mu.Unlock()

	if s, ok := r.stores[name]; ok {
		s.Close()
		delete(r.stores, name)
	}

	dbPath := filepath.Join(r.dir, name+".db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		p := dbPath + suffix
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	slog.Info("router.delete", "project", name)
	return nil
}

// HasProject checks if a .db file exists for the given project (without opening it).
func (r *StoreRouter) HasProject(name string) bool {
	name = strings.ToLower(name)
	// On case-insensitive FS, os.Stat matches regardless of casing,
	// so this also catches legacy uppercase files.
	dbPath := filepath.Join(r.dir, name+".db")
	_, err := os.Stat(dbPath)
	if err == nil {
		return true
	}
	// Fallback: scan directory entries for case-insensitive match
	return r.findLegacyDB(name) != ""
}

// migrateDBCase renames a legacy mixed-case .db file (and WAL/SHM) to lowercase.
// On case-insensitive filesystems, os.Rename is a no-op for casing changes,
// so we rename through a temp name.
func (r *StoreRouter) migrateDBCase(lowerName string) {
	legacy := r.findLegacyDB(lowerName)
	if legacy == "" {
		return // no legacy file or already lowercase
	}

	target := filepath.Join(r.dir, lowerName+".db")

	// On case-insensitive FS, direct rename won't change casing.
	// Rename through a temp intermediate.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := legacy + suffix
		dst := target + suffix
		if _, err := os.Stat(src); err != nil {
			continue
		}
		tmp := dst + ".migrating"
		if err := os.Rename(src, tmp); err != nil {
			slog.Warn("router.migrate_case.rename_tmp", "src", src, "err", err)
			continue
		}
		if err := os.Rename(tmp, dst); err != nil {
			slog.Warn("router.migrate_case.rename_final", "tmp", tmp, "err", err)
			// Try to restore
			os.Rename(tmp, src)
			continue
		}
	}
	slog.Info("router.migrate_case", "from", filepath.Base(legacy), "to", lowerName+".db")
}

// findLegacyDB scans the cache dir for a .db file whose name matches lowerName
// case-insensitively but has different casing (i.e., contains uppercase letters).
// On case-insensitive filesystems os.Stat can't distinguish casing, so we must
// read the actual directory entries.
func (r *StoreRouter) findLegacyDB(lowerName string) string {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fname := e.Name()
		if !strings.HasSuffix(fname, ".db") || strings.HasSuffix(fname, "-wal") || strings.HasSuffix(fname, "-shm") {
			continue
		}
		name := strings.TrimSuffix(fname, ".db")
		if strings.ToLower(name) == lowerName && name != lowerName {
			return filepath.Join(r.dir, name+".db")
		}
	}
	return ""
}

// Dir returns the cache directory path.
func (r *StoreRouter) Dir() string {
	return r.dir
}

// CloseAll closes all open Store connections.
func (r *StoreRouter) CloseAll() {
	r.mu.Lock()
	defer r.mu.Unlock()

	for name, s := range r.stores {
		if err := s.Close(); err != nil {
			slog.Warn("router.close", "project", name, "err", err)
		}
	}
	r.stores = make(map[string]*Store)
}

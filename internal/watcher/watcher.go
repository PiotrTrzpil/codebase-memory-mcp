package watcher

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

const (
	graceInterval = 5 * time.Second // delay before first poll to avoid startup contention
	baseInterval  = 1 * time.Second
	maxInterval   = 60 * time.Second
)

type fileSnapshot struct {
	modTime time.Time
	size    int64
}

type projectState struct {
	snapshot map[string]fileSnapshot
	interval time.Duration
	nextPoll time.Time
}

// IndexFunc is the callback signature for triggering a re-index.
type IndexFunc func(ctx context.Context, projectName, rootPath string) error

// Watcher polls the session project for file changes and triggers re-indexing.
// Each MCP server instance watches only its own project — not all projects.
type Watcher struct {
	router   *store.StoreRouter
	indexFn  IndexFunc
	projects map[string]*projectState
	ctx      context.Context

	// Session-scoped: only watch this project (set via SetSessionProject).
	sessionProject string
	sessionRoot    string
}

// New creates a Watcher. indexFn is called when file changes are detected.
func New(r *store.StoreRouter, indexFn IndexFunc) *Watcher {
	return &Watcher{
		router:   r,
		indexFn:  indexFn,
		projects: make(map[string]*projectState),
	}
}

// SetSessionProject tells the watcher which project to monitor.
// Must be called before Run. If not called, the watcher does nothing.
func (w *Watcher) SetSessionProject(name, rootPath string) {
	w.sessionProject = name
	w.sessionRoot = rootPath
}

// Run blocks until ctx is cancelled. Ticks at baseInterval, polling the
// session project only when its adaptive interval has elapsed.
// Waits graceInterval before the first poll to avoid I/O during MCP startup.
func (w *Watcher) Run(ctx context.Context) {
	w.ctx = ctx

	if w.sessionProject == "" || w.sessionRoot == "" {
		slog.Debug("watcher.skip", "reason", "no_session_project")
		// No session project — just block until cancelled.
		<-ctx.Done()
		return
	}

	// Grace period: let the MCP server finish initialization and first tool
	// calls before we start opening databases and walking file trees.
	select {
	case <-ctx.Done():
		return
	case <-time.After(graceInterval):
	}

	ticker := time.NewTicker(baseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.pollSession()
		}
	}
}

// pollSession polls only the session project for changes.
func (w *Watcher) pollSession() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("watcher.pollSession.panic", "panic", r)
		}
	}()

	now := time.Now()
	state, exists := w.projects[w.sessionProject]
	if !exists {
		state = &projectState{}
		w.projects[w.sessionProject] = state
	}

	if exists && now.Before(state.nextPoll) {
		return // not due yet
	}

	proj := &store.Project{
		Name:     w.sessionProject,
		RootPath: w.sessionRoot,
	}
	w.pollProject(proj, state)
}

// pollProject captures a snapshot of the file tree and compares with previous.
// First poll: captures baseline without triggering indexing.
// Subsequent polls: triggers indexFn if any file changed.
func (w *Watcher) pollProject(proj *store.Project, state *projectState) {
	// Verify root path still exists
	if _, err := os.Stat(proj.RootPath); err != nil {
		slog.Warn("watcher.root_gone", "project", proj.Name, "path", proj.RootPath)
		state.nextPoll = time.Now().Add(maxInterval)
		return
	}

	snap, err := captureSnapshot(proj.RootPath)
	if err != nil {
		slog.Warn("watcher.snapshot", "project", proj.Name, "err", err)
		state.nextPoll = time.Now().Add(state.interval)
		return
	}

	interval := pollInterval(len(snap))

	if state.snapshot == nil {
		// First poll — capture baseline, no index trigger
		slog.Debug("watcher.baseline", "project", proj.Name, "files", len(snap))
		state.snapshot = snap
		state.interval = interval
		state.nextPoll = time.Now().Add(interval)
		return
	}

	if snapshotsEqual(state.snapshot, snap) {
		state.interval = interval
		state.nextPoll = time.Now().Add(interval)
		return
	}

	slog.Info("watcher.changed", "project", proj.Name, "files", len(snap))
	if err := w.indexFn(w.ctx, proj.Name, proj.RootPath); err != nil {
		slog.Warn("watcher.index", "project", proj.Name, "err", err)
		// Keep old snapshot so we retry next cycle
		state.nextPoll = time.Now().Add(interval)
		return
	}

	// Successful index — update snapshot and recalculate interval
	state.snapshot = snap
	state.interval = pollInterval(len(snap))
	state.nextPoll = time.Now().Add(state.interval)
}

// captureSnapshot walks the file tree using discover.Discover and captures
// mtime+size for each file.
func captureSnapshot(rootPath string) (map[string]fileSnapshot, error) {
	files, err := discover.Discover(context.Background(), rootPath, nil)
	if err != nil {
		return nil, err
	}

	snap := make(map[string]fileSnapshot, len(files))
	for _, f := range files {
		info, statErr := os.Stat(f.Path)
		if statErr != nil {
			continue
		}
		snap[f.RelPath] = fileSnapshot{
			modTime: info.ModTime(),
			size:    info.Size(),
		}
	}
	return snap, nil
}

// snapshotsEqual returns true if two snapshots have identical files with same mtime+size.
func snapshotsEqual(a, b map[string]fileSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for path, aSnap := range a {
		bSnap, ok := b[path]
		if !ok {
			return false
		}
		if !aSnap.modTime.Equal(bSnap.modTime) || aSnap.size != bSnap.size {
			return false
		}
	}
	return true
}

// pollInterval computes the adaptive interval from file count.
// 1s base + 1s per 500 files, capped at 60s.
func pollInterval(fileCount int) time.Duration {
	ms := 1000 + (fileCount/500)*1000
	if ms > 60000 {
		ms = 60000
	}
	return time.Duration(ms) * time.Millisecond
}

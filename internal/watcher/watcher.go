package watcher

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/fsnotify/fsnotify"
)

const (
	fallbackInterval = 30 * time.Second
	debounceWindow   = 100 * time.Millisecond
)

type fileSnapshot struct {
	modTime time.Time
	size    int64
}

type projectState struct {
	snapshot map[string]fileSnapshot
	nextPoll time.Time
}

// IndexFunc is the callback signature for triggering a re-index.
type IndexFunc func(ctx context.Context, projectName, rootPath string) error

// Watcher detects file changes via fsnotify (inotify/kqueue) and triggers
// re-indexing. A snapshot-based fallback poll runs every 30 s to catch events
// missed on network mounts or other edge cases.
type Watcher struct {
	router  *store.StoreRouter
	indexFn IndexFunc

	// fsnotify watcher; nil when fsnotify is unavailable.
	fsw *fsnotify.Watcher

	// debounce: project name → pending timer.
	debounceMu  sync.Mutex
	debounceMap map[string]*time.Timer

	// fallback poll state (snapshot comparison).
	pollMu   sync.Mutex
	projects map[string]*projectState
}

// New creates a Watcher. indexFn is called when file changes are detected.
func New(r *store.StoreRouter, indexFn IndexFunc) *Watcher {
	return &Watcher{
		router:      r,
		indexFn:     indexFn,
		debounceMap: make(map[string]*time.Timer),
		projects:    make(map[string]*projectState),
	}
}

// Run blocks until ctx is cancelled. It starts fsnotify watching for
// instant detection and a 30 s fallback poll as a safety net.
func (w *Watcher) Run(ctx context.Context) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("watcher.fsnotify_unavailable", "err", err, "fallback", "poll_only")
	} else {
		w.fsw = fsw
		defer fsw.Close()

		// Register all directories for currently-indexed projects.
		w.watchAllProjects()

		go w.runFSNotify(ctx)
	}

	w.runFallbackPoll(ctx)
}

// runFSNotify processes fsnotify events until ctx is cancelled.
func (w *Watcher) runFSNotify(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handleFSEvent(ctx, event)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			slog.Warn("watcher.fsnotify_err", "err", err)
		}
	}
}

// handleFSEvent routes a single fsnotify event to the owning project.
func (w *Watcher) handleFSEvent(ctx context.Context, event fsnotify.Event) {
	// When a new directory is created, watch it immediately so new files
	// inside it are caught without waiting for a fallback poll.
	if event.Has(fsnotify.Create) {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			if addErr := w.fsw.Add(event.Name); addErr != nil {
				slog.Debug("watcher.add_dir", "path", event.Name, "err", addErr)
			}
		}
	}

	proj, ok := w.projectForPath(event.Name)
	if !ok {
		return
	}
	w.triggerDebounced(ctx, proj)
}

// runFallbackPoll runs the snapshot-based poll loop at a fixed 30 s interval.
func (w *Watcher) runFallbackPoll(ctx context.Context) {
	ticker := time.NewTicker(fallbackInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.pollAll(ctx)
		}
	}
}

// watchAllProjects registers fsnotify watchers for all known indexed projects.
func (w *Watcher) watchAllProjects() {
	infos, err := w.router.ListProjects()
	if err != nil {
		slog.Warn("watcher.list_projects", "err", err)
		return
	}
	for _, info := range infos {
		w.addProjectDirs(info.RootPath)
	}
}

// addProjectDirs recursively adds all subdirectories of rootPath to fsw.
func (w *Watcher) addProjectDirs(rootPath string) {
	if w.fsw == nil {
		return
	}
	walkErr := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if addErr := w.fsw.Add(path); addErr != nil {
				slog.Debug("watcher.add_dir", "path", path, "err", addErr)
			}
		}
		return nil
	})
	if walkErr != nil {
		slog.Debug("watcher.walk_err", "root", rootPath, "err", walkErr)
	}
}

// projectForPath finds the indexed project whose RootPath is a prefix of path.
func (w *Watcher) projectForPath(path string) (*store.ProjectInfo, bool) {
	infos, err := w.router.ListProjects()
	if err != nil {
		return nil, false
	}
	for _, info := range infos {
		root := info.RootPath
		if !strings.HasSuffix(root, string(filepath.Separator)) {
			root += string(filepath.Separator)
		}
		if strings.HasPrefix(path, root) || path == info.RootPath {
			return info, true
		}
	}
	return nil, false
}

// triggerDebounced coalesces rapid events for a project into a single re-index
// fired after a 100 ms quiet window.
func (w *Watcher) triggerDebounced(ctx context.Context, proj *store.ProjectInfo) {
	w.debounceMu.Lock()
	if t, ok := w.debounceMap[proj.Name]; ok {
		t.Reset(debounceWindow)
	} else {
		name := proj.Name
		rootPath := proj.RootPath
		w.debounceMap[proj.Name] = time.AfterFunc(debounceWindow, func() {
			if err := w.indexFn(ctx, name, rootPath); err != nil {
				slog.Warn("watcher.index", "project", name, "err", err)
			}
			w.debounceMu.Lock()
			delete(w.debounceMap, name)
			w.debounceMu.Unlock()
		})
	}
	w.debounceMu.Unlock()
}

// pollAll runs a snapshot comparison for all projects due for a fallback poll.
func (w *Watcher) pollAll(ctx context.Context) {
	projectInfos, err := w.router.ListProjects()
	if err != nil {
		slog.Warn("watcher.list_projects", "err", err)
		return
	}

	// Register any newly-indexed projects with fsnotify.
	if w.fsw != nil {
		w.pollMu.Lock()
		for _, info := range projectInfos {
			if _, seen := w.projects[info.Name]; !seen {
				w.addProjectDirs(info.RootPath)
			}
		}
		w.pollMu.Unlock()
	}

	now := time.Now()
	for _, info := range projectInfos {
		st, stErr := w.router.ForProject(info.Name)
		if stErr != nil {
			continue
		}
		proj, projErr := st.GetProject(info.Name)
		if projErr != nil || proj == nil {
			continue
		}

		w.pollMu.Lock()
		state, exists := w.projects[info.Name]
		if !exists {
			state = &projectState{}
			w.projects[info.Name] = state
		}
		w.pollMu.Unlock()

		if exists && now.Before(state.nextPoll) {
			continue
		}

		w.pollProject(ctx, proj, state)
	}
}

// pollProject captures a snapshot and triggers indexFn when the tree changed.
func (w *Watcher) pollProject(ctx context.Context, proj *store.Project, state *projectState) {
	if _, err := os.Stat(proj.RootPath); err != nil {
		slog.Warn("watcher.root_gone", "project", proj.Name, "path", proj.RootPath)
		state.nextPoll = time.Now().Add(fallbackInterval)
		return
	}

	snap, err := captureSnapshot(proj.RootPath)
	if err != nil {
		slog.Warn("watcher.snapshot", "project", proj.Name, "err", err)
		state.nextPoll = time.Now().Add(fallbackInterval)
		return
	}

	if state.snapshot == nil {
		// First poll — capture baseline without triggering re-index.
		slog.Debug("watcher.baseline", "project", proj.Name, "files", len(snap))
		state.snapshot = snap
		state.nextPoll = time.Now().Add(fallbackInterval)
		return
	}

	if snapshotsEqual(state.snapshot, snap) {
		state.nextPoll = time.Now().Add(fallbackInterval)
		return
	}

	slog.Info("watcher.changed", "project", proj.Name, "files", len(snap))
	if err := w.indexFn(ctx, proj.Name, proj.RootPath); err != nil {
		slog.Warn("watcher.index", "project", proj.Name, "err", err)
		state.nextPoll = time.Now().Add(fallbackInterval)
		return
	}

	state.snapshot = snap
	state.nextPoll = time.Now().Add(fallbackInterval)
}

// captureSnapshot walks the file tree using discover.Discover and records
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

// snapshotsEqual returns true if both snapshots contain the same files with
// identical mtime and size.
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

// pollInterval is retained for tests that reference it directly.
// It computes the adaptive interval from file count (kept for test compatibility).
func pollInterval(fileCount int) time.Duration {
	ms := 1000 + (fileCount/500)*1000
	if ms > 60000 {
		ms = 60000
	}
	return time.Duration(ms) * time.Millisecond
}

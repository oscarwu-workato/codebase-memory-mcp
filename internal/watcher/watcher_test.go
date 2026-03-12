package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// newTestRouter creates a StoreRouter backed by a temp directory with a project registered.
func newTestRouter(t *testing.T, projectName, rootPath string) *store.StoreRouter {
	t.Helper()
	dbDir := t.TempDir()
	r, err := store.NewRouterWithDir(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.CloseAll)
	if projectName != "" {
		st, err := r.ForProject(projectName)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertProject(projectName, rootPath); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestSnapshotsEqual(t *testing.T) {
	now := time.Now()

	a := map[string]fileSnapshot{
		"main.go": {modTime: now, size: 100},
		"util.go": {modTime: now, size: 200},
	}
	b := map[string]fileSnapshot{
		"main.go": {modTime: now, size: 100},
		"util.go": {modTime: now, size: 200},
	}
	if !snapshotsEqual(a, b) {
		t.Error("identical snapshots should be equal")
	}

	// Different size
	c := map[string]fileSnapshot{
		"main.go": {modTime: now, size: 101},
		"util.go": {modTime: now, size: 200},
	}
	if snapshotsEqual(a, c) {
		t.Error("different size should not be equal")
	}

	// Different mtime
	d := map[string]fileSnapshot{
		"main.go": {modTime: now.Add(time.Second), size: 100},
		"util.go": {modTime: now, size: 200},
	}
	if snapshotsEqual(a, d) {
		t.Error("different mtime should not be equal")
	}

	// Missing file
	e := map[string]fileSnapshot{
		"main.go": {modTime: now, size: 100},
	}
	if snapshotsEqual(a, e) {
		t.Error("different file count should not be equal")
	}

	// Extra file
	f := map[string]fileSnapshot{
		"main.go": {modTime: now, size: 100},
		"util.go": {modTime: now, size: 200},
		"new.go":  {modTime: now, size: 50},
	}
	if snapshotsEqual(a, f) {
		t.Error("extra file should not be equal")
	}

	// Both empty
	if !snapshotsEqual(map[string]fileSnapshot{}, map[string]fileSnapshot{}) {
		t.Error("both empty should be equal")
	}
}


func TestCaptureSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a Go file that discover.Discover will pick up
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snap, err := captureSnapshot(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(snap) != 1 {
		t.Fatalf("expected 1 file, got %d", len(snap))
	}

	s, ok := snap["main.go"]
	if !ok {
		t.Fatal("expected main.go in snapshot")
	}
	if s.size == 0 {
		t.Error("expected non-zero size")
	}
	if s.modTime.IsZero() {
		t.Error("expected non-zero modtime")
	}
}

func TestCaptureSnapshotDetectsChanges(t *testing.T) {
	tmpDir := t.TempDir()
	goFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	snap1, err := captureSnapshot(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Ensure mtime advances (some filesystems have 1s granularity)
	time.Sleep(10 * time.Millisecond)
	now := time.Now().Add(time.Second)
	if err := os.Chtimes(goFile, now, now); err != nil {
		t.Fatal(err)
	}

	snap2, err := captureSnapshot(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if snapshotsEqual(snap1, snap2) {
		t.Error("snapshots should differ after mtime change")
	}
}

func TestWatcherTriggersOnChange(t *testing.T) {
	tmpDir := t.TempDir()
	goFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := newTestRouter(t, filepath.Base(tmpDir), tmpDir)
	defer r.CloseAll()

	var indexCount atomic.Int32
	indexFn := func(_ context.Context, _, _ string) error {
		indexCount.Add(1)
		return nil
	}

	w := New(r, indexFn)
	ctx := context.Background()

	// First poll — baseline capture, no index
	w.pollAll(ctx)
	if indexCount.Load() != 0 {
		t.Errorf("first poll should not trigger index, got %d", indexCount.Load())
	}

	// Poll again without changes — no index
	// Reset nextPoll to allow immediate re-poll
	for _, state := range w.projects {
		state.nextPoll = time.Time{}
	}
	w.pollAll(ctx)
	if indexCount.Load() != 0 {
		t.Errorf("no-change poll should not trigger index, got %d", indexCount.Load())
	}

	// Modify the file
	futureTime := time.Now().Add(time.Second)
	if err := os.Chtimes(goFile, futureTime, futureTime); err != nil {
		t.Fatal(err)
	}

	// Reset nextPoll and poll again — should trigger
	for _, state := range w.projects {
		state.nextPoll = time.Time{}
	}
	w.pollAll(ctx)
	if indexCount.Load() != 1 {
		t.Errorf("changed file should trigger index, got %d", indexCount.Load())
	}
}

func TestWatcherCancellation(t *testing.T) {
	dbDir := t.TempDir()
	r, err := store.NewRouterWithDir(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	w := New(r, func(_ context.Context, _, _ string) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// OK — goroutine exited cleanly
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not stop after context cancellation")
	}
}

func TestWatcherSkipsMissingRoot(t *testing.T) {
	r := newTestRouter(t, "ghost", "/nonexistent/path")
	defer r.CloseAll()

	var indexCount atomic.Int32
	w := New(r, func(_ context.Context, _, _ string) error {
		indexCount.Add(1)
		return nil
	})

	w.pollAll(context.Background())
	if indexCount.Load() != 0 {
		t.Errorf("should not index missing root, got %d", indexCount.Load())
	}
}

// ── Week 2: project-cache and debounce tests ──────────────────────────────

func TestWatcherProjectCache(t *testing.T) {
	tmpDir := t.TempDir()
	projName := filepath.Base(tmpDir)
	r := newTestRouter(t, projName, tmpDir)

	w := New(r, func(_ context.Context, _, _ string) error { return nil })

	w.refreshProjectCache()

	w.cachedProjectsMu.RLock()
	cached := w.cachedProjects
	w.cachedProjectsMu.RUnlock()
	if len(cached) == 0 {
		t.Fatal("expected cachedProjects to be non-nil after refreshProjectCache")
	}

	proj, ok := w.projectForPath(tmpDir + "/somefile.go")
	if !ok {
		t.Fatal("projectForPath should return true for file under registered root")
	}
	if proj == nil {
		t.Fatal("projectForPath returned nil project")
	}
	if proj.Name != projName {
		t.Errorf("got project name %q, want %q", proj.Name, projName)
	}
}

func TestWatcherProjectCacheMiss(t *testing.T) {
	tmpDir := t.TempDir()
	r := newTestRouter(t, filepath.Base(tmpDir), tmpDir)

	w := New(r, func(_ context.Context, _, _ string) error { return nil })
	// intentionally do NOT call refreshProjectCache

	proj, ok := w.projectForPath("/some/unrelated/path")
	if ok {
		t.Errorf("expected false for unrelated path, got project %v", proj)
	}
	if proj != nil {
		t.Errorf("expected nil project, got %v", proj)
	}
}

func TestWatcherProjectForPathPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	projName := "myrepo"
	r := newTestRouter(t, projName, tmpDir)

	w := New(r, func(_ context.Context, _, _ string) error { return nil })
	w.refreshProjectCache()

	proj, ok := w.projectForPath(tmpDir + "/subdir/file.go")
	if !ok {
		t.Fatal("expected projectForPath to match subdirectory file")
	}
	if proj.Name != projName {
		t.Errorf("got project name %q, want %q", proj.Name, projName)
	}
}

func TestWatcherProjectForPathNoFalsePositive(t *testing.T) {
	tmpDir := t.TempDir()
	projName := "repo"
	r := newTestRouter(t, projName, tmpDir)

	w := New(r, func(_ context.Context, _, _ string) error { return nil })
	w.refreshProjectCache()

	// Construct a path that starts with the same bytes but is a different directory.
	// tmpDir is something like /tmp/TestXxx123; append "Other" to simulate /tmp/TestXxx123Other.
	otherPath := tmpDir + "Other/file.go"

	proj, ok := w.projectForPath(otherPath)
	if ok {
		t.Errorf("projectForPath should not match sibling directory %q, got project %v", otherPath, proj)
	}
}

// newDebouncedTestWatcher creates a Watcher backed by a fresh router rooted at
// tmpDir and returns the watcher, a counter incremented by indexFn, and a
// ProjectInfo for the registered project. Extracted because all three
// triggerDebounced tests share this exact setup.
func newDebouncedTestWatcher(t *testing.T) (*Watcher, *atomic.Int32, *store.ProjectInfo) {
	t.Helper()
	tmpDir := t.TempDir()
	projName := filepath.Base(tmpDir)
	r := newTestRouter(t, projName, tmpDir)

	var counter atomic.Int32
	w := New(r, func(_ context.Context, _, _ string) error {
		counter.Add(1)
		return nil
	})
	proj := &store.ProjectInfo{Name: projName, RootPath: tmpDir}
	return w, &counter, proj
}

func TestWatcherTriggerDebouncedFires(t *testing.T) {
	w, counter, proj := newDebouncedTestWatcher(t)

	w.triggerDebounced(context.Background(), proj)

	// Wait longer than the debounce window.
	time.Sleep(200 * time.Millisecond)

	if n := counter.Load(); n != 1 {
		t.Errorf("expected indexFn called exactly once, got %d", n)
	}
}

func TestWatcherTriggerDebouncedCoalesces(t *testing.T) {
	w, counter, proj := newDebouncedTestWatcher(t)

	// Call 5 times rapidly — all within the debounce window.
	for i := 0; i < 5; i++ {
		w.triggerDebounced(context.Background(), proj)
	}

	// Wait for the single coalesced fire.
	time.Sleep(200 * time.Millisecond)

	if n := counter.Load(); n != 1 {
		t.Errorf("expected exactly 1 coalesced fire, got %d", n)
	}
}

func TestWatcherTriggerDebouncedCtxCancel(t *testing.T) {
	w, counter, proj := newDebouncedTestWatcher(t)

	ctx, cancel := context.WithCancel(context.Background())
	w.triggerDebounced(ctx, proj)
	// Cancel before the debounce window elapses.
	cancel()

	time.Sleep(200 * time.Millisecond)

	if n := counter.Load(); n != 0 {
		t.Errorf("expected indexFn NOT called after ctx cancel, got %d", n)
	}
}

func TestWatcherWatchAllProjectsPrimesState(t *testing.T) {
	tmpDir := t.TempDir()
	projName := filepath.Base(tmpDir)
	r := newTestRouter(t, projName, tmpDir)

	w := New(r, func(_ context.Context, _, _ string) error { return nil })

	w.watchAllProjects()

	w.pollMu.Lock()
	state := w.projects[projName]
	w.pollMu.Unlock()

	if state == nil {
		t.Errorf("expected w.projects[%q] to be non-nil after watchAllProjects", projName)
	}
}

func TestWatcherNewFileTriggersIndex(t *testing.T) {
	tmpDir := t.TempDir()
	goFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := newTestRouter(t, filepath.Base(tmpDir), tmpDir)
	defer r.CloseAll()

	var indexCount atomic.Int32
	w := New(r, func(_ context.Context, _, _ string) error {
		indexCount.Add(1)
		return nil
	})

	ctx := context.Background()

	// Baseline
	w.pollAll(ctx)

	// Add a new file
	if err := os.WriteFile(filepath.Join(tmpDir, "util.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, state := range w.projects {
		state.nextPoll = time.Time{}
	}
	w.pollAll(ctx)
	if indexCount.Load() != 1 {
		t.Errorf("new file should trigger index, got %d", indexCount.Load())
	}
}

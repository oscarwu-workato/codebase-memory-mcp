package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testArchServer creates a Server backed by a real (but empty) store, suitable for
// exercising handleGetArchitecture and the arch cache logic.
func testArchServer(t *testing.T) (*Server, string) {
	t.Helper()

	tmpDir := t.TempDir()
	routerDir := filepath.Join(tmpDir, "db")
	projRoot := filepath.Join(tmpDir, "project")

	if err := os.MkdirAll(projRoot, 0o750); err != nil {
		t.Fatal(err)
	}

	router, err := store.NewRouterWithDir(routerDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.CloseAll)

	projName := "arch-test-project"
	st, err := router.ForProject(projName)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProject(projName, projRoot); err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		router:         router,
		handlers:       make(map[string]mcp.ToolHandler),
		archCache:      make(map[string]string),
		sessionProject: projName,
	}
	return srv, projName
}

// callGetArchitecture invokes handleGetArchitecture with the given aspects list.
func callGetArchitecture(t *testing.T, srv *Server, projName string, aspects []string) *mcp.CallToolResult {
	t.Helper()

	args := map[string]any{
		"project": projName,
		"aspects": func() []any {
			out := make([]any, len(aspects))
			for i, a := range aspects {
				out[i] = a
			}
			return out
		}(),
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	req := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Name:      "get_architecture",
			Arguments: rawArgs,
		},
	}
	result, err := srv.handleGetArchitecture(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGetArchitecture error: %v", err)
	}
	return result
}

// TestBumpGraphVersionConcurrent verifies that 100 concurrent invalidateArchCache
// calls each atomically increment graphWriteVer to exactly 100.
// Run with -race to confirm no data races.
func TestBumpGraphVersionConcurrent(t *testing.T) {
	srv := &Server{
		archCache: make(map[string]string),
	}

	const goroutines = 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.invalidateArchCache("testproject")
		}()
	}
	wg.Wait()

	raw, ok := srv.graphWriteVer.Load("testproject")
	if !ok {
		t.Fatal("expected graphWriteVer entry for testproject")
	}
	got, ok := raw.(uint64)
	if !ok {
		t.Fatalf("expected uint64 version, got %T", raw)
	}
	if got != goroutines {
		t.Errorf("expected version %d, got %d", goroutines, got)
	}
}

// TestArchCacheHit verifies that a second handleGetArchitecture call for the same
// project+aspects is served from the cache: s.archCache holds an entry for the key
// after the first call, and it is unchanged after the second call.
func TestArchCacheHit(t *testing.T) {
	srv, projName := testArchServer(t)

	cacheKey := projName + ":languages"

	// First call — should miss cache and populate it.
	result1 := callGetArchitecture(t, srv, projName, []string{"languages"})
	if result1 == nil || result1.IsError {
		t.Fatal("first handleGetArchitecture call returned error or nil")
	}

	srv.archCacheMu.RLock()
	firstCached, exists := srv.archCache[cacheKey]
	srv.archCacheMu.RUnlock()

	if !exists {
		t.Fatal("expected cache entry after first call")
	}
	if firstCached == "" {
		t.Fatal("expected non-empty cached value after first call")
	}

	// Second call — must hit the cache; value must be identical.
	result2 := callGetArchitecture(t, srv, projName, []string{"languages"})
	if result2 == nil || result2.IsError {
		t.Fatal("second handleGetArchitecture call returned error or nil")
	}

	srv.archCacheMu.RLock()
	secondCached := srv.archCache[cacheKey]
	srv.archCacheMu.RUnlock()

	if firstCached != secondCached {
		t.Error("cache entry changed between first and second call")
	}

	// Both results should return the same text.
	text1 := textContent(t, result1)
	text2 := textContent(t, result2)
	if text1 != text2 {
		t.Errorf("result text differs between calls:\n  1: %s\n  2: %s", text1, text2)
	}
}

// TestArchCacheKeyIsolation verifies that calls with different aspects produce
// distinct cache keys and both entries coexist in the cache.
func TestArchCacheKeyIsolation(t *testing.T) {
	srv, projName := testArchServer(t)

	callGetArchitecture(t, srv, projName, []string{"languages"})
	callGetArchitecture(t, srv, projName, []string{"packages"})

	srv.archCacheMu.RLock()
	_, hasLanguages := srv.archCache[projName+":languages"]
	_, hasPackages := srv.archCache[projName+":packages"]
	srv.archCacheMu.RUnlock()

	if !hasLanguages {
		t.Error("expected cache entry for aspects=languages")
	}
	if !hasPackages {
		t.Error("expected cache entry for aspects=packages")
	}

	srv.archCacheMu.RLock()
	langVal := srv.archCache[projName+":languages"]
	pkgVal := srv.archCache[projName+":packages"]
	srv.archCacheMu.RUnlock()

	if langVal == pkgVal {
		t.Error("expected distinct cache values for different aspect keys")
	}
}

// TestArchCacheInvalidation verifies that invalidateArchCache("myproj") removes all
// keys with prefix "myproj:" while leaving other projects' keys untouched.
func TestArchCacheInvalidation(t *testing.T) {
	srv := &Server{
		archCache: map[string]string{
			"myproj:all":       `{"project":"myproj","languages":[]}`,
			"myproj:languages": `{"project":"myproj"}`,
			"otherproj:all":    `{"project":"otherproj"}`,
		},
	}

	srv.invalidateArchCache("myproj")

	if _, ok := srv.archCache["myproj:all"]; ok {
		t.Error("expected myproj:all to be evicted after invalidation")
	}
	if _, ok := srv.archCache["myproj:languages"]; ok {
		t.Error("expected myproj:languages to be evicted after invalidation")
	}
	if _, ok := srv.archCache["otherproj:all"]; !ok {
		t.Error("expected otherproj:all to survive invalidation of myproj")
	}
}

// TestArchCacheConcurrent verifies that 20 goroutines concurrently calling
// handleGetArchitecture for the same project produce no data races (run with -race)
// and all return non-nil, non-error results.
func TestArchCacheConcurrent(t *testing.T) {
	srv, projName := testArchServer(t)

	const goroutines = 20
	results := make([]*mcp.CallToolResult, goroutines)
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = callGetArchitecture(t, srv, projName, []string{"languages"})
		}()
	}
	wg.Wait()

	for i, r := range results {
		if r == nil {
			t.Errorf("goroutine %d: got nil result", i)
			continue
		}
		if r.IsError {
			t.Errorf("goroutine %d: got error result: %v", i, textContent(t, r))
		}
	}
}

// textContent extracts the text from the first TextContent in a CallToolResult.
func textContent(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if len(r.Content) == 0 {
		t.Fatal("result has no content")
	}
	tc, ok := r.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected *mcp.TextContent, got %T", r.Content[0])
	}
	return tc.Text
}

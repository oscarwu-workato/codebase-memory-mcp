package store

import (
	"context"
	"strings"
	"testing"
)

// --- Partial index tests (use OpenMemory) ---

func TestPartialIndexesExist(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	rows, err := s.DB().QueryContext(context.Background(), `SELECT name FROM pragma_index_list('nodes')`)
	if err != nil {
		t.Fatalf("pragma_index_list: %v", err)
	}
	defer rows.Close()

	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	required := []string{"idx_nodes_file_line", "idx_nodes_entry_point", "idx_nodes_is_test"}
	for _, idx := range required {
		if !found[idx] {
			t.Errorf("expected index %q to exist, found indexes: %v", idx, found)
		}
	}
}

func TestPartialIndexDropAndRecreate(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Drop indexes.
	if err := s.DropUserIndexes(ctx); err != nil {
		t.Fatalf("DropUserIndexes: %v", err)
	}

	// Verify the three partial indexes are gone.
	found := indexSet(t, s)
	partial := []string{"idx_nodes_file_line", "idx_nodes_entry_point", "idx_nodes_is_test"}
	for _, idx := range partial {
		if found[idx] {
			t.Errorf("expected index %q to be absent after drop", idx)
		}
	}

	// Recreate.
	if err := s.CreateUserIndexes(ctx); err != nil {
		t.Fatalf("CreateUserIndexes: %v", err)
	}

	// Verify the three partial indexes are back.
	found = indexSet(t, s)
	for _, idx := range partial {
		if !found[idx] {
			t.Errorf("expected index %q to exist after recreate", idx)
		}
	}
}

func TestCreateUserIndexesIdempotent(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	if err := s.CreateUserIndexes(ctx); err != nil {
		t.Fatalf("CreateUserIndexes first call: %v", err)
	}
	if err := s.CreateUserIndexes(ctx); err != nil {
		t.Fatalf("CreateUserIndexes second call: %v", err)
	}
}

// indexSet returns the set of index names on the 'nodes' table.
func indexSet(t *testing.T, s *Store) map[string]bool {
	t.Helper()
	rows, err := s.DB().QueryContext(context.Background(), `SELECT name FROM pragma_index_list('nodes')`)
	if err != nil {
		t.Fatalf("pragma_index_list: %v", err)
	}
	defer rows.Close()

	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return found
}

// --- OpenReadOnly tests (use real temp files) ---

func TestOpenReadOnlyMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/nonexistent.db"

	s, err := OpenReadOnly(path)
	if err == nil {
		s.Close()
		t.Fatal("expected error opening nonexistent file, got nil")
	}
	// The error message should reference the path or describe the failure.
	if !strings.Contains(err.Error(), path) && !strings.Contains(err.Error(), "nonexistent") && !strings.Contains(err.Error(), "no such file") {
		t.Errorf("unexpected error message (expected path or 'no such file'): %v", err)
	}
}

func TestOpenReadOnlySuccess(t *testing.T) {
	dir := t.TempDir()

	// Create and seed a writable database.
	ws, err := OpenInDir(dir, "testproject")
	if err != nil {
		t.Fatalf("OpenInDir: %v", err)
	}
	if err := ws.UpsertProject("testproject", "/tmp/testproject"); err != nil {
		ws.Close()
		t.Fatalf("UpsertProject: %v", err)
	}
	_, _ = ws.UpsertNode(&Node{
		Project:       "testproject",
		Label:         "Function",
		Name:          "main",
		QualifiedName: "testproject.main",
	})
	ws.Close()

	// Open read-only.
	rs, err := OpenReadOnly(dir + "/testproject.db")
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer rs.Close()

	var count int
	err = rs.DB().QueryRowContext(context.Background(), "SELECT count(*) FROM nodes").Scan(&count)
	if err != nil {
		t.Fatalf("SELECT count(*): %v", err)
	}
	if count < 0 {
		t.Errorf("unexpected count: %d", count)
	}
}

func TestOpenReadOnlyRejectsWrites(t *testing.T) {
	dir := t.TempDir()

	// Create an initial writable database.
	ws, err := OpenInDir(dir, "ro_test")
	if err != nil {
		t.Fatalf("OpenInDir: %v", err)
	}
	if err := ws.UpsertProject("ro_test", "/tmp/ro_test"); err != nil {
		ws.Close()
		t.Fatalf("UpsertProject: %v", err)
	}
	ws.Close()

	// Open read-only.
	rs, err := OpenReadOnly(dir + "/ro_test.db")
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer rs.Close()

	// Attempt to write — should fail.
	_, writeErr := rs.DB().ExecContext(context.Background(),
		`INSERT INTO nodes(project, label, name, qualified_name) VALUES (?,?,?,?)`,
		"ro_test", "Function", "bad", "ro_test.bad")
	if writeErr == nil {
		t.Error("expected error on INSERT into read-only store, got nil")
	}
}

func TestOpenReadOnlyPragmas(t *testing.T) {
	dir := t.TempDir()

	ws, err := OpenInDir(dir, "pragma_test")
	if err != nil {
		t.Fatalf("OpenInDir: %v", err)
	}
	if err := ws.UpsertProject("pragma_test", "/tmp/pragma_test"); err != nil {
		ws.Close()
		t.Fatalf("UpsertProject: %v", err)
	}
	ws.Close()

	rs, err := OpenReadOnly(dir + "/pragma_test.db")
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer rs.Close()

	var val string
	if err := rs.DB().QueryRowContext(context.Background(), "PRAGMA temp_store").Scan(&val); err != nil {
		t.Fatalf("PRAGMA temp_store: %v", err)
	}
	if val != "2" {
		t.Errorf("temp_store = %q, want \"2\" (MEMORY)", val)
	}
}

// --- ForProjectReadOnly tests ---

func TestForProjectReadOnlyMissing(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}

	_, err = r.ForProjectReadOnly("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent project, got nil")
	}
}

func TestForProjectReadOnlyExists(t *testing.T) {
	dir := t.TempDir()

	// Create the project database.
	ws, err := OpenInDir(dir, "myproject")
	if err != nil {
		t.Fatalf("OpenInDir: %v", err)
	}
	if err := ws.UpsertProject("myproject", "/tmp/myproject"); err != nil {
		ws.Close()
		t.Fatalf("UpsertProject: %v", err)
	}
	_, _ = ws.UpsertNode(&Node{
		Project:       "myproject",
		Label:         "Function",
		Name:          "hello",
		QualifiedName: "myproject.hello",
	})
	ws.Close()

	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}

	rs, err := r.ForProjectReadOnly("myproject")
	if err != nil {
		t.Fatalf("ForProjectReadOnly: %v", err)
	}
	defer rs.Close()

	var count int
	if err := rs.DB().QueryRowContext(context.Background(), "SELECT count(*) FROM nodes").Scan(&count); err != nil {
		t.Fatalf("SELECT count(*): %v", err)
	}
	if count < 1 {
		t.Errorf("expected at least 1 node, got %d", count)
	}
}

func TestForProjectReadOnlyNoCache(t *testing.T) {
	dir := t.TempDir()

	// Create the project database.
	ws, err := OpenInDir(dir, "nocache")
	if err != nil {
		t.Fatalf("OpenInDir: %v", err)
	}
	if err := ws.UpsertProject("nocache", "/tmp/nocache"); err != nil {
		ws.Close()
		t.Fatalf("UpsertProject: %v", err)
	}
	ws.Close()

	r, err := NewRouterWithDir(dir)
	if err != nil {
		t.Fatalf("NewRouterWithDir: %v", err)
	}

	s1, err := r.ForProjectReadOnly("nocache")
	if err != nil {
		t.Fatalf("ForProjectReadOnly first: %v", err)
	}
	defer s1.Close()

	s2, err := r.ForProjectReadOnly("nocache")
	if err != nil {
		t.Fatalf("ForProjectReadOnly second: %v", err)
	}
	defer s2.Close()

	if s1 == s2 {
		t.Error("ForProjectReadOnly returned the same pointer twice (unexpected caching)")
	}
}

// --- archLayers tests ---

func TestArchLayersWithNilCached(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	// nil cached → archLayers fetches boundaries internally.
	layers, err := s.archLayers("test", nil)
	if err != nil {
		t.Fatalf("archLayers(nil): %v", err)
	}
	if len(layers) == 0 {
		t.Fatal("expected at least one layer classification")
	}

	for _, l := range layers {
		if l.Name == "" {
			t.Error("layer with empty name")
		}
		if l.Layer == "" {
			t.Error("layer with empty layer field")
		}
	}
}

func TestArchLayersWithCached(t *testing.T) {
	s := setupArchTestStore(t)
	defer s.Close()

	// Pre-compute boundaries.
	boundaries, err := s.archBoundaries("test")
	if err != nil {
		t.Fatalf("archBoundaries: %v", err)
	}

	// Pass pre-computed boundaries.
	layersCached, err := s.archLayers("test", boundaries)
	if err != nil {
		t.Fatalf("archLayers(cached): %v", err)
	}

	// Also call with nil for comparison.
	layersNil, err := s.archLayers("test", nil)
	if err != nil {
		t.Fatalf("archLayers(nil): %v", err)
	}

	if len(layersCached) == 0 {
		t.Fatal("expected non-empty layers with cached boundaries")
	}

	// Both calls should produce the same number of layers (boundaries are the same data).
	if len(layersCached) != len(layersNil) {
		t.Errorf("cached=%d layers, nil=%d layers — should be equal", len(layersCached), len(layersNil))
	}
}

// --- validateProjectName tests (security: path-traversal prevention) ---

// TestValidateProjectNameRejectsInvalid verifies that path-traversal payloads
// and other invalid names are rejected by ForProject, ForProjectReadOnly,
// and DeleteProject before they can escape the cache directory.
func TestValidateProjectNameRejectsInvalid(t *testing.T) {
	cases := []struct {
		name  string
		label string
	}{
		{"../../etc/passwd", "path traversal"},
		{"..", "dot-dot"},
		{"a/b", "slash"},
		{"a\\b", "backslash"},
		{"a b", "space"},
		{"a*b", "wildcard"},
		{"", "empty string"},
		{"a|b", "pipe"},
		{"a;b", "semicolon"},
	}
	for _, tc := range cases {
		dir := t.TempDir()
		r, err := NewRouterWithDir(dir)
		if err != nil {
			t.Fatalf("%s: NewRouterWithDir: %v", tc.label, err)
		}
		if _, err := r.ForProject(tc.name); err == nil {
			t.Errorf("ForProject(%q) [%s]: expected error, got nil", tc.name, tc.label)
		}
		if _, err := r.ForProjectReadOnly(tc.name); err == nil {
			t.Errorf("ForProjectReadOnly(%q) [%s]: expected error, got nil", tc.name, tc.label)
		}
		if err := r.DeleteProject(tc.name); err == nil {
			t.Errorf("DeleteProject(%q) [%s]: expected error, got nil", tc.name, tc.label)
		}
	}
}

// TestValidateProjectNameAcceptsValid verifies that well-formed project names
// are allowed through the validation boundary.
func TestValidateProjectNameAcceptsValid(t *testing.T) {
	valid := []string{"my-project", "repo.v2", "My_App123", "a", "x-y-z"}
	for _, name := range valid {
		dir := t.TempDir()
		r, err := NewRouterWithDir(dir)
		if err != nil {
			t.Fatalf("%s: NewRouterWithDir: %v", name, err)
		}
		if _, err := r.ForProject(name); err != nil {
			t.Errorf("ForProject(%q): expected nil error for valid name, got: %v", name, err)
		}
	}
}

// TestCommunityCacheLookupIndexExists verifies that both community_cache indexes
// (single-column and composite) are created by initSchema / OpenMemory.
func TestCommunityCacheLookupIndexExists(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	rows, err := s.DB().QueryContext(context.Background(), `SELECT name FROM pragma_index_list('community_cache')`)
	if err != nil {
		t.Fatalf("pragma_index_list: %v", err)
	}
	defer rows.Close()

	found := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	for _, idx := range []string{"idx_community_cache_project", "idx_community_cache_lookup"} {
		if !found[idx] {
			t.Errorf("expected index %q in community_cache, found: %v", idx, found)
		}
	}
}

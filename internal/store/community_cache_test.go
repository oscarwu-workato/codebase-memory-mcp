package store

import (
	"context"
	"testing"
)

func setupCacheStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	if err := s.UpsertProject("test", "/tmp/test"); err != nil {
		s.Close()
		t.Fatalf("UpsertProject: %v", err)
	}
	return s
}

// TestCommunityCacheRoundtrip saves a 5-node partition and loads it back,
// verifying the returned map is identical.
func TestCommunityCacheRoundtrip(t *testing.T) {
	s := setupCacheStore(t)
	defer s.Close()

	ctx := context.Background()
	partition := map[int64]int{1: 0, 2: 0, 3: 1, 4: 1, 5: 2}

	if err := s.SaveCommunityCache(ctx, "test", "hashA", partition); err != nil {
		t.Fatalf("SaveCommunityCache: %v", err)
	}

	got, err := s.LoadCommunityCache(ctx, "test", "hashA")
	if err != nil {
		t.Fatalf("LoadCommunityCache: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil partition, got nil")
	}
	if len(got) != len(partition) {
		t.Fatalf("partition size: got %d, want %d", len(got), len(partition))
	}
	for nodeID, comm := range partition {
		if got[nodeID] != comm {
			t.Errorf("node %d: got community %d, want %d", nodeID, got[nodeID], comm)
		}
	}
}

// TestCommunityCacheHashMiss saves with hash "A" but loads with hash "B",
// expecting nil (cache miss).
func TestCommunityCacheHashMiss(t *testing.T) {
	s := setupCacheStore(t)
	defer s.Close()

	ctx := context.Background()
	partition := map[int64]int{1: 0, 2: 1}

	if err := s.SaveCommunityCache(ctx, "test", "A", partition); err != nil {
		t.Fatalf("SaveCommunityCache: %v", err)
	}

	got, err := s.LoadCommunityCache(ctx, "test", "B")
	if err != nil {
		t.Fatalf("LoadCommunityCache: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil on hash miss, got partition with %d entries", len(got))
	}
}

// TestCommunityCacheEmptyDB loads from a fresh store with no cache rows,
// expecting nil.
func TestCommunityCacheEmptyDB(t *testing.T) {
	s := setupCacheStore(t)
	defer s.Close()

	ctx := context.Background()

	got, err := s.LoadCommunityCache(ctx, "test", "anyHash")
	if err != nil {
		t.Fatalf("LoadCommunityCache: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil from empty DB, got %v", got)
	}
}

// TestCommunityCacheReplace saves V1 then V2 for the same project+hash,
// verifying only V2 is returned.
func TestCommunityCacheReplace(t *testing.T) {
	s := setupCacheStore(t)
	defer s.Close()

	ctx := context.Background()
	v1 := map[int64]int{1: 0, 2: 0}
	v2 := map[int64]int{1: 1, 2: 1, 3: 1}

	if err := s.SaveCommunityCache(ctx, "test", "h1", v1); err != nil {
		t.Fatalf("SaveCommunityCache v1: %v", err)
	}
	if err := s.SaveCommunityCache(ctx, "test", "h1", v2); err != nil {
		t.Fatalf("SaveCommunityCache v2: %v", err)
	}

	got, err := s.LoadCommunityCache(ctx, "test", "h1")
	if err != nil {
		t.Fatalf("LoadCommunityCache: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil partition, got nil")
	}
	if len(got) != len(v2) {
		t.Fatalf("partition size: got %d, want %d (v2)", len(got), len(v2))
	}
	for nodeID, comm := range v2 {
		if got[nodeID] != comm {
			t.Errorf("node %d: got %d, want %d", nodeID, got[nodeID], comm)
		}
	}
}

// TestCommunityCacheProjectIsolation saves for "proj-a" and "proj-b",
// then loads "proj-a" and verifies only its partition is returned.
func TestCommunityCacheProjectIsolation(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if err := s.UpsertProject("proj-a", "/tmp/proj-a"); err != nil {
		t.Fatalf("UpsertProject proj-a: %v", err)
	}
	if err := s.UpsertProject("proj-b", "/tmp/proj-b"); err != nil {
		t.Fatalf("UpsertProject proj-b: %v", err)
	}

	partA := map[int64]int{10: 0, 20: 0}
	partB := map[int64]int{30: 1, 40: 1, 50: 2}

	if err := s.SaveCommunityCache(ctx, "proj-a", "h", partA); err != nil {
		t.Fatalf("SaveCommunityCache proj-a: %v", err)
	}
	if err := s.SaveCommunityCache(ctx, "proj-b", "h", partB); err != nil {
		t.Fatalf("SaveCommunityCache proj-b: %v", err)
	}

	got, err := s.LoadCommunityCache(ctx, "proj-a", "h")
	if err != nil {
		t.Fatalf("LoadCommunityCache proj-a: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil partition for proj-a, got nil")
	}
	if len(got) != len(partA) {
		t.Fatalf("proj-a partition size: got %d, want %d", len(got), len(partA))
	}
	for nodeID := range partB {
		if _, ok := got[nodeID]; ok {
			t.Errorf("proj-b node %d leaked into proj-a result", nodeID)
		}
	}
}

// TestCommunityCacheBatchBoundary249 saves exactly 249 nodes (one batch) and
// loads them all back.
func TestCommunityCacheBatchBoundary249(t *testing.T) {
	s := setupCacheStore(t)
	defer s.Close()

	ctx := context.Background()
	const n = 249
	partition := make(map[int64]int, n)
	for i := int64(1); i <= n; i++ {
		partition[i] = int(i % 5)
	}

	if err := s.SaveCommunityCache(ctx, "test", "h249", partition); err != nil {
		t.Fatalf("SaveCommunityCache: %v", err)
	}

	got, err := s.LoadCommunityCache(ctx, "test", "h249")
	if err != nil {
		t.Fatalf("LoadCommunityCache: %v", err)
	}
	if len(got) != n {
		t.Fatalf("expected %d entries, got %d", n, len(got))
	}
	for nodeID, comm := range partition {
		if got[nodeID] != comm {
			t.Errorf("node %d: got %d, want %d", nodeID, got[nodeID], comm)
		}
	}
}

// TestCommunityCacheBatchBoundary250 saves 250 nodes (two batches: 249+1) and
// verifies all 250 are returned.
func TestCommunityCacheBatchBoundary250(t *testing.T) {
	s := setupCacheStore(t)
	defer s.Close()

	ctx := context.Background()
	const n = 250 // cacheBatchSize + 1
	partition := make(map[int64]int, n)
	for i := int64(1); i <= n; i++ {
		partition[i] = int(i % 5)
	}

	if err := s.SaveCommunityCache(ctx, "test", "h250", partition); err != nil {
		t.Fatalf("SaveCommunityCache: %v", err)
	}

	got, err := s.LoadCommunityCache(ctx, "test", "h250")
	if err != nil {
		t.Fatalf("LoadCommunityCache: %v", err)
	}
	if len(got) != n {
		t.Fatalf("expected %d entries, got %d", n, len(got))
	}
	for nodeID, comm := range partition {
		if got[nodeID] != comm {
			t.Errorf("node %d: got %d, want %d", nodeID, got[nodeID], comm)
		}
	}
}

// TestCommunityCacheBatchBoundary498 saves 498 nodes (two full batches) and
// verifies all are returned.
func TestCommunityCacheBatchBoundary498(t *testing.T) {
	s := setupCacheStore(t)
	defer s.Close()

	ctx := context.Background()
	const n = 498 // 2 * cacheBatchSize
	partition := make(map[int64]int, n)
	for i := int64(1); i <= n; i++ {
		partition[i] = int(i % 7)
	}

	if err := s.SaveCommunityCache(ctx, "test", "h498", partition); err != nil {
		t.Fatalf("SaveCommunityCache: %v", err)
	}

	got, err := s.LoadCommunityCache(ctx, "test", "h498")
	if err != nil {
		t.Fatalf("LoadCommunityCache: %v", err)
	}
	if len(got) != n {
		t.Fatalf("expected %d entries, got %d", n, len(got))
	}
	for nodeID, comm := range partition {
		if got[nodeID] != comm {
			t.Errorf("node %d: got %d, want %d", nodeID, got[nodeID], comm)
		}
	}
}

// TestCommunityCacheEmptyPartition saves an empty map and expects nil back
// (0 rows == cache miss sentinel).
func TestCommunityCacheEmptyPartition(t *testing.T) {
	s := setupCacheStore(t)
	defer s.Close()

	ctx := context.Background()

	if err := s.SaveCommunityCache(ctx, "test", "hEmpty", map[int64]int{}); err != nil {
		t.Fatalf("SaveCommunityCache: %v", err)
	}

	got, err := s.LoadCommunityCache(ctx, "test", "hEmpty")
	if err != nil {
		t.Fatalf("LoadCommunityCache: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty partition, got %v", got)
	}
}

// TestCommunityCacheIndex verifies idx_community_cache_project is present on
// the community_cache table.
func TestCommunityCacheIndex(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	rows, err := s.DB().QueryContext(ctx, `SELECT name FROM pragma_index_list('community_cache')`)
	if err != nil {
		t.Fatalf("pragma_index_list: %v", err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name == "idx_community_cache_project" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if !found {
		t.Error("expected index idx_community_cache_project to exist on community_cache table")
	}
}

// TestCommunityCacheTableExists verifies the community_cache table was created
// by initSchema (SELECT 1 must not fail with "no such table").
func TestCommunityCacheTableExists(t *testing.T) {
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	_, err = s.DB().QueryContext(ctx, `SELECT 1 FROM community_cache LIMIT 1`)
	if err != nil {
		t.Fatalf("community_cache table missing or inaccessible: %v", err)
	}
}

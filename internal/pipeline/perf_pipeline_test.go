package pipeline

import (
	"context"
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/discover"
	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// newTestPipeline creates a Pipeline backed by an in-memory store.
// The project row is inserted so FK constraints on nodes/edges are satisfied.
func newTestPipeline(t *testing.T) (*Pipeline, *store.Store) {
	t.Helper()
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatalf("store.OpenMemory: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	dir := t.TempDir()
	p := New(context.Background(), s, dir, discover.ModeFull)

	if err := s.UpsertProject(p.ProjectName, dir); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	return p, s
}

// upsertTestNode inserts a Function node for the given file path and returns its ID.
func upsertTestNode(t *testing.T, s *store.Store, project, name, qn, filePath string) int64 {
	t.Helper()
	id, err := s.UpsertNode(&store.Node{
		Project:       project,
		Label:         "Function",
		Name:          name,
		QualifiedName: qn,
		FilePath:      filePath,
		StartLine:     1,
		EndLine:       10,
	})
	if err != nil {
		t.Fatalf("UpsertNode(%s): %v", qn, err)
	}
	return id
}

// TestChangedFilesAffectCommunities_Empty verifies that an empty changed-file
// list returns false (no changes → communities unaffected).
func TestChangedFilesAffectCommunities_Empty(t *testing.T) {
	p, _ := newTestPipeline(t)

	got := p.changedFilesAffectCommunities(nil)
	if got {
		t.Error("expected false for empty changed files, got true")
	}

	got = p.changedFilesAffectCommunities([]discover.FileInfo{})
	if got {
		t.Error("expected false for empty slice, got true")
	}
}

// TestChangedFilesAffectCommunities_LeafOnly verifies that a file containing
// only a leaf node (no outbound CALLS edges) returns false.
func TestChangedFilesAffectCommunities_LeafOnly(t *testing.T) {
	p, s := newTestPipeline(t)

	const filePath = "leaf.go"
	upsertTestNode(t, s, p.ProjectName, "LeafFunc", p.ProjectName+".LeafFunc", filePath)

	changed := []discover.FileInfo{
		{Path: "/repo/" + filePath, RelPath: filePath},
	}
	got := p.changedFilesAffectCommunities(changed)
	if got {
		t.Error("expected false for leaf-only file (no CALLS edges), got true")
	}
}

// TestChangedFilesAffectCommunities_WithCalls verifies that a file containing a
// node with at least one outbound CALLS edge returns true.
func TestChangedFilesAffectCommunities_WithCalls(t *testing.T) {
	p, s := newTestPipeline(t)

	const srcFile = "caller.go"
	const dstFile = "callee.go"

	callerID := upsertTestNode(t, s, p.ProjectName, "Caller", p.ProjectName+".Caller", srcFile)
	calleeID := upsertTestNode(t, s, p.ProjectName, "Callee", p.ProjectName+".Callee", dstFile)

	_, err := s.InsertEdge(&store.Edge{
		Project:  p.ProjectName,
		SourceID: callerID,
		TargetID: calleeID,
		Type:     "CALLS",
	})
	if err != nil {
		t.Fatalf("InsertEdge: %v", err)
	}

	changed := []discover.FileInfo{
		{Path: "/repo/" + srcFile, RelPath: srcFile},
	}
	got := p.changedFilesAffectCommunities(changed)
	if !got {
		t.Error("expected true for file with outbound CALLS edge, got false")
	}
}

// TestChangedFilesAffectCommunities_Conservative_NoNodes verifies that a
// changed file that has no nodes in the store returns true (conservative path).
func TestChangedFilesAffectCommunities_Conservative_NoNodes(t *testing.T) {
	p, _ := newTestPipeline(t)

	// No nodes inserted — file is unknown to the store.
	changed := []discover.FileInfo{
		{Path: "/repo/ghost.go", RelPath: "ghost.go"},
	}
	got := p.changedFilesAffectCommunities(changed)
	if !got {
		t.Error("expected true (conservative) when changed file has no nodes in store, got false")
	}
}

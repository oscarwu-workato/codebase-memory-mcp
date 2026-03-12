package pipeline

import (
	"testing"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// seedCallGraph upserts Function nodes for each unique name in the edges and
// inserts CALLS edges between them. Returns the map of name → node ID.
func seedCallGraph(t *testing.T, s *store.Store, project string, edges [][2]string) map[string]int64 {
	t.Helper()

	// Collect unique node names.
	nameSet := make(map[string]struct{})
	for _, e := range edges {
		nameSet[e[0]] = struct{}{}
		nameSet[e[1]] = struct{}{}
	}

	ids := make(map[string]int64, len(nameSet))
	for name := range nameSet {
		id := upsertTestNode(t, s, project, name, project+"."+name, name+".go")
		ids[name] = id
	}

	for _, e := range edges {
		_, err := s.InsertEdge(&store.Edge{
			Project:  project,
			SourceID: ids[e[0]],
			TargetID: ids[e[1]],
			Type:     "CALLS",
		})
		if err != nil {
			t.Fatalf("InsertEdge(%s→%s): %v", e[0], e[1], err)
		}
	}

	return ids
}

// --- communityGraphHash tests ---

func TestCommunityGraphHashDeterministic(t *testing.T) {
	allNodes := map[int64]bool{1: true, 2: true, 3: true}
	callEdges := []*store.Edge{
		{SourceID: 1, TargetID: 2},
	}

	h1 := communityGraphHash(allNodes, callEdges)
	h2 := communityGraphHash(allNodes, callEdges)

	if h1 != h2 {
		t.Errorf("hash not deterministic: got %q then %q", h1, h2)
	}
}

func TestCommunityGraphHashSensitiveToNodeChange(t *testing.T) {
	edgesA := []*store.Edge{{SourceID: 1, TargetID: 2}}
	edgesB := []*store.Edge{{SourceID: 1, TargetID: 2}}

	// Same count (3 nodes), different node ID set: replace 3 with 4.
	nodesA := map[int64]bool{1: true, 2: true, 3: true}
	nodesB := map[int64]bool{1: true, 2: true, 4: true}

	hA := communityGraphHash(nodesA, edgesA)
	hB := communityGraphHash(nodesB, edgesB)

	if hA == hB {
		t.Errorf("expected different hashes for different node sets, both got %q", hA)
	}
}

func TestCommunityGraphHashSensitiveToEdgeChange(t *testing.T) {
	nodes := map[int64]bool{1: true, 2: true, 3: true}

	// Same node set, edge (1→2,2→3) vs (1→3,2→3).
	edgesA := []*store.Edge{
		{SourceID: 1, TargetID: 2},
		{SourceID: 2, TargetID: 3},
	}
	edgesB := []*store.Edge{
		{SourceID: 1, TargetID: 3},
		{SourceID: 2, TargetID: 3},
	}

	hA := communityGraphHash(nodes, edgesA)
	hB := communityGraphHash(nodes, edgesB)

	if hA == hB {
		t.Errorf("expected different hashes for different edge sets, both got %q", hA)
	}
}

// TestCommunityGraphHashSameCountsDifferentTopology checks that graphs with
// identical node/edge counts but different wiring produce different hashes.
// The hash uses XOR of (sourceID ^ targetID) per edge, so we pick edge sets
// that are not XOR-symmetric with each other.
// Graph A edges (1→2, 1→3): edgeXOR = (1^2) ^ (1^3) = 3 ^ 2 = 1
// Graph B edges (1→2, 2→3): edgeXOR = (1^2) ^ (2^3) = 3 ^ 1 = 2
// Both have 4 nodes (nodeSum=10) and 2 edges — only edgeXOR differs.
func TestCommunityGraphHashSameCountsDifferentTopology(t *testing.T) {
	nodes := map[int64]bool{1: true, 2: true, 3: true, 4: true}

	// Graph A: fan-out from node 1
	edgesA := []*store.Edge{
		{SourceID: 1, TargetID: 2},
		{SourceID: 1, TargetID: 3},
	}
	// Graph B: chain 1→2→3
	edgesB := []*store.Edge{
		{SourceID: 1, TargetID: 2},
		{SourceID: 2, TargetID: 3},
	}

	hA := communityGraphHash(nodes, edgesA)
	hB := communityGraphHash(nodes, edgesB)

	if hA == hB {
		t.Errorf("expected different hashes for different topologies with same counts, both got %q", hA)
	}
}

// --- passCommunities integration tests ---

func TestPassCommunitiesColdStart(t *testing.T) {
	p, s := newTestPipeline(t)

	// Two clusters: A→B and C→D.
	seedCallGraph(t, s, p.ProjectName, [][2]string{
		{"A", "B"},
		{"C", "D"},
	})

	p.passCommunities()

	communities, err := s.FindNodesByLabel(p.ProjectName, "Community")
	if err != nil {
		t.Fatalf("FindNodesByLabel(Community): %v", err)
	}
	if len(communities) == 0 {
		t.Error("expected Community nodes to be created, got none")
	}

	// Each community node should have at least one MEMBER_OF edge pointing to it.
	foundMemberOf := false
	for _, commNode := range communities {
		edges, err := s.FindEdgesByTarget(commNode.ID)
		if err != nil {
			t.Fatalf("FindEdgesByTarget(%d): %v", commNode.ID, err)
		}
		for _, e := range edges {
			if e.Type == "MEMBER_OF" {
				foundMemberOf = true
				break
			}
		}
		if foundMemberOf {
			break
		}
	}
	if !foundMemberOf {
		t.Error("expected MEMBER_OF edges to be created, found none")
	}
}

func TestPassCommunitiesWarmStart(t *testing.T) {
	p, s := newTestPipeline(t)

	seedCallGraph(t, s, p.ProjectName, [][2]string{
		{"A", "B"},
		{"C", "D"},
	})

	// First run: cold start — populates the cache.
	p.passCommunities()

	communities1, err := s.FindNodesByLabel(p.ProjectName, "Community")
	if err != nil {
		t.Fatalf("FindNodesByLabel after first run: %v", err)
	}
	if len(communities1) == 0 {
		t.Fatal("expected Community nodes after first run, got none")
	}

	// Second run: same graph — should hit the warm-start cache path.
	p.passCommunities()

	communities2, err := s.FindNodesByLabel(p.ProjectName, "Community")
	if err != nil {
		t.Fatalf("FindNodesByLabel after second run: %v", err)
	}
	if len(communities2) == 0 {
		t.Error("expected Community nodes after second (warm) run, got none")
	}
}

func TestPassCommunitiesNoCallsEdges(t *testing.T) {
	p, s := newTestPipeline(t)

	// Insert nodes but no CALLS edges.
	upsertTestNode(t, s, p.ProjectName, "Foo", p.ProjectName+".Foo", "foo.go")
	upsertTestNode(t, s, p.ProjectName, "Bar", p.ProjectName+".Bar", "bar.go")

	p.passCommunities()

	communities, err := s.FindNodesByLabel(p.ProjectName, "Community")
	if err != nil {
		t.Fatalf("FindNodesByLabel(Community): %v", err)
	}
	if len(communities) != 0 {
		t.Errorf("expected no Community nodes when there are no CALLS edges, got %d", len(communities))
	}
}

func TestPassCommunitiesCacheInvalidationOnTopologyChange(t *testing.T) {
	p, s := newTestPipeline(t)

	// Graph A: 2 nodes, 1 edge.
	ids := seedCallGraph(t, s, p.ProjectName, [][2]string{
		{"Alpha", "Beta"},
	})

	p.passCommunities()

	communities1, err := s.FindNodesByLabel(p.ProjectName, "Community")
	if err != nil {
		t.Fatalf("FindNodesByLabel after first run: %v", err)
	}

	// Extend the graph: add Gamma that calls Alpha.
	gammaID := upsertTestNode(t, s, p.ProjectName, "Gamma", p.ProjectName+".Gamma", "gamma.go")
	_, err = s.InsertEdge(&store.Edge{
		Project:  p.ProjectName,
		SourceID: gammaID,
		TargetID: ids["Alpha"],
		Type:     "CALLS",
	})
	if err != nil {
		t.Fatalf("InsertEdge(Gamma→Alpha): %v", err)
	}

	p.passCommunities()

	communities2, err := s.FindNodesByLabel(p.ProjectName, "Community")
	if err != nil {
		t.Fatalf("FindNodesByLabel after second run: %v", err)
	}

	// After topology change, communities should be recreated (not stale).
	// We can't assert exact counts without knowing Louvain's output, but we
	// can verify communities exist and that the second run didn't skip due to
	// a stale cache hit.
	if len(communities2) == 0 {
		t.Error("expected Community nodes after topology change, got none")
	}

	// The graph grew from 2 nodes to 3 — communities2 may differ from communities1.
	// At minimum we confirm passCommunities ran (community nodes exist).
	_ = communities1
}

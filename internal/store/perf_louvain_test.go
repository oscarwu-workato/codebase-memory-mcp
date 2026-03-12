package store

import (
	"testing"
)

// addTestEdge adds an undirected weighted edge to the adjacency / weight slices
// used by louvainLocalMoving tests.
func addTestEdge(adj [][]int, weight [][]float64, a, b int, w float64) {
	adj[a] = append(adj[a], b)
	weight[a] = append(weight[a], w)
	adj[b] = append(adj[b], a)
	weight[b] = append(weight[b], w)
}

// --- louvainLocalMoving tests ---

// TestLouvainLocalMovingNoMoves verifies that when two fully-connected clusters
// are already in their optimal communities, no node moves occur.
func TestLouvainLocalMovingNoMoves(t *testing.T) {
	// Two fully-connected triangles already in distinct communities.
	// Cluster A: nodes 0,1,2 in community 0.
	// Cluster B: nodes 3,4,5 in community 1.
	// No edges between clusters — no reason to move.
	n := 6
	adj := make([][]int, n)
	weight := make([][]float64, n)

	// Cluster A: 0-1, 1-2, 0-2
	addTestEdge(adj, weight, 0, 1, 1)
	addTestEdge(adj, weight, 1, 2, 1)
	addTestEdge(adj, weight, 0, 2, 1)

	// Cluster B: 3-4, 4-5, 3-5
	addTestEdge(adj, weight, 3, 4, 1)
	addTestEdge(adj, weight, 4, 5, 1)
	addTestEdge(adj, weight, 3, 5, 1)

	totalWeight := 6.0 // 6 edges total (undirected, each counted once in totalWeight)

	community := []int{0, 0, 0, 1, 1, 1}

	degree := make([]float64, n)
	for i := 0; i < n; i++ {
		for _, w := range weight[i] {
			degree[i] += w
		}
	}

	changed, improved := louvainLocalMoving(n, adj, weight, degree, community, totalWeight, 1.0)

	if changed != 0 {
		t.Errorf("expected 0 moves, got %d", changed)
	}
	if improved {
		t.Error("expected improved=false, got true")
	}
}

// TestLouvainLocalMovingWithMoves verifies that when all nodes start in the same
// community but form two dense clusters, at least one node moves.
func TestLouvainLocalMovingWithMoves(t *testing.T) {
	// Two dense clusters with a weak bridge — all nodes start in community 0.
	// Cluster A: 0-1, 1-2, 0-2 (fully connected).
	// Cluster B: 3-4, 4-5, 3-5 (fully connected).
	// Bridge: 2-3 (weak).
	n := 6
	adj := make([][]int, n)
	weight := make([][]float64, n)

	addTestEdge(adj, weight, 0, 1, 1)
	addTestEdge(adj, weight, 1, 2, 1)
	addTestEdge(adj, weight, 0, 2, 1)
	addTestEdge(adj, weight, 3, 4, 1)
	addTestEdge(adj, weight, 4, 5, 1)
	addTestEdge(adj, weight, 3, 5, 1)
	addTestEdge(adj, weight, 2, 3, 0.1) // weak bridge

	totalWeight := 6.1

	// Start each node in its own community — Louvain local-moving only moves nodes
	// into existing neighbouring communities, so a single starting community
	// would have nowhere to move to. The canonical Louvain initialisation is
	// one community per node.
	community := make([]int, n)
	for i := range community {
		community[i] = i
	}

	degree := make([]float64, n)
	for i := 0; i < n; i++ {
		for _, w := range weight[i] {
			degree[i] += w
		}
	}

	changed, improved := louvainLocalMoving(n, adj, weight, degree, community, totalWeight, 1.0)

	if changed == 0 {
		t.Error("expected at least one node to move, got 0 moves")
	}
	if !improved {
		t.Error("expected improved=true")
	}
}

// --- louvain tests ---

// TestLouvainEarlyExitOnConvergence verifies louvain terminates and returns
// correct partitions on a two-cluster graph.
func TestLouvainEarlyExitOnConvergence(t *testing.T) {
	// Build two fully-connected clusters of 5 nodes each with a single bridge.
	var nodes []int64
	var edges []louvainEdge

	for i := int64(1); i <= 5; i++ {
		nodes = append(nodes, i)
		for j := i + 1; j <= 5; j++ {
			edges = append(edges, louvainEdge{src: i, dst: j})
		}
	}
	for i := int64(6); i <= 10; i++ {
		nodes = append(nodes, i)
		for j := i + 1; j <= 10; j++ {
			edges = append(edges, louvainEdge{src: i, dst: j})
		}
	}
	// Single bridge.
	edges = append(edges, louvainEdge{src: 5, dst: 6})

	partition := louvain(nodes, edges)

	if len(partition) != 10 {
		t.Fatalf("expected 10 entries in partition, got %d", len(partition))
	}

	// Nodes 1-5 should be in the same community.
	comm1 := partition[1]
	for i := int64(2); i <= 5; i++ {
		if partition[i] != comm1 {
			t.Errorf("node %d not in same community as node 1: got %d, want %d", i, partition[i], comm1)
		}
	}

	// Nodes 6-10 should be in the same community.
	comm2 := partition[6]
	for i := int64(7); i <= 10; i++ {
		if partition[i] != comm2 {
			t.Errorf("node %d not in same community as node 6: got %d, want %d", i, partition[i], comm2)
		}
	}

	// The two clusters should be in different communities.
	if comm1 == comm2 {
		t.Errorf("clusters should be in different communities, both in %d", comm1)
	}
}

// TestLouvainCompleteGraphTerminates verifies that louvain terminates on a
// fully-connected graph and returns a valid partition. (Testing the fraction
// threshold itself requires n > 1000; this test verifies absence of infinite loops.)
func TestLouvainCompleteGraphTerminates(t *testing.T) {
	// Build a 20-node fully-connected graph.
	nodeCount := 20
	var nodes []int64
	var edges []louvainEdge

	for i := int64(1); i <= int64(nodeCount); i++ {
		nodes = append(nodes, i)
	}
	for i := int64(1); i <= int64(nodeCount); i++ {
		for j := i + 1; j <= int64(nodeCount); j++ {
			edges = append(edges, louvainEdge{src: i, dst: j})
		}
	}

	// Just verify it terminates (no infinite loop) and returns a valid map.
	partition := louvain(nodes, edges)
	if len(partition) != nodeCount {
		t.Fatalf("expected %d entries, got %d", nodeCount, len(partition))
	}
}

// TestLouvainSingleNodeGraph verifies louvain handles a single node with no edges.
func TestLouvainSingleNodeGraph(t *testing.T) {
	partition := louvain([]int64{42}, nil)
	if len(partition) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(partition))
	}
	if _, ok := partition[42]; !ok {
		t.Error("expected node 42 in partition")
	}
}

// --- louvainWithWarmStart tests ---

// twoClusterGraph returns a 4-node graph with two clear clusters:
// nodes 1,2 form cluster A (connected) and nodes 3,4 form cluster B (connected),
// with no edges between clusters.
func twoClusterGraph() ([]int64, []louvainEdge) {
	nodes := []int64{1, 2, 3, 4}
	edges := []louvainEdge{
		{src: 1, dst: 2},
		{src: 3, dst: 4},
	}
	return nodes, edges
}

// TestLouvainWithWarmStartNil verifies that louvainWithWarmStart(nodes, edges, nil)
// and louvain(nodes, edges) both produce valid full partitions.
func TestLouvainWithWarmStartNil(t *testing.T) {
	nodes, edges := twoClusterGraph()

	p1 := louvainWithWarmStart(nodes, edges, nil)
	p2 := louvain(nodes, edges)

	for _, p := range []map[int64]int{p1, p2} {
		if len(p) != len(nodes) {
			t.Fatalf("expected %d entries, got %d", len(nodes), len(p))
		}
		for _, id := range nodes {
			if _, ok := p[id]; !ok {
				t.Errorf("node %d missing from partition", id)
			}
		}
	}
}

// TestLouvainWithWarmStartPerfect runs louvain on a 2-cluster graph to get
// partition P, then calls louvainWithWarmStart with P. The result must still
// group each original cluster together.
func TestLouvainWithWarmStartPerfect(t *testing.T) {
	nodes := []int64{1, 2, 3, 4, 5, 6}
	edges := []louvainEdge{
		{src: 1, dst: 2}, {src: 2, dst: 3}, {src: 1, dst: 3},
		{src: 4, dst: 5}, {src: 5, dst: 6}, {src: 4, dst: 6},
	}

	warmStart := louvain(nodes, edges)
	if len(warmStart) != len(nodes) {
		t.Fatalf("initial partition: expected %d entries, got %d", len(nodes), len(warmStart))
	}

	result := louvainWithWarmStart(nodes, edges, warmStart)
	if len(result) != len(nodes) {
		t.Fatalf("warm-start partition: expected %d entries, got %d", len(nodes), len(result))
	}

	// Cluster A (nodes 1,2,3) must be in the same community.
	commA := result[1]
	for _, id := range []int64{2, 3} {
		if result[id] != commA {
			t.Errorf("cluster A: node %d in community %d, want %d", id, result[id], commA)
		}
	}

	// Cluster B (nodes 4,5,6) must be in the same community.
	commB := result[4]
	for _, id := range []int64{5, 6} {
		if result[id] != commB {
			t.Errorf("cluster B: node %d in community %d, want %d", id, result[id], commB)
		}
	}

	// The two clusters must be in different communities.
	if commA == commB {
		t.Errorf("expected two distinct communities, both clusters in community %d", commA)
	}
}

// TestLouvainWithWarmStartNewNode runs louvain on 4 nodes, then adds a 5th node
// not present in the warm-start partition. The 5th node must appear in the result.
func TestLouvainWithWarmStartNewNode(t *testing.T) {
	nodes4 := []int64{1, 2, 3, 4}
	edges4 := []louvainEdge{
		{src: 1, dst: 2},
		{src: 3, dst: 4},
	}

	warmStart := louvain(nodes4, edges4)

	// Extend to 5 nodes; node 5 connects to cluster B.
	nodes5 := []int64{1, 2, 3, 4, 5}
	edges5 := append(edges4, louvainEdge{src: 4, dst: 5})

	result := louvainWithWarmStart(nodes5, edges5, warmStart)
	if len(result) != len(nodes5) {
		t.Fatalf("expected %d entries, got %d", len(nodes5), len(result))
	}
	if _, ok := result[5]; !ok {
		t.Error("node 5 (new node) missing from warm-start result")
	}
}

// TestLouvainWithWarmStartCompactIDs verifies that when warmStart has sparse
// community IDs (100, 200, 300), the algorithm internally compacts them and
// returns a valid partition without panics.
func TestLouvainWithWarmStartCompactIDs(t *testing.T) {
	nodes := []int64{10, 20, 30, 40, 50, 60}
	edges := []louvainEdge{
		{src: 10, dst: 20}, {src: 20, dst: 30}, {src: 10, dst: 30},
		{src: 40, dst: 50}, {src: 50, dst: 60}, {src: 40, dst: 60},
	}

	// Sparse community IDs — not sequential from 0.
	warmStart := map[int64]int{
		10: 100, 20: 100, 30: 100,
		40: 200, 50: 200, 60: 200,
	}

	result := louvainWithWarmStart(nodes, edges, warmStart)
	if len(result) != len(nodes) {
		t.Fatalf("expected %d entries, got %d", len(nodes), len(result))
	}
	for _, id := range nodes {
		if _, ok := result[id]; !ok {
			t.Errorf("node %d missing from result", id)
		}
	}
}

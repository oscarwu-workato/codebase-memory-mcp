package store

import (
	"log/slog"
	"math/rand" // WHY: graph algorithm randomness — not cryptographic use, math/rand is correct here
)

// LouvainEdge is an exported edge type for cross-package use of the Louvain algorithm.
type LouvainEdge struct {
	Src int64
	Dst int64
}

// RunLouvain runs community detection with optional warm-start and GPU acceleration.
// If CBM_GPU=1 is set, attempts to offload to the cuGraph GPU sidecar first.
// Falls back silently to the pure-Go implementation on any GPU failure.
func RunLouvain(nodes []int64, edges []LouvainEdge, warmStart map[int64]int) map[int64]int {
	if isGPUEnabled() && len(nodes) >= 3 {
		if partition, ok := tryGPULouvain(nodes, edges); ok {
			slog.Info("louvain.gpu.ok", "nodes", len(nodes), "edges", len(edges), "communities", communityCount(partition))
			return partition
		}
		slog.Warn("louvain.gpu.fallback", "nodes", len(nodes), "edges", len(edges))
	}
	// Convert LouvainEdge → internal louvainEdge
	internal := make([]louvainEdge, len(edges))
	for i, e := range edges {
		internal[i] = louvainEdge{src: e.Src, dst: e.Dst}
	}
	return louvainWithWarmStart(nodes, internal, warmStart)
}

// communityCount returns the number of distinct communities in a partition.
func communityCount(partition map[int64]int) int {
	seen := map[int]bool{}
	for _, c := range partition {
		seen[c] = true
	}
	return len(seen)
}

// LouvainEdge is an exported edge type for cross-package use of the Louvain algorithm.
type LouvainEdge struct {
	Src int64
	Dst int64
}

// RunLouvain runs community detection with optional warm-start.
// GPU acceleration (CBM_GPU=1) is wired in the full implementation;
// this consolidation commit adds the cross-package interface only.
func RunLouvain(nodes []int64, edges []LouvainEdge, warmStart map[int64]int) map[int64]int {
	internal := make([]louvainEdge, len(edges))
	for i, e := range edges {
		internal[i] = louvainEdge{src: e.Src, dst: e.Dst}
	}
	return louvainWithWarmStart(nodes, internal, warmStart)
}

// louvainEdge represents an edge for the Louvain algorithm.
type louvainEdge struct {
	src      int64
	dst      int64
	edgeType string // for post-processing only
}

// louvain implements community detection using the Louvain algorithm.
// Input: node IDs + edges (treated as undirected).
// Output: map[nodeID] → communityID.
func louvain(nodes []int64, edges []louvainEdge) map[int64]int {
	return louvainWithWarmStart(nodes, edges, nil)
}

// louvainGraph holds the compact adjacency representation for the Louvain algorithm.
type louvainGraph struct {
	n           int
	adj         [][]int
	weight      [][]float64
	totalWeight float64
	degree      []float64
}

// buildLouvainGraph creates a compact adjacency representation from nodes and edges.
// Returns the graph, the nodeID-to-index mapping, and whether any edges exist.
func buildLouvainGraph(nodes []int64, edges []louvainEdge) (louvainGraph, map[int64]int) {
	n := len(nodes)
	idxOf := make(map[int64]int, n)
	for i, id := range nodes {
		idxOf[id] = i
	}

	edgeWeight := map[[2]int]float64{}
	for _, e := range edges {
		si, ok1 := idxOf[e.src]
		di, ok2 := idxOf[e.dst]
		if !ok1 || !ok2 || si == di {
			continue
		}
		key := [2]int{si, di}
		if si > di {
			key = [2]int{di, si}
		}
		edgeWeight[key]++
	}

	g := louvainGraph{n: n, adj: make([][]int, n), weight: make([][]float64, n)}
	for key, w := range edgeWeight {
		si, di := key[0], key[1]
		g.adj[si] = append(g.adj[si], di)
		g.weight[si] = append(g.weight[si], w)
		g.adj[di] = append(g.adj[di], si)
		g.weight[di] = append(g.weight[di], w)
		g.totalWeight += w
	}

	g.degree = make([]float64, n)
	for i := 0; i < n; i++ {
		for _, w := range g.weight[i] {
			g.degree[i] += w
		}
	}

	return g, idxOf
}

// initCommunities assigns initial community IDs, optionally seeded from a
// warm-start partition. External IDs are remapped to dense internal indices.
func initCommunities(nodes []int64, warmStart map[int64]int) []int {
	community := make([]int, len(nodes))
	for i := range community {
		community[i] = i
	}
	if warmStart == nil {
		return community
	}

	commIDs := make(map[int]int) // external community -> internal community
	nextComm := 0
	for i, id := range nodes {
		extComm, ok := warmStart[id]
		if !ok {
			continue
		}
		if internalComm, seen := commIDs[extComm]; seen {
			community[i] = internalComm
		} else {
			commIDs[extComm] = nextComm
			community[i] = nextComm
			nextComm++
		}
	}
	return community
}

// louvainWithWarmStart implements community detection using the Louvain algorithm
// with an optional warm-start partition to accelerate convergence.
// When warmStart is non-nil and graph fingerprints match, the algorithm starts from
// the prior partition instead of the singleton assignment, reducing iterations from
// 10-15 down to 1-3 for small incremental changes.
// Input: node IDs + edges (treated as undirected), optional prior partition map.
// Output: map[nodeID] -> communityID.
func louvainWithWarmStart(nodes []int64, edges []louvainEdge, warmStart map[int64]int) map[int64]int {
	const resolution = 1.0
	if len(nodes) == 0 {
		return map[int64]int{}
	}

	g, _ := buildLouvainGraph(nodes, edges)

	if g.totalWeight == 0 {
		result := make(map[int64]int, g.n)
		for i, id := range nodes {
			result[id] = i
		}
		return result
	}

	community := initCommunities(nodes, warmStart)

	maxIter := 15
	for iter := 0; iter < maxIter; iter++ {
		changed, improved := louvainLocalMoving(g.n, g.adj, g.weight, g.degree, community, g.totalWeight, resolution)
		louvainRefine(g.n, g.adj, g.weight, g.degree, community, g.totalWeight, resolution)

		if !improved || (g.n > 0 && float64(changed)/float64(g.n) < 0.001) {
			break
		}
	}

	result := make(map[int64]int, g.n)
	for i, id := range nodes {
		result[id] = community[i]
	}
	return result
}

// louvainLocalMoving greedily moves each node to the community that maximizes modularity gain.
// Returns the count of moved nodes and whether any node was moved.
func louvainLocalMoving(n int, adj [][]int, weight [][]float64, degree []float64, community []int, totalWeight, resolution float64) (int, bool) {
	changed := 0

	// Community total degree
	commDegree := make(map[int]float64)
	for i := 0; i < n; i++ {
		commDegree[community[i]] += degree[i]
	}

	// Random order for convergence stability
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	rand.Shuffle(n, func(i, j int) { order[i], order[j] = order[j], order[i] })

	for _, i := range order {
		curComm := community[i]

		// Compute weights to each neighboring community
		neighborComm := map[int]float64{}
		for j, neighbor := range adj[i] {
			nc := community[neighbor]
			neighborComm[nc] += weight[i][j]
		}

		// Remove node from its community
		commDegree[curComm] -= degree[i]

		bestComm := curComm
		bestGain := 0.0

		for comm, wIn := range neighborComm {
			// Modularity gain formula
			gain := wIn - resolution*degree[i]*commDegree[comm]/(2*totalWeight)
			if gain > bestGain {
				bestGain = gain
				bestComm = comm
			}
		}

		// Also consider staying in current community
		wInCur := neighborComm[curComm]
		curGain := wInCur - resolution*degree[i]*commDegree[curComm]/(2*totalWeight)
		if curGain >= bestGain {
			bestComm = curComm
		}

		community[i] = bestComm
		commDegree[bestComm] += degree[i]

		if bestComm != curComm {
			changed++
		}
	}

	return changed, changed > 0
}

// louvainRefine checks each community for well-connectedness and potentially splits
// poorly-connected ones.
func louvainRefine(n int, adj [][]int, weight [][]float64, degree []float64, community []int, totalWeight, resolution float64) {
	commMembers := map[int][]int{}
	for i := 0; i < n; i++ {
		commMembers[community[i]] = append(commMembers[community[i]], i)
	}
	for _, members := range commMembers {
		if len(members) <= 2 {
			continue
		}
		refineCommunity(members, adj, weight, degree, community, n, totalWeight, resolution)
	}
}

// refineCommunity checks one community for well-connectedness and splits if density is too low.
func refineCommunity(members []int, adj [][]int, weight [][]float64, degree []float64, community []int, n int, totalWeight, resolution float64) {
	memberSet := map[int]bool{}
	for _, m := range members {
		memberSet[m] = true
	}

	var internalWeight float64
	for _, m := range members {
		for j, neighbor := range adj[m] {
			if memberSet[neighbor] {
				internalWeight += weight[m][j]
			}
		}
	}
	internalWeight /= 2 // each edge counted twice

	maxInternal := float64(len(members)*(len(members)-1)) / 2
	if maxInternal == 0 {
		return
	}
	density := internalWeight / maxInternal
	if density >= 0.01 || len(members) <= 5 {
		return
	}

	nextComm := maxCommunity(community, n) + 1
	for _, m := range members {
		var wInternal float64
		for j, neighbor := range adj[m] {
			if memberSet[neighbor] {
				wInternal += weight[m][j]
			}
		}
		expectedInternal := resolution * degree[m] * (internalWeight * 2 / totalWeight)
		if wInternal < expectedInternal*0.5 {
			community[m] = nextComm
			nextComm++
		}
	}
}

func maxCommunity(community []int, n int) int {
	maxVal := 0
	for i := 0; i < n; i++ {
		if community[i] > maxVal {
			maxVal = community[i]
		}
	}
	return maxVal
}

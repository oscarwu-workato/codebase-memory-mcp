package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"

	"github.com/DeusData/codebase-memory-mcp/internal/store"
)

// communityGraphHash returns a fingerprint of the community graph state.
// Includes node count, edge count, and a checksum of node/edge IDs to detect
// topology changes that preserve counts (e.g., one function deleted, another added).
func communityGraphHash(allNodes map[int64]bool, callEdges []*store.Edge) string {
	var nodeSum, edgeXOR uint64
	for id := range allNodes {
		nodeSum += uint64(id)
	}
	for _, e := range callEdges {
		edgeXOR ^= uint64(e.SourceID) ^ uint64(e.TargetID)
	}
	return fmt.Sprintf("%d:%d:%x:%x", len(allNodes), len(callEdges), nodeSum, edgeXOR)
}

// passCommunities runs Louvain community detection on the CALLS graph
// and creates Community nodes + MEMBER_OF edges.
func (p *Pipeline) passCommunities() {
	slog.Info("pass.communities")

	// Load CALLS edges
	callEdges, err := p.Store.FindEdgesByType(p.ProjectName, "CALLS")
	if err != nil || len(callEdges) == 0 {
		slog.Info("pass.communities.skip", "reason", "no_calls")
		return
	}

	// Build adjacency list (undirected for community detection)
	adj := make(map[int64]map[int64]bool)
	allNodes := make(map[int64]bool)
	for _, e := range callEdges {
		allNodes[e.SourceID] = true
		allNodes[e.TargetID] = true
		if adj[e.SourceID] == nil {
			adj[e.SourceID] = make(map[int64]bool)
		}
		if adj[e.TargetID] == nil {
			adj[e.TargetID] = make(map[int64]bool)
		}
		adj[e.SourceID][e.TargetID] = true
		adj[e.TargetID][e.SourceID] = true
	}

	// Load warm-start partition from cache if graph fingerprint matches.
	ctx := context.Background()
	graphHash := communityGraphHash(allNodes, callEdges)
	warmStart, err := p.Store.LoadCommunityCache(ctx, p.ProjectName, graphHash)
	if err != nil {
		slog.Warn("pass.communities.cache.load.err", "err", err)
		warmStart = nil
	}
	slog.Info("pass.communities.cache", "hit", warmStart != nil, "hash", graphHash)

	// Run Louvain community detection
	communities, nodeCommunity := louvainCommunities(adj, allNodes, warmStart)

	// Persist the partition for warm-starting future runs.
	if saveErr := p.Store.SaveCommunityCache(ctx, p.ProjectName, graphHash, nodeCommunity); saveErr != nil {
		slog.Warn("pass.communities.cache.save.err", "err", saveErr)
	}

	// Create Community nodes + MEMBER_OF edges
	communityCount, memberOfCount := p.storeCommunities(communities)
	slog.Info("pass.communities.done", "communities", communityCount, "member_of", memberOfCount)
}

// initNodeCommunities assigns initial community IDs to nodes, optionally
// seeded from a warm-start partition. New nodes not in warmStart get fresh IDs.
func initNodeCommunities(allNodes map[int64]bool, warmStart map[int64]int) map[int64]int {
	nodeCommunity := make(map[int64]int, len(allNodes))
	if warmStart == nil {
		commID := 0
		for nodeID := range allNodes {
			nodeCommunity[nodeID] = commID
			commID++
		}
		return nodeCommunity
	}

	nextComm := 0
	for _, c := range warmStart {
		if c >= nextComm {
			nextComm = c + 1
		}
	}
	for nodeID := range allNodes {
		if c, ok := warmStart[nodeID]; ok {
			nodeCommunity[nodeID] = c
		} else {
			nodeCommunity[nodeID] = nextComm
			nextComm++
		}
	}
	return nodeCommunity
}

// louvainCommunities implements the Louvain algorithm for community detection.
// Uses per-community degree accumulators for O(m) per iteration instead of O(N^2).
// warmStart, when non-nil, seeds node assignments from a prior run, reducing
// iterations from up to 50 down to 1-3 for small incremental changes.
// Returns the grouped community map and the flat nodeCommunity map for caching.
func louvainCommunities(adj map[int64]map[int64]bool, allNodes map[int64]bool, warmStart map[int64]int) (map[int][]int64, map[int64]int) {
	nodeCommunity := initNodeCommunities(allNodes, warmStart)

	nodeDegree := make(map[int64]float64, len(allNodes))
	totalEdges := 0
	for nodeID, neighbors := range adj {
		nodeDegree[nodeID] = float64(len(neighbors))
		totalEdges += len(neighbors)
	}
	m := float64(totalEdges) / 2.0
	if m == 0 {
		m = 1
	}

	commSumTot := make(map[int]float64, len(allNodes))
	for nodeID, comm := range nodeCommunity {
		commSumTot[comm] += nodeDegree[nodeID]
	}

	improved := true
	for iteration := 0; improved && iteration < 50; iteration++ {
		improved = louvainIteration(adj, nodeCommunity, nodeDegree, commSumTot, m)
	}

	return groupAndFilter(nodeCommunity), nodeCommunity
}

// louvainIteration runs one pass of greedy modularity optimization.
// For each node, computes modularity gain for neighboring communities in O(degree)
// using pre-maintained commSumTot accumulators. Returns true if any node moved.
func louvainIteration(
	adj map[int64]map[int64]bool,
	nodeCommunity map[int64]int,
	nodeDegree map[int64]float64,
	commSumTot map[int]float64,
	m float64,
) bool {
	improved := false
	m2 := 2.0 * m * m

	for nodeID, neighbors := range adj {
		currentComm := nodeCommunity[nodeID]
		ki := nodeDegree[nodeID]

		// Aggregate edges to each neighboring community: O(degree)
		edgesToComm := make(map[int]float64, len(neighbors))
		for neighborID := range neighbors {
			edgesToComm[nodeCommunity[neighborID]]++
		}

		// Remove self from current community for fair comparison
		commSumTot[currentComm] -= ki
		kiInCurrent := edgesToComm[currentComm]
		removeCost := kiInCurrent/m - ki*commSumTot[currentComm]/m2

		bestComm := currentComm
		bestGain := 0.0

		for comm, kiIn := range edgesToComm {
			if comm == currentComm {
				continue
			}
			gain := kiIn/m - ki*commSumTot[comm]/m2 - removeCost
			if gain > bestGain {
				bestGain = gain
				bestComm = comm
			}
		}

		// Restore / update accumulator
		if bestComm != currentComm && bestGain > 1e-10 {
			nodeCommunity[nodeID] = bestComm
			commSumTot[bestComm] += ki
			// currentComm already had ki subtracted
			improved = true
		} else {
			commSumTot[currentComm] += ki // restore
		}
	}
	return improved
}

// groupAndFilter groups nodes by community and filters out singletons.
func groupAndFilter(nodeCommunity map[int64]int) map[int][]int64 {
	communities := make(map[int][]int64)
	for nodeID, comm := range nodeCommunity {
		communities[comm] = append(communities[comm], nodeID)
	}

	filtered := make(map[int][]int64)
	idx := 0
	for _, members := range communities {
		if len(members) >= 2 {
			filtered[idx] = members
			idx++
		}
	}
	return filtered
}

// buildCommunityNode creates a Community node with metadata for a single community.
func buildCommunityNode(project string, commIdx int, memberIDs []int64, nodeMap map[int64]*store.Node) *store.Node {
	topNames := topMemberNames(memberIDs, nodeMap, 5)

	commName := fmt.Sprintf("community_%d", commIdx)
	if len(topNames) > 0 {
		commName = topNames[0] + "_cluster"
	}

	cohesion := communityCohesion(memberIDs, nodeMap)

	return &store.Node{
		Project:       project,
		Label:         "Community",
		Name:          commName,
		QualifiedName: fmt.Sprintf("%s.__community__.%d", project, commIdx),
		Properties: map[string]any{
			"cohesion":     math.Round(cohesion*100) / 100,
			"symbol_count": len(memberIDs),
			"top_symbols":  topNames,
		},
	}
}

// collectMemberEdges builds pending MEMBER_OF edges and tags member nodes
// with the community index.
func collectMemberEdges(commIdx int, commQN string, memberIDs []int64, nodeMap map[int64]*store.Node) []pendingEdge {
	var edges []pendingEdge
	for _, memberID := range memberIDs {
		memberNode := nodeMap[memberID]
		if memberNode == nil {
			continue
		}
		edges = append(edges, pendingEdge{
			SourceQN: memberNode.QualifiedName,
			TargetQN: commQN,
			Type:     "MEMBER_OF",
		})
		if memberNode.Properties == nil {
			memberNode.Properties = make(map[string]any)
		}
		memberNode.Properties["community_id"] = commIdx
	}
	return edges
}

// resolveMemberOfEdges resolves pending MEMBER_OF edges to concrete store edges.
func (p *Pipeline) resolveMemberOfEdges(pending []pendingEdge, idMap map[string]int64) []*store.Edge {
	var edges []*store.Edge
	for _, pe := range pending {
		srcNode, _ := p.Store.FindNodeByQN(p.ProjectName, pe.SourceQN)
		tgtID, tgtOK := idMap[pe.TargetQN]
		if srcNode != nil && tgtOK {
			edges = append(edges, &store.Edge{
				Project:  p.ProjectName,
				SourceID: srcNode.ID,
				TargetID: tgtID,
				Type:     "MEMBER_OF",
			})
		}
	}
	return edges
}

// storeCommunities creates Community nodes and MEMBER_OF edges in the database.
func (p *Pipeline) storeCommunities(communities map[int][]int64) (communityCount, memberOfCount int) {
	if len(communities) == 0 {
		return 0, 0
	}

	var allMemberIDs []int64
	for _, members := range communities {
		allMemberIDs = append(allMemberIDs, members...)
	}
	nodeMap, _ := p.Store.FindNodesByIDs(allMemberIDs)

	communityNodes := make([]*store.Node, 0, len(communities))
	var memberEdges []pendingEdge

	for commIdx, memberIDs := range communities {
		commNode := buildCommunityNode(p.ProjectName, commIdx, memberIDs, nodeMap)
		communityNodes = append(communityNodes, commNode)
		memberEdges = append(memberEdges, collectMemberEdges(commIdx, commNode.QualifiedName, memberIDs, nodeMap)...)
	}

	idMap, err := p.Store.UpsertNodeBatch(communityNodes)
	if err != nil {
		slog.Warn("pass.communities.upsert.err", "err", err)
		return 0, 0
	}

	edges := p.resolveMemberOfEdges(memberEdges, idMap)
	if len(edges) > 0 {
		if err := p.Store.InsertEdgeBatch(edges); err != nil {
			slog.Warn("pass.communities.edges.err", "err", err)
		}
	}

	return len(communityNodes), len(edges)
}

func topMemberNames(memberIDs []int64, nodeMap map[int64]*store.Node, limit int) []string {
	type entry struct {
		name  string
		label string
	}
	var entries []entry
	for _, id := range memberIDs {
		n := nodeMap[id]
		if n != nil {
			entries = append(entries, entry{n.Name, n.Label})
		}
	}

	// Sort: Classes first, then Functions, alphabetical
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].label != entries[j].label {
			// Prefer Class/Interface over Function/Method
			return labelPriority(entries[i].label) < labelPriority(entries[j].label)
		}
		return entries[i].name < entries[j].name
	})

	names := make([]string, 0, limit)
	for i, e := range entries {
		if i >= limit {
			break
		}
		names = append(names, e.name)
	}
	return names
}

func labelPriority(label string) int {
	switch label {
	case "Class":
		return 0
	case "Interface":
		return 1
	case "Type":
		return 2
	case "Function":
		return 3
	case "Method":
		return 4
	default:
		return 5
	}
}

func communityCohesion(memberIDs []int64, nodeMap map[int64]*store.Node) float64 {
	n := len(memberIDs)
	if n < 2 {
		return 1.0
	}
	// Simplified cohesion: proportion of members with known types
	knownCount := 0
	for _, id := range memberIDs {
		if nodeMap[id] != nil {
			knownCount++
		}
	}
	return float64(knownCount) / float64(n)
}

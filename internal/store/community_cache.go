package store

import (
	"context"
	"strings"
)

// cacheBatchSize is the number of community cache rows per INSERT statement.
// 999 (SQLite param limit) / 4 (columns) = 249.
const cacheBatchSize = 249

// SaveCommunityCache persists the Louvain partition for a project in batched
// multi-row INSERTs. graphHash is a fingerprint of the graph state.
//
// Uses s.q (the active Querier) rather than s.db so that callers inside a
// write transaction (e.g. passCommunities inside WithTransaction + MEMORY
// journal mode) don't deadlock by opening a second connection on the pool.
func (s *Store) SaveCommunityCache(ctx context.Context, project, graphHash string, partition map[int64]int) error {
	if _, err := s.q.Exec(`DELETE FROM community_cache WHERE project = ?`, project); err != nil {
		return err
	}

	type entry struct {
		nodeID    int64
		community int
	}
	entries := make([]entry, 0, len(partition))
	for nodeID, community := range partition {
		entries = append(entries, entry{nodeID, community})
	}

	for i := 0; i < len(entries); i += cacheBatchSize {
		end := i + cacheBatchSize
		if end > len(entries) {
			end = len(entries)
		}
		batch := entries[i:end]

		placeholders := make([]string, len(batch))
		args := make([]any, 0, len(batch)*4)
		for j, e := range batch {
			placeholders[j] = "(?,?,?,?)"
			args = append(args, project, e.nodeID, e.community, graphHash)
		}
		q := "INSERT OR REPLACE INTO community_cache(project, node_id, community, graph_hash) VALUES " +
			strings.Join(placeholders, ",")
		if _, err := s.q.Exec(q, args...); err != nil {
			return err
		}
	}
	return nil
}

// LoadCommunityCache loads the previous Louvain partition for a project.
// Returns nil if no cache exists or if graphHash doesn't match the stored hash.
//
// Uses s.q (the active Querier) rather than s.db so that callers inside a
// write transaction (e.g. passCommunities inside WithTransaction + MEMORY
// journal mode) don't deadlock by opening a second connection on the pool.
func (s *Store) LoadCommunityCache(ctx context.Context, project, graphHash string) (map[int64]int, error) {
	// Single query: filter by both project and graph_hash so an empty result
	// set means cache miss (no need for a separate hash-check query).
	rows, err := s.q.Query(
		`SELECT node_id, community FROM community_cache WHERE project = ? AND graph_hash = ?`,
		project, graphHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64]int)
	for rows.Next() {
		var nodeID int64
		var community int
		if err := rows.Scan(&nodeID, &community); err != nil {
			return nil, err
		}
		result[nodeID] = community
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, nil // cache miss
	}
	return result, nil
}

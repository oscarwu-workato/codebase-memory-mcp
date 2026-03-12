package store

import (
	"context"
	"database/sql"
	"errors"
)

// SaveCommunityCache persists the Louvain partition for a project.
// graphHash is a fingerprint of the graph state (e.g. fmt.Sprintf("%d:%d", nodes, edges)).
func (s *Store) SaveCommunityCache(project, graphHash string, partition map[int64]int) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM community_cache WHERE project = ?`, project); err != nil {
		_ = tx.Rollback()
		return err
	}
	for nodeID, community := range partition {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO community_cache(project, node_id, community, graph_hash) VALUES (?,?,?,?)`,
			project, nodeID, community, graphHash); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// LoadCommunityCache loads the previous Louvain partition for a project.
// Returns nil if no cache exists or if graphHash doesn't match the stored hash.
func (s *Store) LoadCommunityCache(project, graphHash string) (map[int64]int, error) {
	ctx := context.Background()
	var storedHash string
	err := s.db.QueryRowContext(ctx,
		`SELECT graph_hash FROM community_cache WHERE project = ? LIMIT 1`, project).
		Scan(&storedHash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && storedHash != graphHash) {
		return nil, nil // cache miss
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT node_id, community FROM community_cache WHERE project = ?`, project)
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
	return result, rows.Err()
}

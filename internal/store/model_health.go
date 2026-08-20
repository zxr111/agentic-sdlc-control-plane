package store

import "context"

type ModelHealthSnapshot struct {
	Healthy   bool
	LatencyMS int64
}

func (s *Store) LatestModelHealth(ctx context.Context) (map[string]ModelHealthSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT ON(mv.model_key) mv.model_key,mh.healthy,mh.latency_ms
		FROM model_health_events mh JOIN model_versions mv ON mv.id=mh.model_version_id
		ORDER BY mv.model_key,mh.observed_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]ModelHealthSnapshot{}
	for rows.Next() {
		var key string
		var snapshot ModelHealthSnapshot
		if err := rows.Scan(&key, &snapshot.Healthy, &snapshot.LatencyMS); err != nil {
			return nil, err
		}
		result[key] = snapshot
	}
	return result, rows.Err()
}

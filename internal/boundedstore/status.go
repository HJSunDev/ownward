package boundedstore

import (
	"context"
)

func (s *Store) SemanticCounts(ctx context.Context) (map[string]int, error) {
	out := map[string]int{}
	e := s.view(ctx, func(q queryer) error {
		rows, e := q.QueryContext(ctx, `SELECT o.status,count(*) FROM derived_state d JOIN organization_current c ON c.generation=d.generation JOIN organizations o ON o.id=c.organization JOIN live_assets a ON a.id=o.asset AND a.revision=o.revision GROUP BY o.status`)
		if e != nil {
			return e
		}
		for rows.Next() {
			var key string
			var n int
			if e = rows.Scan(&key, &n); e != nil {
				rows.Close()
				return e
			}
			out[key] = n
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		var pending int
		if e = q.QueryRowContext(ctx, "SELECT count(*) FROM semantic_jobs j JOIN live_assets a ON a.id=j.asset AND a.revision=j.revision").Scan(&pending); e != nil {
			return e
		}
		out["pending_work"] = pending
		return nil
	})
	return out, e
}

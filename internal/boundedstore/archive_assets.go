package boundedstore

import (
	"context"
	"database/sql"
)

// Asset backups retain the authoritative originals and control decisions. The
// copied database is private; deleting caches here never changes the live store.
func stripArchiveDerived(ctx context.Context, path string) error {
	c, e := sql.Open("sqlite", path)
	if e != nil {
		return e
	}
	defer c.Close()
	c.SetMaxOpenConns(1)
	if _, e = c.ExecContext(ctx, "PRAGMA cache_size=-1024; PRAGMA temp_store=FILE; PRAGMA foreign_keys=OFF;"); e != nil {
		return e
	}
	for _, name := range []string{"vector_delta", "vector_members", "vector_filter_pages", "vector_blocks", "vectors", "organization_publications", "organization_current", "organization_heads", "organization_headers", "organization_chunks", "dependencies", "organization_contexts", "graph_links", "graph_explicit", "graph_units", "graph_names", "navigation_cursors", "organizations", "derived_reclaim", "controlled_copies", "invalidation_jobs"} {
		if _, e = c.ExecContext(ctx, "DELETE FROM "+name); e != nil {
			return e
		}
	}
	if _, e = c.ExecContext(ctx, `DELETE FROM staging_owners WHERE kind!='payload'; DELETE FROM reclaim_estimates WHERE kind!='payload'; DELETE FROM semantic_jobs; INSERT INTO semantic_jobs SELECT id,revision,'restored' FROM live_assets;`); e != nil {
		return e
	}
	_, e = c.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE); VACUUM; PRAGMA wal_checkpoint(TRUNCATE);")
	return e
}

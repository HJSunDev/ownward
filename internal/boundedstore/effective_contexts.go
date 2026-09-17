package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/HJSunDev/ownward/internal/domain"
)

func (s *Store) MatchesEffectiveContexts(ctx context.Context, generation, id string, contexts []domain.Context) (bool, error) {
	if _, ok := ctx.Value(snapshotKey{}).(snapshot); !ok {
		var result bool
		e := s.WithSnapshot(ctx, func(ctx context.Context) error {
			var e error
			result, e = s.MatchesEffectiveContexts(ctx, generation, id, contexts)
			return e
		})
		return result, e
	}
	valid := true
	e := s.view(ctx, func(q queryer) error {
		var payload string
		if e := q.QueryRowContext(ctx, "SELECT payload FROM live_assets WHERE id=? AND deleted=0", id).Scan(&payload); e != nil {
			return e
		}
		org, e := s.CurrentOrganization(ctx, generation, id)
		if e != nil && !errors.Is(e, sql.ErrNoRows) && !errors.Is(e, ErrNotFound) {
			return e
		}
		valid, e = visitRequiredContexts(ctx, contexts, func(key, value [32]byte) (bool, error) {
			var count, compatible int
			e := q.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(value=?),0) FROM (SELECT key,value FROM lexical_contexts WHERE payload=? UNION ALL SELECT i.key,i.value FROM organization_contexts i WHERE i.organization=? AND NOT EXISTS(SELECT 1 FROM lexical_contexts x WHERE x.payload=? AND x.lower_key=i.lower_key)) WHERE key=?`, value[:], payload, org.ID, payload, key[:]).Scan(&count, &compatible)
			return count == 0 || compatible > 0, e
		})
		return e
	})
	return valid, e
}
func (s *Store) ExplicitContextKey(ctx context.Context, id, key string) (bool, error) {
	var yes bool
	digest, _ := lowerDigest(strings.NewReader(key))
	e := s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM lexical_contexts c JOIN live_assets a ON a.payload=c.payload AND a.deleted=0 WHERE a.id=? AND c.lower_key=?)", id, digest[:]).Scan(&yes)
	})
	return yes, e
}

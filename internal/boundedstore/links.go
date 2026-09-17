package boundedstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
)

type ExplicitLink struct {
	Ordinal            int64
	Target             string
	Qualifies          bool
	StartRune, EndRune int64
}

func (s *Store) StageLink(ctx context.Context, payload Staged, link ExplicitLink) error {
	if link.Ordinal < 0 || link.Target == "" || link.StartRune < 0 || link.EndRune < link.StartRune {
		return errors.New("明确关系定位无效")
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		var state, owner string
		if err := tx.QueryRowContext(ctx, "SELECT state,operation FROM payloads WHERE id=?", payload.ID).Scan(&state, &owner); err != nil {
			return err
		}
		if state != "ready" || owner != payload.Operation {
			return errors.New("关系暂存已经失效")
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO explicit_links VALUES(?,?,?,?,?,?)", payload.ID, link.Ordinal, link.Target, link.Qualifies, link.StartRune, link.EndRune)
		return err
	})
}

type Qualifier struct {
	Meta contract.AssetMeta
	Link ExplicitLink
}

func (s *Store) VisitQualifiers(ctx context.Context, target string, visit func(Qualifier) error) error {
	// 游标不跨外部回调持有连接，避免回调读取同一资料时耗尽固定读连接。
	last := ""
	ordinal := int64(-1)
	for {
		c, done, err := s.reader(ctx)
		if err != nil {
			return err
		}
		var q Qualifier
		var created, updated string
		err = c.QueryRowContext(ctx, "SELECT "+assetColumns+",l.ordinal,l.start_rune,l.end_rune FROM explicit_links l JOIN live_assets a ON a.payload=l.payload JOIN payloads p ON p.id=a.payload WHERE l.target=? AND l.qualifies=1 AND a.deleted=0 AND (a.id>? OR (a.id=? AND l.ordinal>?)) ORDER BY a.id,l.ordinal LIMIT 1", target, last, last, ordinal).Scan(&q.Meta.ID, &q.Meta.Revision, &created, &updated, &q.Meta.Kind, &q.Meta.ContentBytes, &q.Meta.ContentSHA256, &q.Link.Ordinal, &q.Link.StartRune, &q.Link.EndRune)
		done()
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		q.Link.Target = target
		q.Meta.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return err
		}
		q.Meta.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return err
		}
		q.Link.Qualifies = true
		last = q.Meta.ID
		ordinal = q.Link.Ordinal
		if err = visit(q); err != nil {
			return err
		}
	}
}
func (s *Store) AssetEpoch(ctx context.Context) (uint64, error) {
	c, done, err := s.reader(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	var n uint64
	err = c.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='asset_epoch'").Scan(&n)
	return n, err
}
func (s *Store) SourceEpoch(ctx context.Context, id string) (uint64, error) {
	c, done, err := s.reader(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	var n uint64
	err = c.QueryRowContext(ctx, "SELECT revision FROM source_epochs WHERE id=?", id).Scan(&n)
	return n, err
}

package boundedstore

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"

	"github.com/HJSunDev/ownward/internal/semantics"
)

// InitializeGeneration initializes only a new empty store. Existing stores
// change vector spaces through staged generations, never silent replacement.
func (s *Store) InitializeGeneration(ctx context.Context, space string) (string, error) {
	s.organizationMu.Lock()
	defer s.organizationMu.Unlock()
	generation, current, e := s.Generation(ctx)
	if e == nil {
		if current != space {
			return "", errors.New("向量空间已变化，必须重建派生世代")
		}
		return generation, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return "", e
	}
	id, e := newID()
	if e != nil {
		return "", e
	}
	e = s.write(ctx, func(tx *sql.Tx) error {
		if e := checkAccess(ctx, tx); e != nil {
			return e
		}
		var count int
		if e := tx.QueryRowContext(ctx, "SELECT count(*) FROM live_assets WHERE deleted=0").Scan(&count); e != nil {
			return e
		}
		if count != 0 {
			return errors.New("已有资产需通过派生世代构建接入")
		}
		if _, e := tx.ExecContext(ctx, "INSERT INTO generations VALUES(?,?,'active')", id, space); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "INSERT INTO derived_state VALUES(1,?,1)", id)
		return e
	})
	return id, e
}

func (s *Store) RawEmbedding(ctx context.Context, v OrganizationVersion) ([]float32, error) {
	var out []float32
	e := s.view(ctx, func(q queryer) error {
		var data, digest []byte
		e := q.QueryRowContext(ctx, "SELECT data,digest FROM vectors WHERE organization=?", v.ID).Scan(&data, &digest)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		if !checkedBlob(data, digest) || len(data) != vectorDimensions*4 {
			return errors.New("原向量校验失败")
		}
		out = make([]float32, vectorDimensions)
		for i := range out {
			out[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
		}
		return nil
	})
	return out, e
}

func (s *Store) ReferencesCurrent(ctx context.Context, generation string, refs []semantics.CandidateReference) error {
	return s.view(ctx, func(q queryer) error {
		for _, ref := range refs {
			var revision uint64
			if e := q.QueryRowContext(ctx, "SELECT revision FROM live_assets WHERE id=? AND deleted=0", ref.ID).Scan(&revision); e != nil {
				return e
			}
			if ref.Revision == 0 || revision != ref.Revision {
				return errors.New("语义实际输入版本已变化")
			}
			if ref.OrganizationSnapshot != "" {
				var org, snapshot string
				if e := q.QueryRowContext(ctx, "SELECT o.id,o.snapshot FROM organization_current c JOIN organizations o ON o.id=c.organization WHERE c.generation=? AND c.asset=?", generation, ref.ID).Scan(&org, &snapshot); e != nil {
					return e
				}
				if snapshot != ref.OrganizationSnapshot {
					return errors.New("语义实际组织输入已变化")
				}
				ok, e := organizationInputsCurrent(ctx, q, org)
				if e != nil {
					return e
				}
				if !ok {
					return errors.New("语义实际组织输入已失效")
				}
			}
		}
		return nil
	})
}

func (s *Store) DependsOn(ctx context.Context, v OrganizationVersion, id string) (bool, error) {
	var yes bool
	e := s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM dependencies WHERE organization=? AND asset=?)", v.ID, id).Scan(&yes)
	})
	return yes, e
}

func (s *Store) HasVectors(ctx context.Context) (bool, error) {
	var yes bool
	e := s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM vectors v JOIN organization_current c ON c.organization=v.organization JOIN derived_state d ON d.generation=c.generation)").Scan(&yes)
	})
	return yes, e
}

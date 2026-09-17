package boundedstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

type OrganizationVersion struct {
	ID, Generation, Asset, Snapshot, Status, WorkID, Expected string
	Revision                                                  uint64
}

func (s *Store) CreateGeneration(ctx context.Context, id, space string) error {
	if id == "" || space == "" {
		return errors.New("派生世代及向量空间不能为空")
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if err := checkAccess(ctx, tx); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO generations VALUES(?,?,'building')", id, space)
		return err
	})
}
func (s *Store) ActivateGeneration(ctx context.Context, id, expected string) error {
	return contract.Commit(ctx, func() error {
		return s.write(ctx, func(tx *sql.Tx) error {
			if err := checkAccess(ctx, tx); err != nil {
				return err
			}
			var old, state string
			err := tx.QueryRowContext(ctx, "SELECT generation FROM derived_state WHERE singleton=1").Scan(&old)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if old != expected {
				return errors.New("派生世代已变化")
			}
			if err = tx.QueryRowContext(ctx, "SELECT state FROM generations WHERE id=?", id).Scan(&state); err != nil {
				return err
			}
			if state != "building" {
				return errors.New("派生世代不可发布")
			}
			var missing int
			if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM live_assets a WHERE a.deleted=0 AND NOT EXISTS(SELECT 1 FROM organization_current c JOIN organizations o ON o.id=c.organization WHERE c.generation=? AND c.asset=a.id AND o.revision=a.revision)`, id).Scan(&missing); err != nil {
				return err
			}
			if missing > 0 {
				return errors.New("派生世代尚未覆盖当前资产")
			}
			rows, e := tx.QueryContext(ctx, "SELECT organization FROM organization_current WHERE generation=?", id)
			if e != nil {
				return e
			}
			for rows.Next() {
				var org string
				if e = rows.Scan(&org); e != nil {
					rows.Close()
					return e
				}
				valid, e := organizationInputsCurrent(ctx, tx, org)
				if e != nil {
					rows.Close()
					return e
				}
				if !valid {
					rows.Close()
					return errors.New("派生世代包含失效输入")
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			if old != "" {
				if _, err = tx.ExecContext(ctx, "UPDATE generations SET state='retired' WHERE id=?", old); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim VALUES(?,'generation','replaced')", old); err != nil {
					return err
				}
			}
			// A rebuilt index starts in asset order, then live replacements append.
			if _, err = tx.ExecContext(ctx, "DELETE FROM organization_publications WHERE organization IN (SELECT organization FROM organization_current WHERE generation=?)", id); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO organization_publications(organization) SELECT organization FROM organization_current WHERE generation=? ORDER BY asset", id); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE generations SET state='active' WHERE id=?", id); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO derived_state VALUES(1,?,1) ON CONFLICT(singleton) DO UPDATE SET generation=excluded.generation,epoch=epoch+1", id)
			return err
		})
	})
}
func (s *Store) Generation(ctx context.Context) (string, string, error) {
	var generation, space string
	err := s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT g.id,g.space FROM derived_state d JOIN generations g ON g.id=d.generation WHERE d.singleton=1").Scan(&generation, &space)
	})
	return generation, space, err
}
func nodeString(n streamjson.Node, key string) (string, error) {
	v, ok, err := n.Field(key)
	if err != nil || !ok {
		return "", err
	}
	return v.String(64 * 1024)
}
func nodeDecode(n streamjson.Node, key string, out any) error {
	v, ok, err := n.Field(key)
	if err != nil || !ok {
		return err
	}
	return v.DecodeSmall(out, 256*1024)
}

// StageOrganization accepts the kernel's normalized record, not an AI response.
// Original semantic content is preserved in chunks; indexes are projections.
func (s *Store) StageOrganization(ctx context.Context, generation string, source contract.ContentSource) (OrganizationVersion, error) {
	var v OrganizationVersion
	v.Generation = generation
	r, err := source.Open(ctx)
	if err != nil {
		return v, err
	}
	doc, err := streamjson.Parse(ctx, s.directory, r, resourcebudget.FromContext(ctx, s.budget), 256*resourcebudget.MiB)
	r.Close()
	if err != nil {
		return v, err
	}
	defer doc.Close()
	root := doc.Root()
	v.ID, err = newID()
	if err != nil {
		return v, err
	}
	v.Asset, err = nodeString(root, "asset_id")
	if err != nil {
		return v, err
	}
	if err = nodeDecode(root, "asset_revision", &v.Revision); err != nil {
		return v, err
	}
	v.Status, err = nodeString(root, "status")
	if err != nil {
		return v, err
	}
	if v.Asset == "" || v.Revision == 0 {
		return v, errors.New("派生资产身份无效")
	}
	analysis, ok, err := root.Field("analysis")
	if err != nil || !ok {
		return v, errors.New("派生分析缺失")
	}
	org, hasOrg, err := analysis.Field("organization")
	if err != nil {
		return v, err
	}
	hasOrg = hasOrg && org.Kind != 'n'
	if hasOrg {
		v.Snapshot, err = nodeString(org, "snapshot")
		if err != nil {
			return v, err
		}
	}
	work, hasWork, err := root.Field("semantic_work_reference")
	if err != nil {
		return v, err
	}
	hasWork = hasWork && work.Kind != 'n'
	if hasWork {
		v.WorkID, err = nodeString(work, "id")
		if err != nil {
			return v, err
		}
	}
	err = s.view(ctx, func(q queryer) error {
		e := q.QueryRowContext(ctx, "SELECT coalesce((SELECT organization FROM organization_heads WHERE generation=? AND asset=?),(SELECT organization FROM organization_current WHERE generation=? AND asset=?),'')", generation, v.Asset, generation, v.Asset).Scan(&v.Expected)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		return e
	})
	if err != nil {
		return v, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO organizations VALUES(?,?,?,?,?,'staging',?,?,?)", v.ID, generation, v.Asset, v.Revision, v.Snapshot, v.Status, v.WorkID, v.Expected)
		return err
	})
	if err != nil {
		return v, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = s.write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
				_, e := tx.ExecContext(context.WithoutCancel(ctx), "INSERT OR IGNORE INTO derived_reclaim VALUES(?,'organization','aborted')", v.ID)
				return e
			})
		}
	}()
	// Keep vectors in their native representation, without a second JSON copy.
	metadata, err := streamjson.Build(ctx, s.directory, resourcebudget.FromContext(ctx, s.budget), 256*resourcebudget.MiB, func(w io.Writer) error {
		io.WriteString(w, "{")
		first := true
		cursor := root.Children()
		for {
			n, e := cursor.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			key, e := n.Key()
			if e != nil {
				return e
			}
			if key == "embedding" {
				continue
			}
			if !first {
				io.WriteString(w, ",")
			}
			first = false
			if e = streamjson.WriteString(ctx, w, strings.NewReader(key)); e != nil {
				return e
			}
			io.WriteString(w, ":")
			if e = n.Copy(w); e != nil {
				return e
			}
		}
		_, e := io.WriteString(w, "}")
		return e
	})
	if err != nil {
		return v, err
	}
	defer metadata.Close()
	r, err = (streamjson.RawSource{Node: metadata.Root()}).Open(ctx)
	if err != nil {
		return v, err
	}
	buffer := make([]byte, ChunkBytes)
	ordinal := 0
	for {
		n, e := io.ReadFull(r, buffer)
		if e != nil && e != io.EOF && e != io.ErrUnexpectedEOF {
			r.Close()
			return v, e
		}
		if n == 0 {
			break
		}
		if err = s.write(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, "INSERT INTO organization_chunks VALUES(?,?,?)", v.ID, ordinal, buffer[:n])
			return e
		}); err != nil {
			r.Close()
			return v, err
		}
		ordinal++
		if e != nil {
			break
		}
	}
	r.Close()
	var vector []float32
	if err = nodeDecode(root, "embedding", &vector); err != nil {
		return v, err
	}
	if len(vector) > 0 {
		if len(vector) != 512 {
			return v, errors.New("向量维度与固定空间不一致")
		}
		space, e := nodeString(root, "embedding_space")
		if e != nil {
			return v, e
		}
		data := make([]byte, len(vector)*4)
		for i, x := range vector {
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				return v, errors.New("向量包含非有限值")
			}
			binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(x))
		}
		digest := sha256.Sum256(data)
		if err = s.write(ctx, func(tx *sql.Tx) error {
			var expected string
			if e := tx.QueryRowContext(ctx, "SELECT space FROM generations WHERE id=?", generation).Scan(&expected); e != nil {
				return e
			}
			if space != expected {
				return errors.New("向量空间不匹配")
			}
			_, e := tx.ExecContext(ctx, "INSERT INTO vectors VALUES(?,?,?,?,?)", v.ID, space, len(vector), data, digest[:])
			return e
		}); err != nil {
			return v, err
		}
	}
	dependency := func(n streamjson.Node) error {
		var ref semantics.CandidateReference
		if err := n.DecodeSmall(&ref, 64*1024); err != nil {
			return err
		}
		if ref.ID == "" || ref.ID == v.Asset {
			return nil
		}
		return s.write(ctx, func(tx *sql.Tx) error {
			_, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO dependencies VALUES(?,?,?,?)", v.ID, ref.ID, ref.Revision, ref.OrganizationSnapshot)
			return e
		})
	}
	for _, pair := range []struct {
		n   streamjson.Node
		key string
	}{{root, "input_assets"}, {work, "candidates"}} {
		if pair.n.Kind == 0 {
			continue
		}
		if err = visitArray(pair.n, pair.key, dependency); err != nil {
			return v, err
		}
	}
	if err = visitArray(analysis, "inferred_contexts", func(n streamjson.Node) error {
		var c semantics.InferredContext
		if e := n.DecodeSmall(&c, 64*1024); e != nil {
			return e
		}
		kd, _ := foldDigest(strings.NewReader(c.Key))
		vd, _ := foldDigest(strings.NewReader(c.Value))
		return s.write(ctx, func(tx *sql.Tx) error {
			lk, _ := lowerDigest(strings.NewReader(c.Key))
			_, e := tx.ExecContext(ctx, "INSERT INTO organization_contexts SELECT ?,coalesce(max(ordinal)+1,0),?,?,? FROM organization_contexts WHERE organization=?", v.ID, kd[:], vd[:], lk[:], v.ID)
			return e
		})
	}); err != nil {
		return v, err
	}
	indexedAnalysis, _, err := metadata.Root().Field("analysis")
	if err != nil {
		return v, err
	}
	indexedOrg, _, err := indexedAnalysis.Field("organization")
	if err != nil {
		return v, err
	}
	if err = s.stageOrganizationHeader(ctx, v, metadata.Root(), indexedAnalysis); err != nil {
		return v, err
	}
	if err = s.stageGraph(ctx, v, indexedAnalysis, indexedOrg, hasOrg); err != nil {
		return v, err
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "UPDATE organizations SET state='ready' WHERE id=?", v.ID)
		return e
	})
	complete = err == nil
	return v, err
}
func visitArray(root streamjson.Node, key string, fn func(streamjson.Node) error) error {
	n, ok, err := root.Field(key)
	if err != nil {
		return err
	}
	if !ok || n.Kind == 'n' {
		return nil
	}
	if n.Kind != '[' {
		return errors.New("派生数组格式无效")
	}
	c := n.Children()
	for {
		x, e := c.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		if e = fn(x); e != nil {
			return e
		}
	}
}

func (s *Store) PublishOrganization(ctx context.Context, v OrganizationVersion) error {
	if err := s.awaitReclaim(ctx); err != nil {
		return err
	}
	s.organizationMu.Lock()
	defer s.organizationMu.Unlock()
	header, err := s.RecordHeader(ctx, v)
	if err != nil {
		return err
	}
	if err := s.boundDelta(ctx); err != nil {
		return err
	}
	var rejected error
	err = contract.Commit(ctx, func() error {
		return s.write(ctx, func(tx *sql.Tx) error {
			if err := checkAccess(ctx, tx); err != nil {
				return err
			}
			var generation, asset, state, expected string
			var revision uint64
			if err := tx.QueryRowContext(ctx, "SELECT generation,asset,revision,state,expected FROM organizations WHERE id=?", v.ID).Scan(&generation, &asset, &revision, &state, &expected); err != nil {
				return err
			}
			if generation != v.Generation || asset != v.Asset || revision != v.Revision {
				return errors.New("派生发布身份不一致")
			}
			var current string
			err := tx.QueryRowContext(ctx, "SELECT organization FROM organization_current WHERE generation=? AND asset=?", generation, asset).Scan(&current)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if current == v.ID {
				return nil
			}
			var head string
			if err = tx.QueryRowContext(ctx, "SELECT coalesce((SELECT organization FROM organization_heads WHERE generation=? AND asset=?),? )", generation, asset, current).Scan(&head); err != nil {
				return err
			}
			if state != "ready" {
				return errors.New("派生结果已被替换")
			}
			// 永久失效与回收登记同一事务提交；临时错误或取消仍保留可重试候选。
			reject := func(reason error) error {
				rejected = reason
				return s.retireOrganization(ctx, tx, v.ID)
			}
			if head != expected {
				return reject(errors.New("派生结果已被替换"))
			}
			var actual uint64
			if err = tx.QueryRowContext(ctx, "SELECT revision FROM live_assets WHERE id=? AND deleted=0", asset).Scan(&actual); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return reject(ErrNotFound)
				}
				return err
			}
			if actual != revision {
				return reject(errors.New("语义来源已变化"))
			}
			var gstate string
			if err = tx.QueryRowContext(ctx, "SELECT state FROM generations WHERE id=?", generation).Scan(&gstate); err != nil {
				return err
			}
			if gstate == "retired" {
				return reject(errors.New("派生世代已退出使用"))
			}
			valid, err := organizationInputsCurrent(ctx, tx, v.ID)
			if err != nil {
				return err
			}
			if !valid {
				return reject(errors.New("语义判断的实际输入已变化"))
			}
			if current != "" {
				if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim VALUES(?,'organization','replaced')", current); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "DELETE FROM vector_delta WHERE organization=?", current); err != nil {
					return err
				}
			}
			if _, err = tx.ExecContext(ctx, "UPDATE organizations SET state='active' WHERE id=?", v.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO organization_current VALUES(?,?,?) ON CONFLICT(generation,asset) DO UPDATE SET organization=excluded.organization", generation, asset, v.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO organization_heads VALUES(?,?,?) ON CONFLICT(generation,asset) DO UPDATE SET organization=excluded.organization", generation, asset, v.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO organization_publications(organization) VALUES(?)", v.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO vector_delta SELECT organization,space FROM vectors WHERE organization=?", v.ID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE derived_state SET epoch=epoch+1 WHERE singleton=1"); err != nil {
				return err
			}
			if !header.HasPendingSemanticWork() {
				if _, err = tx.ExecContext(ctx, "DELETE FROM semantic_jobs WHERE asset=? AND revision=?", asset, revision); err != nil {
					return err
				}
			}
			_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO invalidation_jobs(asset,revision,snapshot,forget) VALUES(?,0,?,0)", asset, v.Snapshot)
			return err
		})
	})
	if err != nil {
		return err
	}
	if rejected != nil {
		return rejected
	}
	var space string
	err = s.view(ctx, func(q queryer) error {
		return q.QueryRowContext(ctx, "SELECT space FROM generations WHERE id=?", v.Generation).Scan(&space)
	})
	if err != nil {
		return err
	}
	return s.write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, e := tx.ExecContext(context.WithoutCancel(ctx), "INSERT OR IGNORE INTO derived_reclaim VALUES(?,'vector_pack','published')", space)
		return e
	})
}

// Dependencies are checked recursively at read/publication time. UNION uses a
// disk-backed visited set, so cycles and high fan-out do not grow a Go map.
func organizationInputsCurrent(ctx context.Context, q queryer, id string) (bool, error) {
	var invalid int
	err := q.QueryRowContext(ctx, `WITH RECURSIVE closure(id) AS (
 SELECT ? UNION SELECT c.organization FROM closure x JOIN dependencies d ON d.organization=x.id JOIN organizations o ON o.id=x.id JOIN organization_current c ON c.generation=o.generation AND c.asset=d.asset WHERE d.snapshot<>''
) SELECT EXISTS(SELECT 1 FROM closure x JOIN organizations o ON o.id=x.id LEFT JOIN live_assets owner ON owner.id=o.asset AND owner.deleted=0 WHERE owner.id IS NULL OR owner.revision<>o.revision
 UNION ALL SELECT 1 FROM closure x JOIN dependencies d ON d.organization=x.id JOIN organizations owner ON owner.id=x.id LEFT JOIN live_assets a ON a.id=d.asset AND a.deleted=0 LEFT JOIN organization_current c ON c.generation=owner.generation AND c.asset=d.asset LEFT JOIN organizations target ON target.id=c.organization
 WHERE a.id IS NULL OR (d.revision<>0 AND a.revision<>d.revision) OR (d.snapshot<>'' AND (target.snapshot IS NULL OR target.snapshot<>d.snapshot OR target.revision<>a.revision)))`, id).Scan(&invalid)
	return invalid == 0, err
}

func (s *Store) CurrentOrganization(ctx context.Context, generation, asset string) (OrganizationVersion, error) {
	var v OrganizationVersion
	err := s.view(ctx, func(q queryer) error {
		err := q.QueryRowContext(ctx, "SELECT o.id,o.generation,o.asset,o.revision,o.snapshot,o.status,o.work_id,o.expected FROM organization_current c JOIN organizations o ON o.id=c.organization JOIN live_assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0 WHERE c.generation=? AND c.asset=?", generation, asset).Scan(&v.ID, &v.Generation, &v.Asset, &v.Revision, &v.Snapshot, &v.Status, &v.WorkID, &v.Expected)
		if err != nil {
			return err
		}
		valid, err := organizationInputsCurrent(ctx, q, v.ID)
		if err != nil {
			return err
		}
		if !valid {
			return ErrNotFound
		}
		return nil
	})
	return v, err
}

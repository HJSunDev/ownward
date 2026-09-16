package boundedstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"sort"

	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/viterin/vek/vek32"
)

const vectorDimensions = 512
const filterRowBytes = 264
const filterPageMembers = 56

type vectorHeader struct {
	Version           int
	Lo, Width, Center [vectorDimensions]float64
	Radius, MaxNorm   float64
}
type vectorMember struct {
	organization, asset string
	values              []float32
}
type VectorStats struct {
	Blocks, PrunedBlocks, Coarse, Fine, Exact, CorruptFilters int
	PayloadBytes                                              int64
}

func vectorNorm(v []float32) float64 {
	sum := 0.0
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}
func normalizeVector(v []float32) []float32 {
	out := append([]float32(nil), v...)
	n := vectorNorm(v)
	if n > 0 {
		vek32.MulNumber_Inplace(out, float32(1/n))
	}
	return out
}
func decodeVector(data, digest []byte) ([]float32, error) {
	if len(data) != vectorDimensions*4 || len(digest) != 32 {
		return nil, errors.New("原向量格式损坏")
	}
	actual := sha256.Sum256(data)
	if string(actual[:]) != string(digest) {
		return nil, errors.New("原向量校验失败")
	}
	v := make([]float32, vectorDimensions)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
		if math.IsNaN(float64(v[i])) || math.IsInf(float64(v[i]), 0) {
			return nil, errors.New("原向量包含非有限值")
		}
	}
	return normalizeVector(v), nil
}

func (s *Store) boundDelta(ctx context.Context) error {
	if err := s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM vector_delta WHERE NOT EXISTS(SELECT 1 FROM organizations o JOIN organization_current c ON c.organization=o.id JOIN generations g ON g.id=o.generation AND g.state<>'retired' JOIN assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0 WHERE o.id=vector_delta.organization)`)
		return err
	}); err != nil {
		return err
	}
	for {
		var count int
		var space string
		err := s.view(ctx, func(q queryer) error {
			if e := q.QueryRowContext(ctx, "SELECT count(*) FROM vector_delta").Scan(&count); e != nil {
				return e
			}
			if count < 1024 {
				return nil
			}
			return q.QueryRowContext(ctx, "SELECT space FROM vector_delta GROUP BY space ORDER BY count(*) DESC,space LIMIT 1").Scan(&space)
		})
		if err != nil {
			return err
		}
		if count < 1024 {
			return nil
		}
		packed, err := s.PackVectors(ctx, space, true)
		if err != nil {
			return err
		}
		if packed == 0 {
			return errors.New("向量增量归块未取得进展")
		}
	}
}

// PackVectors replaces at most one block and a bounded delta batch. New
// vectors remain queryable until the membership switch commits atomically.
func (s *Store) PackVectors(ctx context.Context, space string, force bool) (int, error) {
	s.vectorMu.Lock()
	defer s.vectorMu.Unlock()
	release, err := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 3*resourcebudget.MiB, false)
	if err != nil {
		return 0, err
	}
	defer release()
	var members []vectorMember
	old := ""
	err = s.view(ctx, func(q queryer) error {
		var count int
		if e := q.QueryRowContext(ctx, "SELECT count(*) FROM vector_delta WHERE space=?", space).Scan(&count); e != nil {
			return e
		}
		// Repack a depleted block even without new writes; otherwise coalesce a
		// partial block rather than creating one tiny block per small import.
		err := q.QueryRowContext(ctx, `SELECT id FROM (SELECT b.id,b.members,(SELECT count(*) FROM vector_members m JOIN organization_current c ON c.organization=m.organization JOIN organizations o ON o.id=m.organization JOIN generations g ON g.id=o.generation AND g.state<>'retired' JOIN assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0 WHERE m.block=b.id) AS live FROM vector_blocks b WHERE b.space=? AND b.state='active') WHERE members<1024 OR live*4<=members*3 ORDER BY (live*4<=members*3) DESC,members,id LIMIT 1`, space).Scan(&old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if count < 256 && !force {
			if old == "" {
				return nil
			}
			var live, total int
			if err = q.QueryRowContext(ctx, `SELECT b.members,(SELECT count(*) FROM vector_members m JOIN organization_current c ON c.organization=m.organization JOIN organizations o ON o.id=m.organization JOIN generations g ON g.id=o.generation AND g.state<>'retired' JOIN assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0 WHERE m.block=b.id) FROM vector_blocks b WHERE id=?`, old).Scan(&total, &live); err != nil {
				return err
			}
			if live*4 > total*3 {
				return nil
			}
		}
		rows, err := q.QueryContext(ctx, `SELECT v.organization,o.asset,v.data,v.digest FROM vectors v JOIN organizations o ON o.id=v.organization JOIN organization_current c ON c.organization=o.id JOIN generations g ON g.id=o.generation AND g.state<>'retired' JOIN assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0 WHERE v.organization IN (SELECT organization FROM vector_members WHERE block=? UNION SELECT organization FROM vector_delta WHERE space=?) ORDER BY CASE WHEN v.organization IN(SELECT organization FROM vector_members WHERE block=?) THEN 0 ELSE 1 END,v.organization LIMIT 1024`, old, space, old)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m vectorMember
			var data, digest []byte
			if err = rows.Scan(&m.organization, &m.asset, &data, &digest); err != nil {
				return err
			}
			m.values, err = decodeVector(data, digest)
			if err != nil {
				return err
			}
			members = append(members, m)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}
	if len(members) == 0 {
		if old != "" {
			err = s.write(ctx, func(tx *sql.Tx) error {
				var live int
				if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM vector_members m JOIN organization_current c ON c.organization=m.organization JOIN organizations o ON o.id=m.organization JOIN generations g ON g.id=o.generation AND g.state<>'retired' JOIN assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0 WHERE m.block=?`, old).Scan(&live); e != nil {
					return e
				}
				if live > 0 {
					return nil
				}
				if _, e := tx.ExecContext(ctx, "UPDATE vector_blocks SET state='retired' WHERE id=?", old); e != nil {
					return e
				}
				_, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim VALUES(?,'vector_block','empty')", old)
				return e
			})
			return 0, err
		}
		return 0, nil
	}
	h := vectorHeader{Version: 1}
	for d := 0; d < vectorDimensions; d++ {
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, m := range members {
			x := float64(m.values[d])
			lo = math.Min(lo, x)
			hi = math.Max(hi, x)
			h.Center[d] += x / float64(len(members))
		}
		h.Lo[d] = lo
		h.Width[d] = (hi - lo) / 256
	}
	coarse, fine := make([]byte, len(members)*filterRowBytes), make([]byte, len(members)*filterRowBytes)
	for j, m := range members {
		r4, r8, rr := 0.0, 0.0, 0.0
		for d, x32 := range m.values {
			x := float64(x32)
			code := 0
			if h.Width[d] > 0 {
				code = min(255, max(0, int(math.Floor((x-h.Lo[d])/h.Width[d]))))
			}
			shift := uint(4 * (d % 2))
			coarse[j*filterRowBytes+d/2] |= byte(code>>4) << shift
			fine[j*filterRowBytes+d/2] |= byte(code&15) << shift
			c4 := h.Lo[d] + float64((code>>4)*16+8)*h.Width[d]
			c8 := h.Lo[d] + (float64(code)+0.5)*h.Width[d]
			r4 += (x - c4) * (x - c4)
			r8 += (x - c8) * (x - c8)
			rr += (x - h.Center[d]) * (x - h.Center[d])
		}
		binary.LittleEndian.PutUint64(coarse[j*filterRowBytes+256:], math.Float64bits(math.Nextafter(math.Sqrt(r4)+1e-12, math.Inf(1))))
		binary.LittleEndian.PutUint64(fine[j*filterRowBytes+256:], math.Float64bits(math.Nextafter(math.Sqrt(r8)+1e-12, math.Inf(1))))
		h.Radius = math.Max(h.Radius, math.Nextafter(math.Sqrt(rr)+1e-12, math.Inf(1)))
		h.MaxNorm = math.Max(h.MaxNorm, vectorNorm(m.values))
	}
	id, err := newID()
	if err != nil {
		return 0, err
	}
	data, err := json.Marshal(h)
	if err != nil {
		return 0, err
	}
	digest := sha256.Sum256(data)
	if err = s.write(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO vector_blocks VALUES(?,?,?,'staging',?,?,?)", id, space, vectorDimensions, data, digest[:], len(members))
		return e
	}); err != nil {
		return 0, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = s.write(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
				_, e := tx.ExecContext(context.WithoutCancel(ctx), "INSERT OR IGNORE INTO derived_reclaim VALUES(?,'vector_block','aborted')", id)
				return e
			})
		}
	}()
	for page, start := 0, 0; start < len(members); page, start = page+1, start+filterPageMembers {
		end := min(len(members), start+filterPageMembers)
		if err = s.write(ctx, func(tx *sql.Tx) error {
			for level, plane := range [][]byte{coarse, fine} {
				part := plane[start*filterRowBytes : end*filterRowBytes]
				sum := sha256.Sum256(part)
				if _, e := tx.ExecContext(ctx, "INSERT INTO vector_filter_pages VALUES(?,?,?,?,?)", id, level, page, part, sum[:]); e != nil {
					return e
				}
			}
			for j := start; j < end; j++ {
				if _, e := tx.ExecContext(ctx, "INSERT INTO vector_members VALUES(?,?,?)", id, j, members[j].organization); e != nil {
					return e
				}
			}
			return nil
		}); err != nil {
			return 0, err
		}
	}
	err = s.write(ctx, func(tx *sql.Tx) error {
		if old != "" {
			var state string
			if e := tx.QueryRowContext(ctx, "SELECT state FROM vector_blocks WHERE id=?", old).Scan(&state); e != nil {
				return e
			}
			if state != "active" {
				return errors.New("待归块版本已变化")
			}
			if _, e := tx.ExecContext(ctx, "UPDATE vector_blocks SET state='retired' WHERE id=?", old); e != nil {
				return e
			}
			if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim VALUES(?,'vector_block','replaced')", old); e != nil {
				return e
			}
		}
		if _, e := tx.ExecContext(ctx, "UPDATE vector_blocks SET state='active' WHERE id=?", id); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "DELETE FROM vector_delta WHERE organization IN(SELECT organization FROM vector_members WHERE block=?)", id)
		return e
	})
	complete = err == nil
	return len(members), err
}

func (s *Store) VectorSearch(ctx context.Context, generation, space string, query []float32, contexts []domain.Context, limit int) ([]Ranked, VectorStats, error) {
	stats := VectorStats{}
	if limit <= 0 {
		limit = 10
	}
	if limit > 400 {
		return nil, stats, errors.New("向量候选数量超过工具预算")
	}
	if len(query) != vectorDimensions {
		return nil, stats, nil
	}
	for _, x := range query {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return nil, stats, nil
		}
	}
	if vectorNorm(query) == 0 {
		return nil, stats, nil
	}
	release, err := resourcebudget.FromContext(ctx, s.budget).Acquire(ctx, 2*resourcebudget.MiB, false)
	if err != nil {
		return nil, stats, err
	}
	defer release()
	qv := normalizeVector(query)
	qn := vectorNorm(qv)
	best := []Ranked{}
	threshold := func() float64 {
		if len(best) < limit {
			return 0
		}
		return best[len(best)-1].Score
	}
	add := func(id string, score float64) {
		if score <= 0 {
			return
		}
		for _, hit := range best {
			if hit.ID == id {
				return
			}
		}
		best = append(best, Ranked{ID: id, Score: score, Signals: []string{"semantic"}})
		sort.Slice(best, func(a, b int) bool {
			if best[a].Score == best[b].Score {
				return best[a].ID < best[b].ID
			}
			return best[a].Score > best[b].Score
		})
		if len(best) > limit {
			best = best[:limit]
		}
	}
	err = s.view(ctx, func(q queryer) error {
		valid := func(org string) (string, bool, error) {
			var id string
			e := q.QueryRowContext(ctx, `SELECT o.asset FROM organizations o JOIN organization_current c ON c.organization=o.id AND c.generation=? JOIN assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0 WHERE o.id=?`, generation, org).Scan(&id)
			if errors.Is(e, sql.ErrNoRows) {
				return "", false, nil
			}
			if e != nil {
				return "", false, e
			}
			ok, e := organizationInputsCurrent(ctx, q, org)
			if e != nil || !ok {
				return id, false, e
			}
			ok, e = matchStoredContexts(ctx, q, "organization_contexts", "organization", org, contexts)
			return id, ok, e
		}
		eval := func(org string) error {
			id, ok, e := valid(org)
			if e != nil || !ok {
				return e
			}
			var data, digest []byte
			if e = q.QueryRowContext(ctx, "SELECT data,digest FROM vectors WHERE organization=? AND space=?", org, space).Scan(&data, &digest); e != nil {
				return e
			}
			v, e := decodeVector(data, digest)
			if e != nil {
				return e
			}
			stats.Exact++
			stats.PayloadBytes += int64(len(data))
			add(id, float64(vek32.Dot(qv, v)))
			return nil
		}
		rows, e := q.QueryContext(ctx, "SELECT organization FROM vector_delta WHERE space=? ORDER BY organization", space)
		if e != nil {
			return e
		}
		for rows.Next() {
			var org string
			if e = rows.Scan(&org); e != nil {
				rows.Close()
				return e
			}
			if e = eval(org); e != nil {
				rows.Close()
				return e
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		// Block headers and sort keys live in a connection-local B-tree; the
		// directory never becomes a corpus-sized in-memory slice.
		if _, e = q.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS vector_order(id TEXT PRIMARY KEY,priority REAL) WITHOUT ROWID; DELETE FROM vector_order;"); e != nil {
			return e
		}
		rows, e = q.QueryContext(ctx, "SELECT id,header,digest FROM vector_blocks WHERE space=? AND state='active' ORDER BY id", space)
		if e != nil {
			return e
		}
		for rows.Next() {
			var id string
			var data, digest []byte
			if e = rows.Scan(&id, &data, &digest); e != nil {
				rows.Close()
				return e
			}
			priority := math.MaxFloat64
			var h vectorHeader
			if checkedBlob(data, digest) && json.Unmarshal(data, &h) == nil && h.Version == 1 {
				priority = 0
				for d, x := range qv {
					priority += float64(x) * h.Center[d]
				}
			}
			if _, e = q.ExecContext(ctx, "INSERT INTO vector_order VALUES(?,?)", id, priority); e != nil {
				rows.Close()
				return e
			}
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		rows, e = q.QueryContext(ctx, "SELECT b.id,b.header,b.digest,b.members FROM vector_order o JOIN vector_blocks b ON b.id=o.id ORDER BY o.priority DESC,o.id")
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var data, digest []byte
			var count int
			if e = rows.Scan(&id, &data, &digest, &count); e != nil {
				return e
			}
			stats.Blocks++
			stats.PayloadBytes += int64(len(data))
			var h vectorHeader
			good := checkedBlob(data, digest) && json.Unmarshal(data, &h) == nil && h.Version == 1 && count > 0 && count <= 1024
			exactBlock := func() error {
				if err := s.write(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO derived_reclaim VALUES(?,'vector_filter','corrupt')", id)
					return err
				}); err != nil {
					return err
				}
				members, e := q.QueryContext(ctx, "SELECT organization FROM vector_members WHERE block=? ORDER BY ordinal", id)
				if e != nil {
					return e
				}
				defer members.Close()
				for members.Next() {
					var org string
					if e = members.Scan(&org); e != nil {
						return e
					}
					if e = eval(org); e != nil {
						return e
					}
				}
				return members.Err()
			}
			if !good {
				stats.CorruptFilters++
				if e = exactBlock(); e != nil {
					return e
				}
				continue
			}
			epsilon := (1024*math.Pow(2, -24))/(1-1024*math.Pow(2, -24))*qn*h.MaxNorm + 1e-10
			center := 0.0
			for d, x := range qv {
				center += float64(x) * h.Center[d]
			}
			if center+qn*h.Radius+epsilon < threshold() {
				stats.PrunedBlocks++
				continue
			}
			var coarse []byte
			for level := 0; level < 1; level++ {
				pages, e := q.QueryContext(ctx, "SELECT data,digest FROM vector_filter_pages WHERE block=? AND level=? ORDER BY page", id, level)
				if e != nil {
					return e
				}
				for pages.Next() {
					var data, digest []byte
					if e = pages.Scan(&data, &digest); e != nil {
						pages.Close()
						return e
					}
					if !checkedBlob(data, digest) {
						good = false
					}
					stats.PayloadBytes += int64(len(data))
					coarse = append(coarse, data...)
				}
				e = pages.Err()
				pages.Close()
				if e != nil {
					return e
				}
			}
			if !good || len(coarse) != count*filterRowBytes {
				stats.CorruptFilters++
				if e = exactBlock(); e != nil {
					return e
				}
				continue
			}
			type candidate struct {
				ordinal       int
				approx, upper float64
			}
			candidates := make([]candidate, 0, count)
			for j := 0; j < count; j++ {
				row := coarse[j*filterRowBytes : (j+1)*filterRowBytes]
				approx := 0.0
				for d, x := range qv {
					code := (row[d/2] >> uint(4*(d%2))) & 15
					approx += float64(x) * (h.Lo[d] + float64(int(code)*16+8)*h.Width[d])
				}
				radius := math.Float64frombits(binary.LittleEndian.Uint64(row[256:]))
				upper := approx + qn*radius + epsilon
				candidates = append(candidates, candidate{j, approx, upper})
				stats.Coarse++
			}
			sort.Slice(candidates, func(a, b int) bool { return candidates[a].approx > candidates[b].approx })
			finePages := make(map[int][]byte)
			for _, c := range candidates {
				if c.upper < threshold() {
					continue
				}
				page := c.ordinal / filterPageMembers
				fine, loaded := finePages[page]
				if !loaded {
					var digest []byte
					e := q.QueryRowContext(ctx, "SELECT data,digest FROM vector_filter_pages WHERE block=? AND level=1 AND page=?", id, page).Scan(&fine, &digest)
					if e != nil && !errors.Is(e, sql.ErrNoRows) {
						return e
					}
					stats.PayloadBytes += int64(len(fine))
					if e != nil || !checkedBlob(fine, digest) || len(fine) != min(filterPageMembers, count-page*filterPageMembers)*filterRowBytes {
						stats.CorruptFilters++
						if e = exactBlock(); e != nil {
							return e
						}
						break
					}
					finePages[page] = fine
				}
				at := (c.ordinal % filterPageMembers) * filterRowBytes
				row := fine[at : at+filterRowBytes]
				approx := c.approx
				for d, x := range qv {
					code := (row[d/2] >> uint(4*(d%2))) & 15
					approx += float64(x) * (float64(code) - 7.5) * h.Width[d]
				}
				radius := math.Float64frombits(binary.LittleEndian.Uint64(row[256:]))
				stats.Fine++
				if math.Min(c.upper, approx+qn*radius+epsilon) < threshold() {
					continue
				}
				var org string
				if e = q.QueryRowContext(ctx, "SELECT organization FROM vector_members WHERE block=? AND ordinal=?", id, c.ordinal).Scan(&org); e != nil {
					return e
				}
				if e = eval(org); e != nil {
					return e
				}
			}
		}
		return rows.Err()
	})
	return best, stats, err
}
func checkedBlob(data, digest []byte) bool {
	sum := sha256.Sum256(data)
	return string(sum[:]) == string(digest)
}

package boundedstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
)

type adjacencyPosition struct {
	Direction int
	Sequence  int64
	Ordinal   int
}
type navigationPosition struct {
	ID    string
	Depth int
	Via   string
	After adjacencyPosition
}
type navigationState struct {
	Queue []navigationPosition
	Types []string
	Depth int
}

// Expiry, reclamation or an authority-version change invalidates a navigation
// position. Storage failures and corrupt cursor state are separate errors.
var ErrNavigationExpired = errors.New("导航接续已失效，请从原资产重新导航")

func nextAdjacent(ctx context.Context, q queryer, generation, id string, after adjacencyPosition) (Edge, bool, adjacencyPosition, error) {
	var owner, org string
	var grounded bool
	var data []byte
	var next adjacencyPosition
	err := q.QueryRowContext(ctx, `SELECT o.asset,o.id,l.grounded,l.data,l.direction,p.sequence,l.ordinal FROM (
 SELECT organization,ordinal,grounded,data,0 AS direction FROM graph_links WHERE source=? AND grounded=0
 UNION ALL SELECT organization,ordinal,grounded,data,1 FROM graph_links WHERE target=? AND grounded=0
 UNION ALL SELECT organization,ordinal,grounded,data,2 FROM graph_links WHERE grounded=1 AND (source=? OR target=?)
 ) l JOIN organizations o ON o.id=l.organization JOIN organization_publications p ON p.organization=o.id JOIN organization_current c ON c.organization=o.id AND c.generation=? JOIN live_assets a ON a.id=o.asset AND a.revision=o.revision AND a.deleted=0
 WHERE l.direction>? OR (l.direction=? AND (p.sequence>? OR (p.sequence=? AND l.ordinal>?)))
 ORDER BY l.direction,p.sequence,l.ordinal LIMIT 1`, id, id, id, id, generation, after.Direction, after.Direction, after.Sequence, after.Sequence, after.Ordinal).Scan(&owner, &org, &grounded, &data, &next.Direction, &next.Sequence, &next.Ordinal)
	if err != nil {
		return Edge{}, false, next, err
	}
	edge, valid, err := graphEdge(ctx, q, generation, owner, org, grounded, data)
	return edge, valid, next, err
}

// NavigatePage uses the same bounded DFS and edge budget as the original
// navigation. Continuations retain positions, not graph contents or open reads.
func (s *Store) NavigatePage(ctx context.Context, generation string, start, types []string, depth, limit int) (NavigationPage, error) {
	page := NavigationPage{}
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 100)
	if depth <= 0 {
		depth = 1
	}
	depth = min(depth, 5)
	state := navigationState{Types: types, Depth: depth}
	err := s.view(ctx, func(q queryer) error {
		var assetEpoch, derivedEpoch uint64
		if err := q.QueryRowContext(ctx, "SELECT value FROM store_meta WHERE key='asset_epoch'").Scan(&assetEpoch); err != nil {
			return err
		}
		err := q.QueryRowContext(ctx, "SELECT epoch FROM derived_state WHERE singleton=1").Scan(&derivedEpoch)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if len(start) == 1 && strings.HasPrefix(start[0], "nav2:") {
			var data []byte
			err = q.QueryRowContext(ctx, "SELECT state FROM navigation_cursors WHERE id=? AND principal=? AND generation=? AND asset_epoch=? AND derived_epoch=? AND expires>?", strings.TrimPrefix(start[0], "nav2:"), contract.AuthenticationDigest(ctx), generation, assetEpoch, derivedEpoch, time.Now().Unix()).Scan(&data)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNavigationExpired
			}
			if err != nil {
				return err
			}
			if json.Unmarshal(data, &state) != nil || state.Depth < 1 || state.Depth > 5 || len(state.Queue) > 105 {
				return errors.New("导航接续无效")
			}
		} else {
			if len(start) > 100 {
				return errors.New("导航起点超过单次范围")
			}
			seen := map[string]bool{}
			for n := len(start) - 1; n >= 0; n-- {
				id := strings.TrimSpace(start[n])
				if id != "" && !seen[id] {
					seen[id] = true
					state.Queue = append(state.Queue, navigationPosition{ID: id, After: adjacencyPosition{Direction: -1, Ordinal: -1}})
				}
			}
		}
		allowed := map[string]bool{}
		for _, typ := range state.Types {
			allowed[strings.TrimSpace(typ)] = true
		}
		seen := map[string]bool{}
		for len(state.Queue) > 0 && len(page.Edges) < limit && page.Examined < 512 {
			pos := &state.Queue[len(state.Queue)-1]
			if pos.Depth >= state.Depth {
				state.Queue = state.Queue[:len(state.Queue)-1]
				continue
			}
			edge, valid, after, e := nextAdjacent(ctx, q, generation, pos.ID, pos.After)
			if errors.Is(e, sql.ErrNoRows) {
				state.Queue = state.Queue[:len(state.Queue)-1]
				continue
			}
			if e != nil {
				return e
			}
			pos.After = after
			page.Examined++
			if !valid || (len(allowed) > 0 && !allowed[edge.Type]) {
				continue
			}
			key := edge.SourceID + "\x00" + edge.Type + "\x00" + edge.TargetID + "\x00" + edge.Evidence
			if edge.Grounded != nil {
				key = edge.OwnerID + "\x00" + edge.Grounded.ID
			}
			digest := sha256.Sum256([]byte(key))
			key = hex.EncodeToString(digest[:16])
			if key == pos.Via || seen[key] {
				continue
			}
			seen[key] = true
			edge.Depth = pos.Depth + 1
			next := edge.TargetID
			if next == pos.ID {
				next = edge.SourceID
			}
			page.Edges = append(page.Edges, edge)
			onPath := false
			for _, ancestor := range state.Queue {
				if ancestor.ID == next {
					onPath = true
					break
				}
			}
			if !onPath && edge.Depth < state.Depth {
				state.Queue = append(state.Queue, navigationPosition{ID: next, Depth: edge.Depth, Via: key, After: adjacencyPosition{Direction: -1, Ordinal: -1}})
			}
		}
		if len(state.Queue) > 0 {
			data, e := json.Marshal(state)
			if e != nil {
				return e
			}
			if len(data) > 65536 {
				return errors.New("导航接续超过工作预算")
			}
			id, e := newID()
			if e != nil {
				return e
			}
			if e = s.write(context.WithValue(ctx, snapshotWriteKey{}, true), func(tx *sql.Tx) error {
				_, e := tx.ExecContext(ctx, "INSERT INTO navigation_cursors VALUES(?,?,?,?,?,?,?)", id, contract.AuthenticationDigest(ctx), generation, assetEpoch, derivedEpoch, time.Now().Add(30*time.Minute).Unix(), data)
				return e
			}); e != nil {
				return e
			}
			page.Continuation = "nav2:" + id
			page.Incomplete = true
		}
		return nil
	})
	return page, err
}

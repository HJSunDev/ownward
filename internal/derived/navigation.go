package derived

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

const navigationPrefix = "nav1:"
const navigationWorkBudget = 512

type NavigationPage struct {
	Edges        []Edge
	Continuation string
	Examined     int
	Incomplete   bool
}
type navigationPosition struct {
	ID            string
	Depth, Offset int
	Via           string
}
type navigationCursor struct {
	Epoch    uint64
	Instance string
	Queue    []navigationPosition
	Types    []string
	Depth    int
}

// The cursor carries only navigation state. Every page validates the current
// index incarnation and publications; it never embeds evidence or graph data.
func (i *Index) NavigatePage(start, types []string, depth, limit int) (NavigationPage, error) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if depth <= 0 {
		depth = 1
	}
	if depth > 5 {
		depth = 5
	}
	cursor := navigationCursor{Epoch: i.navigationEpoch, Instance: i.navigationInstance, Types: types, Depth: depth}
	if len(start) == 1 && strings.HasPrefix(start[0], navigationPrefix) {
		if len(start[0]) > 64*1024 {
			return NavigationPage{}, errors.New("导航接续无效")
		}
		data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(start[0], navigationPrefix))
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Depth < 1 || cursor.Depth > 5 || len(cursor.Queue) > 105 {
			return NavigationPage{}, errors.New("导航接续无效")
		}
		if cursor.Epoch != i.navigationEpoch || cursor.Instance != i.navigationInstance {
			return NavigationPage{}, errors.New("资料组织已变化，请从原资产重新导航")
		}
	} else {
		if len(start) > 100 {
			return NavigationPage{}, errors.New("导航起点超过单次范围")
		}
		seen := map[string]bool{}
		for n := len(start) - 1; n >= 0; n-- {
			id := start[n]
			id = strings.TrimSpace(id)
			if id != "" && !seen[id] {
				cursor.Queue = append(cursor.Queue, navigationPosition{ID: id})
				seen[id] = true
			}
		}
	}
	allowed := map[string]bool{}
	for _, typ := range cursor.Types {
		allowed[strings.TrimSpace(typ)] = true
	}
	page := NavigationPage{}
	seenEdges := map[string]bool{}
	for len(cursor.Queue) > 0 && len(page.Edges) < limit && page.Examined < navigationWorkBudget {
		position := &cursor.Queue[len(cursor.Queue)-1]
		forward, reverse, grounded := i.forward[position.ID], i.reverse[position.ID], i.organized.adjacent[position.ID]
		if position.Offset < 0 || position.Depth < 0 || position.Depth > cursor.Depth {
			return NavigationPage{}, errors.New("导航位置无效")
		}
		if position.Depth >= cursor.Depth || position.Offset >= len(forward)+len(reverse)+len(grounded) {
			cursor.Queue = cursor.Queue[:len(cursor.Queue)-1]
			continue
		}
		var edge Edge
		valid := true
		offset := position.Offset
		position.Offset++
		page.Examined++
		switch {
		case offset < len(forward):
			edge = forward[offset]
		case offset < len(forward)+len(reverse):
			edge = reverse[offset-len(forward)]
		default:
			edge, valid = i.groundedEdgeLocked(grounded[offset-len(forward)-len(reverse)])
		}
		if !valid || (len(allowed) > 0 && !allowed[edge.Type]) {
			continue
		}
		key := edge.SourceID + "\x00" + edge.Type + "\x00" + edge.TargetID + "\x00" + edge.Evidence
		if edge.Grounded != nil {
			key = edge.OwnerID + "\x00" + edge.Grounded.ID
		}
		digest := sha256.Sum256([]byte(key))
		key = hex.EncodeToString(digest[:16])
		if key == position.Via || seenEdges[key] {
			continue
		}
		seenEdges[key] = true
		edge.Depth = position.Depth + 1
		next := edge.TargetID
		if next == position.ID {
			next = edge.SourceID
		}
		page.Edges = append(page.Edges, edge)
		onPath := false
		for _, ancestor := range cursor.Queue {
			if ancestor.ID == next {
				onPath = true
				break
			}
		}
		if !onPath && edge.Depth < cursor.Depth {
			cursor.Queue = append(cursor.Queue, navigationPosition{ID: next, Depth: edge.Depth, Via: key})
		}
	}
	if len(cursor.Queue) > 0 {
		data, _ := json.Marshal(cursor)
		page.Continuation = navigationPrefix + base64.RawURLEncoding.EncodeToString(data)
		page.Incomplete = true
	}
	return page, nil
}

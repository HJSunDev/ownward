package boundedstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
)

func recordOwnerAccess(ctx context.Context, tx *sql.Tx, kind, id, status string, fact contract.OwnerAccessFact) error {
	b, e := json.Marshal(fact)
	if e != nil {
		return e
	}
	r, e := tx.ExecContext(ctx, "INSERT INTO owner_events(kind,asset,revision,operation,status,at) VALUES(?,'',0,?,?,?)", kind, id, status, time.Now().UnixMilli())
	if e != nil {
		return e
	}
	sequence, e := r.LastInsertId()
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, "INSERT INTO owner_event_access VALUES(?,?)", sequence, b)
	return e
}

// Compare original authority records, not their effective pending projections.
// Credential issuance, acknowledgement, cleanup and retries are not decisions.
// Cancellation is recorded by controlItem, including the owner-work transaction.
func recordAccessDecisions(ctx context.Context, tx *sql.Tx, before, after *contract.AccessState) error {
	previous := map[string]contract.Enrollment{}
	if before != nil {
		for _, e := range before.Enrollments {
			previous[e.ID] = e
		}
	}
	for _, e := range after.Enrollments {
		old := previous[e.ID]
		if e.Status != "approved" && e.Status != "declined" && e.Status != "cancelled" {
			continue
		}
		if old.Status == e.Status && old.Approver == e.Approver && old.ApproverRevision == e.ApproverRevision {
			continue
		}
		if err := recordOwnerAccess(ctx, tx, "enrollment", e.ID, e.Status, contract.OwnerAccessFact{Subject: e.Name, Permissions: e.Permissions}); err != nil {
			return err
		}
	}
	h := after.Handoff
	if h == nil {
		return nil
	}
	var old contract.Handoff
	if before != nil && before.Handoff != nil && before.Handoff.ID == h.ID {
		old = *before.Handoff
	}
	status := ""
	if h.Phase == "active" && old.Phase != "active" {
		status = "completed"
	} else if (h.ApprovalStatus == "approved" || h.ApprovalStatus == "declined") &&
		(old.ApprovalStatus != h.ApprovalStatus || old.Approver != h.Approver || old.ApproverRevision != h.ApproverRevision) {
		status = h.ApprovalStatus
	}
	if status != "" {
		return recordOwnerAccess(ctx, tx, "handoff", h.ID, status, contract.OwnerAccessFact{Subject: h.Target.Endpoint})
	}
	return nil
}

type OwnerHistoryRow struct {
	Key       string
	Kind      string
	Status    string
	At        time.Time
	Operation *contract.ManagementReceipt
	Access    *contract.OwnerAccessFact
}

// One retained history across all three decision sources. Public access facts
// survive expiry/replacement of the short-lived authority record, but cannot
// grant rights. Management scope remains a reference to its original request.
func (s *Store) OwnerHistory(ctx context.Context, after string, limit int) (out []OwnerHistoryRow, next string, err error) {
	if limit < 1 || limit > contract.OwnerPageLimit {
		return nil, "", errors.New("历史分页范围无效")
	}
	var at int64
	key := ""
	if after != "" {
		stamp, tail, ok := strings.Cut(after, ":")
		var e error
		at, e = strconv.ParseInt(stamp, 10, 64)
		if !ok || e != nil {
			return nil, "", contract.ErrOwnerRefresh
		}
		key = tail
	}
	err = s.view(ctx, func(q queryer) error {
		if _, e := requireOwner(ctx, q, false); e != nil {
			return e
		}
		cutoff := time.Now().Add(-OwnerHistoryRetention).UnixMilli()
		rows, e := q.QueryContext(ctx, `WITH facts AS (
 SELECT 'm:'||t.id AS key,'management' AS kind,t.updated AS at,t.state AS status,a.data
 FROM owner_operation_times t JOIN authority_items a ON a.kind='operations' AND a.id=t.id
 WHERE t.state IN ('completed','declined','superseded') AND t.updated>=?
 UNION ALL
 SELECT 'a:'||e.sequence,e.kind,e.at,e.status,d.data
 FROM owner_events e JOIN owner_event_access d ON d.sequence=e.sequence WHERE e.at>=?
 ), retained AS (SELECT * FROM facts ORDER BY at DESC,key DESC LIMIT ?),
 page AS (SELECT * FROM retained WHERE ?='' OR at<? OR (at=? AND key<?) ORDER BY at DESC,key DESC LIMIT ?)
 SELECT key,kind,at,status,data FROM (
 SELECT *,sum(length(data)) OVER(ORDER BY at DESC,key DESC) AS bytes FROM page
 ) WHERE bytes<=524288 ORDER BY at DESC,key DESC`, cutoff, cutoff, OwnerEventLimit, after, at, at, key, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v OwnerHistoryRow
			var stamp int64
			var b []byte
			if e = rows.Scan(&v.Key, &v.Kind, &stamp, &v.Status, &b); e != nil {
				return e
			}
			v.At = time.UnixMilli(stamp).UTC()
			if v.Kind == "management" {
				v.Operation = new(contract.ManagementReceipt)
				e = json.Unmarshal(b, v.Operation)
			} else {
				v.Access = new(contract.OwnerAccessFact)
				e = json.Unmarshal(b, v.Access)
			}
			if e != nil {
				return e
			}
			out = append(out, v)
			next = strconv.FormatInt(stamp, 10) + ":" + v.Key
		}
		return rows.Err()
	})
	s.wakeMaintenance()
	return
}

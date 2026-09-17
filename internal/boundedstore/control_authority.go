package boundedstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

const authoritySchema = `CREATE TABLE IF NOT EXISTS authority_header(singleton INTEGER PRIMARY KEY CHECK(singleton=1),data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS authority_items(kind TEXT NOT NULL,id TEXT NOT NULL,sequence INTEGER NOT NULL,state TEXT NOT NULL,data BLOB NOT NULL,PRIMARY KEY(kind,id)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS authority_state ON authority_items(kind,state,id);`

type ControlAuthority struct{ store *Store }

var _ contract.ControlAuthority = (*ControlAuthority)(nil)
var _ contract.SelectedControlAuthority = (*ControlAuthority)(nil)

// OpenControlAuthority converts the control snapshot once. Thereafter decisions
// load only their principal, operation and bounded in-flight handoff records.
func (s *Store) OpenControlAuthority(ctx context.Context, initial contract.ControlState) (*ControlAuthority, error) {
	if e := initial.Validate(); e != nil {
		return nil, e
	}
	if _, e := s.writer.ExecContext(ctx, authoritySchema); e != nil {
		return nil, e
	}
	var exists bool
	if e := s.writer.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM authority_header)").Scan(&exists); e != nil {
		return nil, e
	}
	if !exists {
		_, r, e := s.OpenControl(ctx, "authority")
		if errors.Is(e, sql.ErrNoRows) {
			b, _ := json.Marshal(initial)
			if _, e = s.writer.ExecContext(ctx, "INSERT INTO authority_header VALUES(1,?)", b); e != nil {
				return nil, e
			}
		} else if e != nil {
			return nil, e
		} else {
			doc, e := streamjson.Parse(ctx, s.directory, r, s.budget, 256*resourcebudget.MiB)
			r.Close()
			if e != nil {
				return nil, e
			}
			e = s.splitControl(ctx, doc.Root())
			doc.Close()
			if e != nil {
				return nil, e
			}
		}
	}
	// The migration changes the physical implementation identity, not ownership.
	e := s.write(ctx, func(tx *sql.Tx) error {
		var b []byte
		if e := tx.QueryRowContext(ctx, "SELECT data FROM authority_header WHERE singleton=1").Scan(&b); e != nil {
			return e
		}
		var h contract.ControlState
		if e := json.Unmarshal(b, &h); e != nil {
			return e
		}
		if exists && (h.ActiveComposition != initial.ActiveComposition || h.ActiveKernelGeneration != initial.ActiveKernelGeneration) {
			return errors.New("存储的组合身份不兼容，需要显式迁移")
		}
		h.ActiveComposition, h.ActiveKernelGeneration = initial.ActiveComposition, initial.ActiveKernelGeneration
		b, e := json.Marshal(h)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "UPDATE authority_header SET data=? WHERE singleton=1", b)
		return e
	})
	if e != nil {
		return nil, e
	}
	return &ControlAuthority{s}, nil
}

func controlItem(ctx context.Context, tx *sql.Tx, kind, id, state string, data []byte) error {
	if id == "" || len(data) > 256*1024 {
		return errors.New("控制决定身份或大小无效")
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO authority_items SELECT ?,?,coalesce(max(sequence)+1,1),?,? FROM authority_items WHERE kind=? ON CONFLICT(kind,id) DO UPDATE SET state=excluded.state,data=excluded.data`, kind, id, state, data, kind)
	return e
}

func (s *Store) splitControl(ctx context.Context, n streamjson.Node) error {
	var h contract.ControlState
	for _, p := range []struct {
		k string
		v any
	}{{"schema", &h.Schema}, {"revision", &h.Revision}, {"active_composition", &h.ActiveComposition}, {"active_kernel_generation", &h.ActiveKernelGeneration}} {
		if e := nodeDecode(n, p.k, p.v); e != nil {
			return e
		}
	}
	info, _, e := n.Field("information_control")
	if e != nil {
		return e
	}
	if info.Kind == '{' {
		h.InformationControl = &contract.InformationControlState{}
		for _, p := range []struct {
			k string
			v any
		}{{"system_id", &h.InformationControl.SystemID}, {"owner_id", &h.InformationControl.OwnerID}, {"deletion_revision", &h.InformationControl.DeletionRevision}} {
			if e = nodeDecode(info, p.k, p.v); e != nil {
				return e
			}
		}
		for _, k := range []string{"principals", "operations"} {
			if e = visitArray(info, k, func(v streamjson.Node) error {
				var id, state string
				var data []byte
				if k == "principals" {
					var p contract.Principal
					if e := v.DecodeSmall(&p, 256*1024); e != nil {
						return e
					}
					id = p.ID
					data, e = json.Marshal(p)
				} else {
					var op contract.ManagementReceipt
					if e := v.DecodeSmall(&op, 256*1024); e != nil {
						return e
					}
					id, state = op.Request.ID, op.Status
					data, e = json.Marshal(op)
				}
				if e != nil {
					return e
				}
				return s.write(ctx, func(tx *sql.Tx) error { return controlItem(ctx, tx, k, id, state, data) })
			}); e != nil {
				return e
			}
		}
	}
	access, _, e := n.Field("access")
	if e != nil {
		return e
	}
	if access.Kind == '{' {
		h.Access = &contract.AccessState{}
		if e = nodeDecode(access, "enrollments", &h.Access.Enrollments); e != nil {
			return e
		}
		if e = nodeDecode(access, "handoff", &h.Access.Handoff); e != nil {
			return e
		}
		if e = visitArray(access, "cancelled", func(v streamjson.Node) error {
			var handoff contract.Handoff
			if e := v.DecodeSmall(&handoff, 65536); e != nil {
				return e
			}
			state := "pending"
			if handoff.Cleaned {
				state = "cleaned"
			}
			data, e := json.Marshal(handoff)
			if e != nil {
				return e
			}
			return s.write(ctx, func(tx *sql.Tx) error { return controlItem(ctx, tx, "cancelled", handoff.ID, state, data) })
		}); e != nil {
			return e
		}
	}
	data, e := json.Marshal(h)
	if e != nil {
		return e
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, "INSERT INTO authority_header VALUES(1,?)", data); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, "INSERT OR IGNORE INTO reclaim_jobs SELECT payload,'control_migrated' FROM control_records WHERE key='authority'"); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, "DELETE FROM control_records WHERE key='authority'")
		return e
	})
}

func (a *ControlAuthority) ReadControl() contract.ControlState {
	s, e := a.ReadSelectedControl(contract.ControlSelection{})
	s.ReadError = e
	return s
}
func (a *ControlAuthority) ReadSelectedControl(selection contract.ControlSelection) (contract.ControlState, error) {
	ctx := context.Background()
	var out contract.ControlState
	done, e := a.store.budget.Acquire(ctx, 4*resourcebudget.MiB, true)
	if e != nil {
		return out, e
	}
	defer done()
	e = a.store.view(ctx, func(q queryer) error {
		var data []byte
		if e := q.QueryRowContext(ctx, "SELECT data FROM authority_header WHERE singleton=1").Scan(&data); e != nil {
			return e
		}
		if e := json.Unmarshal(data, &out); e != nil {
			return e
		}
		if out.InformationControl == nil {
			return nil
		}
		ids := map[string]bool{out.InformationControl.OwnerID: true}
		if selection.Principal != "" {
			ids[selection.Principal] = true
		}
		if selection.Credential != "" {
			var id string
			e := q.QueryRowContext(ctx, "SELECT id FROM access_principals WHERE credential=?", selection.Credential).Scan(&id)
			if e == nil {
				ids[id] = true
			} else if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		if out.Access != nil {
			for _, p := range out.Access.Enrollments {
				if p.ID != selection.Enrollment {
					continue
				}
				ids[p.Manager] = true
				if p.Principal != "" {
					ids[p.Principal] = true
				}
			}
			rows, e := q.QueryContext(ctx, "SELECT data FROM authority_items WHERE kind='cancelled' AND (state='pending' OR id=?) ORDER BY id=? DESC,sequence LIMIT 64", selection.Handoff, selection.Handoff)
			if e != nil {
				return e
			}
			for rows.Next() {
				var b []byte
				var h contract.Handoff
				if e = rows.Scan(&b); e == nil {
					e = json.Unmarshal(b, &h)
				}
				if e != nil {
					rows.Close()
					return e
				}
				out.Access.Cancelled = append(out.Access.Cancelled, h)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
		}
		limit := selection.Limit
		if limit < 1 || limit > 64 {
			limit = 64
		}
		if selection.Operation != "" || selection.Pending != "" {
			rows, e := q.QueryContext(ctx, `SELECT data FROM (SELECT data,id,sum(length(data)) OVER(ORDER BY id=? DESC,id) AS bytes FROM authority_items WHERE kind='operations' AND (id=? OR (?='cleaning' AND state IN ('stopping','cleaning')) OR (?='approval' AND state='awaiting_approval')) AND id>?) WHERE bytes<=524288 ORDER BY id=? DESC,id LIMIT ?`, selection.Operation, selection.Operation, selection.Pending, selection.Pending, selection.After, selection.Operation, limit)
			if e != nil {
				return e
			}
			for rows.Next() {
				var b []byte
				var op contract.ManagementReceipt
				if e = rows.Scan(&b); e == nil {
					e = json.Unmarshal(b, &op)
				}
				if e != nil {
					rows.Close()
					return e
				}
				out.InformationControl.Operations = append(out.InformationControl.Operations, op)
				ids[op.Requester] = true
				ids[op.Approver] = true
				ids[op.Request.SubjectID] = true
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
		}
		if e := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM authority_items WHERE kind='operations' AND state='stopping')").Scan(&out.Stopping); e != nil {
			return e
		}
		if selection.Principals {
			rows, e := q.QueryContext(ctx, "SELECT id FROM authority_items WHERE kind='principals' AND id>? ORDER BY id LIMIT ?", selection.After, limit)
			if e != nil {
				return e
			}
			for rows.Next() {
				var id string
				if e = rows.Scan(&id); e != nil {
					rows.Close()
					return e
				}
				ids[id] = true
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
		}
		// The selected set is bounded by the protocol's enrollment and operation limits.
		principalBytes := 0
		for id := range ids {
			if id == "" {
				continue
			}
			var b []byte
			e := q.QueryRowContext(ctx, "SELECT data FROM authority_items WHERE kind='principals' AND id=?", id).Scan(&b)
			if errors.Is(e, sql.ErrNoRows) {
				continue
			}
			if e != nil {
				return e
			}
			principalBytes += len(b)
			if principalBytes > 1024*1024 {
				return errors.New("本次控制决定超过工作区，请拆分操作")
			}
			var p contract.Principal
			if e = json.Unmarshal(b, &p); e != nil {
				return e
			}
			out.InformationControl.Principals = append(out.InformationControl.Principals, p)
		}
		return nil
	})
	return out, e
}

func (a *ControlAuthority) CompareAndSwapControl(expected uint64, next contract.ControlState) (contract.ControlState, error) {
	if next.ReadError != nil {
		return contract.ControlState{}, next.ReadError
	}
	if next.Revision != expected+1 {
		return contract.ControlState{}, errors.New("控制修订不连续")
	}
	if e := next.Validate(); e != nil {
		return contract.ControlState{}, e
	}
	ctx := workContext(context.Background(), controlWork)
	e := a.store.write(ctx, func(tx *sql.Tx) error {
		var data []byte
		if e := tx.QueryRowContext(ctx, "SELECT data FROM authority_header WHERE singleton=1").Scan(&data); e != nil {
			return e
		}
		var old contract.ControlState
		if e := json.Unmarshal(data, &old); e != nil {
			return e
		}
		if old.Revision != expected {
			return errors.New("控制状态已变化")
		}
		h := next
		h.Stopping = false
		if next.InformationControl != nil {
			info := *next.InformationControl
			headerInfo := info
			h.InformationControl = &headerInfo
			h.InformationControl.Principals = nil
			h.InformationControl.Operations = nil
			if old.InformationControl != nil && (info.SystemID != old.InformationControl.SystemID || info.OwnerID != old.InformationControl.OwnerID || info.DeletionRevision < old.InformationControl.DeletionRevision) {
				return errors.New("体系所有权或删除版本不能改写")
			}
			for _, p := range info.Principals {
				var before []byte
				e := tx.QueryRowContext(ctx, "SELECT data FROM authority_items WHERE kind='principals' AND id=?", p.ID).Scan(&before)
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				var previous contract.Principal
				if len(before) > 0 {
					if e = json.Unmarshal(before, &previous); e != nil {
						return e
					}
					if reflect.DeepEqual(previous, p) {
						continue
					}
				}
				if p.Revision != previous.Revision+1 {
					return errors.New("主体修订不连续")
				}
				b, e := json.Marshal(p)
				if e != nil {
					return e
				}
				if e = controlItem(ctx, tx, "principals", p.ID, "", b); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, "INSERT INTO access_principals VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,credential=excluded.credential,permissions=excluded.permissions", p.ID, p.Revision, storageCredential(p.ID, p.CredentialDigest), permissionMask(p.Permissions)); e != nil {
					return e
				}
			}
			for _, op := range info.Operations {
				if op.Request.Operation == "forget" && op.Status == "stopping" {
					if e := publishAuthorizedForget(ctx, tx, op); e != nil {
						return e
					}
				}
				b, e := json.Marshal(op)
				if e != nil {
					return e
				}
				if e = controlItem(ctx, tx, "operations", op.Request.ID, op.Status, b); e != nil {
					return e
				}
			}
			frozen, retired := false, false
			if next.Access != nil && next.Access.Handoff != nil {
				frozen = next.Access.Handoff.Phase == "frozen"
				retired = next.Access.Handoff.Phase == "retired"
			}
			if _, e := tx.ExecContext(ctx, "INSERT OR REPLACE INTO access_header SELECT 1,?,max(?,coalesce((SELECT revision FROM access_header),0)),max(?,coalesce((SELECT deletion_epoch FROM access_header),0)), ?,?,EXISTS(SELECT 1 FROM authority_items WHERE kind='operations' AND state='stopping')", info.SystemID, next.Revision, info.DeletionRevision, frozen, retired); e != nil {
				return e
			}
		}
		if next.Access != nil {
			access := *next.Access
			headerAccess := access
			h.Access = &headerAccess
			h.Access.Cancelled = nil
			for _, hand := range access.Cancelled {
				b, e := json.Marshal(hand)
				if e != nil {
					return e
				}
				state := "pending"
				if hand.Cleaned {
					state = "cleaned"
				}
				if e = controlItem(ctx, tx, "cancelled", hand.ID, state, b); e != nil {
					return e
				}
			}
		}
		data, e := json.Marshal(h)
		if e != nil {
			return e
		}
		if len(data) > 256*1024 {
			return errors.New("控制头超过工作区预算")
		}
		_, e = tx.ExecContext(ctx, "UPDATE authority_header SET data=? WHERE singleton=1", data)
		return e
	})
	if e != nil {
		return contract.ControlState{}, e
	}
	return next, nil
}

// ExportControl reconstructs the durable control contract without collecting
// the principals or completed operation history in memory.
func (a *ControlAuthority) ExportControl(ctx context.Context, w io.Writer) error {
	return a.store.view(ctx, func(q queryer) error {
		var b []byte
		if e := q.QueryRowContext(ctx, "SELECT data FROM authority_header WHERE singleton=1").Scan(&b); e != nil {
			return e
		}
		var h contract.ControlState
		if e := json.Unmarshal(b, &h); e != nil {
			return e
		}
		if h.InformationControl == nil {
			_, e := w.Write(b)
			return e
		}
		writeItems := func(kind string) error {
			rows, e := q.QueryContext(ctx, "SELECT data FROM authority_items WHERE kind=? ORDER BY sequence", kind)
			if e != nil {
				return e
			}
			defer rows.Close()
			first := true
			for rows.Next() {
				var b []byte
				if e = rows.Scan(&b); e != nil {
					return e
				}
				if !first {
					io.WriteString(w, ",")
				}
				first = false
				if _, e = w.Write(b); e != nil {
					return e
				}
			}
			return rows.Err()
		}
		header := h
		header.InformationControl = nil
		header.Access = nil
		b, e := json.Marshal(header)
		if e != nil {
			return e
		}
		if _, e = w.Write(b[:len(b)-1]); e != nil {
			return e
		}
		if _, e = io.WriteString(w, `,"information_control":`); e != nil {
			return e
		}
		small := struct {
			System   string `json:"system_id"`
			Owner    string `json:"owner_id"`
			Deletion uint64 `json:"deletion_revision"`
		}{h.InformationControl.SystemID, h.InformationControl.OwnerID, h.InformationControl.DeletionRevision}
		b, e = json.Marshal(small)
		if e != nil {
			return e
		}
		if _, e = w.Write(b[:len(b)-1]); e != nil {
			return e
		}
		if _, e = io.WriteString(w, `,"principals":[`); e != nil {
			return e
		}
		if e = writeItems("principals"); e != nil {
			return e
		}
		if _, e = io.WriteString(w, `],"operations":[`); e != nil {
			return e
		}
		if e = writeItems("operations"); e != nil {
			return e
		}
		if _, e = io.WriteString(w, `]} `); e != nil {
			return e
		}
		if h.Access != nil {
			b, e = json.Marshal(h.Access)
			if e != nil {
				return e
			}
			if _, e = io.WriteString(w, `,"access":`); e != nil {
				return e
			}
			if _, e = w.Write(b[:len(b)-1]); e != nil {
				return e
			}
			if len(b) > 2 {
				if _, e = io.WriteString(w, ","); e != nil {
					return e
				}
			}
			if _, e = io.WriteString(w, `"cancelled":[`); e != nil {
				return e
			}
			if e = writeItems("cancelled"); e != nil {
				return e
			}
			if _, e = io.WriteString(w, `]}`); e != nil {
				return e
			}
		}
		_, e = io.WriteString(w, "}")
		return e
	})
}

package boundedstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

func TestOwnerAccessHistoryTracksDecisionsNotDelivery(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	proof := strings.Repeat("private-proof", 4)
	if _, e := c.Invite(ctx, "joining"); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Join("joining", proof, "reader", []contract.Permission{contract.ReadPermission}); e != nil {
		t.Fatal(e)
	}
	marker := informationcontrol.EnrollmentMarker("joining", proof)
	if _, e := c.DecideEnrollment(ctx, "joining", marker, true); e != nil {
		t.Fatal(e)
	}
	if _, e := c.DecideEnrollment(ctx, "joining", marker, true); e != nil {
		t.Fatal(e)
	}
	credential, e := c.RecoverOwner()
	if e != nil {
		t.Fatal(e)
	}
	ctx = informationcontrol.Authenticate(context.Background(), credential)
	preview, _, e := c.EnrollmentPreview(ctx, "joining")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.DecideEnrollmentVersion(ctx, "joining", marker, preview.Decision, true); e != nil {
		t.Fatal(e)
	}
	var token string
	for i := 0; i < 2; i++ {
		_, token, e = c.ClaimEnrollment("joining", proof)
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = c.AcknowledgeEnrollment(informationcontrol.Authenticate(context.Background(), token), "joining", proof); e != nil {
		t.Fatal(e)
	}
	if n := countTest(t, s, "SELECT count(*) FROM owner_event_access"); n != 2 {
		t.Fatal("delivery duplicated decisions", n)
	}
	page, e := s.OwnerEvents(ctx, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(page)
	if strings.Contains(string(b), proof) || strings.Contains(string(b), token) || strings.Contains(string(b), "proof_digest") {
		t.Fatal("history contains private material")
	}
	// The transient invitation may expire without erasing retained decisions.
	a, e := s.OpenControlAuthority(ctx, ownerInitial())
	if e != nil {
		t.Fatal(e)
	}
	state := a.ReadControl()
	state.Access.Enrollments[0].Expires = time.Now().Add(-time.Hour)
	old := state.Revision
	state.Revision++
	if _, e = a.CompareAndSwapControl(old, state); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Invite(ctx, "next"); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	reopened := openOwnerTest(t, s.path)
	rows, _, e := reopened.OwnerHistory(ctx, "", 100)
	if e != nil || len(rows) != 2 {
		t.Fatal("history lost after expiry/restart", rows, e)
	}
}

func TestOwnerAccessHistoryCancellationAtomicityAndLegacyImport(t *testing.T) {
	s, c, ctx := ownerFixture(t)
	stopMaintenance(s)
	p, agent := workAgent(t, c, ctx)
	d := newDraft(t, s, ctx, "draft")
	grant, e := s.GrantDraft(ctx, d.ID, p.ID, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	target := contract.Location{SystemID: c.SystemID(), ServiceID: "target", Endpoint: "https://localhost:9443", Certificate: "public-certificate", Composition: "composition"}
	h, e := c.PrepareHandoff(ctx, "move", target)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.DecideHandoff(ctx, h.ID, h.Revision, true); e != nil {
		t.Fatal(e)
	}
	if _, e = c.FreezeHandoff(ctx, h.ID, true); e != nil {
		t.Fatal(e)
	}
	if n := countTest(t, s, "SELECT count(*) FROM owner_event_access"); n != 1 {
		t.Fatal("freeze duplicated approval", n)
	}
	if _, e = s.writer.ExecContext(ctx, `CREATE TRIGGER fail_access_fact BEFORE INSERT ON owner_event_access BEGIN SELECT RAISE(ABORT,'fact failure'); END`); e != nil {
		t.Fatal(e)
	}
	if e = c.RevokeDraftGrant(ctx, s, grant.ID); e == nil {
		t.Fatal("event failure did not reject transaction")
	}
	if _, e = s.DraftMetadata(agent, d.ID, grant.ID); e != nil {
		t.Fatal("failed transaction removed grant", e)
	}
	if h, e = c.HandoffStatus(ctx, "move"); e != nil || h.Phase != "frozen" {
		t.Fatal("failed transaction cancelled handoff", h, e)
	}
	if _, e = s.writer.ExecContext(ctx, "DROP TRIGGER fail_access_fact"); e != nil {
		t.Fatal(e)
	}
	if e = c.RevokeDraftGrant(ctx, s, grant.ID); e != nil {
		t.Fatal(e)
	}
	if e = c.CancelHandoff(ctx, "move"); e != nil {
		t.Fatal(e)
	}
	if e = c.MarkHandoffClean("move"); e != nil {
		t.Fatal(e)
	}
	if n := countTest(t, s, "SELECT count(*) FROM owner_event_access"); n != 2 {
		t.Fatal("cancel/cleanup repeated event", n)
	}
	if _, e = s.DraftMetadata(agent, d.ID, grant.ID); e == nil {
		t.Fatal("revoked grant usable")
	}
	// Importing source-format authority facts has no known historical time.
	a, e := s.OpenControlAuthority(ctx, ownerInitial())
	if e != nil {
		t.Fatal(e)
	}
	var buf bytes.Buffer
	if e = a.ExportControl(ctx, &buf); e != nil {
		t.Fatal(e)
	}
	legacy := openOwnerTest(t, filepath.Join(t.TempDir(), "legacy.sqlite"))
	payload, e := legacy.Stage(ctx, "legacy", StringSource(buf.String()), nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = legacy.PublishControl(ctx, ControlRecord{Key: "authority", Revision: 1, Payload: payload}, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = legacy.OpenControlAuthority(ctx, ownerInitial()); e != nil {
		t.Fatal(e)
	}
	if n := countTest(t, legacy, "SELECT count(*) FROM owner_events"); n != 0 {
		t.Fatal("legacy cancellation became a new event", n)
	}
}

func TestOwnerCombinedHistoryBoundsAndCascadingRetention(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	stopMaintenance(s)
	e := s.write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < OwnerEventLimit+5; i++ {
			if e := recordOwnerAccess(ctx, tx, "enrollment", fmt.Sprint(i), "approved", contract.OwnerAccessFact{Subject: "reader"}); e != nil {
				return e
			}
		}
		for i := 0; i < 5; i++ {
			op := contract.ManagementReceipt{Request: contract.ManagementRequest{ID: fmt.Sprintf("op-%d", i), Operation: "permissions"}, Status: "completed"}
			b, _ := json.Marshal(op)
			if e := controlItem(ctx, tx, "operations", op.Request.ID, op.Status, b); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	count, position := 0, ""
	for {
		rows, next, e := s.OwnerHistory(ctx, position, 100)
		if e != nil {
			t.Fatal(e)
		}
		count += len(rows)
		if next == "" {
			break
		}
		position = next
	}
	if count != OwnerEventLimit {
		t.Fatal("combined history cap not enforced on read", count)
	}
	page, e := s.OwnerEvents(ctx, 1, 100)
	if e != nil || !page.Reset {
		t.Fatal("event cap requires maintenance", page.Reset, e)
	}
	if _, e = s.writer.ExecContext(ctx, "UPDATE owner_events SET at=0; UPDATE owner_operation_times SET updated=0"); e != nil {
		t.Fatal(e)
	}
	rows, _, e := s.OwnerHistory(ctx, "", 100)
	if e != nil || len(rows) != 0 {
		t.Fatal("expired history visible", rows, e)
	}
	for {
		more := false
		e = s.write(ctx, func(tx *sql.Tx) error { var e error; more, e = s.expireOwnerHistory(ctx, tx); return e })
		if e != nil {
			t.Fatal(e)
		}
		if !more {
			break
		}
	}
	if n := countTest(t, s, "SELECT count(*) FROM owner_event_access"); n != 0 {
		t.Fatal("event details outlive history", n)
	}
	if n := countTest(t, s, "SELECT count(*) FROM authority_items WHERE kind='operations'"); n != 5 {
		t.Fatal("history expiry removed authority", n)
	}
}

func TestOwnerAccessHistoryBytePaging(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	stopMaintenance(s)
	e := s.write(ctx, func(tx *sql.Tx) error {
		for i := 0; i < 8; i++ {
			if e := recordOwnerAccess(ctx, tx, "handoff", fmt.Sprint(i), "declined", contract.OwnerAccessFact{Subject: strings.Repeat("x", 130<<10)}); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	count, position := 0, ""
	for {
		rows, next, e := s.OwnerHistory(ctx, position, 100)
		if e != nil {
			t.Fatal(e)
		}
		if len(rows) > 3 {
			t.Fatal("history byte budget exceeded", len(rows))
		}
		count += len(rows)
		if next == "" {
			break
		}
		position = next
	}
	if count != 8 {
		t.Fatal("byte paging lost history", count)
	}
	count = 0
	var after uint64
	for {
		page, e := s.OwnerEvents(ctx, after, 100)
		if e != nil {
			t.Fatal(e)
		}
		if len(page.Items) > 3 {
			t.Fatal("event byte budget exceeded", len(page.Items))
		}
		count += len(page.Items)
		if page.Next == after {
			break
		}
		after = page.Next
	}
	if count != 8 {
		t.Fatal("byte paging lost activity", count)
	}
}

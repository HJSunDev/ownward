package boundedstore

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
)

func TestNavigationExpiryDoesNotHideStorageFault(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	stopMaintenance(s)
	cp, err := s.OwnerCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	change := func(query string, args ...any) {
		t.Helper()
		if err := s.write(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, query, args...); return err }); err != nil {
			t.Fatal(err)
		}
	}
	validState := []byte(`{"Queue":[],"Depth":1}`)
	change("INSERT INTO navigation_cursors VALUES(?,?,?,?,?,?,?)", "probe", contract.AuthenticationDigest(ctx), cp.Generation, cp.Assets, cp.Derived, time.Now().Add(time.Hour).Unix(), validState)
	read := func() error {
		_, err := s.NavigatePage(ctx, cp.Generation, []string{"nav2:probe"}, nil, 1, 1)
		return err
	}
	if err := read(); err != nil {
		t.Fatal("valid empty continuation", err)
	}
	if _, err := s.NavigatePage(ctx, "different-generation", []string{"nav2:probe"}, nil, 1, 1); !errors.Is(err, ErrNavigationExpired) {
		t.Fatal("generation mismatch", err)
	}
	change("UPDATE navigation_cursors SET expires=0")
	if err := read(); !errors.Is(err, ErrNavigationExpired) {
		t.Fatal("expired continuation", err)
	}
	change("UPDATE navigation_cursors SET expires=?,state=?", time.Now().Add(time.Hour).Unix(), []byte(`{broken`))
	if err := read(); err == nil || errors.Is(err, ErrNavigationExpired) {
		t.Fatal("corrupt state hidden", err)
	}
	change("UPDATE navigation_cursors SET state=?", validState)
	change("ALTER TABLE navigation_cursors RENAME TO unavailable_navigation_cursors")
	err = read()
	change("ALTER TABLE unavailable_navigation_cursors RENAME TO navigation_cursors")
	if err == nil || errors.Is(err, ErrNavigationExpired) || !strings.Contains(err.Error(), "navigation_cursors") {
		t.Fatal("SQL failure hidden as expiry", err)
	}
	if err := read(); err != nil {
		t.Fatal("restored continuation", err)
	}
	change("DELETE FROM navigation_cursors")
	if err := read(); !errors.Is(err, ErrNavigationExpired) {
		t.Fatal("reclaimed continuation", err)
	}
}

func TestOwnerReadDistinguishesUnavailableObjectsFromStorageFailure(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	stopMaintenance(s)
	d := newDraft(t, s, ctx, "retained text")
	a, err := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "read-errors"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.OwnerText(ctx, a.ID, a.Revision, "original", 0, ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("missing original was not explicit", err)
	}
	change := func(query string) {
		t.Helper()
		if err := s.write(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, query); return err }); err != nil {
			t.Fatal(err)
		}
	}
	change("ALTER TABLE access_header RENAME TO unavailable_access_header")
	_, err = s.OwnerText(ctx, a.ID, a.Revision, "content", 0, "")
	change("ALTER TABLE unavailable_access_header RENAME TO access_header")
	if err == nil || errors.Is(err, ErrAccess) || errors.Is(err, ErrNotFound) || errors.Is(err, contract.ErrOwnerRefresh) || !strings.Contains(err.Error(), "access_header") {
		t.Fatal("database fault became a permission/refresh outcome", err)
	}
	page, err := s.OwnerText(ctx, a.ID, a.Revision, "content", 0, "")
	if err != nil || page.Text != "retained text" {
		t.Fatal(page, err)
	}
}

func TestOwnerEventPagesRetainFactsAndFullPublicationIdentity(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	stopMaintenance(s)
	longID := strings.Repeat("界", 180000) // bytes, not rune count; formerly larger than an entire event page
	d := newDraft(t, s, ctx, "first publication")
	first, err := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: longID})
	if err != nil {
		t.Fatal(err)
	}
	second := newDraft(t, s, ctx, "later publication")
	if _, err = s.PublishDraft(ctx, second.ID, second.Revision, contract.OperationIdentity{ID: "later"}); err != nil {
		t.Fatal(err)
	}
	var cursor uint64
	publications, facts := 0, 0
	shortReference := false
	for i := 0; i < 8; i++ {
		page, err := s.OwnerEvents(ctx, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 {
			break
		}
		if page.Next <= cursor {
			t.Fatal("nonempty event page made no progress")
		}
		cursor = page.Next
		for _, event := range page.Items {
			facts++
			if event.Kind == "draft_published" {
				publications++
			}
			shortReference = shortReference || event.Operation == "later"
			if len(event.Operation) > contract.OwnerEventReferenceBytes {
				t.Fatal("unbounded optional reference escaped the projection")
			}
		}
	}
	if facts != 4 || publications != 2 || !shortReference {
		t.Fatal("event facts or compatible references lost", facts, publications, shortReference)
	}
	// A shortened projection must not truncate, rewrite or expire replay facts.
	if result, err := s.OwnerPublication(ctx, longID); err != nil || result.State != "completed" || result.Asset != first {
		t.Fatal(result, err)
	}
	if again, err := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: longID}); err != nil || again != first {
		t.Fatal("original long identity no longer replays", again, err)
	}
}

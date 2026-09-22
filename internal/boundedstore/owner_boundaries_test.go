package boundedstore

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestOwnerEditPreservesExplicitRelations(t *testing.T) {
	s, _, ctx := ownerFixture(t)
	op := operation("target")
	v := stageTest(t, s, op, "target", "target text", 1)
	if e := s.Publish(ctx, receipt(op, v), []AssetWrite{v}); e != nil {
		t.Fatal(e)
	}
	op = operation("qualifier")
	v = stageTest(t, s, op, "qualifier", "explicit statement", 1)
	s.Abandon(ctx, v.Payload)
	p, e := s.Stage(ctx, operationKey(op), StringSource("explicit statement"), StringSource(`{"explicit_relations":[{"type":"qualifies","target_id":"target"}]}`))
	if e != nil {
		t.Fatal(e)
	}
	v.Payload = p
	if e = s.StageLink(ctx, p, ExplicitLink{Target: "target", Qualifies: true}); e != nil {
		t.Fatal(e)
	}
	if e = s.Publish(ctx, receipt(op, v), []AssetWrite{v}); e != nil {
		t.Fatal(e)
	}
	d, e := s.CreateDraft(ctx, contract.DraftInput{Target: contract.AssetVersion{ID: "qualifier", Revision: 1}, Content: StringSource("updated explicit statement")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "owner-edit"}); e != nil {
		t.Fatal(e)
	}
	n := 0
	if e = s.VisitQualifiers(ctx, "target", func(Qualifier) error { n++; return nil }); e != nil {
		t.Fatal(e)
	}
	if n != 1 {
		t.Fatalf("edit lost explicit relation: qualifiers=%d", n)
	}
}

func TestOwnerTextEditInvalidatesOnlyUnsupportedInheritedRelations(t *testing.T) {
	for _, scenario := range []struct {
		name, text   string
		forget, keep bool
		changes      contract.RelationInvalidationCounts
	}{
		{"changed quotation", "replacement sentence", false, false, contract.RelationInvalidationCounts{QuoteMissing: 1}},
		{"ambiguous quotation", "statement and statement", false, false, contract.RelationInvalidationCounts{QuoteAmbiguous: 1}},
		{"unrelated edit", "prefix statement suffix", false, true, contract.RelationInvalidationCounts{}},
		{"forgotten target", "statement", true, false, contract.RelationInvalidationCounts{TargetUnavailable: 1}},
		{"unavailable target and quotation", "replacement sentence", true, false, contract.RelationInvalidationCounts{TargetUnavailable: 1}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, _, ctx := ownerFixture(t)
			putRetrievalAsset(t, s, domain.Information{ID: "target", Revision: 1, Content: "target fact"})
			putRetrievalAsset(t, s, domain.Information{ID: "source", Revision: 1, Content: "statement", Source: domain.Source{Ref: "original-document"}, Relations: []domain.ExplicitRelation{{Type: "qualifies", TargetID: "target", Selector: &domain.TextSelector{Exact: "statement"}}}})
			if scenario.forget {
				if e := s.StageForget(ctx, "remove-target", []ForgetTarget{{"target", 1}}); e != nil {
					t.Fatal(e)
				}
				if e := s.CommitForget(ctx, "remove-target"); e != nil {
					t.Fatal(e)
				}
				// Complete the prior control operation: its cleanup deliberately
				// cancels active snapshots before securely truncating the WAL.
				if e := s.DrainMaintenance(ctx); e != nil {
					t.Fatal(e)
				}
			}
			d, e := s.CreateDraft(ctx, contract.DraftInput{Target: contract.AssetVersion{ID: "source", Revision: 1}, Content: StringSource(scenario.text)})
			if e != nil {
				t.Fatal(e)
			}
			a, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "edit"})
			if e != nil {
				t.Fatal("normal text edit blocked", e)
			}
			if again, e := s.PublishDraft(ctx, d.ID, d.Revision, contract.OperationIdentity{ID: "edit"}); e != nil || again != a {
				t.Fatal("retry differs", again, e)
			}
			events, e := s.OwnerEvents(ctx, 0, 100)
			if e != nil {
				t.Fatal(e)
			}
			publications := 0
			for _, event := range events.Items {
				if event.Operation != "edit" || event.Kind != "draft_published" {
					continue
				}
				publications++
				got := contract.RelationInvalidationCounts{}
				if event.InvalidatedRelations != nil {
					got = *event.InvalidatedRelations
				}
				if got != scenario.changes || (scenario.keep && event.InvalidatedRelations != nil) {
					t.Fatal("wrong relation change notice", got, scenario.changes)
				}
			}
			if publications != 1 {
				t.Fatal("publication event repeated or absent", publications)
			}
			if got := readTest(t, s, a.ID); got != scenario.text {
				t.Fatal(got)
			}
			r, e := s.OpenDetails(ctx, a.ID, a.Revision)
			if e != nil {
				t.Fatal(e)
			}
			var current domain.Information
			e = json.NewDecoder(r).Decode(&current)
			r.Close()
			if e != nil {
				t.Fatal(e)
			}
			want := 0
			if scenario.keep {
				want = 1
			}
			if len(current.Relations) != want {
				t.Fatal("metadata differs", current.Relations)
			}
			n := 0
			if e = s.VisitQualifiers(ctx, "target", func(Qualifier) error { n++; return nil }); e != nil || n != want {
				t.Fatal("index differs", n, e)
			}
			_, r, e = s.OpenOriginal(ctx, a.ID, true)
			if e != nil {
				t.Fatal(e)
			}
			var original domain.Information
			e = json.NewDecoder(r).Decode(&original)
			r.Close()
			if e != nil || len(original.Relations) != 1 {
				t.Fatal("source relation not retained", original.Relations, e)
			}
			_, r, e = s.OpenOriginal(ctx, a.ID, false)
			if e != nil {
				t.Fatal(e)
			}
			b, e := io.ReadAll(r)
			r.Close()
			if e != nil || string(b) != "statement" {
				t.Fatal("source changed", string(b), e)
			}
		})
	}
}
func TestOwnerLargeSourceCopyReleasesReadSnapshots(t *testing.T) {
	s, _, owner := ownerFixture(t)
	ctx, cancel := context.WithTimeout(owner, 12*time.Second)
	defer cancel()
	op := operation("large-source")
	v := stageTest(t, s, op, "large", strings.Repeat("x", 12*1024*1024), 1)
	if e := s.Publish(ctx, receipt(op, v), []AssetWrite{v}); e != nil {
		t.Fatal(e)
	}
	if e := s.DrainMaintenance(ctx); e != nil {
		t.Fatal(e)
	}
	d, e := s.CreateDraft(ctx, contract.DraftInput{Target: contract.AssetVersion{ID: "large", Revision: 1}})
	if e != nil {
		t.Fatal("copy cannot complete", e)
	}
	if d.ContentBytes != 12*1024*1024 {
		t.Fatal(d.ContentBytes)
	}
}

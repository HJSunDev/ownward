package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/semanticstream"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func TestStreamingOrganizationNormalizationMatches(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	budget, _ := resourcebudget.New(16*resourcebudget.MiB, resourcebudget.MiB)
	store, e := boundedstore.Open(ctx, filepath.Join(root, "db"), boundedstore.Options{Budget: budget})
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	source := "背景\nAlice leads the team.\nALICE owns the budget.\n结束"
	asset := domain.Information{ID: "a", Revision: 1, Content: source}
	op := contract.OperationIdentity{ID: "a", System: "test", Principal: "test", Kind: "ownward_create", Generation: 1, Digest: "a"}
	key, e := boundedstore.OperationKey(op)
	if e != nil {
		t.Fatal(e)
	}
	p, e := store.Stage(ctx, key, boundedstore.StringSource(source), nil)
	if e != nil {
		t.Fatal(e)
	}
	e = store.Publish(ctx, contract.MutationReceipt{Operation: op, Results: []contract.MutationOutcome{{Asset: contract.AssetVersion{ID: "a", Revision: 1}}}}, []boundedstore.AssetWrite{{Meta: contract.AssetMeta{ID: "a", Revision: 1, Kind: domain.KindGeneral, CreatedAt: time.Now(), UpdatedAt: time.Now()}, Payload: p}})
	if e != nil {
		t.Fatal(e)
	}
	s := &StreamingAssets{Store: store, Budget: budget, Scratch: root, DiskBytes: 128 * resourcebudget.MiB}
	for _, org := range []*semantics.Organization{
		{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: "whole"}}},
		{Schema: semantics.OrganizationSchema, Units: []semantics.SemanticUnit{{ID: "z", Selector: domain.TextSelector{Exact: "ALICE owns the budget."}, Mentions: []semantics.Mention{{ID: "person", Name: "alice"}}}, {ID: "a", Statement: "Leader", Selector: domain.TextSelector{Exact: "Alice leads the team."}, Context: []domain.TextSelector{{Exact: "背景"}, {Exact: "Alice"}}, Mentions: []semantics.Mention{{ID: "person", Name: "Alice"}}}}, Links: []semantics.GroundedLink{{Type: "same_object", Meaning: "The same person.", Source: semantics.GraphEndpoint{AssetID: "a", UnitID: "a", MentionID: "person"}, Target: semantics.GraphEndpoint{AssetID: "a", UnitID: "z", MentionID: "person"}}}},
	} {
		want, e := semantics.NormalizeOrganization(asset, org, nil)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(org)
		d, e := streamjson.Parse(ctx, root, strings.NewReader(string(raw)), budget, 128*resourcebudget.MiB)
		if e != nil {
			t.Fatal(e)
		}
		value, e := semanticstream.ReadOrganization(d.Root())
		if e != nil {
			t.Fatal(e)
		}
		resolver := &semanticResolver{streamingTexts: &streamingTexts{ctx: ctx, s: s, bodies: map[string]*streamedBody{}, revisions: map[string]uint64{"a": 1}}, documents: map[string]*streamjson.Document{}}
		got, e := semanticstream.Normalize(ctx, "a", 1, value, resolver)
		if e != nil {
			t.Fatal(e)
		}
		out, e := streamjson.Build(ctx, root, budget, 128*resourcebudget.MiB, func(w io.Writer) error { return got.Write(ctx, w) })
		if e != nil {
			t.Fatal(e)
		}
		var actual semantics.Organization
		if e = out.Root().DecodeSmall(&actual, 1024*1024); e != nil {
			t.Fatal(e)
		}
		left, _ := json.Marshal(actual)
		right, _ := json.Marshal(want)
		if !bytes.Equal(left, right) {
			left, _ := json.Marshal(actual)
			right, _ := json.Marshal(want)
			t.Fatalf("got %s\nwant%s", left, right)
		}
		for i, u := range got.Units {
			fp, e := u.Fingerprint(ctx)
			if e != nil || fp != semantics.UnitFingerprint(want.Units[i]) {
				t.Fatal("fingerprint mismatch", fp, e)
			}
		}
		out.Close()
		resolver.close()
		d.Close()
	}
}

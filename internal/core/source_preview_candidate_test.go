//go:build ownward_v2_candidate

package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

type misattributedPreviewProvider struct{ staleContextProvider }

func (misattributedPreviewProvider) Analyze(context.Context, domain.Information, []semantics.Candidate) (semantics.Analysis, error) {
	return semantics.Analysis{Summary: "A different source says the day shift uses amber cloth."}, nil
}

func TestSourceOwnedPreviewCannotPresentNeighborSummaryAsAsset(t *testing.T) {
	root := t.TempDir()
	store, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := newTestOrganized(t, store, state, misattributedPreviewProvider{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	text := strings.Repeat("Unrelated introductory records. ", 25) + "The evening inspection now uses violet linen, replacing red felt."
	created, err := s.Create(context.Background(), CreateInput{Content: text})
	if err != nil {
		t.Fatal(err)
	}
	results, err := s.Search(context.Background(), SearchInput{Query: "evening inspection", Limit: 10})
	if err != nil || len(results) != 1 {
		t.Fatalf("search: %v %#v", err, results)
	}
	if !strings.Contains(results[0].Summary, "violet linen") || strings.Contains(results[0].Summary, "amber cloth") {
		t.Fatalf("misleading preview: %q", results[0].Summary)
	}
	nav, err := s.Navigate(context.Background(), []string{created.Information.ID}, nil, 1, 10)
	if err != nil || len(nav.Nodes) != 1 {
		t.Fatalf("navigation: %v %#v", err, nav)
	}
	if strings.Contains(nav.Nodes[0].Summary, "amber cloth") {
		t.Fatal("navigation presented foreign summary")
	}
	read, err := s.Read(context.Background(), created.Information.ID)
	if err != nil || read.Content != text {
		t.Fatal("source changed")
	}
}

//go:build ownward_v2_representation

package kernelv2candidate_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/authorityport"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
)

type gatedRepresentationCapability struct {
	started       sync.Once
	begin         chan struct{}
	release       chan struct{}
	mu            sync.Mutex
	documentCalls int
	base          embedding.HashForTesting
}

func newGatedRepresentationCapability() *gatedRepresentationCapability {
	return &gatedRepresentationCapability{
		begin: make(chan struct{}), release: make(chan struct{}),
		base: embedding.HashForTesting{Dimensions: 64},
	}
}

func (g *gatedRepresentationCapability) Name() string           { return g.base.Name() }
func (g *gatedRepresentationCapability) Space() embedding.Space { return g.base.Space() }
func (g *gatedRepresentationCapability) EmbedQuery(ctx context.Context, value string) ([]float32, error) {
	return g.base.EmbedQuery(ctx, value)
}
func (g *gatedRepresentationCapability) EmbedDocuments(ctx context.Context, values []string) ([][]float32, error) {
	g.started.Do(func() { close(g.begin) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g.release:
		g.mu.Lock()
		g.documentCalls++
		g.mu.Unlock()
		return g.base.EmbedDocuments(ctx, values)
	}
}
func (*gatedRepresentationCapability) Close() error { return nil }

func TestCandidateCreateDoesNotWaitAndSubmissionJoinsExactCurrentRevision(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	authority, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	embedder := newGatedRepresentationCapability()
	port, err := authorityport.Bind(authority)
	if err != nil {
		t.Fatal(err)
	}
	service, err := core.NewCollaborativeWithAuthority(port, state, embedder)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	created := make(chan core.MutationResult, 1)
	failed := make(chan error, 1)
	go func() {
		result, createErr := service.Create(ctx, core.CreateInput{Content: "A durable current-revision fact."})
		created <- result
		failed <- createErr
	}()
	var result core.MutationResult
	select {
	case result = <-created:
		if err := <-failed; err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate create waited for raw vector inference")
	}
	select {
	case <-embedder.begin:
	case <-time.After(time.Second):
		t.Fatal("candidate did not start the sealed representation job")
	}
	work, err := service.SemanticWorkFor(ctx, []string{result.Information.ID})
	if err != nil || len(work) != 1 {
		t.Fatalf("semantic work missing: %#v %v", work, err)
	}
	submission := semantics.Submission{
		Schema: semantics.SubmissionSchema, WorkID: work[0].ID,
		AssetID: work[0].Asset.ID, Revision: work[0].Asset.Revision,
		Capability: semantics.Capability{ID: "codex", Version: "gpt-5.6-luna", Execution: "isolated"},
		Status:     semantics.SubmissionComplete,
		Analysis:   semantics.Analysis{Summary: "A durable fact.", Cues: []semantics.Cue{}, Topics: []string{}},
	}
	submitted := make(chan core.OrganizationState, 1)
	submitFailure := make(chan error, 1)
	go func() {
		organization, submitErr := service.SubmitSemantic(ctx, submission)
		submitted <- organization
		submitFailure <- submitErr
	}()
	select {
	case <-submitted:
		t.Fatal("semantic submission did not join the current-revision vector job")
	case <-time.After(50 * time.Millisecond):
	}
	close(embedder.release)
	organization := <-submitted
	if err := <-submitFailure; err != nil {
		t.Fatal(err)
	}
	if organization.Status != "ready" {
		t.Fatalf("submission did not atomically reach ready: %#v", organization)
	}
	record, exists := state.GetWithEmbedding(result.Information.ID)
	if !exists || record.AssetRevision != result.Information.Revision || len(record.Embedding) != 64 || record.SemanticReceipt == nil {
		t.Fatalf("ready representation is not sealed to the accepted revision: %#v", record)
	}
	for count := 0; count < 2; count++ {
		results, searchErr := service.Search(ctx, core.SearchInput{Query: "durable fact", Limit: 8})
		if searchErr != nil || len(results) == 0 {
			t.Fatalf("ready derived index was not reusable: %#v %v", results, searchErr)
		}
	}
	embedder.mu.Lock()
	documentCalls := embedder.documentCalls
	embedder.mu.Unlock()
	if documentCalls != 1 {
		t.Fatalf("ready queries regenerated the document representation %d times", documentCalls)
	}
}

func TestCandidatePendingQueryJoinsBeforeExactSemanticSearch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	authority, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	embedder := newGatedRepresentationCapability()
	port, err := authorityport.Bind(authority)
	if err != nil {
		t.Fatal(err)
	}
	service, err := core.NewCollaborativeWithAuthority(port, state, embedder)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	created, err := service.Create(ctx, core.CreateInput{Content: "queryable current fact"})
	if err != nil {
		t.Fatal(err)
	}
	searched := make(chan []core.SearchResult, 1)
	go func() {
		result, _ := service.Search(ctx, core.SearchInput{Query: "queryable", Limit: 8})
		searched <- result
	}()
	select {
	case <-searched:
		t.Fatal("pending query bypassed the sealed current-revision vector job")
	case <-time.After(50 * time.Millisecond):
	}
	close(embedder.release)
	results := <-searched
	if len(results) == 0 || results[0].ID != created.Information.ID {
		t.Fatalf("pending query lost its current authority source: %#v", results)
	}
	record, exists := state.GetWithEmbedding(created.Information.ID)
	if !exists || len(record.Embedding) != 64 || record.SemanticReceipt != nil || record.Status != "pending" {
		t.Fatalf("pending query changed formal semantic state: %#v", record)
	}
}

func TestCandidateQueuePressureKeepsAuthoritySuccessfulAndRecoversPendingWork(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	authority, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	state, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	embedder := newGatedRepresentationCapability()
	port, err := authorityport.Bind(authority)
	if err != nil {
		t.Fatal(err)
	}
	service, err := core.NewCollaborativeWithAuthority(port, state, embedder)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	queuePressureObserved := false
	for batch := 0; batch < 4; batch++ {
		inputs := make([]core.CreateInput, 20)
		for index := range inputs {
			inputs[index] = core.CreateInput{Content: fmt.Sprintf("durable queue fact %d-%d", batch, index)}
		}
		results, createErr := service.CreateBatch(ctx, inputs)
		if createErr != nil || len(results) != len(inputs) {
			t.Fatalf("authority batch failed under representation pressure: %d %v", len(results), createErr)
		}
		for _, result := range results {
			if result.Error != "" || result.Result == nil {
				t.Fatalf("background queue error became an authority failure: %#v", result)
			}
			if strings.Contains(result.Result.Organization.Error, "队列已满") {
				queuePressureObserved = true
			}
		}
	}
	if !queuePressureObserved || len(authority.All()) != 80 {
		t.Fatalf("queue pressure was not isolated from authority success: observed=%v assets=%d", queuePressureObserved, len(authority.All()))
	}
	close(embedder.release)
	if _, err := service.Search(ctx, core.SearchInput{Query: "durable queue fact", Limit: 8}); err != nil {
		t.Fatal(err)
	}
	for _, asset := range authority.All() {
		record, exists := state.GetWithEmbedding(asset.ID)
		if !exists || record.AssetRevision != asset.Revision || len(record.Embedding) == 0 {
			t.Fatalf("queue-full pending work did not recover: %#v", record)
		}
	}
}

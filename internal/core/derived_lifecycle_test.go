//go:build ownward_migration

package core

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/authorityport"
	"github.com/HJSunDev/ownward/internal/derived"
)

func TestCollaborativeKernelBuildsAndIncrementallyCatchesUpIsolatedGeneration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	assets, err := assetlog.Open(filepath.Join(root, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authorityport.Bind(assets)
	if err != nil {
		t.Fatal(err)
	}
	active, err := derived.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewCollaborativeWithAuthority(authority, active, constantEmbedding{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateBatch(ctx, []CreateInput{{Content: "alpha durable fact"}, {Content: "beta durable fact"}})
	if err != nil || len(created) != 2 || created[0].Result == nil {
		t.Fatalf("create fixture: %#v %v", created, err)
	}
	if _, err := service.Maintain(ctx, true); err != nil {
		t.Fatal(err)
	}
	activeGeneration := active.Generation()
	oldRecord, exists := active.GetWithEmbedding(created[0].Result.Information.ID)
	if !exists {
		t.Fatal("active derived fixture is missing")
	}
	oldRecord.Analysis.Summary = "private-active-derived-result"
	oldRecord.Status = "ready"
	if err := active.Put(oldRecord); err != nil {
		t.Fatal(err)
	}
	candidate, counts, err := service.BuildIsolatedDerivedGeneration(ctx, "gen-real-candidate", authority.ListCurrent())
	if err != nil || candidate == nil || counts["pending"] != 2 {
		t.Fatalf("real collaborative baseline build failed: counts=%#v err=%v", counts, err)
	}
	isolated, exists := candidate.Get(created[0].Result.Information.ID)
	if !exists || isolated.Analysis.Summary == oldRecord.Analysis.Summary {
		t.Fatal("candidate baseline reused the active derived result")
	}
	readOnly, err := newCandidateSnapshotAuthority(authority.ListCurrent())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readOnly.CreateAsset(created[0].Result.Information); err == nil {
		t.Fatal("isolated candidate authority accepted a product write")
	}
	snapshot, err := informationSnapshotDigest(authority.ListCurrent())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := candidate.SealGeneration(derived.GenerationMetadata{AssetCount: 2, AssetSnapshot: snapshot, EmbeddingSpace: constantEmbedding{}.Space().ID}); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(); err != nil {
		t.Fatal(err)
	}
	changed := created[0].Result.Information
	changed.Revision++
	changed.Content = "alpha durable fact updated"
	scope, err := authority.UpdateAsset(changed, changed.Revision-1)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = derived.OpenGeneration(filepath.Join(root, "state"), "gen-real-candidate")
	if err != nil {
		t.Fatal(err)
	}
	candidateService, err := NewCollaborativeWithAuthority(authority, candidate, constantEmbedding{})
	if err != nil {
		t.Fatal(err)
	}
	if err := candidateService.ApplyAcceptedChanges(ctx, scope); err != nil {
		t.Fatal(err)
	}
	record, exists := candidate.Get(changed.ID)
	if !exists || record.AssetRevision != changed.Revision {
		t.Fatalf("accepted authority delta did not reach isolated generation: %#v", record)
	}
	if active.Generation() != activeGeneration {
		t.Fatalf("isolated lifecycle polluted the active generation: before=%s after=%s", activeGeneration, active.Generation())
	}
	if len(authority.ListCurrent()) != 2 {
		t.Fatal("candidate lifecycle wrote a second authority truth")
	}
	_ = candidateService.Close()
	_ = service.Close()
	_ = assets.Close()
}

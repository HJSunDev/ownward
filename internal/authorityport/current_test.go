package authorityport

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
)

func TestCurrentMapsVersionedReadsChangeScopeAndRecovery(t *testing.T) {
	store, err := assetlog.Open(filepath.Join(t.TempDir(), "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	port, err := Bind(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := domain.Information{
		Schema: domain.AssetSchema, ID: "a", Revision: 1, CreatedAt: now, UpdatedAt: now,
		Kind: domain.KindGeneral, Content: "stable authority fact",
	}
	scope, err := port.CreateAsset(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.Validate(); err != nil || scope.Schema != contract.AssetChangeScopeSchema || scope.Assets[0].Revision != 1 {
		t.Fatalf("invalid change scope: %#v (%v)", scope, err)
	}
	if _, exists := port.ReadVersion("a", 1); !exists {
		t.Fatal("current revision was not readable by version")
	}
	if _, exists := port.ReadVersion("a", 2); exists {
		t.Fatal("a non-current revision was exposed")
	}
	updated := value
	updated.Revision = 2
	updated.UpdatedAt = now.Add(time.Second)
	updated.Content = "updated authority fact"
	if scope, err = port.UpdateAsset(updated, 1); err != nil || scope.Assets[0].Revision != 2 {
		t.Fatalf("update scope: %#v (%v)", scope, err)
	}
	archive := filepath.Join(t.TempDir(), "backup.json")
	if err := port.Backup(archive); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, restored); err != nil {
		t.Fatal(err)
	}
	reopened, err := assetlog.Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, exists := reopened.Get("a"); !exists || got.Revision != 2 || got.Content != updated.Content {
		t.Fatalf("restored authority mismatch: %#v", got)
	}
}

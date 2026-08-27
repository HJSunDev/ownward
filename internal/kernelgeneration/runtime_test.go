package kernelgeneration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/authorityport"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/embedding"
	compositionv1 "github.com/HJSunDev/ownward/manifests/compositions/v1"
)

func TestCompleteGenerationOpensAsOneUnitAndRejectsPartMixing(t *testing.T) {
	manifest, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(t.TempDir(), "product")
	store, err := assetlog.Open(filepath.Join(dataDir, "assets"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authority, err := authorityport.Bind(store)
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(manifest, OpenRequest{
		DataDir: dataDir, Mode: Collaborative, Authority: authority,
		VectorProvider: embedding.HashForTesting{Dimensions: 32},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), core.CreateInput{Kind: domain.KindGeneral, Content: "完整内核世代必须统一打开。"})
	if err != nil || created.Information.ID == "" {
		t.Fatalf("complete generation did not preserve behavior: %#v %v", created, err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	mixed := manifest
	for componentIndex := range mixed.Components {
		if mixed.Components[componentIndex].Role == "kernel" {
			mixed.Components[componentIndex].Dependencies[0].Identity = strings.Repeat("a", 64)
		}
	}
	rejectedDir := filepath.Join(t.TempDir(), "rejected")
	if _, err := Open(mixed, OpenRequest{
		DataDir: rejectedDir, Mode: Collaborative, Authority: authority,
		VectorProvider: embedding.HashForTesting{Dimensions: 32},
	}); err == nil {
		t.Fatal("mixed generation parts were accepted")
	}
	if _, err := os.Stat(rejectedDir); !os.IsNotExist(err) {
		t.Fatalf("rejected generation created state: %v", err)
	}
}

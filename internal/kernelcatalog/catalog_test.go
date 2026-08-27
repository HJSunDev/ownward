package kernelcatalog

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
)

func TestCurrentCatalogMapsFrozenV0AndV1WithoutChangingQualification(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	catalog, err := Load(filepath.Join(root, "manifests", "kernel-generations", "v1", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	verification, err := Verify(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if verification.FormalBaseline != "v0" || len(verification.UnpromotedCandidates) != 1 || verification.UnpromotedCandidates[0] != "v1" {
		t.Fatalf("candidate lifecycle was reinterpreted: %#v", verification)
	}
	if verification.GitIsGenerationIdentity || verification.ControlIsQualification {
		t.Fatalf("audit/control leaked into candidate identity: %#v", verification)
	}
	if err := VerifyFrozenBaseline(root, filepath.FromSlash("benchmarks/acceptance/migration/v1/frozen-baseline.json"), catalog); err != nil {
		t.Fatal(err)
	}
	v1 := generationByName(t, catalog, "v1")
	if len(v1.Mapping.Evidence) != 4 {
		t.Fatalf("V1 internal checkpoints were reinterpreted: %#v", v1.Mapping.Evidence)
	}
}

func TestGenerationIdentityExcludesGitAndPathsButTracksRealDependencies(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	catalog, err := Load(filepath.Join(root, "manifests", "kernel-generations", "v1", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	v1 := generationByName(t, catalog, "v1")
	original := v1.Kernel.Identity
	v1.Audit.SourceGit = strings.Repeat("f", 40)
	v1.Kernel.Content[0].Path = "unrelated/audit/location.go"
	identity, err := composition.ComponentIdentity(v1.Kernel)
	if err != nil || identity != original {
		t.Fatalf("Git/path became generation identity: %s %v", identity, err)
	}
	v1.Kernel.Dependencies[0].Identity = strings.Repeat("a", 64)
	identity, err = composition.ComponentIdentity(v1.Kernel)
	if err != nil || identity == original {
		t.Fatalf("real direct dependency did not change generation identity: %s %v", identity, err)
	}
}

func TestCatalogIdentityExcludesAuditSource(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	catalog, err := Load(filepath.Join(root, "manifests", "kernel-generations", "v1", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog.Generations[0].Audit.SourceGit = strings.Repeat("f", 40)
	catalog.Generations[0].Audit.Note = "a different retrieval location"
	if verification, err := Verify(catalog); err != nil || !verification.Passed {
		t.Fatalf("audit source became a catalog or generation identity: %#v %v", verification, err)
	}
}

func TestCatalogRejectsMixedPartsMissingFacetsAndFalsePromotion(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	original, err := Load(filepath.Join(root, "manifests", "kernel-generations", "v1", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Catalog)
	}{
		{name: "mixed-direct-dependency", mutate: func(value *Catalog) {
			value.Generations[0].Kernel.Dependencies[0].Identity = value.Generations[1].Dependencies[0].Identity
		}},
		{name: "missing-facet", mutate: func(value *Catalog) {
			value.Generations[0].Facets = value.Generations[0].Facets[1:]
		}},
		{name: "candidate-pretends-to-be-baseline", mutate: func(value *Catalog) {
			generation := generationByNameMutable(t, value, "v1")
			generation.Mapping.Lifecycle = FormalBaseline
			generation.Mapping.AcceptanceBaseline = true
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := cloneCatalog(t, original)
			test.mutate(&value)
			resealIdentitiesForTest(t, &value)
			if _, err := Verify(value); err == nil {
				t.Fatal("invalid kernel catalog was accepted")
			}
		})
	}
}

func TestAuthoritativeContractsRejectTamperEvenAfterIdentityRecalculation(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	original, err := Load(filepath.Join(root, "manifests", "kernel-generations", "v1", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := composition.Load(filepath.Join(root, "manifests", "compositions", "v1", "current-collaborative.json"))
	if err != nil {
		t.Fatal(err)
	}
	authoritative := make(map[string][]contract.Reference, len(manifest.Components))
	for _, component := range manifest.Components {
		authoritative[component.Role] = append([]contract.Reference(nil), component.Contracts...)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Catalog)
	}{
		{name: "tampered-reference", mutate: func(value *Catalog) {
			value.Generations[0].Kernel.Contracts[0].DefinitionSHA256 = strings.Repeat("a", 64)
		}},
		{name: "deleted-reference", mutate: func(value *Catalog) {
			value.Generations[0].Kernel.Contracts = value.Generations[0].Kernel.Contracts[1:]
		}},
		{name: "misbound-reference", mutate: func(value *Catalog) {
			dependency := dependencyByRoleMutable(t, &value.Generations[0], "semantic")
			dependency.Contracts = append([]contract.Reference(nil), authoritative["vector"]...)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := cloneCatalog(t, original)
			test.mutate(&value)
			resealAllComponentIdentitiesForTest(t, &value)
			if _, err := Verify(value); err != nil {
				t.Fatalf("attacker-resealed catalog should pass its self-contained identity before authority comparison: %v", err)
			}
			if err := VerifyAuthoritativeContracts(value, authoritative); err == nil {
				t.Fatal("catalog contract drift was accepted after attacker recalculated all identities")
			}
		})
	}
}

func resealIdentitiesForTest(t *testing.T, catalog *Catalog) {
	t.Helper()
	for index := range catalog.Generations {
		identity, err := composition.ComponentIdentity(catalog.Generations[index].Kernel)
		if err != nil {
			t.Fatal(err)
		}
		catalog.Generations[index].Kernel.Identity = identity
	}
	identity, err := catalogIdentity(catalog.Generations)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Identity = identity
}

func resealAllComponentIdentitiesForTest(t *testing.T, catalog *Catalog) {
	t.Helper()
	for generationIndex := range catalog.Generations {
		generation := &catalog.Generations[generationIndex]
		byRole := make(map[string]string, len(generation.Dependencies))
		for dependencyIndex := range generation.Dependencies {
			dependency := &generation.Dependencies[dependencyIndex]
			identity, err := composition.ComponentIdentity(*dependency)
			if err != nil {
				t.Fatal(err)
			}
			dependency.Identity = identity
			byRole[dependency.Role] = identity
		}
		for dependencyIndex := range generation.Kernel.Dependencies {
			dependency := &generation.Kernel.Dependencies[dependencyIndex]
			dependency.Identity = byRole[dependency.Role]
		}
		identity, err := composition.ComponentIdentity(generation.Kernel)
		if err != nil {
			t.Fatal(err)
		}
		generation.Kernel.Identity = identity
	}
	identity, err := catalogIdentity(catalog.Generations)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Identity = identity
}

func dependencyByRoleMutable(t *testing.T, generation *Generation, role string) *composition.Component {
	t.Helper()
	for index := range generation.Dependencies {
		if generation.Dependencies[index].Role == role {
			return &generation.Dependencies[index]
		}
	}
	t.Fatalf("dependency not found: %s", role)
	return nil
}

func generationByName(t *testing.T, catalog Catalog, name string) Generation {
	t.Helper()
	for _, value := range catalog.Generations {
		if value.Name == name {
			return value
		}
	}
	t.Fatalf("generation not found: %s", name)
	return Generation{}
}

func generationByNameMutable(t *testing.T, catalog *Catalog, name string) *Generation {
	t.Helper()
	for index := range catalog.Generations {
		if catalog.Generations[index].Name == name {
			return &catalog.Generations[index]
		}
	}
	t.Fatalf("generation not found: %s", name)
	return nil
}

func cloneCatalog(t *testing.T, value Catalog) Catalog {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result Catalog
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

package contract_test

import (
	"testing"

	"github.com/HJSunDev/ownward/internal/authorityport"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/embedding"
)

var _ contract.AssetAuthority = (*authorityport.Current)(nil)
var _ contract.AssetRestore = authorityport.Restore
var _ contract.VectorCapability = embedding.Unavailable{}

func TestContractCatalogIsVersionedAndDeterministic(t *testing.T) {
	definitions := contract.Definitions()
	if len(definitions) != 9 {
		t.Fatalf("contract definitions = %d, want 9", len(definitions))
	}
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		key := definition.ID
		if definition.Version != 1 || definition.Responsibility == "" || len(definition.Operations) == 0 || len(definition.Schemas) == 0 || definition.Source == "" {
			t.Fatalf("incomplete contract definition: %#v", definition)
		}
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate contract id: %s", key)
		}
		seen[key] = struct{}{}
		first, err := contract.DefinitionSHA256(definition)
		if err != nil {
			t.Fatal(err)
		}
		second, err := contract.DefinitionSHA256(definition)
		if err != nil || first != second || len(first) != 64 {
			t.Fatalf("unstable definition identity for %s: %q / %q (%v)", key, first, second, err)
		}
	}
}

func TestChangeScopeAndMinimumControlStateRejectAmbiguity(t *testing.T) {
	if err := (contract.ChangeScope{Schema: contract.AssetChangeScopeSchema, Assets: []contract.AssetVersion{{ID: "a", Revision: 1}}}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (contract.ChangeScope{Schema: contract.AssetChangeScopeSchema, Assets: []contract.AssetVersion{{ID: "a", Revision: 1}, {ID: "a", Revision: 2}}}).Validate(); err == nil {
		t.Fatal("duplicate asset change was accepted")
	}
	state := contract.ControlState{
		Schema: contract.ControlStateSchema, Revision: 1,
		ActiveComposition: "a", ActiveKernelGeneration: "g",
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	state.ActiveComposition = ""
	if err := state.Validate(); err == nil {
		t.Fatal("empty active composition was accepted")
	}
}

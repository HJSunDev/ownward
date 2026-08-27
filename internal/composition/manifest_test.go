package composition

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
)

func TestSealIsStableAndIgnoresGitAndUnrelatedFiles(t *testing.T) {
	root, manifest := testRepository(t)
	first, err := Seal(root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Seal(root, first)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same inputs produced different sealed manifests")
	}
	kernel := componentByRole(first, "kernel").Identity
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, ".git/HEAD", "ref: refs/heads/changed")
	writeFile(t, root, "docs/unrelated.md", "unrelated")
	third, err := Seal(root, first)
	if err != nil {
		t.Fatal(err)
	}
	if third.Identity != first.Identity || componentByRole(third, "kernel").Identity != kernel {
		t.Fatal("Git or unrelated documentation changed a content identity")
	}
	if _, err := Verify(root, first); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "components/access.txt", "access-v2")
	fourth, err := Seal(root, third)
	if err != nil {
		t.Fatal(err)
	}
	if componentByRole(fourth, "kernel").Identity != kernel {
		t.Fatal("an unrelated access component changed the kernel identity")
	}
	if componentByRole(fourth, "access").Identity == componentByRole(third, "access").Identity ||
		componentByRole(fourth, "assembly").Identity == componentByRole(third, "assembly").Identity ||
		fourth.Identity == third.Identity {
		t.Fatal("an access change did not reach itself, assembly, and the composition")
	}
}

func TestDirectDependencyChangePropagatesOnlyAlongDependencyGraph(t *testing.T) {
	root, draft := testRepository(t)
	before, err := Seal(root, draft)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "components/vector.txt", "vector-v2")
	after, err := Seal(root, before)
	if err != nil {
		t.Fatal(err)
	}
	changed := map[string]bool{}
	for _, component := range before.Components {
		changed[component.Role] = component.Identity != componentByRole(after, component.Role).Identity
	}
	for _, role := range []string{"vector", "kernel", "access", "assembly"} {
		if !changed[role] {
			t.Fatalf("direct dependency change did not reach %s", role)
		}
	}
	for _, role := range []string{"authority-substrate", "semantic", "product-rules"} {
		if changed[role] {
			t.Fatalf("unrelated component identity changed: %s", role)
		}
	}
	if before.Identity == after.Identity {
		t.Fatal("composition identity did not change")
	}
}

func TestContractContentChangePropagatesOnlyToItsRoleAndDependents(t *testing.T) {
	root, draft := testRepository(t)
	before, err := Seal(root, draft)
	if err != nil {
		t.Fatal(err)
	}
	definition, exists := contract.Resolve(contract.VectorCapabilityContract, 1)
	if !exists {
		t.Fatal("missing vector contract")
	}
	writeFile(t, root, definition.Source, "vector-contract-v2")
	after, err := Seal(root, before)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"vector", "kernel", "access", "assembly"} {
		if componentByRole(before, role).Identity == componentByRole(after, role).Identity {
			t.Fatalf("contract content change did not reach %s", role)
		}
	}
	for _, role := range []string{"authority-substrate", "semantic", "product-rules"} {
		if componentByRole(before, role).Identity != componentByRole(after, role).Identity {
			t.Fatalf("unrelated component changed after vector contract update: %s", role)
		}
	}
}

func TestVerifyRejectsInvalidCompositionsWithoutWritingState(t *testing.T) {
	root, draft := testRepository(t)
	sealed, err := Seal(root, draft)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(Manifest) Manifest{
		"missing identity": func(value Manifest) Manifest {
			value.Identity = ""
			return value
		},
		"missing component identity": func(value Manifest) Manifest {
			value.Components[0].Identity = ""
			return value
		},
		"duplicate role": func(value Manifest) Manifest {
			value.Components = append(value.Components, value.Components[0])
			return value
		},
		"missing role": func(value Manifest) Manifest {
			value.Components = value.Components[1:]
			return value
		},
		"unknown contract": func(value Manifest) Manifest {
			value.Components[0].Contracts[0].ID = "ownward.unknown"
			return value
		},
		"incompatible version": func(value Manifest) Manifest {
			value.Components[0].Contracts[0].Version = 2
			return value
		},
		"misbound dependency": func(value Manifest) Manifest {
			componentByRole(value, "access").Dependencies[0].Identity = strings.Repeat("a", 64)
			return value
		},
		"content drift": func(value Manifest) Manifest {
			value.Components[0].Content[0].SHA256 = strings.Repeat("b", 64)
			return value
		},
	}
	before := snapshotFiles(t, root)
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			value := cloneForTest(t, sealed)
			value = mutate(value)
			if _, err := Verify(root, value); err == nil {
				t.Fatal("invalid composition was accepted")
			}
		})
	}
	after := snapshotFiles(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed verification wrote repository or state files")
	}
}

func TestDependencyCycleIsRejected(t *testing.T) {
	a := &Component{Role: "a", Dependencies: []Dependency{{Role: "b"}}}
	b := &Component{Role: "b", Dependencies: []Dependency{{Role: "a"}}}
	if err := validateGraph(map[string]*Component{"a": a, "b": b}); err == nil || !strings.Contains(err.Error(), "循环") {
		t.Fatalf("cycle was not rejected: %v", err)
	}
}

func TestVerifyRejectsChangedDeclaredContent(t *testing.T) {
	root, draft := testRepository(t)
	sealed, err := Seal(root, draft)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "components/kernel.txt", "tampered")
	if _, err := Verify(root, sealed); err == nil || !strings.Contains(err.Error(), "摘要漂移") {
		t.Fatalf("changed component content was not rejected: %v", err)
	}
}

func TestVerifySealedNeedsNoRepositoryAndRejectsIdentityDrift(t *testing.T) {
	root, draft := testRepository(t)
	sealed, err := Seal(root, draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySealed(sealed); err != nil {
		t.Fatalf("sealed release composition needed source files: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if result, err := VerifySealed(sealed); err != nil || !result.Passed || result.Composition != sealed.Identity {
		t.Fatalf("sealed release composition did not survive source removal: %#v %v", result, err)
	}
	tampered := cloneForTest(t, sealed)
	tampered.Components[0].Content[0].SHA256 = strings.Repeat("f", 64)
	if _, err := VerifySealed(tampered); err == nil || !strings.Contains(err.Error(), "身份漂移") {
		t.Fatalf("tampered embedded composition was accepted: %v", err)
	}
}

func TestCurrentCollaborativeManifestVerifies(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "manifests", "compositions", "v1", "current-collaborative.json")
	manifest, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range manifest.Components {
		for _, content := range component.Content {
			if strings.HasPrefix(content.Path, ".tmp/") {
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(content.Path))); os.IsNotExist(err) {
					t.Skip("frozen candidate artifacts are not present in this checkout")
				}
			}
		}
	}
	result, err := Verify(root, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Passed || result.Components != 7 || result.GitIsIdentity || result.ActiveStateModified {
		t.Fatalf("unexpected verification: %#v", result)
	}
}

func testRepository(t *testing.T) (string, Manifest) {
	t.Helper()
	root := t.TempDir()
	for _, definition := range contract.Definitions() {
		writeFile(t, root, definition.Source, definition.ID+"-v1")
	}
	components := []Component{}
	for _, role := range []string{"authority-substrate", "semantic", "vector", "product-rules", "kernel", "access", "assembly"} {
		path := filepath.ToSlash(filepath.Join("components", role+".txt"))
		writeFile(t, root, path, role+"-v1")
		components = append(components, Component{
			Role: role, Content: []Content{{Name: role, Path: path}}, Config: map[string]any{"mode": role},
		})
	}
	return root, Manifest{Schema: ManifestSchema, Name: "test", Components: components, Audit: map[string]string{"git_source": "audit-only"}}
}

func writeFile(t *testing.T, root, relative, value string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func componentByRole(manifest Manifest, role string) *Component {
	for index := range manifest.Components {
		if manifest.Components[index].Role == role {
			return &manifest.Components[index]
		}
	}
	panic("missing component " + role)
}

func cloneForTest(t *testing.T, source Manifest) Manifest {
	t.Helper()
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result Manifest
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func snapshotFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		value, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = string(value)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

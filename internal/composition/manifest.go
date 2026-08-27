package composition

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HJSunDev/ownward/internal/contract"
)

const ManifestSchema = "ownward.composition/v1"

type Content struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type Dependency struct {
	Role     string `json:"role"`
	Identity string `json:"identity"`
}

type Component struct {
	Role         string               `json:"role"`
	Contracts    []contract.Reference `json:"contracts"`
	Content      []Content            `json:"content"`
	Config       map[string]any       `json:"config"`
	Dependencies []Dependency         `json:"dependencies"`
	Identity     string               `json:"identity"`
}

type Manifest struct {
	Schema     string            `json:"schema"`
	Name       string            `json:"name"`
	Components []Component       `json:"components"`
	Identity   string            `json:"identity"`
	Audit      map[string]string `json:"audit,omitempty"`
}

type Verification struct {
	Schema              string `json:"schema"`
	Passed              bool   `json:"passed"`
	Composition         string `json:"composition"`
	Components          int    `json:"components"`
	Contracts           int    `json:"contracts"`
	ContentFiles        int    `json:"content_files"`
	GitIsIdentity       bool   `json:"git_is_identity"`
	ActiveStateModified bool   `json:"active_state_modified"`
}

type roleSpec struct {
	contracts    []contractKey
	dependencies []string
}

type contractKey struct {
	id      string
	version int
}

var roles = map[string]roleSpec{
	"authority-substrate": {
		contracts: []contractKey{{contract.AssetAuthorityContract, 1}, {contract.ControlStateContract, 1}},
	},
	"semantic": {
		contracts: []contractKey{{contract.SemanticCapabilityContract, 1}},
	},
	"vector": {
		contracts: []contractKey{{contract.VectorCapabilityContract, 1}},
	},
	"product-rules": {
		contracts: []contractKey{{contract.ProductRulesContract, 1}},
	},
	"kernel": {
		contracts:    []contractKey{{contract.KernelLifecycleContract, 1}, {contract.ProductCapabilityContract, 1}},
		dependencies: []string{"authority-substrate", "product-rules", "semantic", "vector"},
	},
	"access": {
		contracts:    []contractKey{{contract.AccessAdapterContract, 1}},
		dependencies: []string{"kernel", "product-rules"},
	},
	"assembly": {
		contracts:    []contractKey{{contract.AssemblyContract, 1}},
		dependencies: []string{"access", "authority-substrate", "kernel", "product-rules", "semantic", "vector"},
	},
}

func Load(path string) (Manifest, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("读取组合清单: %w", err)
	}
	return Parse(encoded)
}

// Parse decodes one composition description without consulting a repository.
// It is used for the exact source manifest embedded in a release binary.
func Parse(encoded []byte) (Manifest, error) {
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("解析组合清单: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func WriteJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// Seal computes contract, content, component and composition identities. It
// reads the declared repository files but never writes either the repository
// or product state.
func Seal(repository string, source Manifest) (Manifest, error) {
	root, err := filepath.Abs(repository)
	if err != nil {
		return Manifest{}, fmt.Errorf("解析仓库路径: %w", err)
	}
	manifest, err := cloneManifest(source)
	if err != nil {
		return Manifest{}, err
	}
	if manifest.Schema != ManifestSchema || strings.TrimSpace(manifest.Name) == "" {
		return Manifest{}, errors.New("组合清单 schema 或名称无效")
	}
	if err := rejectVolatileOrSecret(manifest.Audit, "audit"); err != nil {
		return Manifest{}, err
	}
	byRole, err := indexComponents(manifest.Components)
	if err != nil {
		return Manifest{}, err
	}
	if len(byRole) != len(roles) {
		return Manifest{}, fmt.Errorf("组合角色不完整: 需要 %d 个，实际 %d 个", len(roles), len(byRole))
	}
	for role := range roles {
		if _, exists := byRole[role]; !exists {
			return Manifest{}, fmt.Errorf("组合缺少角色: %s", role)
		}
	}
	for index := range manifest.Components {
		component := &manifest.Components[index]
		if err := prepareComponent(root, component); err != nil {
			return Manifest{}, fmt.Errorf("组件 %s: %w", component.Role, err)
		}
		byRole[component.Role] = component
	}
	if err := validateGraph(byRole); err != nil {
		return Manifest{}, err
	}
	identities := make(map[string]string, len(byRole))
	visiting := make(map[string]bool, len(byRole))
	var sealRole func(string) error
	sealRole = func(role string) error {
		if identities[role] != "" {
			return nil
		}
		if visiting[role] {
			return fmt.Errorf("组合依赖存在循环: %s", role)
		}
		visiting[role] = true
		component := byRole[role]
		for index := range component.Dependencies {
			dependency := &component.Dependencies[index]
			if err := sealRole(dependency.Role); err != nil {
				return err
			}
			dependency.Identity = identities[dependency.Role]
		}
		identity, err := componentIdentity(*component)
		if err != nil {
			return fmt.Errorf("计算组件 %s 身份: %w", role, err)
		}
		component.Identity = identity
		identities[role] = identity
		visiting[role] = false
		return nil
	}
	for role := range byRole {
		if err := sealRole(role); err != nil {
			return Manifest{}, err
		}
	}
	sort.Slice(manifest.Components, func(left, right int) bool { return manifest.Components[left].Role < manifest.Components[right].Role })
	identity, err := compositionIdentity(manifest.Components)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Identity = identity
	return manifest, nil
}

func Verify(repository string, manifest Manifest) (Verification, error) {
	if err := validateDeclared(repository, manifest); err != nil {
		return Verification{}, err
	}
	sealed, err := Seal(repository, manifest)
	if err != nil {
		return Verification{}, err
	}
	want, err := canonicalJSON(sealed)
	if err != nil {
		return Verification{}, err
	}
	got, err := canonicalJSON(manifest)
	if err != nil {
		return Verification{}, err
	}
	if string(got) != string(want) {
		return Verification{}, errors.New("组合清单未封存或身份、顺序、直接依赖、契约或内容摘要发生漂移")
	}
	return verificationFor(manifest), nil
}

// VerifySealed validates the structure and all content-derived identities of a
// composition already sealed during the build. It deliberately performs no
// repository discovery or source-file reads.
func VerifySealed(manifest Manifest) (Verification, error) {
	if err := validateSealed(manifest); err != nil {
		return Verification{}, err
	}
	return verificationFor(manifest), nil
}

func verificationFor(manifest Manifest) Verification {
	contracts := 0
	content := 0
	for _, component := range manifest.Components {
		contracts += len(component.Contracts)
		content += len(component.Content)
	}
	return Verification{
		Schema: "ownward.composition-check/v1", Passed: true, Composition: manifest.Identity,
		Components: len(manifest.Components), Contracts: contracts, ContentFiles: content,
		GitIsIdentity: false, ActiveStateModified: false,
	}
}

func validateDeclared(repository string, manifest Manifest) error {
	if err := validateSealed(manifest); err != nil {
		return err
	}
	root, err := filepath.Abs(repository)
	if err != nil {
		return err
	}
	for _, component := range manifest.Components {
		for _, reference := range component.Contracts {
			definition, _ := contract.Resolve(reference.ID, reference.Version)
			expected, err := resolvedContractReference(root, definition)
			if err != nil || reference.DefinitionSHA256 != expected.DefinitionSHA256 {
				return fmt.Errorf("组件 %s 契约定义摘要漂移: %s/v%d", component.Role, reference.ID, reference.Version)
			}
		}
		for _, item := range component.Content {
			path, err := confinedFile(root, item.Path)
			if err != nil {
				return err
			}
			digest, err := fileSHA256(path)
			if err != nil || digest != item.SHA256 {
				return fmt.Errorf("组件 %s 内容摘要漂移: %s", component.Role, item.Name)
			}
		}
	}
	return nil
}

func validateSealed(manifest Manifest) error {
	if manifest.Schema != ManifestSchema || strings.TrimSpace(manifest.Name) == "" {
		return errors.New("组合清单 schema 或名称无效")
	}
	if !isSHA256(manifest.Identity) {
		return errors.New("组合清单缺失有效身份")
	}
	if err := rejectVolatileOrSecret(manifest.Audit, "audit"); err != nil {
		return err
	}
	components := make(map[string]*Component, len(manifest.Components))
	for index := range manifest.Components {
		component := &manifest.Components[index]
		if _, exists := roles[component.Role]; !exists {
			return fmt.Errorf("未知组件角色: %s", component.Role)
		}
		if _, exists := components[component.Role]; exists {
			return fmt.Errorf("组合角色重复: %s", component.Role)
		}
		components[component.Role] = component
	}
	if len(components) != len(roles) {
		return fmt.Errorf("组合角色不完整: 需要 %d 个，实际 %d 个", len(roles), len(components))
	}
	for role := range roles {
		if _, exists := components[role]; !exists {
			return fmt.Errorf("组合缺少角色: %s", role)
		}
	}
	if err := validateGraph(components); err != nil {
		return err
	}
	for role, component := range components {
		if !isSHA256(component.Identity) {
			return fmt.Errorf("组件 %s 缺失有效身份", role)
		}
		if err := rejectVolatileOrSecret(component.Config, "config"); err != nil {
			return err
		}
		spec := roles[role]
		if len(component.Contracts) != len(spec.contracts) {
			return fmt.Errorf("组件 %s 契约集合不兼容", role)
		}
		actualContracts := make(map[string]contract.Reference, len(component.Contracts))
		for _, reference := range component.Contracts {
			key := fmt.Sprintf("%s/v%d", reference.ID, reference.Version)
			if _, exists := actualContracts[key]; exists {
				return fmt.Errorf("组件 %s 契约重复: %s", role, key)
			}
			_, exists := contract.Resolve(reference.ID, reference.Version)
			if !exists {
				knownID := false
				for _, candidate := range contract.Definitions() {
					knownID = knownID || candidate.ID == reference.ID
				}
				if knownID {
					return fmt.Errorf("组件 %s 使用不兼容契约版本: %s", role, key)
				}
				return fmt.Errorf("组件 %s 使用未知契约: %s", role, key)
			}
			if !isSHA256(reference.DefinitionSHA256) {
				return fmt.Errorf("组件 %s 契约定义身份无效: %s", role, key)
			}
			actualContracts[key] = reference
		}
		for _, expected := range spec.contracts {
			key := fmt.Sprintf("%s/v%d", expected.id, expected.version)
			if _, exists := actualContracts[key]; !exists {
				return fmt.Errorf("组件 %s 缺少兼容契约: %s", role, key)
			}
		}
		if len(component.Dependencies) != len(spec.dependencies) {
			return fmt.Errorf("组件 %s 直接依赖集合不兼容", role)
		}
		actualDependencies := make(map[string]string, len(component.Dependencies))
		for _, dependency := range component.Dependencies {
			if _, exists := actualDependencies[dependency.Role]; exists {
				return fmt.Errorf("组件 %s 直接依赖重复: %s", role, dependency.Role)
			}
			actualDependencies[dependency.Role] = dependency.Identity
			target, exists := components[dependency.Role]
			if !exists {
				return fmt.Errorf("组件 %s 缺少直接依赖: %s", role, dependency.Role)
			}
			if dependency.Identity != target.Identity {
				return fmt.Errorf("组件 %s 直接依赖错绑: %s", role, dependency.Role)
			}
		}
		for _, expected := range spec.dependencies {
			if _, exists := actualDependencies[expected]; !exists {
				return fmt.Errorf("组件 %s 缺少声明依赖: %s", role, expected)
			}
		}
		if len(component.Content) == 0 {
			return fmt.Errorf("组件 %s 缺少内容", role)
		}
		names := make(map[string]struct{}, len(component.Content))
		paths := make(map[string]struct{}, len(component.Content))
		for _, item := range component.Content {
			if strings.TrimSpace(item.Name) == "" || !isSHA256(item.SHA256) {
				return fmt.Errorf("组件 %s 内容身份无效", role)
			}
			if _, exists := names[item.Name]; exists {
				return fmt.Errorf("组件 %s 内容名称重复: %s", role, item.Name)
			}
			names[item.Name] = struct{}{}
			if filepath.IsAbs(item.Path) || strings.TrimSpace(item.Path) == "" || strings.TrimSpace(item.Path) != item.Path {
				return fmt.Errorf("组件 %s 内容路径无效: %s", role, item.Name)
			}
			clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(item.Path)))
			if clean == ".." || strings.HasPrefix(clean, "../") {
				return fmt.Errorf("组件 %s 内容路径越出仓库: %s", role, item.Name)
			}
			if _, exists := paths[clean]; exists {
				return fmt.Errorf("组件 %s 内容路径重复: %s", role, clean)
			}
			paths[clean] = struct{}{}
		}
		identity, err := componentIdentity(*component)
		if err != nil || identity != component.Identity {
			return fmt.Errorf("组件 %s 内容身份漂移", role)
		}
	}
	ordered := append([]Component(nil), manifest.Components...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Role < ordered[right].Role })
	identity, err := compositionIdentity(ordered)
	if err != nil || identity != manifest.Identity {
		return errors.New("组合身份与组件依赖图不一致")
	}
	return nil
}

func prepareComponent(root string, component *Component) error {
	spec, exists := roles[component.Role]
	if !exists {
		return errors.New("未知组件角色")
	}
	if err := rejectVolatileOrSecret(component.Config, "config"); err != nil {
		return err
	}
	expectedContracts := append([]contractKey(nil), spec.contracts...)
	sort.Slice(expectedContracts, func(left, right int) bool {
		if expectedContracts[left].id == expectedContracts[right].id {
			return expectedContracts[left].version < expectedContracts[right].version
		}
		return expectedContracts[left].id < expectedContracts[right].id
	})
	component.Contracts = make([]contract.Reference, len(expectedContracts))
	for index, expected := range expectedContracts {
		definition, exists := contract.Resolve(expected.id, expected.version)
		if !exists {
			return fmt.Errorf("未知契约: %s/v%d", expected.id, expected.version)
		}
		reference, err := resolvedContractReference(root, definition)
		if err != nil {
			return err
		}
		component.Contracts[index] = reference
	}
	if len(component.Content) == 0 {
		return errors.New("缺少组件内容")
	}
	names := make(map[string]struct{}, len(component.Content))
	paths := make(map[string]struct{}, len(component.Content))
	for index := range component.Content {
		item := &component.Content[index]
		if strings.TrimSpace(item.Name) == "" {
			return errors.New("组件内容名称为空")
		}
		if _, exists := names[item.Name]; exists {
			return fmt.Errorf("组件内容名称重复: %s", item.Name)
		}
		names[item.Name] = struct{}{}
		path, err := confinedFile(root, item.Path)
		if err != nil {
			return err
		}
		clean := filepath.ToSlash(item.Path)
		if _, exists := paths[clean]; exists {
			return fmt.Errorf("组件内容路径重复: %s", clean)
		}
		paths[clean] = struct{}{}
		item.Path = clean
		item.SHA256, err = fileSHA256(path)
		if err != nil {
			return err
		}
	}
	sort.Slice(component.Content, func(left, right int) bool { return component.Content[left].Name < component.Content[right].Name })
	component.Dependencies = make([]Dependency, len(spec.dependencies))
	for index, role := range spec.dependencies {
		component.Dependencies[index] = Dependency{Role: role}
	}
	sort.Slice(component.Dependencies, func(left, right int) bool {
		return component.Dependencies[left].Role < component.Dependencies[right].Role
	})
	return nil
}

func indexComponents(components []Component) (map[string]*Component, error) {
	result := make(map[string]*Component, len(components))
	for index := range components {
		role := strings.TrimSpace(components[index].Role)
		if role == "" {
			return nil, errors.New("组合包含空角色")
		}
		if _, exists := result[role]; exists {
			return nil, fmt.Errorf("组合角色重复: %s", role)
		}
		result[role] = &components[index]
	}
	return result, nil
}

func validateGraph(components map[string]*Component) error {
	for role, component := range components {
		for _, dependency := range component.Dependencies {
			if dependency.Role == role {
				return fmt.Errorf("组件不能依赖自身: %s", role)
			}
			if _, exists := components[dependency.Role]; !exists {
				return fmt.Errorf("组件 %s 缺失直接依赖: %s", role, dependency.Role)
			}
		}
	}
	state := make(map[string]uint8, len(components))
	var visit func(string) error
	visit = func(role string) error {
		if state[role] == 1 {
			return fmt.Errorf("组合依赖存在循环: %s", role)
		}
		if state[role] == 2 {
			return nil
		}
		state[role] = 1
		for _, dependency := range components[role].Dependencies {
			if err := visit(dependency.Role); err != nil {
				return err
			}
		}
		state[role] = 2
		return nil
	}
	for role := range components {
		if err := visit(role); err != nil {
			return err
		}
	}
	return nil
}

func componentIdentity(component Component) (string, error) {
	type contentIdentity struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	}
	content := make([]contentIdentity, len(component.Content))
	for index, item := range component.Content {
		content[index] = contentIdentity{item.Name, item.SHA256}
	}
	payload := struct {
		Schema       string               `json:"schema"`
		Role         string               `json:"role"`
		Contracts    []contract.Reference `json:"contracts"`
		Content      []contentIdentity    `json:"content"`
		Config       map[string]any       `json:"config"`
		Dependencies []Dependency         `json:"dependencies"`
	}{"ownward.component-identity/v1", component.Role, component.Contracts, content, component.Config, component.Dependencies}
	return digestJSON(payload)
}

// ComponentIdentity returns the deterministic identity of one already
// described component. Paths and Git metadata are deliberately excluded: the
// identity is formed only from the component's contracts, named content
// digests, configuration and declared direct dependency identities.
//
// Complete compositions remain the responsibility of Seal and Verify. This
// narrower entry exists for versioned capability generations whose component
// boundary must be verified independently before it is selected by assembly.
func ComponentIdentity(component Component) (string, error) {
	return componentIdentity(component)
}

func resolvedContractReference(root string, definition contract.Definition) (contract.Reference, error) {
	metadata, err := contract.DefinitionSHA256(definition)
	if err != nil {
		return contract.Reference{}, err
	}
	sourcePath, err := confinedFile(root, definition.Source)
	if err != nil {
		return contract.Reference{}, fmt.Errorf("契约 %s/v%d 来源无效: %w", definition.ID, definition.Version, err)
	}
	source, err := fileSHA256(sourcePath)
	if err != nil {
		return contract.Reference{}, err
	}
	digest, err := digestJSON(struct {
		Schema     string `json:"schema"`
		Definition string `json:"definition_sha256"`
		Source     string `json:"source_sha256"`
	}{"ownward.contract-identity/v1", metadata, source})
	if err != nil {
		return contract.Reference{}, err
	}
	return contract.Reference{ID: definition.ID, Version: definition.Version, DefinitionSHA256: digest}, nil
}

func compositionIdentity(components []Component) (string, error) {
	type componentIdentity struct {
		Role         string       `json:"role"`
		Identity     string       `json:"identity"`
		Dependencies []Dependency `json:"dependencies"`
	}
	values := make([]componentIdentity, len(components))
	for index, component := range components {
		values[index] = componentIdentity{component.Role, component.Identity, component.Dependencies}
	}
	return digestJSON(struct {
		Schema     string              `json:"schema"`
		Components []componentIdentity `json:"components"`
	}{"ownward.composition-identity/v1", values})
}

func confinedFile(root, relative string) (string, error) {
	trimmed := strings.TrimSpace(relative)
	if trimmed == "" || filepath.IsAbs(trimmed) || trimmed != relative {
		return "", fmt.Errorf("组件内容路径必须是仓库相对路径: %q", relative)
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(trimmed)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("组件内容路径越出仓库: %s", relative)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("组件内容缺失: %s", relative)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("组件内容不是普通文件: %s", relative)
	}
	return path, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func digestJSON(value any) (string, error) {
	encoded, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

func cloneManifest(source Manifest) (Manifest, error) {
	encoded, err := canonicalJSON(source)
	if err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	var result Manifest
	if err := decoder.Decode(&result); err != nil {
		return Manifest{}, err
	}
	return result, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("组合清单包含无效尾部: %w", err)
	}
	return errors.New("组合清单只能包含一个 JSON 对象")
}

func rejectVolatileOrSecret(value any, location string) error {
	forbidden := []string{"token", "secret", "credential", "password", "pid", "port"}
	var walk func(any, string) error
	walk = func(current any, path string) error {
		switch typed := current.(type) {
		case map[string]any:
			for key, nested := range typed {
				parts := strings.FieldsFunc(strings.ToLower(key), func(character rune) bool {
					return character == '_' || character == '-' || character == '.'
				})
				for _, word := range forbidden {
					if contains(parts, word) {
						return fmt.Errorf("组合清单不得固化秘密或易失字段: %s.%s", path, key)
					}
				}
				if err := walk(nested, path+"."+key); err != nil {
					return err
				}
			}
		case map[string]string:
			converted := make(map[string]any, len(typed))
			for key, nested := range typed {
				converted[key] = nested
			}
			return walk(converted, path)
		case []any:
			for index, nested := range typed {
				if err := walk(nested, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value, location)
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

package capabilitylifecycle

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

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
)

// CandidateContent is the only caller-authored part of a stateless candidate.
// Stable contracts, configuration and direct dependencies are inherited from
// the sealed baseline and cannot be hand assembled by the caller.
type CandidateContent struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func sealReplacement(repository string, current composition.Manifest, role string, content []CandidateContent) (composition.Component, error) {
	if _, err := composition.VerifySealed(current); err != nil {
		return composition.Component{}, fmt.Errorf("当前组合无效: %w", err)
	}
	root, err := filepath.Abs(repository)
	if err != nil {
		return composition.Component{}, err
	}
	baseline, exists := lookupComponent(current, role)
	if !exists {
		return composition.Component{}, fmt.Errorf("候选组件角色不属于当前组合: %s", role)
	}
	if len(content) == 0 {
		return composition.Component{}, errors.New("候选组件缺少内容")
	}
	candidate := composition.Component{
		Role: role, Contracts: append([]contract.Reference(nil), baseline.Contracts...),
		Config: cloneConfig(baseline.Config), Dependencies: append([]composition.Dependency(nil), baseline.Dependencies...),
	}
	candidate.Content = make([]composition.Content, len(content))
	names := make(map[string]struct{}, len(content))
	paths := make(map[string]struct{}, len(content))
	for index, source := range content {
		name := strings.TrimSpace(source.Name)
		if name == "" || name != source.Name {
			return composition.Component{}, errors.New("候选组件内容名称为空或未规范化")
		}
		if _, exists := names[name]; exists {
			return composition.Component{}, fmt.Errorf("候选组件内容名称重复: %s", name)
		}
		names[name] = struct{}{}
		path, clean, err := confinedCandidateFile(root, source.Path)
		if err != nil {
			return composition.Component{}, err
		}
		if _, exists := paths[clean]; exists {
			return composition.Component{}, fmt.Errorf("候选组件内容路径重复: %s", clean)
		}
		paths[clean] = struct{}{}
		digest, err := candidateFileSHA256(path)
		if err != nil {
			return composition.Component{}, err
		}
		candidate.Content[index] = composition.Content{Name: name, Path: clean, SHA256: digest}
	}
	sort.Slice(candidate.Content, func(left, right int) bool { return candidate.Content[left].Name < candidate.Content[right].Name })
	candidate.Identity, err = composition.ComponentIdentity(candidate)
	return candidate, err
}

func replaceComponent(current composition.Manifest, replacement composition.Component) (composition.Manifest, error) {
	if _, err := composition.VerifySealed(current); err != nil {
		return composition.Manifest{}, fmt.Errorf("当前组合无效: %w", err)
	}
	baseline, exists := lookupComponent(current, replacement.Role)
	if !exists {
		return composition.Manifest{}, fmt.Errorf("候选组件角色不属于当前组合: %s", replacement.Role)
	}
	baselineContracts, _ := json.Marshal(baseline.Contracts)
	replacementContracts, _ := json.Marshal(replacement.Contracts)
	if string(baselineContracts) != string(replacementContracts) {
		return composition.Manifest{}, errors.New("候选组件稳定契约与当前角色不兼容")
	}
	if len(replacement.Dependencies) != len(baseline.Dependencies) {
		return composition.Manifest{}, errors.New("候选组件直接依赖集合与当前角色不兼容")
	}
	expectedDependencies := make(map[string]string, len(baseline.Dependencies))
	for _, dependency := range baseline.Dependencies {
		expectedDependencies[dependency.Role] = dependency.Identity
	}
	for _, dependency := range replacement.Dependencies {
		if expected, exists := expectedDependencies[dependency.Role]; !exists || dependency.Identity != expected {
			return composition.Manifest{}, fmt.Errorf("候选组件直接依赖错绑: %s", dependency.Role)
		}
	}
	identity, err := composition.ComponentIdentity(replacement)
	if err != nil || identity != replacement.Identity {
		return composition.Manifest{}, errors.New("候选组件内容身份漂移")
	}
	target, err := cloneComposition(current)
	if err != nil {
		return composition.Manifest{}, err
	}
	components := make(map[string]*composition.Component, len(target.Components))
	for index := range target.Components {
		role := target.Components[index].Role
		if _, duplicate := components[role]; duplicate {
			return composition.Manifest{}, fmt.Errorf("组合角色重复: %s", role)
		}
		components[role] = &target.Components[index]
	}
	*components[replacement.Role] = replacement
	identities := make(map[string]string, len(components))
	for role, component := range components {
		identities[role] = component.Identity
	}
	for pass := 0; pass < len(components); pass++ {
		changed := false
		for role, component := range components {
			if role == replacement.Role {
				continue
			}
			for index := range component.Dependencies {
				identity, exists := identities[component.Dependencies[index].Role]
				if !exists {
					return composition.Manifest{}, fmt.Errorf("组件 %s 缺失直接依赖: %s", role, component.Dependencies[index].Role)
				}
				component.Dependencies[index].Identity = identity
			}
			next, digestErr := composition.ComponentIdentity(*component)
			if digestErr != nil {
				return composition.Manifest{}, digestErr
			}
			if identities[role] != next {
				identities[role] = next
				component.Identity = next
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	sort.Slice(target.Components, func(left, right int) bool { return target.Components[left].Role < target.Components[right].Role })
	target.Identity, err = lifecycleCompositionIdentity(target.Components)
	if err != nil {
		return composition.Manifest{}, err
	}
	if _, err := composition.VerifySealed(target); err != nil {
		return composition.Manifest{}, fmt.Errorf("候选组合无效: %w", err)
	}
	return target, nil
}

func lifecycleCompositionIdentity(components []composition.Component) (string, error) {
	type componentIdentity struct {
		Role         string                   `json:"role"`
		Identity     string                   `json:"identity"`
		Dependencies []composition.Dependency `json:"dependencies"`
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

func lookupComponent(manifest composition.Manifest, role string) (composition.Component, bool) {
	for _, component := range manifest.Components {
		if component.Role == role {
			return component, true
		}
	}
	return composition.Component{}, false
}

func cloneComposition(source composition.Manifest) (composition.Manifest, error) {
	encoded, err := json.Marshal(source)
	if err != nil {
		return composition.Manifest{}, err
	}
	var result composition.Manifest
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil {
		return composition.Manifest{}, err
	}
	return result, nil
}

func cloneConfig(source map[string]any) map[string]any {
	encoded, _ := json.Marshal(source)
	var result map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	_ = decoder.Decode(&result)
	return result
}

func confinedCandidateFile(root, relative string) (string, string, error) {
	trimmed := strings.TrimSpace(relative)
	if trimmed == "" || filepath.IsAbs(trimmed) || trimmed != relative {
		return "", "", fmt.Errorf("候选内容路径必须是仓库相对路径: %q", relative)
	}
	path, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(trimmed)))
	if err != nil {
		return "", "", err
	}
	relativeToRoot, err := filepath.Rel(root, path)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("候选内容路径越出仓库: %s", relative)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", fmt.Errorf("候选内容缺失: %s", relative)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("候选内容不是普通文件: %s", relative)
	}
	return path, filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative))), nil
}

func candidateFileSHA256(path string) (string, error) {
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
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

package kernelcatalog

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

const CatalogSchema = "ownward.kernel-generation-catalog/v1"

const (
	FormalBaseline      = "formal-baseline"
	UnpromotedCandidate = "unpromoted-candidate"
)

var requiredFacets = []string{
	"organization",
	"representation",
	"retrieval",
	"derived-storage",
	"execution",
	"semantic-dependency",
	"vector-dependency",
	"index",
	"resource-and-degradation",
}

// Facet maps every behavior-bearing part of a generation to content,
// configuration and direct dependencies that participate in its identity.
// It is a coverage proof, not another source of implementation truth.
type Facet struct {
	Name         string   `json:"name"`
	Content      []string `json:"content,omitempty"`
	Config       []string `json:"config,omitempty"`
	Dependencies []string `json:"dependencies,omitempty"`
}

// EvidenceRef is an immutable Acceptance report reference. Report content is
// not copied into the catalog.
type EvidenceRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Mapping records lifecycle facts without making them part of the kernel
// identity. In particular, a control-state active generation never implies
// either a formal baseline or a candidate promotion.
type Mapping struct {
	Lifecycle          string                 `json:"lifecycle"`
	CandidateSource    string                 `json:"candidate_source"`
	BinaryPath         string                 `json:"binary_path"`
	BinarySHA256       string                 `json:"binary_sha256"`
	Evidence           map[string]EvidenceRef `json:"evidence"`
	AcceptanceBaseline bool                   `json:"acceptance_baseline"`
}

// Audit identifies where the sealed bytes were obtained. It is deliberately
// excluded from the generation identity.
type Audit struct {
	SourceGit string `json:"source_git,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Generation is one indivisible information-kernel version. Kernel.Identity
// is the generation identity: it is derived from contracts, content digests,
// configuration and the identities of the four real direct dependencies.
type Generation struct {
	Name         string                  `json:"name"`
	Kernel       composition.Component   `json:"kernel"`
	Dependencies []composition.Component `json:"dependencies"`
	Facets       []Facet                 `json:"facets"`
	Mapping      Mapping                 `json:"mapping"`
	Audit        Audit                   `json:"audit,omitempty"`
}

type Catalog struct {
	Schema      string       `json:"schema"`
	Generations []Generation `json:"generations"`
	Identity    string       `json:"identity"`
}

type Verification struct {
	Schema                  string            `json:"schema"`
	Passed                  bool              `json:"passed"`
	Catalog                 string            `json:"catalog"`
	Generations             map[string]string `json:"generations"`
	FormalBaseline          string            `json:"formal_baseline"`
	UnpromotedCandidates    []string          `json:"unpromoted_candidates"`
	GitIsGenerationIdentity bool              `json:"git_is_generation_identity"`
	ControlIsQualification  bool              `json:"control_is_qualification"`
}

type frozenBaseline struct {
	Candidates map[string]struct {
		Candidate    string                 `json:"candidate"`
		BinaryPath   string                 `json:"binary_path"`
		BinarySHA256 string                 `json:"binary_sha256"`
		Reports      map[string]EvidenceRef `json:"reports"`
	} `json:"candidates"`
	Acceptance struct {
		BoundCandidate          string            `json:"bound_candidate"`
		FormalBaselineCandidate string            `json:"formal_baseline_candidate"`
		Checkpoints             map[string]string `json:"checkpoints"`
	} `json:"acceptance"`
}

func Load(path string) (Catalog, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, fmt.Errorf("读取内核世代目录: %w", err)
	}
	return Parse(encoded)
}

func Parse(encoded []byte) (Catalog, error) {
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("解析内核世代目录: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Catalog{}, errors.New("内核世代目录包含多余 JSON 值")
		}
		return Catalog{}, fmt.Errorf("内核世代目录尾部无效: %w", err)
	}
	return catalog, nil
}

// ContentReader returns one source file for the audit revision of a
// generation. Revision lookup is intentionally outside the identity model.
type ContentReader func(generation Generation, path string) ([]byte, error)

// Seal resolves source bytes into immutable content and direct-dependency
// identities. It performs no product or Acceptance state writes.
func Seal(source Catalog, read ContentReader) (Catalog, error) {
	encoded, err := json.Marshal(source)
	if err != nil {
		return Catalog{}, err
	}
	var catalog Catalog
	if err := json.Unmarshal(encoded, &catalog); err != nil {
		return Catalog{}, err
	}
	if catalog.Schema != CatalogSchema || len(catalog.Generations) == 0 || read == nil {
		return Catalog{}, errors.New("待封存内核世代目录无效")
	}
	for generationIndex := range catalog.Generations {
		generation := &catalog.Generations[generationIndex]
		for dependencyIndex := range generation.Dependencies {
			dependency := &generation.Dependencies[dependencyIndex]
			if err := sealContent(*generation, dependency.Content, read); err != nil {
				return Catalog{}, fmt.Errorf("封存 %s/%s: %w", generation.Name, dependency.Role, err)
			}
			sort.Slice(dependency.Content, func(i, j int) bool { return dependency.Content[i].Name < dependency.Content[j].Name })
			dependency.Identity, err = composition.ComponentIdentity(*dependency)
			if err != nil {
				return Catalog{}, err
			}
		}
		sort.Slice(generation.Dependencies, func(i, j int) bool { return generation.Dependencies[i].Role < generation.Dependencies[j].Role })
		if err := sealContent(*generation, generation.Kernel.Content, read); err != nil {
			return Catalog{}, fmt.Errorf("封存 %s/kernel: %w", generation.Name, err)
		}
		sort.Slice(generation.Kernel.Content, func(i, j int) bool { return generation.Kernel.Content[i].Name < generation.Kernel.Content[j].Name })
		generation.Kernel.Dependencies = make([]composition.Dependency, len(generation.Dependencies))
		for index, dependency := range generation.Dependencies {
			generation.Kernel.Dependencies[index] = composition.Dependency{Role: dependency.Role, Identity: dependency.Identity}
		}
		generation.Kernel.Identity, err = composition.ComponentIdentity(generation.Kernel)
		if err != nil {
			return Catalog{}, err
		}
	}
	sort.Slice(catalog.Generations, func(i, j int) bool { return catalog.Generations[i].Name < catalog.Generations[j].Name })
	catalog.Identity, err = catalogIdentity(catalog.Generations)
	if err != nil {
		return Catalog{}, err
	}
	if _, err := Verify(catalog); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func sealContent(generation Generation, content []composition.Content, read ContentReader) error {
	for index := range content {
		if strings.TrimSpace(content[index].Name) == "" || strings.TrimSpace(content[index].Path) == "" {
			return errors.New("内容名称或路径为空")
		}
		value, err := read(generation, content[index].Path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(value)
		content[index].SHA256 = hex.EncodeToString(digest[:])
	}
	return nil
}

func WriteJSON(file *os.File, value any) error {
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func Verify(catalog Catalog) (Verification, error) {
	if catalog.Schema != CatalogSchema || len(catalog.Generations) == 0 || !validSHA(catalog.Identity) {
		return Verification{}, errors.New("内核世代目录 schema、内容或身份无效")
	}
	names := make(map[string]struct{}, len(catalog.Generations))
	identities := make(map[string]struct{}, len(catalog.Generations))
	result := Verification{
		Schema: "ownward.kernel-generation-check/v1", Passed: true, Catalog: catalog.Identity,
		Generations: make(map[string]string, len(catalog.Generations)), GitIsGenerationIdentity: false,
		ControlIsQualification: false,
	}
	for _, generation := range catalog.Generations {
		if err := verifyGeneration(generation); err != nil {
			return Verification{}, fmt.Errorf("内核世代 %s: %w", generation.Name, err)
		}
		if _, exists := names[generation.Name]; exists {
			return Verification{}, fmt.Errorf("内核世代名称重复: %s", generation.Name)
		}
		if _, exists := identities[generation.Kernel.Identity]; exists {
			return Verification{}, fmt.Errorf("不同内核世代错误共享同一身份: %s", generation.Kernel.Identity)
		}
		names[generation.Name] = struct{}{}
		identities[generation.Kernel.Identity] = struct{}{}
		result.Generations[generation.Name] = generation.Kernel.Identity
		switch generation.Mapping.Lifecycle {
		case FormalBaseline:
			if result.FormalBaseline != "" || !generation.Mapping.AcceptanceBaseline {
				return Verification{}, errors.New("正式基线映射不唯一或未由 Acceptance 明确成立")
			}
			result.FormalBaseline = generation.Name
		case UnpromotedCandidate:
			if generation.Mapping.AcceptanceBaseline {
				return Verification{}, errors.New("未晋升候选不得登记为 Acceptance 基线")
			}
			result.UnpromotedCandidates = append(result.UnpromotedCandidates, generation.Name)
		default:
			return Verification{}, fmt.Errorf("未知候选生命周期: %s", generation.Mapping.Lifecycle)
		}
	}
	if result.FormalBaseline == "" {
		return Verification{}, errors.New("内核世代目录缺少唯一正式基线")
	}
	identity, err := catalogIdentity(catalog.Generations)
	if err != nil || identity != catalog.Identity {
		return Verification{}, errors.New("内核世代目录身份漂移")
	}
	sort.Strings(result.UnpromotedCandidates)
	return result, nil
}

// VerifyAuthoritativeContracts compares an already verified, immutable
// generation catalog with the current authoritative versioned contract
// definitions. It deliberately performs no normalization or mutation: Seal
// is the only path allowed to fill contract references for a new catalog.
func VerifyAuthoritativeContracts(catalog Catalog, authoritative map[string][]contract.Reference) error {
	for _, generation := range catalog.Generations {
		if err := verifyComponentContracts(generation.Name, generation.Kernel, authoritative); err != nil {
			return err
		}
		for _, dependency := range generation.Dependencies {
			if err := verifyComponentContracts(generation.Name, dependency, authoritative); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyComponentContracts(generation string, component composition.Component, authoritative map[string][]contract.Reference) error {
	expected, exists := authoritative[component.Role]
	if !exists {
		return fmt.Errorf("内核世代 %s 的角色缺少权威契约定义: %s", generation, component.Role)
	}
	if len(component.Contracts) != len(expected) {
		return fmt.Errorf("内核世代 %s 的契约引用与权威定义不一致: %s", generation, component.Role)
	}
	for index := range expected {
		if component.Contracts[index] != expected[index] {
			return fmt.Errorf("内核世代 %s 的契约引用与权威定义不一致: %s", generation, component.Role)
		}
	}
	return nil
}

// VerifyFrozenBaseline proves that lifecycle labels and evidence references
// are exact views of the unique pre-migration Acceptance facts. It is
// deliberately read-only and never treats control-state activation as
// candidate qualification.
func VerifyFrozenBaseline(repository, baselinePath string, catalog Catalog) error {
	if _, err := Verify(catalog); err != nil {
		return err
	}
	root, err := filepath.Abs(repository)
	if err != nil {
		return err
	}
	baselineFile, err := confined(root, baselinePath)
	if err != nil {
		return err
	}
	encoded, err := os.ReadFile(baselineFile)
	if err != nil {
		return fmt.Errorf("读取冻结基线: %w", err)
	}
	var baseline frozenBaseline
	if err := json.Unmarshal(encoded, &baseline); err != nil {
		return fmt.Errorf("解析冻结基线: %w", err)
	}
	if len(baseline.Candidates) != len(catalog.Generations) {
		return errors.New("冻结基线中的有效候选集合与内核世代目录不一致")
	}
	for _, generation := range catalog.Generations {
		candidate, exists := baseline.Candidates[generation.Name]
		if !exists {
			return fmt.Errorf("冻结基线缺少世代映射: %s", generation.Name)
		}
		mapping := generation.Mapping
		if candidate.Candidate != mapping.CandidateSource || candidate.BinaryPath != mapping.BinaryPath || candidate.BinarySHA256 != mapping.BinarySHA256 {
			return fmt.Errorf("冻结基线候选或二进制错绑: %s", generation.Name)
		}
		if !sameEvidence(candidate.Reports, mapping.Evidence) {
			return fmt.Errorf("冻结基线证据映射漂移: %s", generation.Name)
		}
		if mapping.Lifecycle == FormalBaseline && baseline.Acceptance.FormalBaselineCandidate != mapping.CandidateSource {
			return fmt.Errorf("冻结基线正式基线身份不一致: %s", generation.Name)
		}
		if mapping.Lifecycle == UnpromotedCandidate {
			if baseline.Acceptance.BoundCandidate != mapping.CandidateSource {
				return fmt.Errorf("冻结基线当前候选身份不一致: %s", generation.Name)
			}
			for scope, digest := range baseline.Acceptance.Checkpoints {
				evidence, exists := mapping.Evidence[scope]
				if !exists || evidence.SHA256 != digest {
					return fmt.Errorf("冻结基线内部检查点错绑: %s/%s", generation.Name, scope)
				}
			}
		}
		if err := verifyFile(root, mapping.BinaryPath, mapping.BinarySHA256); err != nil {
			return fmt.Errorf("核验 %s 二进制: %w", generation.Name, err)
		}
		for scope, evidence := range mapping.Evidence {
			if err := verifyFile(root, evidence.Path, evidence.SHA256); err != nil {
				return fmt.Errorf("核验 %s/%s 证据: %w", generation.Name, scope, err)
			}
		}
	}
	return nil
}

func sameEvidence(left, right map[string]EvidenceRef) bool {
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		if right[name] != value {
			return false
		}
	}
	return true
}

func verifyFile(root, relative, expected string) error {
	path, err := confined(root, relative)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return errors.New("文件摘要漂移")
	}
	return nil
}

func confined(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || strings.TrimSpace(relative) == "" {
		return "", errors.New("路径必须是仓库内相对路径")
	}
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("路径越出仓库")
	}
	return path, nil
}

func verifyGeneration(generation Generation) error {
	if strings.TrimSpace(generation.Name) == "" || generation.Kernel.Role != "kernel" {
		return errors.New("名称或内核角色无效")
	}
	identity, err := composition.ComponentIdentity(generation.Kernel)
	if err != nil || identity != generation.Kernel.Identity || !validSHA(identity) {
		return errors.New("内核内容身份漂移")
	}
	dependencyByRole := make(map[string]composition.Component, len(generation.Dependencies))
	for _, dependency := range generation.Dependencies {
		if dependency.Role != "authority-substrate" && dependency.Role != "product-rules" && dependency.Role != "semantic" && dependency.Role != "vector" {
			return fmt.Errorf("未知内核直接依赖: %s", dependency.Role)
		}
		if _, exists := dependencyByRole[dependency.Role]; exists {
			return fmt.Errorf("内核直接依赖重复: %s", dependency.Role)
		}
		actual, computeErr := composition.ComponentIdentity(dependency)
		if computeErr != nil || actual != dependency.Identity || !validSHA(actual) {
			return fmt.Errorf("直接依赖内容身份漂移: %s", dependency.Role)
		}
		dependencyByRole[dependency.Role] = dependency
	}
	if len(dependencyByRole) != 4 || len(generation.Kernel.Dependencies) != 4 {
		return errors.New("内核必须绑定权威、产品规则、语义和向量四项直接依赖")
	}
	for _, reference := range generation.Kernel.Dependencies {
		dependency, exists := dependencyByRole[reference.Role]
		if !exists || dependency.Identity != reference.Identity {
			return fmt.Errorf("内核直接依赖错绑: %s", reference.Role)
		}
	}
	if err := verifyFacetCoverage(generation); err != nil {
		return err
	}
	if generation.Mapping.CandidateSource == "" || !validSHA(generation.Mapping.BinarySHA256) || generation.Mapping.BinaryPath == "" {
		return errors.New("候选来源或二进制映射无效")
	}
	if len(generation.Mapping.Evidence) == 0 {
		return errors.New("候选映射缺少 Acceptance 证据")
	}
	for name, evidence := range generation.Mapping.Evidence {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(evidence.Path) == "" || !validSHA(evidence.SHA256) {
			return fmt.Errorf("Acceptance 证据引用无效: %s", name)
		}
	}
	return nil
}

func verifyFacetCoverage(generation Generation) error {
	content := make(map[string]bool, len(generation.Kernel.Content))
	config := make(map[string]bool, len(generation.Kernel.Config))
	dependencies := make(map[string]bool, len(generation.Kernel.Dependencies))
	for _, item := range generation.Kernel.Content {
		content[item.Name] = false
	}
	for key := range generation.Kernel.Config {
		config[key] = false
	}
	for _, dependency := range generation.Kernel.Dependencies {
		dependencies[dependency.Role] = false
	}
	allowedFacets := make(map[string]struct{}, len(requiredFacets))
	for _, name := range requiredFacets {
		allowedFacets[name] = struct{}{}
	}
	if len(generation.Facets) != len(requiredFacets) {
		return errors.New("内核职责分面集合不完整或包含额外分面")
	}
	facets := make(map[string]struct{}, len(generation.Facets))
	for _, facet := range generation.Facets {
		if _, exists := allowedFacets[facet.Name]; !exists {
			return fmt.Errorf("未知内核职责分面: %s", facet.Name)
		}
		if _, exists := facets[facet.Name]; exists {
			return fmt.Errorf("职责分面重复: %s", facet.Name)
		}
		facets[facet.Name] = struct{}{}
		for _, name := range facet.Content {
			if _, exists := content[name]; !exists {
				return fmt.Errorf("职责分面引用未知内核内容: %s/%s", facet.Name, name)
			}
			content[name] = true
		}
		for _, key := range facet.Config {
			if _, exists := config[key]; !exists {
				return fmt.Errorf("职责分面引用未知配置: %s/%s", facet.Name, key)
			}
			config[key] = true
		}
		for _, role := range facet.Dependencies {
			if _, exists := dependencies[role]; !exists {
				return fmt.Errorf("职责分面引用未知直接依赖: %s/%s", facet.Name, role)
			}
			dependencies[role] = true
		}
	}
	for _, required := range requiredFacets {
		if _, exists := facets[required]; !exists {
			return fmt.Errorf("缺少内核职责分面: %s", required)
		}
	}
	for name, covered := range content {
		if !covered {
			return fmt.Errorf("内核内容未归入任何职责分面: %s", name)
		}
	}
	for key, covered := range config {
		if !covered {
			return fmt.Errorf("内核配置未归入任何职责分面: %s", key)
		}
	}
	for role, covered := range dependencies {
		if !covered {
			return fmt.Errorf("内核直接依赖未归入任何职责分面: %s", role)
		}
	}
	return nil
}

func catalogIdentity(generations []Generation) (string, error) {
	values := append([]Generation(nil), generations...)
	sort.Slice(values, func(i, j int) bool { return values[i].Name < values[j].Name })
	for index := range values {
		values[index].Audit = Audit{}
	}
	payload := struct {
		Schema      string       `json:"schema"`
		Generations []Generation `json:"generations"`
	}{"ownward.kernel-generation-catalog-identity/v1", values}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

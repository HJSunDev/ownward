package embedding

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
)

const ManifestSchema = "ownward.embedding-bundle/v2"

type Manifest struct {
	Schema     string          `json:"schema"`
	Capability string          `json:"capability"`
	Model      ModelArtifact   `json:"model"`
	Runtime    RuntimeArtifact `json:"runtime"`
	Legal      LegalArtifacts  `json:"legal"`
	Space      SpaceDefinition `json:"space"`
}

type ModelArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type RuntimeArtifact struct {
	Entry               string            `json:"entry"`
	SourceArchiveSHA256 string            `json:"source_archive_sha256"`
	Files               map[string]string `json:"files"`
}

type LegalArtifacts struct {
	AcceptanceID string            `json:"acceptance_id"`
	Files        map[string]string `json:"files"`
}

type SpaceDefinition struct {
	ID               string `json:"id"`
	Dimensions       int    `json:"dimensions"`
	SourceDimensions int    `json:"source_dimensions"`
	QueryPrefix      string `json:"query_prefix"`
	DocumentPrefix   string `json:"document_prefix"`
	Pooling          string `json:"pooling"`
	Normalization    string `json:"normalization"`
	Truncation       string `json:"truncation"`
}

type Bundle struct {
	Root         string
	ManifestPath string
	Manifest     Manifest
	ModelPath    string
	RuntimePath  string
	LegalPaths   map[string]string
	verified     bool
}

func LoadBundle(root string) (Bundle, error) {
	bundle, err := InspectBundle(root)
	if err != nil {
		return Bundle{}, err
	}
	if err := VerifyBundle(bundle); err != nil {
		return Bundle{}, err
	}
	bundle.verified = true
	return bundle, nil
}

// InspectBundle 校验清单并解析受根目录约束的制品路径，但不读取完整模型内容。
// 正式运行在返回第一个向量前仍必须完成 VerifyBundle。
func InspectBundle(root string) (Bundle, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Bundle{}, err
	}
	manifestPath := filepath.Join(absolute, "manifest.json")
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		return Bundle{}, fmt.Errorf("读取向量能力清单: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return Bundle{}, fmt.Errorf("解析向量能力清单: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return Bundle{}, err
	}
	modelPath, err := confinedPath(absolute, manifest.Model.Path)
	if err != nil {
		return Bundle{}, fmt.Errorf("模型路径: %w", err)
	}
	runtimePath, err := confinedPath(absolute, manifest.Runtime.Entry)
	if err != nil {
		return Bundle{}, fmt.Errorf("运行时入口: %w", err)
	}
	if err := requireRegularFile(modelPath); err != nil {
		return Bundle{}, fmt.Errorf("向量模型: %w", err)
	}
	files := make([]string, 0, len(manifest.Runtime.Files))
	for path := range manifest.Runtime.Files {
		files = append(files, path)
	}
	sort.Strings(files)
	for _, path := range files {
		absolutePath, pathErr := confinedPath(absolute, path)
		if pathErr != nil {
			return Bundle{}, fmt.Errorf("运行时文件 %q: %w", path, pathErr)
		}
		if err := requireRegularFile(absolutePath); err != nil {
			return Bundle{}, fmt.Errorf("运行时文件 %q: %w", path, err)
		}
	}
	legalPaths := make(map[string]string, len(manifest.Legal.Files))
	legalFiles := make([]string, 0, len(manifest.Legal.Files))
	for path := range manifest.Legal.Files {
		legalFiles = append(legalFiles, path)
	}
	sort.Strings(legalFiles)
	for _, path := range legalFiles {
		absolutePath, pathErr := confinedPath(absolute, path)
		if pathErr != nil {
			return Bundle{}, fmt.Errorf("许可文件 %q: %w", path, pathErr)
		}
		if err := requireRegularFile(absolutePath); err != nil {
			return Bundle{}, fmt.Errorf("许可文件 %q: %w", path, err)
		}
		legalPaths[path] = absolutePath
	}
	if _, exists := manifest.Runtime.Files[filepath.ToSlash(manifest.Runtime.Entry)]; !exists {
		return Bundle{}, errors.New("运行时入口没有进入完整性清单")
	}
	return Bundle{Root: absolute, ManifestPath: manifestPath, Manifest: manifest, ModelPath: modelPath, RuntimePath: runtimePath, LegalPaths: legalPaths}, nil
}

// VerifyBundle 将所有制品与清单中的摘要逐一绑定。
func VerifyBundle(bundle Bundle) error {
	if err := verifyFile(bundle.ModelPath, bundle.Manifest.Model.SHA256); err != nil {
		return fmt.Errorf("校验向量模型: %w", err)
	}
	runtimeFiles := make([]string, 0, len(bundle.Manifest.Runtime.Files))
	for path := range bundle.Manifest.Runtime.Files {
		runtimeFiles = append(runtimeFiles, path)
	}
	sort.Strings(runtimeFiles)
	for _, path := range runtimeFiles {
		absolutePath, err := confinedPath(bundle.Root, path)
		if err != nil {
			return fmt.Errorf("运行时文件 %q: %w", path, err)
		}
		if err := verifyFile(absolutePath, bundle.Manifest.Runtime.Files[path]); err != nil {
			return fmt.Errorf("校验运行时文件 %q: %w", path, err)
		}
	}
	legalFiles := make([]string, 0, len(bundle.Manifest.Legal.Files))
	for path := range bundle.Manifest.Legal.Files {
		legalFiles = append(legalFiles, path)
	}
	sort.Strings(legalFiles)
	for _, path := range legalFiles {
		absolutePath, err := confinedPath(bundle.Root, path)
		if err != nil {
			return fmt.Errorf("许可文件 %q: %w", path, err)
		}
		if err := verifyFile(absolutePath, bundle.Manifest.Legal.Files[path]); err != nil {
			return fmt.Errorf("校验许可文件 %q: %w", path, err)
		}
	}
	return nil
}

func requireRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("路径不是普通文件")
	}
	return nil
}

func ComputeAcceptanceID(manifest Manifest) (string, error) {
	binding := struct {
		ModelSHA256 string            `json:"model_sha256"`
		Files       map[string]string `json:"files"`
	}{manifest.Model.SHA256, manifest.Legal.Files}
	encoded, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "legal_" + hex.EncodeToString(digest[:16]), nil
}

func ComputeSpaceID(manifest Manifest) (string, error) {
	binding := struct {
		Capability string          `json:"capability"`
		Model      ModelArtifact   `json:"model"`
		Runtime    RuntimeArtifact `json:"runtime"`
		Space      SpaceDefinition `json:"space"`
	}{manifest.Capability, manifest.Model, manifest.Runtime, manifest.Space}
	binding.Space.ID = ""
	encoded, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "emb_" + hex.EncodeToString(digest[:16]), nil
}

func validateManifest(value Manifest) error {
	if value.Schema != ManifestSchema || strings.TrimSpace(value.Capability) == "" {
		return errors.New("向量能力清单元数据无效")
	}
	if strings.TrimSpace(value.Model.Path) == "" || !validDigest(value.Model.SHA256) || strings.TrimSpace(value.Runtime.Entry) == "" ||
		!validDigest(value.Runtime.SourceArchiveSHA256) || len(value.Runtime.Files) == 0 {
		return errors.New("向量能力制品清单无效")
	}
	for path, digest := range value.Runtime.Files {
		if strings.TrimSpace(path) == "" || !validDigest(digest) {
			return errors.New("向量运行时文件清单无效")
		}
	}
	requiredLegal := []string{
		"legal/embeddinggemma/GEMMA_TERMS_OF_USE.md",
		"legal/embeddinggemma/GEMMA_PROHIBITED_USE_POLICY.md",
		"legal/embeddinggemma/USE_RESTRICTIONS.md",
		"legal/embeddinggemma/MODIFICATIONS.md",
		"legal/embeddinggemma/NOTICE",
		"legal/llama.cpp/LICENSE",
	}
	for _, path := range requiredLegal {
		if !validDigest(value.Legal.Files[path]) {
			return fmt.Errorf("向量能力包缺少许可文件 %q", path)
		}
	}
	if len(value.Legal.Files) != len(requiredLegal) {
		return errors.New("向量能力包许可文件清单包含未定义内容")
	}
	expectedAcceptance, err := ComputeAcceptanceID(value)
	if err != nil || value.Legal.AcceptanceID != expectedAcceptance {
		return errors.New("向量能力许可确认身份与制品不一致")
	}
	if value.Space.Dimensions != 512 || value.Space.SourceDimensions < value.Space.Dimensions || value.Space.QueryPrefix == "" || value.Space.DocumentPrefix == "" ||
		value.Space.Pooling != "mean" || value.Space.Normalization != "l2" || value.Space.Truncation != "prefix" {
		return errors.New("向量空间定义与第一版方案不一致")
	}
	expected, err := ComputeSpaceID(value)
	if err != nil || value.Space.ID != expected {
		return errors.New("向量空间身份与完整配置不一致")
	}
	return nil
}

func confinedPath(root, value string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(value))
	if filepath.IsAbs(cleaned) {
		return "", errors.New("路径必须相对于能力包")
	}
	absolute := filepath.Join(root, cleaned)
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("路径超出能力包")
	}
	return absolute, nil
}

func verifyFile(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("SHA-256 不一致: got %s want %s", actual, strings.ToLower(expected))
	}
	return nil
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == sha256.Size
}

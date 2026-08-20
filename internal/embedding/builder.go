package embedding

import (
	"archive/zip"
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

const (
	SelectedModelSHA256          = "6fa0c02a9c302be6f977521d399b4de3a46310a4f2621ee0063747881b673f67"
	SelectedRuntimeArchiveSHA256 = "6c938f6d79aac96cb90fda673aade20cff9b1b6c1e97de04f4d5d60bca107082"
	selectedModelName            = "embeddinggemma-300m-qat-Q8_0.gguf"
	selectedRuntimeEntry         = "runtime/llama-server.exe"
	selectedCapability           = "embeddinggemma-300m-qat-q8_0-llamacpp-b10488"
	selectedQueryPrefix          = "task: search result | query: "
	selectedDocumentPrefix       = "title: none | text: "
)

var selectedRuntimeFiles = map[string]struct{}{
	"ggml-base.dll":               {},
	"ggml-cpu-alderlake.dll":      {},
	"ggml-cpu-cannonlake.dll":     {},
	"ggml-cpu-cascadelake.dll":    {},
	"ggml-cpu-cooperlake.dll":     {},
	"ggml-cpu-haswell.dll":        {},
	"ggml-cpu-icelake.dll":        {},
	"ggml-cpu-ivybridge.dll":      {},
	"ggml-cpu-piledriver.dll":     {},
	"ggml-cpu-sandybridge.dll":    {},
	"ggml-cpu-sapphirerapids.dll": {},
	"ggml-cpu-skylakex.dll":       {},
	"ggml-cpu-sse42.dll":          {},
	"ggml-cpu-x64.dll":            {},
	"ggml-cpu-zen4.dll":           {},
	"ggml-rpc.dll":                {},
	"ggml.dll":                    {},
	"libomp140.x86_64.dll":        {},
	"llama-common.dll":            {},
	"llama-server-impl.dll":       {},
	"llama-server.exe":            {},
	"llama.dll":                   {},
	"mtmd.dll":                    {},
}

type BuildOptions struct {
	ModelPath      string
	RuntimeArchive string
	LegalRoot      string
	Output         string
}

// BuildSelectedBundle 使用锁定的上游制品组装第一版向量能力。
// 输出原子写入，且只有通过运行时加载器的完整校验才会被接受。
func BuildSelectedBundle(options BuildOptions) (Bundle, error) {
	modelPath, err := regularFile(options.ModelPath)
	if err != nil {
		return Bundle{}, fmt.Errorf("模型制品: %w", err)
	}
	runtimeArchive, err := regularFile(options.RuntimeArchive)
	if err != nil {
		return Bundle{}, fmt.Errorf("运行时制品: %w", err)
	}
	legalRoot, err := existingDirectory(options.LegalRoot)
	if err != nil {
		return Bundle{}, fmt.Errorf("许可材料: %w", err)
	}
	output, err := filepath.Abs(strings.TrimSpace(options.Output))
	if err != nil || strings.TrimSpace(options.Output) == "" {
		return Bundle{}, errors.New("向量能力包输出目录无效")
	}
	if _, err := os.Stat(output); err == nil {
		return Bundle{}, errors.New("向量能力包输出目录已经存在")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Bundle{}, err
	}
	if err := verifyFile(modelPath, SelectedModelSHA256); err != nil {
		return Bundle{}, fmt.Errorf("模型制品不是锁定版本: %w", err)
	}
	if err := verifyFile(runtimeArchive, SelectedRuntimeArchiveSHA256); err != nil {
		return Bundle{}, fmt.Errorf("运行时制品不是锁定版本: %w", err)
	}
	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Bundle{}, err
	}
	temporary, err := os.MkdirTemp(parent, ".embedding-building-*")
	if err != nil {
		return Bundle{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	modelTarget := filepath.Join(temporary, "model", selectedModelName)
	if err := copyFile(modelPath, modelTarget); err != nil {
		return Bundle{}, err
	}
	runtimeFiles, err := extractRuntime(runtimeArchive, filepath.Join(temporary, "runtime"))
	if err != nil {
		return Bundle{}, err
	}
	if _, exists := runtimeFiles[selectedRuntimeEntry]; !exists {
		return Bundle{}, errors.New("锁定运行时不包含 llama-server.exe")
	}
	legalFiles, err := copyLegalFiles(legalRoot, temporary)
	if err != nil {
		return Bundle{}, err
	}
	manifest := Manifest{
		Schema:     ManifestSchema,
		Capability: selectedCapability,
		Model:      ModelArtifact{Path: filepath.ToSlash(filepath.Join("model", selectedModelName)), SHA256: SelectedModelSHA256},
		Runtime: RuntimeArtifact{
			Entry: selectedRuntimeEntry, SourceArchiveSHA256: SelectedRuntimeArchiveSHA256, Files: runtimeFiles,
		},
		Legal: LegalArtifacts{Files: legalFiles},
		Space: SpaceDefinition{
			Dimensions: 512, SourceDimensions: 768,
			QueryPrefix: selectedQueryPrefix, DocumentPrefix: selectedDocumentPrefix,
			Pooling: "mean", Normalization: "l2", Truncation: "prefix",
		},
	}
	manifest.Legal.AcceptanceID, err = ComputeAcceptanceID(manifest)
	if err != nil {
		return Bundle{}, err
	}
	manifest.Space.ID, err = ComputeSpaceID(manifest)
	if err != nil {
		return Bundle{}, err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Bundle{}, err
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(temporary, "manifest.json"), encoded, 0o644); err != nil {
		return Bundle{}, err
	}
	if _, err := LoadBundle(temporary); err != nil {
		return Bundle{}, fmt.Errorf("复核向量能力包: %w", err)
	}
	if err := os.Rename(temporary, output); err != nil {
		return Bundle{}, fmt.Errorf("提交向量能力包: %w", err)
	}
	committed = true
	return LoadBundle(output)
}

func copyLegalFiles(root, target string) (map[string]string, error) {
	sources := map[string]string{
		"legal/embeddinggemma/GEMMA_TERMS_OF_USE.md":          "embeddinggemma/GEMMA_TERMS_OF_USE.md",
		"legal/embeddinggemma/GEMMA_PROHIBITED_USE_POLICY.md": "embeddinggemma/GEMMA_PROHIBITED_USE_POLICY.md",
		"legal/embeddinggemma/USE_RESTRICTIONS.md":            "embeddinggemma/USE_RESTRICTIONS.md",
		"legal/embeddinggemma/MODIFICATIONS.md":               "embeddinggemma/MODIFICATIONS.md",
		"legal/embeddinggemma/NOTICE":                         "embeddinggemma/NOTICE",
		"legal/llama.cpp/LICENSE":                             "llama.cpp/LICENSE",
	}
	files := make(map[string]string, len(sources))
	for destination, source := range sources {
		sourcePath, err := confinedPath(root, source)
		if err != nil {
			return nil, fmt.Errorf("许可材料 %q: %w", source, err)
		}
		if _, err := regularFile(sourcePath); err != nil {
			return nil, fmt.Errorf("许可材料 %q: %w", source, err)
		}
		targetPath := filepath.Join(target, filepath.FromSlash(destination))
		if err := copyFile(sourcePath, targetPath); err != nil {
			return nil, err
		}
		digest, err := fileDigest(targetPath)
		if err != nil {
			return nil, err
		}
		files[destination] = digest
	}
	return files, nil
}

func extractRuntime(archivePath, target string) (map[string]string, error) {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("打开运行时压缩包: %w", err)
	}
	defer archive.Close()
	files := make(map[string]string)
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		cleaned := filepath.Clean(filepath.FromSlash(entry.Name))
		if filepath.IsAbs(cleaned) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("运行时压缩包包含越界路径 %q", entry.Name)
		}
		if _, required := selectedRuntimeFiles[filepath.ToSlash(cleaned)]; !required {
			continue
		}
		relative := filepath.ToSlash(filepath.Join("runtime", cleaned))
		if _, exists := files[relative]; exists {
			return nil, fmt.Errorf("运行时压缩包包含重复路径 %q", entry.Name)
		}
		destination := filepath.Join(target, cleaned)
		if err := extractFile(entry, destination); err != nil {
			return nil, err
		}
		digest, err := fileDigest(destination)
		if err != nil {
			return nil, err
		}
		files[relative] = digest
	}
	if len(files) != len(selectedRuntimeFiles) {
		missing := make([]string, 0)
		for name := range selectedRuntimeFiles {
			if _, exists := files[filepath.ToSlash(filepath.Join("runtime", name))]; !exists {
				missing = append(missing, name)
			}
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("锁定运行时缺少服务依赖: %s", strings.Join(missing, ", "))
	}
	return files, nil
}

func extractFile(entry *zip.File, target string) error {
	if entry.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("运行时压缩包包含符号链接 %q", entry.Name)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := destination.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyFile(sourcePath, targetPath string) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func regularFile(value string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil || strings.TrimSpace(value) == "" {
		return "", errors.New("路径无效")
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("路径不是普通文件")
	}
	return absolute, nil
}

func existingDirectory(value string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil || strings.TrimSpace(value) == "" {
		return "", errors.New("路径无效")
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("路径不是目录")
	}
	return absolute, nil
}

func fileDigest(path string) (string, error) {
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

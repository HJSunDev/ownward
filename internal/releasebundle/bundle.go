package releasebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HJSunDev/ownward/internal/embedding"
)

const ManifestSchema = "ownward.release-bundle/v2"

type Options struct {
	Binary       string
	EmbeddingDir string
	License      string
	Readme       string
	Output       string
}

type Manifest struct {
	Schema                  string            `json:"schema"`
	Candidate               string            `json:"candidate"`
	Files                   map[string]string `json:"files"`
	EmbeddingSpace          string            `json:"embedding_space"`
	EmbeddingLegalMaterials string            `json:"embedding_legal_materials"`
}

// Assemble 原子生成完整的 Windows 第一版发布包。
// 调用方必须提供已经校验的向量能力，组包过程不得下载或静默替换制品。
func Assemble(options Options) (Manifest, error) {
	binary, err := regularFile(options.Binary)
	if err != nil {
		return Manifest{}, fmt.Errorf("发布二进制: %w", err)
	}
	embeddingRoot, err := existingDirectory(options.EmbeddingDir)
	if err != nil {
		return Manifest{}, fmt.Errorf("向量能力包: %w", err)
	}
	license, err := regularFile(options.License)
	if err != nil {
		return Manifest{}, fmt.Errorf("Ownward 许可证: %w", err)
	}
	readme, err := regularFile(options.Readme)
	if err != nil {
		return Manifest{}, fmt.Errorf("使用说明: %w", err)
	}
	output, err := cleanNewDirectory(options.Output)
	if err != nil {
		return Manifest{}, err
	}
	candidate, err := binaryVersion(binary)
	if err != nil {
		return Manifest{}, err
	}

	embeddingBundle, err := embedding.LoadDistributionBundle(embeddingRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("校验向量能力包: %w", err)
	}

	parent := filepath.Dir(output)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Manifest{}, err
	}
	temporary, err := os.MkdirTemp(parent, ".release-building-*")
	if err != nil {
		return Manifest{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()

	if err := copyFile(binary, filepath.Join(temporary, "bin", "ownward.exe"), 0o755); err != nil {
		return Manifest{}, err
	}
	if err := copyTree(embeddingRoot, filepath.Join(temporary, "bin", "embedding")); err != nil {
		return Manifest{}, err
	}
	if err := copyFile(license, filepath.Join(temporary, "LICENSE"), 0o644); err != nil {
		return Manifest{}, err
	}
	if err := copyFile(readme, filepath.Join(temporary, "README.md"), 0o644); err != nil {
		return Manifest{}, err
	}
	files, err := fileManifest(temporary)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		Schema: ManifestSchema, Candidate: candidate, Files: files,
		EmbeddingSpace: embeddingBundle.Manifest.Space.ID, EmbeddingLegalMaterials: embeddingBundle.Manifest.Legal.LegalMaterialsID,
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(temporary, "manifest.json"), append(manifestBytes, '\n'), 0o644); err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(temporary, output); err != nil {
		return Manifest{}, fmt.Errorf("提交发布包: %w", err)
	}
	committed = true
	return manifest, nil
}

func binaryVersion(path string) (string, error) {
	output, err := exec.Command(path, "version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("读取发布二进制版本: %w: %s", err, strings.TrimSpace(string(output)))
	}
	version := strings.TrimSpace(string(output))
	if version == "" {
		return "", errors.New("发布二进制版本为空")
	}
	return version, nil
}

func fileManifest(root string) (map[string]string, error) {
	paths := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("发布包不得包含符号链接: %s", path)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	files := make(map[string]string, len(paths))
	for _, relative := range paths {
		digest, err := fileDigest(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return nil, err
		}
		files[relative] = digest
	}
	return files, nil
}

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("向量能力包不得包含符号链接: %s", path)
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("向量能力包包含非普通文件: %s", path)
		}
		return copyFile(path, destination, 0o644)
	})
}

func copyFile(sourcePath, targetPath string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
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

func cleanNewDirectory(value string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil || strings.TrimSpace(value) == "" {
		return "", errors.New("发布包输出目录无效")
	}
	if _, err := os.Stat(absolute); err == nil {
		return "", errors.New("发布包输出目录已经存在")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return absolute, nil
}

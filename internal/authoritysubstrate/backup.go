package authoritysubstrate

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
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/contract"
)

const (
	authorityBackupFormat = "ownward.authority-backup/v1"
	backupMetadataLimit   = 1024 * 1024
	backupSnapshotRetries = 2
)

type authorityBackupManifest struct {
	Format    string            `json:"format"`
	CreatedAt time.Time         `json:"created_at"`
	Files     map[string]string `json:"sha256"`
}

// These variables are narrow failure-injection seams. Production always uses
// the streaming asset writer and a single directory rename.
var (
	writeAssetSnapshot = func(store *assetlog.Store, destination io.Writer) error {
		return store.WriteBackup(destination)
	}
	installDirectory = os.Rename
)

// Backup publishes only a snapshot whose asset log and control revision are
// proven to have coexisted. Large asset content is streamed through bounded
// buffers; only the small control envelope and metadata are held in memory.
func (s *Substrate) Backup(destination string) error {
	if s == nil || s.assets == nil || s.control == nil {
		return errors.New("权威运行基座尚未打开")
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("备份目标已经存在")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查备份目标: %w", err)
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	for attempt := 0; attempt < backupSnapshotRetries; attempt++ {
		before := s.control.ReadControl()
		assetsFile, err := os.CreateTemp(parent, ".assets-snapshot-*.zip")
		if err != nil {
			return err
		}
		assetsPath := assetsFile.Name()
		if err := writeAssetSnapshot(s.assets, assetsFile); err != nil {
			_ = assetsFile.Close()
			_ = os.Remove(assetsPath)
			return err
		}
		if err := assetsFile.Sync(); err != nil {
			_ = assetsFile.Close()
			_ = os.Remove(assetsPath)
			return err
		}
		if err := assetsFile.Close(); err != nil {
			_ = os.Remove(assetsPath)
			return err
		}
		control, err := encodeControl(before)
		if err != nil {
			_ = os.Remove(assetsPath)
			return err
		}
		archiveFile, err := os.CreateTemp(parent, ".authority-backup-*.tmp")
		if err != nil {
			_ = os.Remove(assetsPath)
			return err
		}
		archivePath := archiveFile.Name()
		err = writeAuthorityArchive(archiveFile, assetsPath, control)
		if err == nil {
			err = archiveFile.Sync()
		}
		closeErr := archiveFile.Close()
		_ = os.Remove(assetsPath)
		if err != nil {
			_ = os.Remove(archivePath)
			return err
		}
		if closeErr != nil {
			_ = os.Remove(archivePath)
			return closeErr
		}
		if !reflect.DeepEqual(before, s.control.ReadControl()) {
			_ = os.Remove(archivePath)
			continue
		}
		if err := os.Rename(archivePath, destination); err != nil {
			_ = os.Remove(archivePath)
			return fmt.Errorf("提交权威备份: %w", err)
		}
		return nil
	}
	return errors.New("备份期间权威控制修订持续变化，未生成不一致备份")
}

func writeAuthorityArchive(destination io.Writer, assetsPath string, control []byte) error {
	archive := zip.NewWriter(destination)
	assets, err := os.Open(assetsPath)
	if err != nil {
		_ = archive.Close()
		return err
	}
	assetWriter, err := archive.CreateHeader(&zip.FileHeader{Name: "assets.zip", Method: zip.Store})
	if err != nil {
		_ = assets.Close()
		_ = archive.Close()
		return err
	}
	assetDigest := sha256.New()
	_, copyErr := copyBounded(assetWriter, io.TeeReader(assets, assetDigest))
	closeErr := assets.Close()
	if copyErr != nil || closeErr != nil {
		_ = archive.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	controlWriter, err := archive.CreateHeader(&zip.FileHeader{Name: controlFile, Method: zip.Deflate})
	if err != nil {
		_ = archive.Close()
		return err
	}
	if _, err := controlWriter.Write(control); err != nil {
		_ = archive.Close()
		return err
	}
	controlDigest := sha256.Sum256(control)
	metadata, err := json.MarshalIndent(authorityBackupManifest{
		Format:    authorityBackupFormat,
		CreatedAt: time.Now().UTC(),
		Files: map[string]string{
			"assets.zip": hex.EncodeToString(assetDigest.Sum(nil)),
			controlFile:  hex.EncodeToString(controlDigest[:]),
		},
	}, "", "  ")
	if err != nil {
		_ = archive.Close()
		return err
	}
	metadataWriter, err := archive.CreateHeader(&zip.FileHeader{Name: "backup.json", Method: zip.Deflate})
	if err != nil {
		_ = archive.Close()
		return err
	}
	if _, err := metadataWriter.Write(append(metadata, '\n')); err != nil {
		_ = archive.Close()
		return err
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("完成权威备份归档: %w", err)
	}
	return nil
}

// Restore validates the complete authority root in a sibling staging
// directory and installs it with one directory switch. A non-empty target is
// accepted only when it is byte-identical to the restored authority root.
func Restore(archivePath, dataDir string, initialState contract.ControlState) error {
	if !filepath.IsAbs(archivePath) || !filepath.IsAbs(dataDir) {
		return errors.New("恢复路径必须是绝对路径")
	}
	if err := initialState.Validate(); err != nil {
		return err
	}
	format, err := detectBackupFormat(archivePath)
	if err != nil {
		return err
	}
	destination := filepath.Clean(dataDir)
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	staged, err := os.MkdirTemp(parent, ".authority-restore-*")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staged)
		}
	}()
	var control []byte
	if format == authorityBackupFormat {
		control, err = restoreAuthorityArchive(archivePath, parent, staged)
	} else {
		err = assetlogRestore(archivePath, filepath.Join(staged, "assets"))
		if err == nil {
			control, err = encodeControl(initialState)
		}
	}
	if err != nil {
		return err
	}
	controlDir := filepath.Join(staged, controlDirectory)
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		return err
	}
	if err := writeSyncedFile(filepath.Join(controlDir, controlFile), control); err != nil {
		return err
	}
	if err := validateStaged(staged, initialState); err != nil {
		return err
	}
	idempotent, err := prepareRestoreTarget(staged, destination)
	if err != nil {
		return err
	}
	if idempotent {
		return nil
	}
	if err := installDirectory(staged, destination); err != nil {
		return fmt.Errorf("提交完整权威恢复状态: %w", err)
	}
	committed = true
	return nil
}

// set by tests only through package scope; keeps the restore owner explicit.
var assetlogRestore = assetlog.Restore

func detectBackupFormat(path string) (string, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("打开权威备份: %w", err)
	}
	defer reader.Close()
	var metadata *zip.File
	for _, entry := range reader.File {
		if entry.Name == "backup.json" {
			if metadata != nil {
				return "", errors.New("备份重复包含 backup.json")
			}
			metadata = entry
		}
	}
	content, err := readSmallZipEntry(metadata, backupMetadataLimit)
	if err != nil {
		return "", err
	}
	var identity struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(content, &identity); err != nil {
		return "", fmt.Errorf("解析备份清单: %w", err)
	}
	if identity.Format != authorityBackupFormat && identity.Format != "ownward.backup/v1" {
		return "", errors.New("不支持的权威备份格式")
	}
	return identity.Format, nil
}

func restoreAuthorityArchive(path, parent, staged string) ([]byte, error) {
	reader, entries, metadata, err := openAuthorityArchive(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	control, err := readSmallZipEntry(entries[controlFile], backupMetadataLimit)
	if err != nil {
		return nil, err
	}
	controlDigest := sha256.Sum256(control)
	if !strings.EqualFold(metadata.Files[controlFile], hex.EncodeToString(controlDigest[:])) {
		return nil, errors.New("权威备份控制状态校验失败")
	}
	if _, err := decodeControl(control); err != nil {
		return nil, err
	}
	assetsFile, err := os.CreateTemp(parent, ".assets-restore-*.zip")
	if err != nil {
		return nil, err
	}
	assetsPath := assetsFile.Name()
	defer os.Remove(assetsPath)
	assetEntry, err := entries["assets.zip"].Open()
	if err != nil {
		_ = assetsFile.Close()
		return nil, err
	}
	assetDigest := sha256.New()
	written, copyErr := copyBounded(io.MultiWriter(assetsFile, assetDigest), assetEntry)
	entryCloseErr := assetEntry.Close()
	syncErr := assetsFile.Sync()
	closeErr := assetsFile.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if entryCloseErr != nil {
		return nil, entryCloseErr
	}
	if uint64(written) != entries["assets.zip"].UncompressedSize64 {
		return nil, errors.New("权威备份资产归档长度不匹配")
	}
	if syncErr != nil {
		return nil, syncErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if !strings.EqualFold(metadata.Files["assets.zip"], hex.EncodeToString(assetDigest.Sum(nil))) {
		return nil, errors.New("权威备份资产归档校验失败")
	}
	if err := assetlogRestore(assetsPath, filepath.Join(staged, "assets")); err != nil {
		return nil, err
	}
	return control, nil
}

func openAuthorityArchive(path string) (*zip.ReadCloser, map[string]*zip.File, authorityBackupManifest, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, authorityBackupManifest{}, err
	}
	fail := func(err error) (*zip.ReadCloser, map[string]*zip.File, authorityBackupManifest, error) {
		_ = reader.Close()
		return nil, nil, authorityBackupManifest{}, err
	}
	entries := make(map[string]*zip.File, 3)
	for _, entry := range reader.File {
		if entry.Name != "assets.zip" && entry.Name != controlFile && entry.Name != "backup.json" {
			return fail(fmt.Errorf("权威备份包含未知文件 %q", entry.Name))
		}
		if _, duplicate := entries[entry.Name]; duplicate {
			return fail(fmt.Errorf("权威备份重复包含文件 %q", entry.Name))
		}
		entries[entry.Name] = entry
	}
	if len(entries) != 3 {
		return fail(errors.New("权威备份不完整"))
	}
	content, err := readSmallZipEntry(entries["backup.json"], backupMetadataLimit)
	if err != nil {
		return fail(err)
	}
	var metadata authorityBackupManifest
	if err := json.Unmarshal(content, &metadata); err != nil {
		return fail(err)
	}
	if metadata.Format != authorityBackupFormat || len(metadata.Files) != 2 || metadata.Files["assets.zip"] == "" || metadata.Files[controlFile] == "" {
		return fail(errors.New("不支持或不完整的权威备份"))
	}
	return reader, entries, metadata, nil
}

func readSmallZipEntry(entry *zip.File, limit int64) ([]byte, error) {
	if entry == nil || entry.UncompressedSize64 > uint64(limit) {
		return nil, errors.New("备份元数据缺失或超过允许大小")
	}
	opened, err := entry.Open()
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	content, err := io.ReadAll(io.LimitReader(opened, limit+1))
	if err != nil || int64(len(content)) > limit || uint64(len(content)) != entry.UncompressedSize64 {
		return nil, errors.New("读取备份元数据失败或长度不匹配")
	}
	return content, nil
}

func writeSyncedFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func validateStaged(staged string, expected contract.ControlState) error {
	assets, err := assetlog.Open(filepath.Join(staged, "assets"))
	if err != nil {
		return fmt.Errorf("验证恢复资产: %w", err)
	}
	if err := assets.Close(); err != nil {
		return err
	}
	encoded, err := os.ReadFile(filepath.Join(staged, controlDirectory, controlFile))
	if err != nil {
		return err
	}
	state, err := decodeControl(encoded)
	if err != nil {
		return err
	}
	if state.ActiveComposition != expected.ActiveComposition || state.ActiveKernelGeneration != expected.ActiveKernelGeneration {
		return errors.New("恢复控制状态与当前已校验组合不一致")
	}
	return nil
}

func prepareRestoreTarget(staged, destination string) (bool, error) {
	info, err := os.Stat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查恢复目标: %w", err)
	}
	if !info.IsDir() {
		return false, errors.New("恢复目标不是目录")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		if err := os.Remove(destination); err != nil {
			return false, fmt.Errorf("准备空恢复目标: %w", err)
		}
		return false, nil
	}
	same, err := sameAuthorityTree(staged, destination)
	if err != nil {
		return false, err
	}
	if same {
		return true, nil
	}
	return false, errors.New("恢复目标含不同权威状态、派生状态、运行状态或其他数据")
}

type treeEntry struct {
	Path   string
	IsDir  bool
	Size   int64
	SHA256 string
}

func sameAuthorityTree(left, right string) (bool, error) {
	leftEntries, err := describeTree(left)
	if err != nil {
		return false, err
	}
	rightEntries, err := describeTree(right)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(leftEntries, rightEntries), nil
}

func describeTree(root string) ([]treeEntry, error) {
	var entries []treeEntry
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := treeEntry{Path: filepath.ToSlash(relative), IsDir: entry.IsDir(), Size: info.Size()}
		if !entry.IsDir() {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("权威状态包含非普通文件 %q", relative)
			}
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			hasher := sha256.New()
			_, copyErr := copyBounded(hasher, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			item.SHA256 = hex.EncodeToString(hasher.Sum(nil))
		}
		entries = append(entries, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func copyBounded(destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 64*1024)
	return io.CopyBuffer(destination, source, buffer)
}

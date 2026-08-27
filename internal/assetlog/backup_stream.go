package assetlog

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
	"strings"
	"time"
)

const backupMetadataLimit = 1024 * 1024

// WriteBackup writes one coherent asset snapshot without materializing asset
// files in memory. The store lock covers sync and the complete ZIP stream.
func (s *Store) WriteBackup(destination io.Writer) error {
	if destination == nil {
		return errors.New("备份输出不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logFile == nil {
		return errors.New("信息资产日志已关闭")
	}
	if err := s.logFile.Sync(); err != nil {
		return fmt.Errorf("持久化信息资产: %w", err)
	}
	archive := zip.NewWriter(destination)
	digests := make(map[string]string, 2)
	for _, name := range []string{manifestName, logName} {
		digest, err := streamAssetFile(archive, name, filepath.Join(s.dir, name))
		if err != nil {
			_ = archive.Close()
			return err
		}
		digests[name] = digest
	}
	metadata, err := json.MarshalIndent(backupManifest{Format: "ownward.backup/v1", CreatedAt: time.Now().UTC(), Files: digests}, "", "  ")
	if err != nil {
		_ = archive.Close()
		return err
	}
	writer, err := archive.CreateHeader(&zip.FileHeader{Name: "backup.json", Method: zip.Deflate})
	if err != nil {
		_ = archive.Close()
		return err
	}
	if _, err := writer.Write(append(metadata, '\n')); err != nil {
		_ = archive.Close()
		return err
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("完成备份归档: %w", err)
	}
	return nil
}

func streamAssetFile(archive *zip.Writer, name, path string) (string, error) {
	source, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取备份内容 %s: %w", name, err)
	}
	defer source.Close()
	writer, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	if _, err := copyBounded(writer, io.TeeReader(source, hasher)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func backupStreaming(s *Store, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return errors.New("备份目标已经存在")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查备份目标: %w", err)
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("创建备份目录: %w", err)
	}
	temporary, err := os.CreateTemp(parent, ".ownward-backup-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时备份: %w", err)
	}
	path := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if err := s.WriteBackup(temporary); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("持久化备份归档: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(path, destination); err != nil {
		return fmt.Errorf("提交备份归档: %w", err)
	}
	committed = true
	return nil
}

func restoreStreaming(archivePath, destination string) error {
	reader, entries, metadata, err := openAssetBackup(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	if entriesAt, err := os.ReadDir(destination); err == nil {
		if len(entriesAt) > 0 {
			return errors.New("恢复目标不是空白目录")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查恢复目标: %w", err)
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(parent, ".ownward-restore-*")
	if err != nil {
		return fmt.Errorf("创建临时恢复目录: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	for _, name := range []string{manifestName, logName} {
		digest, err := streamZipEntry(entries[name], filepath.Join(temporary, name))
		if err != nil {
			return err
		}
		if !strings.EqualFold(metadata.Files[name], digest) {
			return fmt.Errorf("备份文件 %q 校验失败", name)
		}
	}
	if err := validateRestored(temporary); err != nil {
		return err
	}
	if entriesAt, err := os.ReadDir(destination); err == nil {
		if len(entriesAt) != 0 {
			return errors.New("恢复目标在验证期间发生变化")
		}
		if err := os.Remove(destination); err != nil {
			return fmt.Errorf("准备恢复目标: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return fmt.Errorf("提交恢复结果: %w", err)
	}
	committed = true
	return nil
}

func openAssetBackup(path string) (*zip.ReadCloser, map[string]*zip.File, backupManifest, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, nil, backupManifest{}, fmt.Errorf("打开备份归档: %w", err)
	}
	fail := func(err error) (*zip.ReadCloser, map[string]*zip.File, backupManifest, error) {
		_ = reader.Close()
		return nil, nil, backupManifest{}, err
	}
	entries := make(map[string]*zip.File, 3)
	for _, entry := range reader.File {
		if entry.Name != manifestName && entry.Name != logName && entry.Name != "backup.json" {
			return fail(fmt.Errorf("备份归档包含未知文件 %q", entry.Name))
		}
		if _, duplicate := entries[entry.Name]; duplicate {
			return fail(fmt.Errorf("备份归档重复包含文件 %q", entry.Name))
		}
		entries[entry.Name] = entry
	}
	if len(entries) != 3 {
		return fail(errors.New("备份归档不完整"))
	}
	metadataBytes, err := readSmallZipEntry(entries["backup.json"], backupMetadataLimit)
	if err != nil {
		return fail(err)
	}
	var metadata backupManifest
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return fail(fmt.Errorf("解析备份清单: %w", err))
	}
	if metadata.Format != "ownward.backup/v1" || len(metadata.Files) != 2 || metadata.Files[manifestName] == "" || metadata.Files[logName] == "" {
		return fail(errors.New("不支持或不完整的备份格式"))
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
	if err != nil || int64(len(content)) > limit {
		return nil, errors.New("读取备份元数据失败或超过允许大小")
	}
	return content, nil
}

func streamZipEntry(entry *zip.File, destination string) (string, error) {
	if entry == nil {
		return "", errors.New("备份归档缺少必要文件")
	}
	opened, err := entry.Open()
	if err != nil {
		return "", err
	}
	defer opened.Close()
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	written, copyErr := copyBounded(io.MultiWriter(file, hasher), opened)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if uint64(written) != entry.UncompressedSize64 {
		return "", errors.New("备份文件长度不匹配")
	}
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func copyBounded(destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 64*1024)
	return io.CopyBuffer(destination, source, buffer)
}

package desktop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/HJSunDev/ownward/internal/releasebundle"
)

type Release struct {
	ID       string
	Manifest releasebundle.Manifest
	Raw      []byte
}

// 先核实清单中的目标路径；发行真实性来自官方渠道，不来自自附校验值。
func Inspect(root string) (Release, error) {
	f, err := os.Open(filepath.Join(root, "manifest.json"))
	if err != nil {
		return Release{}, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return Release{}, errors.New("发布清单不可读或过大")
	}
	var m releasebundle.Manifest
	if err = json.Unmarshal(raw, &m); err != nil {
		return Release{}, err
	}
	if m.Schema != releasebundle.ManifestSchema || m.Candidate == "" || len(m.Files) == 0 {
		return Release{}, errors.New("发布清单版本或内容无效")
	}
	targetOS, targetArch := m.Target()
	if targetOS != runtime.GOOS || targetArch != runtime.GOARCH {
		return Release{}, fmt.Errorf("发布制品适用于 %s/%s，当前设备为 %s/%s", targetOS, targetArch, runtime.GOOS, runtime.GOARCH)
	}
	if m.Entry() != "bin/"+releasebundle.ExecutableName(targetOS) {
		return Release{}, errors.New("发布程序入口无效")
	}
	seen := map[string]bool{}
	for name, digest := range m.Files {
		if !validRelative(name) {
			return Release{}, fmt.Errorf("发布清单包含无效路径: %s", name)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return Release{}, errors.New("发布清单路径重复")
		}
		seen[key] = true
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != sha256.Size {
			return Release{}, errors.New("发布校验值无效")
		}
	}
	required := []string{m.Entry(), "bin/embedding/manifest.json", "LICENSE", "README.md"}
	if m.OS != "" {
		required = append(required, "bin/ownward.png", "bin/ownward.ico", "bin/ownward.icns")
	}
	for _, name := range required {
		if m.Files[name] == "" {
			return Release{}, fmt.Errorf("发布制品缺少 %s", name)
		}
	}
	sum := sha256.Sum256(raw)
	return Release{ID: hex.EncodeToString(sum[:]), Manifest: m, Raw: raw}, nil
}

func validRelative(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\:") || strings.HasPrefix(name, "/") || !filepath.IsLocal(filepath.FromSlash(name)) {
		return false
	}
	for _, p := range strings.Split(name, "/") {
		if p == "." || p == ".." || p == "" || strings.TrimRight(p, ". ") != p {
			return false
		}
	}
	return strings.ToLower(name) != "manifest.json"
}

// 完整校验后才发布不可变版本目录，复制失败不覆盖正在使用的安装。
func Install(ctx context.Context, source, destination string) (Release, string, error) {
	var err error
	source, err = filepath.Abs(source)
	if err != nil {
		return Release{}, "", err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return Release{}, "", err
	}
	r, err := Inspect(source)
	if err != nil {
		return r, "", err
	}
	versions := filepath.Join(destination, "releases")
	target := filepath.Join(versions, r.ID)
	if _, err = os.Stat(target); err == nil {
		installed, check := Inspect(target)
		if check != nil || installed.ID != r.ID {
			return r, "", errors.New("已安装版本的清单身份不匹配")
		}
		if err = verifyFiles(ctx, target, r, ""); err != nil {
			return r, "", err
		}
		return r, filepath.Join(target, filepath.FromSlash(r.Manifest.Entry())), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return r, "", err
	}
	if err = os.MkdirAll(versions, 0700); err != nil {
		return r, "", err
	}
	tmp, err := os.MkdirTemp(versions, ".install-*")
	if err != nil {
		return r, "", err
	}
	defer os.RemoveAll(tmp) // 只清理本次创建的临时目录。
	if err = verifyFiles(ctx, source, r, tmp); err != nil {
		return r, "", err
	}
	if err = AtomicWrite(filepath.Join(tmp, "manifest.json"), r.Raw, 0600); err != nil {
		return r, "", err
	}
	if err = os.Rename(tmp, target); err != nil {
		// 并发安装可能已完成；只有身份和内容全部一致才复用。
		installed, check := Inspect(target)
		if check != nil || installed.ID != r.ID {
			return r, "", err
		}
		if check := verifyFiles(ctx, target, r, ""); check != nil {
			return r, "", err
		}
	}
	return r, filepath.Join(target, filepath.FromSlash(r.Manifest.Entry())), nil
}

func verifyFiles(ctx context.Context, root string, r Release, copyTo string) error {
	names := make([]string, 0, len(r.Manifest.Files))
	for name := range r.Manifest.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		// 拒绝链接，避免清单路径越出发布目录。
		for part := path; part != filepath.Clean(root); part = filepath.Dir(part) {
			info, err := os.Lstat(part)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("发布制品包含链接: %s", name)
			}
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			f.Close()
			return fmt.Errorf("发布文件无效: %s", name)
		}
		h := sha256.New()
		var out *os.File
		var writer io.Writer = h
		if copyTo != "" {
			dst := filepath.Join(copyTo, filepath.FromSlash(name))
			if err = os.MkdirAll(filepath.Dir(dst), 0700); err == nil {
				out, err = os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
			}
			if err != nil {
				f.Close()
				return err
			}
			writer = io.MultiWriter(h, out)
		}
		_, err = io.Copy(writer, cancelReader{ctx, f})
		f.Close()
		if out != nil {
			if err == nil {
				err = out.Sync()
			}
			closeErr := out.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != strings.ToLower(r.Manifest.Files[name]) {
			return fmt.Errorf("发布文件校验失败: %s", name)
		}
	}
	return nil
}

type cancelReader struct {
	ctx context.Context
	io.Reader
}

func (r cancelReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

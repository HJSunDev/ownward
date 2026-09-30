//go:build !windows

package desktop

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func ValidateSharedDirectory(path string) error { _, err := os.Stat(path); return err }

func shortcutFolders() (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Applications"), filepath.Join(home, "Desktop"), nil
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		base = filepath.Join(home, ".local", "share")
	}
	if !filepath.IsAbs(base) {
		return "", "", errors.New("用户应用目录无效")
	}
	desk := filepath.Join(home, "Desktop")
	config, e := os.UserConfigDir()
	if e != nil {
		return "", "", e
	}
	if data, e := os.ReadFile(filepath.Join(config, "user-dirs.dirs")); e == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "XDG_DESKTOP_DIR=\""); ok && strings.HasSuffix(value, "\"") {
				value = strings.TrimSuffix(value, "\"")
				value = strings.ReplaceAll(value, "$HOME", home)
				if filepath.IsAbs(value) && !strings.ContainsAny(value, "$`\\\n\r") {
					desk = value
				}
			}
		}
	}
	return filepath.Join(base, "applications"), desk, nil
}
func replace(a, b string) error { return os.Rename(a, b) }

func OpenBrowser(address string) error {
	program := "open"
	if runtime.GOOS != "darwin" {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("当前设备没有图形桌面，无法显示网页；可在有桌面的设备上打开")
		}
		program = "xdg-open"
	}
	cmd := exec.Command(program, address)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		return nil
	} // 部分桌面启动器会等待浏览器退出。
}
func CreateShortcuts(p Paths, executable string, desktop bool) error {
	if runtime.GOOS == "darwin" {
		script, info, err := MacLauncher(executable)
		if err != nil {
			return err
		}
		app := filepath.Join(p.Programs, "Ownward.app")
		icon, err := os.ReadFile(filepath.Join(filepath.Dir(executable), "ownward.icns"))
		if err != nil {
			return err
		}
		if err = AtomicWrite(filepath.Join(app, "Contents", "Resources", "ownward.icns"), icon, 0600); err != nil {
			return err
		}
		if err = AtomicWrite(filepath.Join(app, "Contents", "MacOS", "Ownward"), script, 0700); err != nil {
			return err
		}
		if err = AtomicWrite(filepath.Join(app, "Contents", "Info.plist"), info, 0600); err != nil {
			return err
		}
		if desktop {
			if err = os.MkdirAll(p.Desktop, 0700); err != nil {
				return err
			}
			link := filepath.Join(p.Desktop, "Ownward.app")
			if target, e := os.Readlink(link); e == nil && target == app {
				return nil
			}
			return os.Symlink(app, link)
		}
		return nil
	}
	if runtime.GOOS != "linux" {
		return errors.New("当前平台没有桌面入口实现")
	}
	content, err := LinuxLauncher(executable)
	if err != nil {
		return err
	}
	if err = AtomicWrite(filepath.Join(p.Programs, "ownward.desktop"), content, 0700); err != nil {
		return err
	}
	if desktop {
		return AtomicWrite(filepath.Join(p.Desktop, "Ownward.desktop"), content, 0700)
	}
	return nil
}

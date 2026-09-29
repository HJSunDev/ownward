//go:build !windows

package desktop

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func dialogCommand(kind, message string) ([]byte, error) {
	if runtime.GOOS == "darwin" {
		script := `on run argv
display alert "Ownward" message (item 1 of argv) buttons {"好"}
end run`
		if kind == "folder" {
			script = `POSIX path of (choose folder with prompt "选择已有的 Ownward 资料文件夹")`
		}
		return exec.Command("osascript", "-e", script, "--", message).Output()
	}
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return nil, errors.New("当前设备没有可用的图形桌面")
	}
	if path, err := exec.LookPath("zenity"); err == nil {
		args := []string{"--title=Ownward", "--no-markup"}
		switch kind {
		case "folder":
			args = append(args, "--file-selection", "--directory")
		default:
			args = append(args, "--error", "--text="+message)
		}
		return exec.Command(path, args...).Output()
	}
	return nil, errors.New("当前桌面缺少系统对话框组件 zenity")
}

func dialogCancelled(err error) bool {
	var status *exec.ExitError
	if !errors.As(err, &status) {
		return false
	}
	if runtime.GOOS == "darwin" {
		return strings.Contains(string(status.Stderr), "(-128)")
	}
	return status.ExitCode() == 1
}

func Choose(title, message, primary, secondary string) (int, error) {
	var out []byte
	var err error
	if runtime.GOOS == "darwin" {
		script := `on run argv
set choices to {"取消",item 4 of argv,item 3 of argv}
if item 4 of argv is "" then set choices to {"取消",item 3 of argv}
button returned of (display dialog (item 2 of argv) with title (item 1 of argv) buttons choices default button (item 3 of argv) cancel button "取消")
end run`
		out, err = exec.Command("osascript", "-e", script, "--", title, message, primary, secondary).Output()
	} else {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return 0, errors.New("当前设备没有可用的图形桌面")
		}
		args := []string{"--question", "--no-markup", "--title=Ownward", "--text=" + title + "\n\n" + message, "--ok-label=" + primary, "--cancel-label=取消"}
		if secondary != "" {
			args = append(args, "--extra-button="+secondary)
		}
		out, err = exec.Command("zenity", args...).Output()
	}
	// zenity 的附加按钮输出标签并使用非零退出码；先识别具体选择。
	if secondary != "" && strings.TrimSpace(string(out)) == secondary {
		return 2, nil
	}
	if dialogCancelled(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return 1, nil
}
func Alert(message string) error { _, err := dialogCommand("alert", message); return err }
func PickFolder() (string, error) {
	out, err := dialogCommand("folder", "")
	if dialogCancelled(err) {
		return "", nil
	}
	return strings.TrimSpace(string(out)), err
}

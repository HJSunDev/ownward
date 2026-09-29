package desktop

import (
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
)

// 启动文件按各平台语法转义，不把路径拼入执行中的命令。
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func LinuxLauncher(executable string) ([]byte, error) {
	if !filepath.IsAbs(executable) || strings.ContainsAny(executable, "\x00\r\n") {
		return nil, fmt.Errorf("启动程序位置无效")
	}
	escape := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "`", "\\`", "$", "\\$", "%", "%%")
	valueEscape := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\r", "\\r")
	// Exec 会先解析桌面配置转义，再解析命令引号。
	command := valueEscape.Replace("\"" + escape.Replace(executable) + "\" owner-window --interactive")
	return []byte("[Desktop Entry]\nType=Application\nName=Ownward\nComment=打开自己的资料\nExec=" + command + "\nIcon=" + valueEscape.Replace(filepath.Join(filepath.Dir(executable), "ownward.png")) + "\nTerminal=false\nCategories=Office;\nStartupNotify=false\n"), nil
}

func MacLauncher(executable string) ([]byte, []byte, error) {
	if !filepath.IsAbs(executable) || strings.ContainsAny(executable, "\x00\r\n") {
		return nil, nil, fmt.Errorf("启动程序位置无效")
	}
	var title strings.Builder
	if err := xml.EscapeText(&title, []byte("Ownward")); err != nil {
		return nil, nil, err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>CFBundleName</key><string>` + title.String() + `</string><key>CFBundleExecutable</key><string>Ownward</string><key>CFBundleIdentifier</key><string>org.ownward.launcher</string><key>CFBundleIconFile</key><string>ownward.icns</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`
	return []byte("#!/bin/sh\nexec " + shellQuote(executable) + " owner-window --interactive\n"), []byte(plist), nil
}

//go:build windows

package desktop

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ole = windows.NewLazySystemDLL("ole32.dll")

func shortcutFolders() (string, string, error) {
	p, err := windows.KnownFolderPath(windows.FOLDERID_Programs, 0)
	if err != nil {
		return "", "", err
	}
	d, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
	return p, d, err
}

func replace(a, b string) error {
	x, err := windows.UTF16PtrFromString(a)
	if err != nil {
		return err
	}
	y, err := windows.UTF16PtrFromString(b)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(x, y, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func OpenBrowser(address string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	target, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return err
	}
	result, _, _ := windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW").Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(target)), 0, 0, 1)
	if result <= 32 {
		return fmt.Errorf("系统未能打开默认浏览器（%d），请设置默认浏览器后重试", result)
	}
	return nil
}

//go:uintptrescapes
func comCall(object unsafe.Pointer, method int, args ...uintptr) uintptr {
	vtable := *(**[32]uintptr)(object)
	fn := vtable[method]
	out, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(object)}, args...)...)
	return out
}

func hresult(value uintptr) error {
	if int32(value) < 0 {
		return fmt.Errorf("Windows 操作失败 (0x%08x)", uint32(value))
	}
	return nil
}

func CreateShortcuts(p Paths, executable string, desktop bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	if err := hresult(hr); err != nil {
		return err
	}
	defer ole.NewProc("CoUninitialize").Call()
	for _, dir := range []string{p.Programs, func() string {
		if desktop {
			return p.Desktop
		}
		return ""
	}()} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := shortcut(filepath.Join(dir, "Ownward.lnk"), executable); err != nil {
			return err
		}
	}
	return nil
}

func shortcut(path, executable string) error {
	class := windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iid := windows.GUID{Data1: 0x000214f9, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	var link unsafe.Pointer
	hr, _, _ := ole.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&class)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&link)))
	if err := hresult(hr); err != nil {
		return err
	}
	defer comCall(link, 2)
	set := func(index int, value string, extra ...uintptr) error {
		v, err := windows.UTF16PtrFromString(value)
		if err != nil {
			return err
		}
		result := hresult(comCall(link, index, append([]uintptr{uintptr(unsafe.Pointer(v))}, extra...)...))
		runtime.KeepAlive(v)
		return result
	}
	if err := set(20, executable); err != nil {
		return err
	}
	if err := set(9, filepath.Dir(executable)); err != nil {
		return err
	}
	if err := set(11, "owner-window --interactive"); err != nil {
		return err
	}
	if err := set(7, "打开自己的资料"); err != nil {
		return err
	}
	if err := set(17, filepath.Join(filepath.Dir(executable), "ownward.ico"), 0); err != nil {
		return err
	}
	persistID := windows.GUID{Data1: 0x0000010b, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	var persist unsafe.Pointer
	if err := hresult(comCall(link, 0, uintptr(unsafe.Pointer(&persistID)), uintptr(unsafe.Pointer(&persist)))); err != nil {
		return err
	}
	defer comCall(persist, 2)
	f, err := os.CreateTemp(filepath.Dir(path), ".ownward-link-*.lnk")
	if err != nil {
		return err
	}
	f.Close()
	defer os.Remove(f.Name())
	n, err := windows.UTF16PtrFromString(f.Name())
	if err != nil {
		return err
	}
	if err := hresult(comCall(persist, 6, uintptr(unsafe.Pointer(n)), 1)); err != nil {
		return err
	}
	return replace(f.Name(), path)
}

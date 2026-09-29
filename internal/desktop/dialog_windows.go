//go:build windows

package desktop

import (
	"encoding/binary"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"
)

// 使用有明确动作名的系统按钮，不让“是／否”承担不同的产品含义。
func Choose(title, message, primary, secondary string) (int, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cleanup, err := activateCommonControls()
	if err != nil {
		return 0, err
	}
	defer cleanup()
	var stringsAlive []*uint16
	text := func(s string) uintptr {
		if s == "" {
			return 0
		}
		p, e := windows.UTF16PtrFromString(s)
		if e != nil {
			return 0
		}
		stringsAlive = append(stringsAlive, p)
		return uintptr(unsafe.Pointer(p))
	}
	// Windows SDK 对 TASKDIALOGCONFIG/BUTTON 使用 pack(1)，不能用 Go 的自然对齐。
	var buttons, config packedDialog
	buttons.u32(1001)
	buttons.ptr(text(primary))
	count := uint32(1)
	if secondary != "" {
		buttons.u32(1002)
		buttons.ptr(text(secondary))
		count++
	}
	config.u32(0)
	config.ptr(0)
	config.ptr(0)
	config.u32(0x08 | 0x10)
	config.u32(8)
	config.ptr(text("Ownward"))
	config.ptr(0)
	config.ptr(text(title))
	config.ptr(text(message))
	config.u32(count)
	config.ptr(uintptr(unsafe.Pointer(&buttons[0])))
	config.u32(1001)
	config.u32(0)
	config.ptr(0)
	config.u32(0)
	for i := 0; i < 8; i++ {
		config.ptr(0)
	}
	config.u32(340)
	binary.LittleEndian.PutUint32(config, uint32(len(config)))
	lib, err := windows.LoadDLL("comctl32.dll")
	if err != nil {
		return 0, err
	}
	defer lib.Release()
	proc, err := lib.FindProc("TaskDialogIndirect")
	if err != nil {
		return 0, err
	}
	var choice int32
	hr, _, _ := proc.Call(uintptr(unsafe.Pointer(&config[0])), uintptr(unsafe.Pointer(&choice)), 0, 0)
	runtime.KeepAlive(stringsAlive)
	runtime.KeepAlive(buttons)
	if err = hresult(hr); err != nil {
		return 0, err
	}
	if choice == 1001 {
		return 1, nil
	}
	if choice == 1002 {
		return 2, nil
	}
	return 0, nil
}

type packedDialog []byte

func (b *packedDialog) u32(n uint32) {
	var x [4]byte
	binary.LittleEndian.PutUint32(x[:], n)
	*b = append(*b, x[:]...)
}
func (b *packedDialog) ptr(n uintptr) {
	if unsafe.Sizeof(n) == 8 {
		var x [8]byte
		binary.LittleEndian.PutUint64(x[:], uint64(n))
		*b = append(*b, x[:]...)
	} else {
		b.u32(uint32(n))
	}
}

func activateCommonControls() (func(), error) {
	dir, err := os.MkdirTemp("", "ownward-dialog-*")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "dialog.manifest")
	manifest := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0"><dependency><dependentAssembly><assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/></dependentAssembly></dependency></assembly>`
	if err = os.WriteFile(path, []byte(manifest), 0600); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	source, _ := windows.UTF16PtrFromString(path)
	context := struct {
		Size, Flags                      uint32
		Source                           *uint16
		Architecture, Language           uint16
		Directory, Resource, Application *uint16
		Module                           uintptr
	}{Source: source}
	context.Size = uint32(unsafe.Sizeof(context))
	api := windows.NewLazySystemDLL("kernel32.dll")
	handle, _, e := api.NewProc("CreateActCtxW").Call(uintptr(unsafe.Pointer(&context)))
	if handle == ^uintptr(0) {
		os.RemoveAll(dir)
		return nil, e
	}
	var cookie uintptr
	ok, _, e := api.NewProc("ActivateActCtx").Call(handle, uintptr(unsafe.Pointer(&cookie)))
	if ok == 0 {
		api.NewProc("ReleaseActCtx").Call(handle)
		os.RemoveAll(dir)
		return nil, e
	}
	return func() {
		api.NewProc("DeactivateActCtx").Call(0, cookie)
		api.NewProc("ReleaseActCtx").Call(handle)
		os.RemoveAll(dir)
	}, nil
}

func Alert(message string) error {
	title, _ := windows.UTF16PtrFromString("Ownward")
	text, err := windows.UTF16PtrFromString(message)
	if err != nil {
		return err
	}
	v, _, e := windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(title)), 0x10)
	if v == 0 {
		return e
	}
	return nil
}

// 使用系统目录选择器，所选路径不经 Shell 执行。
func PickFolder() (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	if err := hresult(hr); err != nil {
		return "", err
	}
	defer ole.NewProc("CoUninitialize").Call()
	class := windows.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
	iid := windows.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}
	var dialog unsafe.Pointer
	hr, _, _ = ole.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&class)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&dialog)))
	if err := hresult(hr); err != nil {
		return "", err
	}
	defer comCall(dialog, 2)
	if err := hresult(comCall(dialog, 9, 0x20|0x40|0x800)); err != nil {
		return "", err
	}
	title, _ := windows.UTF16PtrFromString("选择已有的 Ownward 资料文件夹")
	if err := hresult(comCall(dialog, 17, uintptr(unsafe.Pointer(title)))); err != nil {
		return "", err
	}
	hr = comCall(dialog, 3, 0)
	if uint32(hr) == 0x800704c7 {
		return "", nil
	}
	if err := hresult(hr); err != nil {
		return "", err
	}
	var item unsafe.Pointer
	if err := hresult(comCall(dialog, 20, uintptr(unsafe.Pointer(&item)))); err != nil {
		return "", err
	}
	defer comCall(item, 2)
	var path *uint16
	if err := hresult(comCall(item, 5, 0x80058000, uintptr(unsafe.Pointer(&path)))); err != nil {
		return "", err
	}
	if path == nil {
		return "", errors.New("未取得资料位置")
	}
	defer ole.NewProc("CoTaskMemFree").Call(uintptr(unsafe.Pointer(path)))
	return windows.UTF16PtrToString(path), nil
}

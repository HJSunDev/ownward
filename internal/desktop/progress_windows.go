//go:build windows

package desktop

import (
	"context"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 系统进度窗仅存续于本次启动；后台执行与取消使用同一操作上下文。
func WithProgress(ctx context.Context, work func(context.Context, func(string)) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	if err := hresult(hr); err != nil {
		return err
	}
	defer ole.NewProc("CoUninitialize").Call()
	class := windows.GUID{Data1: 0xf8383852, Data2: 0xfcd3, Data3: 0x11d1, Data4: [8]byte{0xa6, 0xb9, 0, 0x60, 0x97, 0xdf, 0x5b, 0xd4}}
	iid := windows.GUID{Data1: 0xebbc7c04, Data2: 0x315e, Data3: 0x11d2, Data4: [8]byte{0xb6, 0x2f, 0, 0x60, 0x97, 0xdf, 0x5b, 0xd4}}
	var dialog unsafe.Pointer
	hr, _, _ = ole.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&class)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&dialog)))
	if err := hresult(hr); err != nil {
		return err
	}
	defer comCall(dialog, 2)
	title, _ := windows.UTF16PtrFromString("Ownward")
	comCall(dialog, 5, uintptr(unsafe.Pointer(title)))
	if err := hresult(comCall(dialog, 3, 0, 0, 0x08|0x20, 0)); err != nil {
		return err
	}
	defer comCall(dialog, 4)
	setLine := func(text string) {
		p, _ := windows.UTF16PtrFromString(text)
		comCall(dialog, 10, 1, uintptr(unsafe.Pointer(p)), 0, 0)
	}
	setLine("正在打开资料…")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- work(ctx, func(s string) {
			select {
			case updates <- s:
			case <-ctx.Done():
			}
		})
	}()
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		case s := <-updates:
			setLine(s)
		case <-ticker.C:
			if comCall(dialog, 7) != 0 {
				cancel()
				setLine("正在结束本次操作…")
			}
		}
	}
}

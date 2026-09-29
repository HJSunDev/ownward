//go:build windows

package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWindowsShortcutsAreDurableUserLaunchers(t *testing.T) {
	p := Paths{Programs: filepath.Join(t.TempDir(), "menu"), Desktop: filepath.Join(t.TempDir(), "desktop")}
	executable := filepath.Join(t.TempDir(), "Ownward program", "bin", "ownward.exe")
	if err := CreateShortcuts(p, executable, true); err != nil {
		t.Fatal(err)
	}
	if err := CreateShortcuts(p, executable, true); err != nil {
		t.Fatal("idempotent retry", err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := ole.NewProc("CoInitializeEx").Call(0, 2)
	if err := hresult(hr); err != nil {
		t.Fatal(err)
	}
	defer ole.NewProc("CoUninitialize").Call()
	class := windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iid := windows.GUID{Data1: 0x000214f9, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	var link unsafe.Pointer
	hr, _, _ = ole.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&class)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&link)))
	if err := hresult(hr); err != nil {
		t.Fatal(err)
	}
	defer comCall(link, 2)
	persistID := windows.GUID{Data1: 0x0000010b, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	var persist unsafe.Pointer
	if err := hresult(comCall(link, 0, uintptr(unsafe.Pointer(&persistID)), uintptr(unsafe.Pointer(&persist)))); err != nil {
		t.Fatal(err)
	}
	defer comCall(persist, 2)
	for _, dir := range []string{p.Programs, p.Desktop} {
		path, _ := windows.UTF16PtrFromString(filepath.Join(dir, "Ownward.lnk"))
		if err := hresult(comCall(persist, 5, uintptr(unsafe.Pointer(path)), 0)); err != nil {
			t.Fatal(err)
		}
		var argument [1024]uint16
		if err := hresult(comCall(link, 10, uintptr(unsafe.Pointer(&argument[0])), 1024)); err != nil {
			t.Fatal(err)
		}
		if actual := windows.UTF16ToString(argument[:]); actual != "owner-window --interactive" || strings.Contains(actual, "http") {
			t.Fatal(actual)
		}
		var target [1024]uint16
		if err := hresult(comCall(link, 3, uintptr(unsafe.Pointer(&target[0])), 1024, 0, 0)); err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(windows.UTF16ToString(target[:]), executable) {
			t.Fatal("shortcut did not keep the actual program path", windows.UTF16ToString(target[:]))
		}
	}
}

func TestWindowsNativeChoiceDependency(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	finish, err := activateCommonControls()
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	lib, err := windows.LoadDLL("comctl32.dll")
	if err != nil {
		t.Fatal(err)
	}
	defer lib.Release()
	if _, err = lib.FindProc("TaskDialogIndirect"); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsStartupProgress(t *testing.T) {
	if os.Getenv("OWNWARD_NATIVE_ENTRY_TEST") != "1" {
		t.Skip("visible native startup surface")
	}
	start := time.Now()
	err := WithProgress(context.Background(), func(ctx context.Context, update func(string)) error {
		t.Logf("native surface created in %s", time.Since(start))
		update("正在核对测试资料…")
		select {
		case <-time.After(600 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		update("正在打开测试页面…")
		select {
		case <-time.After(600 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWindowsProgressCancellationEndsWithoutFailure(t *testing.T) {
	if os.Getenv("OWNWARD_NATIVE_ENTRY_TEST") != "1" {
		t.Skip("visible native startup surface")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := WithProgress(ctx, func(ctx context.Context, update func(string)) error {
		update("正在验证取消…")
		cancel()
		<-ctx.Done()
		return errors.New("process was interrupted")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation reported as operation failure", err)
	}
}

func TestWindowsFirstUseChoice(t *testing.T) {
	if os.Getenv("OWNWARD_NATIVE_CHOICE_TEST") != "1" {
		t.Skip("requires an observed native choice")
	}
	choice, err := Choose("开始使用 Ownward", "开始一份新资料，或接续已经保存的内容。", "开始使用", "打开已有资料")
	if err != nil || choice != 0 {
		t.Fatalf("expected observed cancellation, got %d: %v", choice, err)
	}
}

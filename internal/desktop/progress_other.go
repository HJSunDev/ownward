//go:build !windows

package desktop

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

func WithProgress(ctx context.Context, work func(context.Context, func(string)) error) error {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var cmd *exec.Cmd
	var update func(string)
	var stream io.WriteCloser
	if runtime.GOOS == "darwin" {
		dir, err := os.MkdirTemp("", "ownward-start-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "phase")
		if err = AtomicWrite(path, []byte("正在打开资料…"), 0600); err != nil {
			return err
		}
		// 只使用系统 Cocoa 呈现启动进度，状态文件不含凭据、正文或命令。
		cmd = exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", macProgressScript, "--", path)
		update = func(s string) { _ = AtomicWrite(path, []byte(s), 0600) }
	} else {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return fmt.Errorf("当前设备没有可用的图形桌面")
		}
		path, err := exec.LookPath("zenity")
		if err != nil {
			return fmt.Errorf("此桌面需要系统对话框组件 zenity")
		}
		cmd = exec.CommandContext(ctx, path, "--progress", "--title=Ownward", "--text=正在打开资料…", "--pulsate", "--auto-close", "--width=380", "--no-markup")
		stream, err = cmd.StdinPipe()
		if err != nil {
			return err
		}
		defer stream.Close()
		var mu sync.Mutex
		update = func(s string) { mu.Lock(); defer mu.Unlock(); _, _ = fmt.Fprintln(stream, "# "+s) }
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	closed := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if ctx.Err() != nil {
			err = ctx.Err()
		} else if err == nil || dialogCancelled(err) {
			err = context.Canceled
		}
		closed <- err
		cancel()
	}()
	err := work(ctx, update)
	interrupted := ctx.Err() != nil
	if stream != nil {
		_ = stream.Close()
	}
	// 启动交互随本次操作结束，不留下常驻辅助进程。
	cancel()
	dialogErr := <-closed
	if interrupted {
		if parent.Err() != nil {
			return parent.Err()
		}
		return dialogErr
	}
	return err
}

const macProgressScript = `ObjC.import('Cocoa');ObjC.import('Foundation');
function run(argv){
 const app=$.NSApplication.sharedApplication;app.setActivationPolicy(1);
 const win=$.NSWindow.alloc.initWithContentRectStyleMaskBackingDefer($.NSMakeRect(0,0,420,128),3,2,false);
 win.title='Ownward';win.center;win.releasedWhenClosed=false;
 const label=$.NSTextField.labelWithString('正在打开资料…');label.frame=$.NSMakeRect(28,65,364,25);win.contentView.addSubview(label);
 const spin=$.NSProgressIndicator.alloc.initWithFrame($.NSMakeRect(28,30,364,14));spin.indeterminate=true;spin.style=0;spin.startAnimation(null);win.contentView.addSubview(spin);
 win.makeKeyAndOrderFront(null);app.activateIgnoringOtherApps(true);
 while(win.visible){const text=$.NSString.stringWithContentsOfFileEncodingError(argv[0],$.NSUTF8StringEncoding,null);if(text)label.stringValue=text;
 const event=app.nextEventMatchingMaskUntilDateInModeDequeue($.NSEventMaskAny,$.NSDate.dateWithTimeIntervalSinceNow(0.1),$.NSDefaultRunLoopMode,true);if(event)app.sendEvent(event);app.updateWindows;}
}`

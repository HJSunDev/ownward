package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/HJSunDev/ownward/internal/assembly"
	"github.com/HJSunDev/ownward/internal/config"
	"github.com/HJSunDev/ownward/internal/desktop"
	"github.com/HJSunDev/ownward/internal/embedding"
)

type entryResult struct {
	Schema     string `json:"schema"`
	Status     string `json:"status"`
	Phase      string `json:"phase"`
	Message    string `json:"message,omitempty"`
	Retry      bool   `json:"retry,omitempty"`
	Executable string `json:"executable,omitempty"`
}

func runInstall(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	p, err := desktop.DefaultPaths()
	if err != nil {
		return err
	}
	return installAt(ctx, args, stdout, stderr, executable, p)
}

// 环境显式传入，集成测试可运行真实入口与子进程而不触碰用户安装。
func installAt(ctx context.Context, args []string, stdout, stderr io.Writer, executable string, p desktop.Paths) (resultErr error) {
	phase := "prepare"
	defer func() {
		if resultErr != nil {
			_ = writeJSON(stdout, entryResult{Schema: "ownward.install/v1", Status: "failed", Phase: phase, Message: resultErr.Error(), Retry: true})
		}
	}()
	f := flag.NewFlagSet("install", flag.ContinueOnError)
	f.SetOutput(stderr)
	source := f.String("source", "", "完整发布包位置")
	data := f.String("data-dir", "", "已有或首次启用的资料位置")
	initialize := f.Bool("new", false, "明确首次启用")
	open := f.Bool("open", false, "安装后打开页面")
	desk := f.Bool("desktop", false, "创建桌面入口")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("安装参数无效")
	}
	root, err := desktop.ReleaseRoot(executable)
	if err != nil {
		return err
	}
	if *source != "" {
		absolute, e := filepath.Abs(*source)
		if e != nil {
			return e
		}
		if absolute != root {
			return errors.New("请运行目标发布包自己的安装命令")
		}
	}
	if err = os.MkdirAll(filepath.Dir(p.State), 0700); err != nil {
		return err
	}
	lock, err := acquireServiceStartupLockContext(ctx, p.State+".lock", 30*time.Second)
	if err != nil {
		return err
	}
	defer lock.release()
	previous, loadErr := desktop.Load(p.State)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return loadErr
	}
	selected, err := config.Load(*data)
	if err != nil {
		return err
	}
	selected.DataDir, err = normalizeDataDirectory(selected.DataDir)
	if err != nil {
		return err
	}
	if previous.Binding != nil && !sameDataDirectory(previous.Binding.Data, selected.DataDir) {
		return errors.New("安装不会更换已选择的资料，请从 Ownward 入口明确切换")
	}
	verification, err := assembly.PreflightSharedConnector(assembly.Collaborative, filepath.Join(root, "bin", "embedding"))
	if err != nil {
		return err
	}
	pending, err := pendingInitialization(p.State, selected.DataDir, verification.Composition)
	if err != nil {
		return err
	}
	control, readErr := assembly.ReadLocalIdentity(ctx, selected.DataDir)
	if previous.Binding != nil && (readErr != nil || control.InformationControl == nil || control.InformationControl.SystemID != previous.Binding.System) {
		return errors.New("已选择的资料暂不可用，请从 Ownward 入口重新定位；未创建替代资料")
	}
	initialized := control.InformationControl != nil && control.InformationControl.SystemID != ""
	if readErr != nil || !initialized {
		if !*initialize && !pending {
			return writeJSON(stdout, entryResult{Schema: "ownward.install/v1", Status: "needs_input", Phase: "select_data", Message: "请选择已有的 Ownward 资料文件夹；首次使用请从入口选择“开始使用”。"})
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) && !pending {
			return fmt.Errorf("资料位置无法核实，未初始化: %w", readErr)
		}
		if !pending && (readErr != nil || control.ActiveComposition == "") {
			if err = checkNewDataDirectory(selected.DataDir, p.State); err != nil {
				return err
			}
		}
	}
	// 修改入口前核对持久权威，普通安装不隐式迁移资料。
	vector, err := embedding.InspectBundle(filepath.Join(root, "bin", "embedding"))
	if err != nil {
		return err
	}
	if err = embedding.VerifyRuntimeTarget(vector, runtime.GOOS, runtime.GOARCH); err != nil {
		return err
	}
	if control.InformationControl != nil {
		if control.Access != nil && control.Access.Handoff != nil && control.Access.Handoff.Phase == "retired" {
			return errors.New("这份资料已经迁出，请在资料所在设备打开；原入口未更改")
		}
		if control.ActiveComposition != verification.Composition {
			return writeJSON(stdout, entryResult{Schema: "ownward.install/v1", Status: "needs_input", Phase: "migration", Message: "这份资料需要先完成正式迁移；当前安装和资料保持不变"})
		}
	}
	phase = "install"
	release, installed, err := desktop.Install(ctx, root, p.Root)
	if err != nil {
		return err
	}
	if err = writeJSON(stdout, entryResult{Schema: "ownward.install/v1", Status: "working", Phase: "installed", Message: "程序与运行资源已校验并安装"}); err != nil {
		return err
	}
	if !initialized {
		phase = "initialize"
		intent, _ := json.Marshal(initializationIntent{Schema: "ownward.initialize/v1", Data: selected.DataDir, Composition: verification.Composition})
		if err = desktop.AtomicWrite(p.State+".preparing", intent, 0600); err != nil {
			return err
		}
		if err = runInstalled(ctx, installed, []string{"setup", "--data-dir", selected.DataDir}, io.Discard, stderr); err != nil {
			return err
		}
		control, err = assembly.ReadLocalIdentity(ctx, selected.DataDir)
		if err != nil {
			return err
		}
	}
	phase = "entry"
	if control.InformationControl == nil {
		return errors.New("资料尚未完成首次启用")
	}
	if selected.System != "" && selected.System != control.InformationControl.SystemID {
		return errors.New("资料身份与日常入口不一致")
	}
	next := desktop.State{Revision: previous.Revision + 1, Release: release.ID, Executable: installed, Binding: &desktop.Binding{System: control.InformationControl.SystemID, Data: selected.DataDir}, Desktop: *desk || previous.Desktop}
	if err = desktop.CreateShortcuts(p, installed, next.Desktop); err != nil {
		return fmt.Errorf("程序已安装，系统入口未完成，可重试: %w", err)
	}
	if err = desktop.Save(p.State, next); err != nil {
		return err
	}
	if err = os.Remove(p.State + ".preparing"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if *open {
		phase = "open"
		if err = runInstalled(ctx, installed, []string{"owner-window", "--data-dir", selected.DataDir}, io.Discard, stderr); err != nil {
			return err
		}
		return writeJSON(stdout, entryResult{Schema: "ownward.install/v1", Status: "ready", Phase: "opened", Message: "Ownward 已安装并打开，可从系统入口再次使用", Executable: installed})
	}
	return writeJSON(stdout, entryResult{Schema: "ownward.install/v1", Status: "ready", Phase: "entry_ready", Message: "Ownward 已安装，可从系统入口打开", Executable: installed})
}

type initializationIntent struct {
	Schema, Data, Composition string
}

// 仅接续本安装器已核对空目录并记录的首次启用，不凭迁移文件名放行未知资料。
func pendingInitialization(statePath, data, composition string) (bool, error) {
	f, err := os.Open(statePath + ".preparing")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	var intent initializationIntent
	d := json.NewDecoder(io.LimitReader(f, 4096))
	if err = d.Decode(&intent); err != nil {
		return false, err
	}
	if intent.Schema != "ownward.initialize/v1" || !sameDataDirectory(intent.Data, data) || intent.Composition != composition {
		return false, errors.New("上次首次启用尚未完成，请使用原版本和原资料位置继续")
	}
	return true, nil
}

func checkNewDataDirectory(dataDir, statePath string) error {
	entries, err := os.ReadDir(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if sameDataDirectory(dataDir, filepath.Dir(statePath)) && (entry.Name() == filepath.Base(statePath)+".lock" || entry.Name() == "connections" || entry.Name() == "program") {
			continue
		}
		return errors.New("所选位置已有内容但无法确认是可用的资料库；未执行初始化，请选择原资料或空文件夹")
	}
	return nil
}

var errRetryDesktop = errors.New("retry desktop entry")

// 已知业务条件直接告诉用户下一步；底层诊断仍留在“查看详情”。
type entryProblem struct {
	message string
	cause   error
}

func (e *entryProblem) Error() string {
	if e.cause != nil {
		return e.message + "\n" + e.cause.Error()
	}
	return e.message
}
func (e *entryProblem) Unwrap() error { return e.cause }

func sameDataDirectory(a, b string) bool {
	x, e := normalizeDataDirectory(a)
	if e != nil {
		return false
	}
	y, e := normalizeDataDirectory(b)
	return e == nil && x == y
}

func runDesktop(ctx context.Context, stderr io.Writer) error {
	return runDesktopLoop(ctx, func(ctx context.Context) error { return desktopAttempt(ctx, stderr) }, desktop.Choose, desktop.Alert)
}

func runDesktopLoop(ctx context.Context, attempt func(context.Context) error, choose func(string, string, string, string) (int, error), alert func(string) error) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := attempt(ctx)
		if errors.Is(err, errRetryDesktop) {
			continue
		}
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}
		message := "可以重新尝试，或查看具体原因。"
		var problem *entryProblem
		if errors.As(err, &problem) {
			message = problem.message
		}
		for {
			choice, e := choose("暂时无法打开 Ownward", message, "重试", "查看详情")
			if e != nil {
				return errors.Join(err, e)
			}
			if choice == 2 {
				if e = alert(err.Error()); e != nil {
					return errors.Join(err, e)
				}
				continue
			}
			if choice != 1 {
				return nil
			}
			break // 只有明确点击重试，才重新执行操作。
		}
	}
}

func desktopAttempt(ctx context.Context, stderr io.Writer) error {
	path, err := desktop.StatePath()
	if err != nil {
		return err
	}
	state, err := desktop.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		p, e := desktop.DefaultPaths()
		if e != nil {
			return e
		}
		args := []string{"--open", "--data-dir", p.Data}
		known, e := assembly.ReadLocalIdentity(ctx, p.Data)
		if e != nil || known.InformationControl == nil || known.InformationControl.SystemID == "" {
			choice, e := desktop.Choose("开始使用 Ownward", "开始一份新资料，或接续已经保存的内容。", "开始使用", "打开已有资料")
			if e != nil {
				return e
			}
			if choice == 0 {
				return nil
			}
			if choice == 1 {
				args = append(args, "--new")
			} else {
				folder, e := desktop.PickFolder()
				if e != nil {
					return e
				}
				if folder == "" {
					return nil
				}
				args = []string{"--open", "--data-dir", folder}
			}
		}
		return desktop.WithProgress(ctx, func(ctx context.Context, update func(string)) error {
			update("正在准备 Ownward…")
			w := &entryProgress{update: update}
			if err := runInstall(ctx, args, w, stderr); err != nil {
				return err
			}
			return w.decision
		})
	} else if err == nil {
		current, e := os.Executable()
		if e != nil {
			return e
		}
		currentInfo, e := os.Stat(current)
		if e != nil {
			return e
		}
		installedInfo, e := os.Stat(state.Executable)
		if e != nil {
			return e
		}
		if !os.SameFile(currentInfo, installedInfo) {
			return runInstalled(ctx, state.Executable, []string{"owner-window", "--interactive"}, io.Discard, stderr)
		}
		if state.Binding == nil {
			return errors.New("尚未选择资料，请重新执行安装并选择已有资料")
		}
		invalid := false
		err = desktop.WithProgress(ctx, func(ctx context.Context, update func(string)) error {
			update("正在定位资料…")
			control, e := assembly.ReadLocalIdentity(ctx, state.Binding.Data)
			if e != nil || control.InformationControl == nil || control.InformationControl.SystemID != state.Binding.System {
				invalid = true
				return errors.New("原资料暂时无法打开")
			}
			return openOwnerWindowProgress(ctx, state.Binding.Data, filepath.Join(filepath.Dir(state.Executable), "embedding"), io.Discard, stderr, update)
		})
		if errors.Is(err, context.Canceled) {
			return err
		}
		if invalid {
			choice, e := desktop.Choose("暂时找不到原资料", "请检查资料所在磁盘是否已连接。原来的选择已保留。", "重试", "重新选择位置")
			if e != nil {
				return e
			}
			if choice == 0 {
				return nil
			}
			if choice == 1 {
				return errRetryDesktop
			}
			folder, e := desktop.PickFolder()
			if e != nil || folder == "" {
				return e
			}
			return desktop.WithProgress(ctx, func(ctx context.Context, update func(string)) error {
				update("正在核对资料位置…")
				target, e := assembly.ReadLocalIdentity(ctx, folder)
				if e != nil || target.InformationControl == nil || target.InformationControl.SystemID != state.Binding.System {
					return errors.New("所选文件夹不是原来的资料，日常入口未更改")
				}
				if e = openOwnerWindowProgress(ctx, folder, filepath.Join(filepath.Dir(state.Executable), "embedding"), io.Discard, stderr, update); e != nil {
					return e
				}
				return updateDesktopBinding(path, state.Revision, *state.Binding, desktop.Binding{System: state.Binding.System, Data: folder})
			})
		}
	}
	return err
}

type entryProgress struct {
	update   func(string)
	decision error
}

func (w *entryProgress) Write(data []byte) (int, error) {
	var r entryResult
	if err := json.Unmarshal(data, &r); err != nil {
		return 0, err
	}
	if r.Status == "needs_input" {
		w.decision = &entryProblem{message: r.Message}
	}
	if r.Message != "" {
		w.update(r.Message)
	}
	return len(data), nil
}

// 只接受用户实际看见的绑定版本，不能借后续刷新绕过重新确认。
func updateDesktopBinding(path string, revision uint64, previous, next desktop.Binding) error {
	lock, err := acquireServiceStartupLock(path+".lock", 10*time.Second)
	if err != nil {
		return err
	}
	defer lock.release()
	state, err := desktop.Load(path)
	if err != nil {
		return err
	}
	if state.Revision != revision || state.Binding == nil || state.Binding.System != previous.System || !sameDataDirectory(state.Binding.Data, previous.Data) {
		return errors.New("日常资料已经变化，请重新核对")
	}
	next.Data, err = normalizeDataDirectory(next.Data)
	if err != nil {
		return err
	}
	state.Binding = &next
	state.Revision++
	return desktop.Save(path, state)
}

func runInstalled(ctx context.Context, executable string, args []string, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdout, command.Stderr = stdout, stderr
	configureSharedServiceProcess(command)
	return command.Run()
}

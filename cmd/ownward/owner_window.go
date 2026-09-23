package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/adapter/ownerwindow"
	"github.com/HJSunDev/ownward/internal/assembly"
	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/ownerview"
)

func newOwnerWindow(r *assembly.Runtime, dataDir string) (*ownerwindow.Server, error) {
	k := r.Streaming()
	if k == nil {
		return nil, errors.New("当前存储未提供物主视图")
	}
	view, e := ownerview.New(k.Store, r.UserControl(), r.Management())
	if e != nil {
		return nil, e
	}
	return ownerwindow.New(view, localOwnerArchives(k, r.UserControl(), dataDir)), nil
}

func localOwnerArchives(k *core.StreamingAssets, control *informationcontrol.Control, dataDir string) ownerwindow.Archives {
	return ownerwindow.Archives{
		Backup: func(ctx context.Context) (string, error) {
			path := filepath.Join(k.Scratch, "owner-backup-"+connectionID()+".zip")
			if e := k.Store.ExportArchive(ctx, path); e != nil {
				os.Remove(path)
				return "", e
			}
			return path, nil
		},
		Restore: func(ctx context.Context, input io.Reader) (string, error) {
			f, e := os.CreateTemp(k.Scratch, "owner-restore-*.zip")
			if e != nil {
				return "", e
			}
			defer os.Remove(f.Name())
			// A bounded deployment archive input; no unbounded upload or live-store
			// replacement. RestoreArchive verifies content and rejects occupied paths.
			free, e := availableSpace(dataDir)
			if e != nil {
				f.Close()
				return "", e
			}
			maxArchive := int64(free / 3)
			n, e := io.CopyBuffer(f, io.LimitReader(input, maxArchive+1), make([]byte, 64<<10))
			if e == nil && n > maxArchive {
				e = errors.New("恢复材料超过本次上传预算")
			}
			if e == nil {
				e = f.Sync()
			}
			closeErr := f.Close()
			if e == nil {
				e = closeErr
			}
			if e != nil {
				return "", e
			}
			if _, e = control.Owner(ctx); e != nil {
				return "", e
			}
			target := filepath.Join(filepath.Dir(dataDir), filepath.Base(dataDir)+"-restored-"+connectionID())
			_, e = boundedstore.RestoreArchive(ctx, f.Name(), target, boundedstore.Options{Budget: k.Budget})
			return target, e
		},
	}
}

func (s controlHTTPServer) BrowserHandler(origin string) http.Handler {
	if s.window == nil {
		return nil
	}
	return s.window.Mount(origin)
}

func (s controlHTTPServer) PublishOwnerEntry(endpoint string) error {
	if s.window == nil || s.dataDir == "" {
		return nil
	}
	state := s.control.State()
	if state.ReadError != nil {
		return state.ReadError
	}
	var handoff *contract.Handoff
	if state.Access != nil {
		handoff = state.Access.Handoff
	}
	return saveOwnerEntry(s.dataDir, s.control.SystemID(), endpoint, handoff)
}
func mountOwnerWindow(machine, browser http.Handler) http.Handler {
	if browser == nil {
		return machine
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, ownerwindow.Prefix) {
			browser.ServeHTTP(w, r)
		} else {
			machine.ServeHTTP(w, r)
		}
	})
}

// The descriptor is public location metadata, not a capability or credential.
// It is recreated after local service restart and follows the library directory.
type ownerEntryDescriptor struct {
	Schema  string             `json:"schema"`
	System  string             `json:"system"`
	Entry   string             `json:"entry"`
	Command []string           `json:"command"`
	Target  *contract.Location `json:"target,omitempty"`
}

func ownerEntry(dataDir, system, endpoint string, handoff *contract.Handoff) ownerEntryDescriptor {
	v := ownerEntryDescriptor{Schema: contract.OwnerViewSchema, System: system, Command: []string{"ownward", "owner-window", "--data-dir", dataDir}}
	if endpoint != "" {
		v.Entry = endpoint + ownerwindow.Prefix
	}
	if handoff != nil && handoff.Phase == "retired" {
		target := handoff.Target
		v.Target, v.Entry = &target, ""
	}
	return v
}

func saveOwnerEntry(dataDir, system, endpoint string, handoff *contract.Handoff) error {
	path := filepath.Join(dataDir, "owner-window.json")
	v := ownerEntry(dataDir, system, endpoint, handoff)
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(dataDir, ".owner-entry-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}

// Location hints are rebuildable metadata, never trust or authentication.
// Validate against the live descriptor / durable handoff before using them.
// Restore and destination activation regenerate them rather than copying an
// old machine's loopback address or device session through an archive.
func discoverOwnerEntry(dataDir, system, endpoint string, handoff *contract.Handoff) (ownerEntryDescriptor, error) {
	expected := ownerEntry(dataDir, system, endpoint, handoff)
	f, e := os.Open(filepath.Join(dataDir, "owner-window.json"))
	if e == nil {
		var saved ownerEntryDescriptor
		e = json.NewDecoder(io.LimitReader(f, 16<<10)).Decode(&saved)
		_ = f.Close()
		if e == nil && reflect.DeepEqual(saved, expected) {
			return saved, nil
		}
	}
	e = saveOwnerEntry(dataDir, system, endpoint, handoff)
	return expected, e
}

func openOwnerWindow(ctx context.Context, dataDir, vectorBundle string, stdout, stderr io.Writer) error {
	// Inspect retired authority before connect-or-start; do not wait 120 seconds
	// trying to reopen a source that already handed its authority to another place.
	if state, e := assembly.ReadControlAtContext(ctx, dataDir); e == nil && state.Access != nil && state.Access.Handoff != nil && state.Access.Handoff.Phase == "retired" {
		entry, e := discoverOwnerEntry(dataDir, state.InformationControl.SystemID, "", state.Access.Handoff)
		if e != nil {
			fmt.Fprintln(stderr, "入口位置提示未能保存；将按资料的实际迁移位置引导。")
		}
		return fmt.Errorf("资料已迁往 %s；请在该部署位置使用物主入口", entry.Target.Endpoint)
	}
	verified, e := assembly.PreflightSharedConnector(assembly.Collaborative, vectorBundle)
	if e != nil {
		return e
	}
	d, e := ensureSharedMCPService(ctx, dataDir, version, verified.Composition, stderr)
	if e != nil {
		return e
	}
	h := hostConnector{descriptor: d}
	var identity struct {
		System string `json:"system_id"`
	}
	if e = h.controlCall(ctx, "identity", "", nil, &identity); e != nil {
		return e
	}
	vault, e := localowner.Default()
	if e != nil {
		return e
	}
	if d.ManagedRoot != "" {
		settings, _, e := loadInstallation(d.ManagedRoot)
		if e != nil {
			return e
		}
		vault = settings.vault()
	}
	credential, e := vault.Load(identity.System, "owner")
	var p contract.Principal
	if e != nil || h.controlCall(ctx, "self", credential, nil, &p) != nil {
		proof, e := vault.Load(ownerRecoveryScope(dataDir), "owner-recovery")
		if e != nil {
			return errors.New("当前系统账户无法验证物主，请使用 recover-owner 恢复入口")
		}
		if e = h.controlCall(ctx, "recover", proof, struct{}{}, &identity); e != nil {
			return e
		}
		credential, e = vault.Load(identity.System, "owner")
		if e != nil {
			return e
		}
	}
	var result struct {
		Entry string `json:"entry"`
	}
	if e = h.controlCall(ctx, "owner-window", credential, struct{}{}, &result); e != nil {
		return e
	}
	return launchOwnerWindow(dataDir, identity.System, d.Endpoint, result.Entry, stdout, stderr, openLocalBrowser)
}

// The authenticated service supplies the entry; location metadata is only a
// rebuildable hint and must never veto an otherwise valid owner entry.
func launchOwnerWindow(dataDir, system, endpoint, entry string, stdout, stderr io.Writer, open func(string) error) error {
	if _, e := discoverOwnerEntry(dataDir, system, endpoint, nil); e != nil {
		fmt.Fprintln(stderr, "入口位置提示未能保存；本次物主入口仍可使用。")
	}
	if e := open(entry); e != nil {
		return fmt.Errorf("无法打开浏览器，请重新运行物主入口: %w", e)
	}
	_, e := fmt.Fprintln(stdout, "物主入口已在本机浏览器打开。")
	return e
}

func openLocalBrowser(address string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", address)
	case "darwin":
		command = exec.Command("open", address)
	default:
		command = exec.Command("xdg-open", address)
	}
	configureSharedServiceProcess(command)
	if e := command.Start(); e != nil {
		return e
	}
	return command.Process.Release()
}

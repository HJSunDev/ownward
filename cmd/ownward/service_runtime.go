package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/adapter/remote"
	"github.com/HJSunDev/ownward/internal/assembly"
	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

type serviceHost struct {
	settings   installation
	identity   remote.Identity
	mu         sync.RWMutex
	runtime    *assembly.Runtime
	handler    http.Handler
	closeLocal func()
	transferMu sync.Mutex
	receiver   receiverState
	open       func() (*assembly.Runtime, error)
	life       context.Context
	jobsMu     sync.Mutex
	jobs       map[string]*migrationJob
}
type migrationJob struct {
	done   chan struct{}
	result receiverState
	err    error
}
type receiverState struct {
	ID       string        `json:"id"`
	Snapshot string        `json:"snapshot,omitempty"`
	Status   string        `json:"status"`
	Permit   *signedPermit `json:"permit,omitempty"`
}
type signedPermit struct {
	Handoff   contract.Handoff `json:"handoff"`
	Signature string           `json:"signature"`
}

func serveInstallation(ctx context.Context, s installation, identity remote.Identity) error {
	return serveInstallationReady(ctx, s, identity, nil)
}

func serveInstallationReady(parent context.Context, s installation, identity remote.Identity, ready func()) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	certificate, err := identity.TLSCertificate()
	if err != nil {
		return err
	}
	h := &serviceHost{settings: s, identity: identity, life: ctx, jobs: map[string]*migrationJob{}}
	h.open = func() (*assembly.Runtime, error) {
		bundle, err := currentVectorBundleDirectory()
		if err != nil {
			return nil, err
		}
		return assembly.Open(assembly.Request{DataDir: s.DataDir, ProductSemantics: assembly.Collaborative, VectorBundleDir: bundle})
	}
	if data, err := s.vault().Load("installation", "receiver"); err == nil {
		if err := json.Unmarshal([]byte(data), &h.receiver); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state, stateErr := authoritysubstrate.ReadControlAt(s.DataDir)
	if stateErr == nil && state.Access != nil && state.Access.Handoff != nil && state.Access.Handoff.Phase == "retired" {
		h.handler = retiredHandler(s.Location, state.Access.Handoff)
	} else if s.Source == nil || (stateErr == nil && state.Access != nil && state.Access.Handoff != nil && state.Access.Handoff.Phase == "active") {
		if err := h.activateRuntime(); err != nil {
			return err
		}
	} else if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
		return stateErr
	}
	defer func() {
		cancel()
		h.jobsMu.Lock()
		jobs := make([]*migrationJob, 0, len(h.jobs))
		for _, job := range h.jobs {
			jobs = append(jobs, job)
		}
		h.jobsMu.Unlock()
		for _, job := range jobs {
			<-job.done
		}
		if h.closeLocal != nil {
			h.closeLocal()
		}
		if h.runtime != nil {
			_ = h.runtime.Close()
		}
	}()
	listener, err := net.Listen("tcp", s.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, ClientAuth: tls.RequestClientCert}}
	if ready != nil {
		ready()
	}
	done := make(chan struct{})
	shutdownDone := make(chan struct{})
	defer func() { close(done); <-shutdownDone; _ = server.Close() }()
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			_ = remote.Shutdown(ctx, server)
		case <-done:
		}
	}()
	err = server.Serve(tls.NewListener(listener, server.TLSConfig))
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func retiredHandler(location contract.Location, h *contract.Handoff) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/remote/identity" && r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(struct {
				Location contract.Location `json:"location"`
				Handoff  *contract.Handoff `json:"handoff"`
			}{location, h})
			return
		}
		http.Error(w, "原位置已退役，请接续新位置", http.StatusGone)
	})
}

func (h *serviceHost) activateRuntime() error {
	r, err := h.open()
	if err != nil {
		return err
	}
	if r.UserControl().SystemID() != h.settings.Location.SystemID {
		r.Close()
		return errors.New("部署身份与资产体系不一致")
	}
	s := controlHTTPServer{server: mcpserver.New(r.Product(), version), control: r.UserControl(), product: r.Management(), kernel: r.Service(), generation: r.OperationGeneration}
	var closeLocal func()
	if h.settings.Name != "" {
		closeLocal, err = startManagedLocal(h.settings, r)
		if err != nil {
			r.Close()
			return err
		}
	}
	r.Management().SetRelatedCleanup(func() error {
		ctx := h.life
		if ctx == nil {
			ctx = context.Background()
		}
		return h.cleanCancelled(ctx, r)
	})
	h.mu.Lock()
	h.runtime = r
	h.closeLocal = closeLocal
	h.handler = remoteHandler(h.settings.Location, s)
	h.mu.Unlock()
	return nil
}

func (h *serviceHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		http.Error(w, "origin not supported", 403)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/remote/receive/") {
		h.receive(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/remote/migration/") {
		h.migrate(w, r)
		return
	}
	h.mu.RLock()
	handler := h.handler
	h.mu.RUnlock()
	if handler == nil {
		http.Error(w, "目的地正在准备，尚未接管", 503)
		return
	}
	handler.ServeHTTP(w, r)
}

func (h *serviceHost) trustedSource(r *http.Request) bool {
	if h.settings.Source == nil || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return false
	}
	digest := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
	return hex.EncodeToString(digest[:]) == h.settings.Source.ServiceID
}
func (h *serviceHost) saveReceiver() error {
	data, err := json.Marshal(h.receiver)
	if err != nil {
		return err
	}
	return h.settings.vault().Save("installation", "receiver", string(data))
}

func handoffSize(dataDir string) (int64, error) {
	var bytes int64
	for _, name := range []string{"assets", "authority", "state"} {
		root := filepath.Join(dataDir, name)
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return errors.New("迁移材料含非普通文件")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			bytes += info.Size()
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return bytes, nil
}

func transferPath(s installation, id string) (string, error) {
	if len(id) < 16 || len(id) > 128 || strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
		return "", errors.New("迁移操作身份无效")
	}
	return filepath.Join(s.Root, "transfers", id), nil
}

func (h *serviceHost) receive(w http.ResponseWriter, r *http.Request) {
	if !h.trustedSource(r) || r.Method != "POST" {
		http.Error(w, "source not authorized", 403)
		return
	}
	h.transferMu.Lock()
	defer h.transferMu.Unlock()
	var err error
	var out any
	action := strings.TrimPrefix(r.URL.Path, "/remote/receive/")
	if action == "archive" {
		id := r.Header.Get("X-Ownward-Transfer")
		snapshot := r.Header.Get("X-Ownward-Snapshot")
		if id != h.receiver.ID || h.receiver.Status == "cancelled" || h.receiver.Status == "active" || len(snapshot) != 64 {
			http.Error(w, "transfer not prepared", 409)
			return
		}
		if h.receiver.Status == "ready" && h.receiver.Snapshot == snapshot {
			_ = json.NewEncoder(w).Encode(h.receiver)
			return
		}
		free, spaceErr := availableSpace(h.settings.Root)
		if spaceErr != nil || r.ContentLength < 0 || uint64(r.ContentLength) > free/3 {
			http.Error(w, "目的地容量不足或迁移长度缺失", 409)
			return
		}
		root, pathErr := transferPath(h.settings, id)
		if pathErr != nil {
			http.Error(w, "invalid transfer", 400)
			return
		}
		if err = os.MkdirAll(root, 0700); err == nil {
			archivePath := filepath.Join(root, "received.zip")
			var f *os.File
			f, err = os.OpenFile(archivePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
			if err == nil {
				hash := sha256.New()
				_, err = io.CopyBuffer(io.MultiWriter(f, hash), r.Body, make([]byte, 64<<10))
				if err == nil {
					err = f.Sync()
				}
				closeErr := f.Close()
				if err == nil {
					err = closeErr
				}
				if err == nil && hex.EncodeToString(hash.Sum(nil)) != snapshot {
					err = errors.New("迁移包完整性检查失败")
				}
			}
			if err == nil {
				stage := filepath.Join(root, "candidate")
				if _, statErr := os.Stat(stage); statErr == nil {
					err = os.RemoveAll(stage)
				}
				var state contract.ControlState
				if err == nil {
					state, err = authoritysubstrate.StageHandoff(archivePath, stage)
					if err == nil {
						err = assembly.ValidateHandoffData(stage)
					}
				}
				if err == nil && (state.InformationControl.SystemID != h.settings.Location.SystemID || state.Access.Handoff.ID != id || state.Access.Handoff.Target != h.settings.Location) {
					err = errors.New("迁移包与确认目的地不匹配")
				}
				if err == nil {
					h.receiver.Snapshot = snapshot
					h.receiver.Status = "ready"
					err = h.saveReceiver()
				}
			}
		}
		out = h.receiver
	} else {
		var in struct {
			ID     string        `json:"id"`
			Permit *signedPermit `json:"permit,omitempty"`
			Bytes  int64         `json:"bytes,omitempty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		root, pathErr := transferPath(h.settings, in.ID)
		if pathErr != nil {
			http.Error(w, "invalid transfer", 400)
			return
		}
		switch action {
		case "prepare":
			free, spaceErr := availableSpace(h.settings.Root)
			if spaceErr != nil || in.Bytes < 0 || uint64(in.Bytes) > free/3 {
				err = errors.New("目的地剩余容量不足")
				break
			}
			if h.receiver.ID != "" && h.receiver.ID != in.ID && h.receiver.Status != "cancelled" {
				err = errors.New("目的地已有另一次交接")
			} else if h.receiver.ID == in.ID && h.receiver.Status == "cancelled" {
				err = errors.New("已取消交接不能恢复")
			} else if h.receiver.ID != in.ID {
				h.receiver = receiverState{ID: in.ID, Status: "prepared"}
				err = h.saveReceiver()
			}
			out = struct {
				Location contract.Location `json:"location"`
				State    receiverState     `json:"state"`
			}{h.settings.Location, h.receiver}
		case "status":
			if h.receiver.ID != in.ID {
				err = errors.New("目的地没有此交接")
			}
			out = h.receiver
		case "cancel":
			if h.receiver.ID != in.ID {
				err = errors.New("目的地没有此交接")
			} else if h.receiver.Status == "active" {
				err = errors.New("目标已接管，不能取消")
			} else {
				h.receiver.Status = "cancelled"
				err = h.saveReceiver()
				if err == nil {
					err = os.RemoveAll(root)
				}
			}
			out = h.receiver
		case "activate":
			if h.receiver.ID != in.ID || in.Permit == nil {
				err = errors.New("接管许可缺失")
				break
			}
			permit := in.Permit
			if err = remote.Verify(*h.settings.Source, permit.Handoff, permit.Signature); err != nil {
				break
			}
			if permit.Handoff.Snapshot != h.receiver.Snapshot || permit.Handoff.Target != h.settings.Location || permit.Handoff.ID != in.ID || h.receiver.Status == "cancelled" {
				err = errors.New("接管许可与已核对快照不一致")
				break
			}
			if h.receiver.Status != "active" {
				stage := filepath.Join(root, "candidate")
				h.receiver.Permit = permit
				err = h.saveReceiver()
				if err != nil {
					break
				}
				if _, statErr := os.Stat(h.settings.DataDir); errors.Is(statErr, os.ErrNotExist) {
					if err = authoritysubstrate.ActivateHandoff(stage, permit.Handoff); err != nil {
						break
					}
					err = os.Rename(stage, h.settings.DataDir)
				} else if statErr != nil {
					err = statErr
				} else {
					state, stateErr := authoritysubstrate.ReadControlAt(h.settings.DataDir)
					if stateErr != nil || state.Access == nil || state.Access.Handoff == nil || state.Access.Handoff.ID != in.ID || state.Access.Handoff.Phase != "active" {
						err = errors.New("目的地已有其他数据")
					}
				}
				if err == nil {
					h.mu.RLock()
					running := h.runtime != nil
					h.mu.RUnlock()
					if !running {
						err = h.activateRuntime()
					}
				}
				if err == nil {
					h.receiver.Status = "active"
					err = h.saveReceiver()
				}
				if err == nil {
					err = os.RemoveAll(root)
				}
			}
			out = h.receiver
		default:
			http.NotFound(w, r)
			return
		}
	}
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (h *serviceHost) migrate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	h.mu.RLock()
	runtime := h.runtime
	h.mu.RUnlock()
	ctx := informationcontrol.Authenticate(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	var in struct {
		ID            string            `json:"id"`
		Target        contract.Location `json:"target"`
		LocationSaved bool              `json:"location_saved"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	var out any
	var err error
	if runtime == nil && r.URL.Path != "/remote/migration/start" {
		http.Error(w, "source not active", 410)
		return
	}
	switch strings.TrimPrefix(r.URL.Path, "/remote/migration/") {
	case "prepare":
		if _, err = runtime.UserControl().Principals(ctx); err != nil {
			break
		}
		if in.Target.ServiceID == h.settings.Location.ServiceID || in.Target.Composition != h.settings.Location.Composition || in.Target.SystemID != h.settings.Location.SystemID {
			err = errors.New("请选择同一发布组合下的独立迁移目的地")
			break
		}
		var client *http.Client
		client, err = remote.Client(in.Target, &h.identity)
		if err != nil {
			break
		}
		var ready struct {
			Location contract.Location `json:"location"`
			State    receiverState     `json:"state"`
		}
		var bytes int64
		bytes, err = handoffSize(h.settings.DataDir)
		if err != nil {
			break
		}
		err = remoteCall(ctx, client, in.Target, "/remote/receive/prepare", "", map[string]any{"id": in.ID, "bytes": bytes}, &ready)
		if err != nil {
			break
		}
		if ready.Location != in.Target {
			err = errors.New("目的地身份发生变化")
			break
		}
		out, err = runtime.UserControl().PrepareHandoff(ctx, in.ID, in.Target)
	case "start":
		var state contract.ControlState
		if runtime != nil {
			state = runtime.UserControl().State()
		} else {
			state, err = authoritysubstrate.ReadControlAt(h.settings.DataDir)
			if err != nil {
				break
			}
		}
		if err = informationcontrol.CheckHandoffManager(ctx, state, in.ID); err != nil {
			break
		}
		h.jobsMu.Lock()
		if h.jobs == nil {
			h.jobs = map[string]*migrationJob{}
		}
		job := h.jobs[in.ID]
		if job == nil {
			job = &migrationJob{done: make(chan struct{})}
			h.jobs[in.ID] = job
			life := h.life
			if life == nil {
				life = context.Background()
			}
			jobCtx := informationcontrol.Authenticate(life, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			go func() {
				if runtime != nil {
					job.result, job.err = h.executeHandoff(jobCtx, runtime, in.ID, in.LocationSaved)
				} else {
					job.result, job.err = h.finishHandoff(jobCtx, nil, *state.Access.Handoff)
				}
				close(job.done)
			}()
		}
		h.jobsMu.Unlock()
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-job.done:
			out, err = job.result, job.err
			if err != nil {
				h.jobsMu.Lock()
				delete(h.jobs, in.ID)
				h.jobsMu.Unlock()
			}
		case <-timer.C:
			out = receiverState{ID: in.ID, Status: "moving"}
		case <-r.Context().Done():
			return
		}
	case "cancel":
		err = runtime.UserControl().CancelHandoff(ctx, in.ID)
		if err == nil {
			err = h.cleanCancelled(ctx, runtime)
		}
		out = map[string]bool{"cancelled": err == nil}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (h *serviceHost) executeHandoff(ctx context.Context, runtime *assembly.Runtime, id string, locationSaved bool) (receiverState, error) {
	h.transferMu.Lock()
	defer h.transferMu.Unlock()
	var result receiverState
	if err := runtime.UserControl().HandoffManager(ctx, id); err != nil {
		return result, err
	}
	state := runtime.UserControl().State()
	var handoff contract.Handoff
	var err error
	if state.Access != nil && state.Access.Handoff != nil && state.Access.Handoff.ID == id && state.Access.Handoff.Phase == "retired" {
		handoff = *state.Access.Handoff
	} else {
		handoff, err = runtime.UserControl().FreezeHandoff(ctx, id, locationSaved)
		if err != nil {
			return result, err
		}
	}
	root, err := transferPath(h.settings, id)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return result, err
	}
	client, err := remote.Client(handoff.Target, &h.identity)
	if err != nil {
		return result, err
	}
	if handoff.Phase != "retired" {
		// Snapshot assembly is local and bounded by accepted work, never network latency.
		stage := filepath.Join(root, "snapshot")
		if _, err := os.Stat(stage); err == nil {
			if err := os.RemoveAll(stage); err != nil {
				return result, err
			}
		}
		if err := os.MkdirAll(stage, 0700); err != nil {
			return result, err
		}
		if err := runtime.ExportHandoff(stage); err != nil {
			return result, err
		}
		archivePath := filepath.Join(root, "snapshot.zip")
		file, err := os.OpenFile(archivePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return result, err
		}
		hash := sha256.New()
		err = authoritysubstrate.WriteHandoffArchive(stage, io.MultiWriter(file, hash))
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return result, err
		}
		if closeErr != nil {
			return result, closeErr
		}
		snapshot := hex.EncodeToString(hash.Sum(nil))
		current := runtime.UserControl().State()
		if current.Revision != handoff.Revision {
			return result, errors.New("迁移已被用户控制决定中止")
		}
		file, err = os.Open(archivePath)
		if err != nil {
			return result, err
		}
		uploadCtx, stopUpload := context.WithCancel(ctx)
		defer stopUpload()
		watchDone := make(chan struct{})
		defer close(watchDone)
		go func() {
			for {
				changed := runtime.UserControl().Changed()
				state := runtime.UserControl().State()
				if state.Access == nil || state.Access.Handoff == nil || state.Access.Handoff.ID != id || state.Access.Handoff.Phase != "frozen" {
					stopUpload()
					return
				}
				select {
				case <-changed:
				case <-watchDone:
					return
				case <-uploadCtx.Done():
					return
				}
			}
		}()
		request, err := http.NewRequestWithContext(uploadCtx, "POST", strings.TrimRight(handoff.Target.Endpoint, "/")+"/remote/receive/archive", file)
		if err != nil {
			file.Close()
			return result, err
		}
		request.Header.Set("X-Ownward-Transfer", id)
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return result, err
		}
		request.ContentLength = info.Size()
		request.Header.Set("X-Ownward-Snapshot", snapshot)
		streamClient := *client
		streamClient.Timeout = 0
		response, err := streamClient.Do(request)
		file.Close()
		if err != nil {
			return result, errors.New("迁移传输未完成，源保持冻结，可接续或取消")
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return result, errors.New("目的地尚未完整接收快照")
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			return result, err
		}
		if result.Snapshot != snapshot || result.Status != "ready" {
			return result, errors.New("目的地尚未就绪")
		}
		handoff, err = runtime.UserControl().RetireHandoff(ctx, id, snapshot, handoff.Revision)
		if err != nil {
			return result, err
		}
	}
	return h.finishHandoff(ctx, runtime, handoff)
}

func (h *serviceHost) finishHandoff(ctx context.Context, runtime *assembly.Runtime, handoff contract.Handoff) (receiverState, error) {
	var result receiverState
	id := handoff.ID
	root, err := transferPath(h.settings, id)
	if err != nil {
		return result, err
	}
	client, err := remote.Client(handoff.Target, &h.identity)
	if err != nil {
		return result, err
	}
	signature, err := h.identity.Sign(handoff)
	if err != nil {
		return result, err
	}
	permit := signedPermit{Handoff: handoff, Signature: signature}
	if err := remoteCall(ctx, client, handoff.Target, "/remote/receive/activate", "", map[string]any{"id": id, "permit": permit}, &result); err != nil {
		return result, errors.New("源已退役，接管结果尚待核对；须接续目标，不能重启旧源为活动体系")
	}
	if result.Status != "active" {
		return result, errors.New("目标尚未接管")
	}
	if runtime != nil {
		if err := runtime.Close(); err != nil {
			return result, err
		}
	}
	h.mu.Lock()
	h.runtime = nil
	closeLocal := h.closeLocal
	h.closeLocal = nil
	h.handler = retiredHandler(h.settings.Location, &handoff)
	h.mu.Unlock()
	if closeLocal != nil {
		closeLocal()
	}
	for _, path := range []string{filepath.Join(h.settings.DataDir, "assets"), filepath.Join(h.settings.DataDir, "state"), root} {
		if err := os.RemoveAll(path); err != nil {
			return result, fmt.Errorf("已接管，源副本清理未完成: %w", err)
		}
	}
	return result, nil
}

func (h *serviceHost) cleanCancelled(ctx context.Context, runtime *assembly.Runtime) error {
	state := runtime.UserControl().State()
	if state.Access == nil {
		return nil
	}
	for _, cancelled := range state.Access.Cancelled {
		if cancelled.Cleaned {
			continue
		}
		client, err := remote.Client(cancelled.Target, &h.identity)
		if err != nil {
			return err
		}
		client.Timeout = 5 * time.Second
		var out receiverState
		if err := remoteCall(ctx, client, cancelled.Target, "/remote/receive/cancel", "", map[string]string{"id": cancelled.ID}, &out); err != nil {
			return errors.New("用户控制已生效，目的地副本清理等待连接恢复")
		}
		root, err := transferPath(h.settings, cancelled.ID)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(root); err != nil {
			return err
		}
		if err := runtime.UserControl().MarkHandoffClean(cancelled.ID); err != nil {
			return err
		}
	}
	return nil
}

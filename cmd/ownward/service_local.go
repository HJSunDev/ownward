package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/adapter/remote"
	"github.com/HJSunDev/ownward/internal/assembly"
)

func startManagedLocal(s installation, r *assembly.Runtime) (func(), error) {
	identity, dataIdentity, err := sharedMCPIdentity(s.DataDir, version, r.Composition().Composition)
	if err != nil {
		return nil, err
	}
	proof, err := s.vault().Load(ownerRecoveryScope(s.DataDir), "owner-recovery")
	if err != nil {
		return nil, err
	}
	secured := controlHTTPServer{server: mcpserver.New(r.Product(), version), control: r.UserControl(), product: r.Management(), kernel: r.Service(), generation: r.OperationGeneration, vault: s.vault(), recovery: proof}
	if err := s.vault().Save(ownerRecoveryScope(s.DataDir), "owner-recovery", secured.recovery); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	d := &sharedMCPDescriptor{Schema: "ownward.shared-mcp/v1", PID: os.Getpid(), Endpoint: "http://" + listener.Addr().String(), BearerToken: connectionID(), ServiceIdentity: identity, DataIdentity: dataIdentity, StartedAt: time.Now().UTC().Format(time.RFC3339), ManagedRoot: s.Root}
	base := secured.HTTPHandler()
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == sharedMCPStatusPath {
			_ = json.NewEncoder(w).Encode(map[string]string{"service_identity": identity})
			return
		}
		if request.URL.Path == sharedMCPShutdownPath {
			http.Error(w, "由系统服务管理停止与更新", 403)
			return
		}
		base.ServeHTTP(w, request)
	})
	server := &http.Server{Handler: bearerTokenHandler(handler, d.BearerToken), ReadHeaderTimeout: 5 * time.Second}
	path := filepath.Join(s.DataDir, "runtime", "mcp-service.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		listener.Close()
		return nil, err
	}
	if err := atomicWriteSharedMCPDescriptor(path, d); err != nil {
		listener.Close()
		return nil, err
	}
	go server.Serve(listener)
	return func() { _ = remote.Shutdown(context.Background(), server); _ = os.Remove(path) }, nil
}

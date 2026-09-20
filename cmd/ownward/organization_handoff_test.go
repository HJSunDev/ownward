package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/adapter/remote"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOrganizationBackgroundFollowsTrustedHandoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var nextSelf atomic.Int32
	var oldSelf atomic.Int32
	next := httptest.NewUnstartedServer(nil)
	id, e := remote.NewIdentity("https://"+next.Listener.Addr().String(), "fixture")
	if e != nil {
		t.Fatal(e)
	}
	id.Location.SystemID = "fixture"
	cert, e := id.TLSCertificate()
	if e != nil {
		t.Fatal(e)
	}
	next.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	service := mcp.NewServer(&mcp.Implementation{Name: "review-successor"}, nil)
	capabilities := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return service }, nil)
	principal := contract.Principal{ID: "p", Revision: 1, Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission}}
	next.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/remote/identity":
			json.NewEncoder(w).Encode(remoteIdentity{Location: id.Location, Handoff: &contract.Handoff{Phase: "active", Target: id.Location}})
		case controlPrefix + "self":
			nextSelf.Add(1)
			json.NewEncoder(w).Encode(principal)
		default:
			capabilities.ServeHTTP(w, r)
		}
	})
	next.StartTLS()
	defer next.Close()
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == controlPrefix+"self" {
			oldSelf.Add(1)
		}
		http.Error(w, "old service offline after trusted handoff", 503)
	}))
	defer old.Close()
	oldLocation := contract.Location{SystemID: "fixture", ServiceID: "old", Endpoint: old.URL}
	h := &hostConnector{system: "fixture", profile: "review", vault: localowner.Vault{Root: t.TempDir()}, descriptor: &sharedMCPDescriptor{Endpoint: old.URL + "/capabilities"}, remote: &remoteConnection{Client: &http.Client{Transport: http.DefaultTransport}, Material: connectionMaterial{Location: oldLocation}}, record: connectorRecord{Credential: "synthetic", Principal: "p", Connected: true, NextLocation: &id.Location}}
	// Non-nil old client is required by refreshRemote's normal replacement cleanup.
	oldMCP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcp.NewServer(&mcp.Implementation{Name: "review-old"}, nil) }, nil))
	defer oldMCP.Close()
	session, e := mcp.NewClient(&mcp.Implementation{Name: "review"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: oldMCP.URL, DisableStandaloneSSE: true}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { session.Close() }()
	j, e := openOrganizationJournal(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer j.close()
	loopCtx, stop := context.WithCancel(ctx)
	o := &organizationHost{host: h, ctx: loopCtx, cancel: stop, journal: j, root: t.TempDir(), active: map[string]bool{}, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	o.call = func(c context.Context, name string, args any) (*mcp.CallToolResult, error) {
		return h.remoteOrganizationCall(c, &session, name, args)
	}
	o.refreshRoute = func(c context.Context) error { return h.refreshOrganizationRoute(c, &session) }
	go o.loop()
	deadline := time.NewTimer(300 * time.Millisecond)
	<-deadline.C
	stop()
	<-o.done
	automatic := nextSelf.Load()
	if e = h.refreshRemote(ctx, &session); e != nil {
		t.Fatal("trusted successor control failed", e)
	}
	var self contract.Principal
	if e = h.controlCall(ctx, "self", h.credential(), nil, &self); e != nil || self.ID != "p" {
		t.Fatal("successor authorization unavailable", e, self)
	}
	t.Logf("autonomous loop old self attempts=%d successor self attempts=%d; existing trusted refresh then self succeeds", oldSelf.Load(), automatic)
	if automatic == 0 {
		t.Error("loop checks unavailable old authority before reaching trusted route refresh; pending work cannot automatically follow handoff")
	}
}

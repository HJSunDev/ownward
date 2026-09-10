package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/adapter/remote"
	"github.com/HJSunDev/ownward/internal/assembly"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestInstalledRuntimeTransfersAndResumesWithoutAnotherAuthority(t *testing.T) {
	binary, working, bundle := buildIsolatedRelease(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sourceRoot, targetRoot := t.TempDir(), t.TempDir()
	data := filepath.Join(sourceRoot, "data")
	runtime, err := assembly.Open(assembly.Request{DataDir: data, ProductSemantics: assembly.Collaborative, VectorBundleDir: bundle})
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, err := runtime.UserControl().InitializeOwner("owner")
	if err != nil {
		t.Fatal(err)
	}
	owner := informationcontrol.Authenticate(ctx, ownerToken)
	composition := runtime.Composition().Composition
	system := runtime.UserControl().SystemID()
	value, err := runtime.Product().Create(owner, contract.CreateInput{Content: "same evidence after moving"})
	if err != nil {
		t.Fatal(err)
	}
	workBefore, err := runtime.Product().SemanticWork(owner, 10)
	if err != nil {
		t.Fatal(err)
	}
	direct := time.Now()
	for i := 0; i < 10000; i++ {
		if _, err := runtime.Product().Read(owner, value.Information.ID); err != nil {
			t.Fatal(err)
		}
	}
	localMean := time.Since(direct) / 10000
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}

	makeInstallation := func(root string, source *contract.Location) (installation, remote.Identity) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := listener.Addr().String()
		listener.Close()
		identity, err := remote.NewIdentity("https://"+addr, composition)
		if err != nil {
			t.Fatal(err)
		}
		identity.Location.SystemID = system
		s := installation{Root: root, DataDir: filepath.Join(root, "data"), Listen: addr, Name: "isolated-test", Location: identity.Location, Source: source}
		encoded, _ := json.Marshal(identity)
		if err := s.vault().Save("installation", "identity", string(encoded)); err != nil {
			t.Fatal(err)
		}
		if err := s.vault().Save(ownerRecoveryScope(s.DataDir), "owner-recovery", connectionID()); err != nil {
			t.Fatal(err)
		}
		if err := s.save(); err != nil {
			t.Fatal(err)
		}
		return s, identity
	}
	source, sourceIdentity := makeInstallation(sourceRoot, nil)
	target, _ := makeInstallation(targetRoot, &source.Location)
	start := func(s installation) func() {
		logfile, err := os.Create(filepath.Join(s.Root, "process.log"))
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, binary, "service-run", "--data-dir", s.Root)
		command.Dir = working
		command.Stdout = logfile
		command.Stderr = logfile
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		stopped := false
		stop := func() {
			if !stopped {
				stopped = true
				_ = command.Process.Kill()
				_ = command.Wait()
				logfile.Close()
			}
		}
		t.Cleanup(stop)
		client, err := remote.Client(s.Location, nil)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			requestErr := remoteCall(ctx, client, s.Location, "/remote/identity", "", nil, nil)
			// Waiting target is reachable but cannot serve data.
			if requestErr == nil {
				return stop
			}
			if ce, ok := requestErr.(*controlError); ok && ce.Status == 503 {
				return stop
			}
			time.Sleep(50 * time.Millisecond)
		}
		stop()
		log, _ := os.ReadFile(filepath.Join(s.Root, "process.log"))
		t.Fatalf("service did not start: %s", log)
		return stop
	}
	stopSource := start(source)
	stopTarget := start(target)
	client, err := remote.Client(source.Location, nil)
	if err != nil {
		t.Fatal(err)
	}
	openSession := func(location contract.Location, token string) *mcp.ClientSession {
		c, err := remote.Client(location, nil)
		if err != nil {
			t.Fatal(err)
		}
		c.Transport = remoteBearer{base: c.Transport, credential: func() string { return token }}
		agent := mcp.NewClient(&mcp.Implementation{Name: "migration-test"}, nil)
		session, err := agent.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: location.Endpoint + "/capabilities", HTTPClient: c, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	setup := time.Now()
	session := openSession(source.Location, ownerToken)
	setupTime := time.Since(setup)
	defer func() { session.Close() }()
	read := &mcp.CallToolParams{Name: "ownward_read", Arguments: map[string]any{"id": value.Information.ID}}
	remoteStart := time.Now()
	for i := 0; i < 20; i++ {
		out, err := session.CallTool(ctx, read)
		if err != nil || out.IsError {
			t.Fatal("remote read", out, err)
		}
	}
	remoteMean := time.Since(remoteStart) / 20
	mutation := &mcp.CallToolParams{Name: "ownward_create", Meta: mcp.Meta{"ownward/operation": "durable-operation", "ownward/generation": 1}, Arguments: map[string]any{"content": "one creation across locations"}}
	first, err := session.CallTool(ctx, mutation)
	if err != nil || first.IsError {
		t.Fatal(first, err)
	}
	var created mcpserver.CreateOutput
	if err := decodeTool(first, &created); err != nil {
		t.Fatal(err)
	}
	vault := localowner.Vault{Root: t.TempDir()}
	host, err := newRemoteHost(ctx, connectionMaterial{Location: source.Location, EnrollmentID: "same-host"}, vault)
	if err != nil {
		t.Fatal(err)
	}
	host.record.Credential = ownerToken
	host.record.NextLocation = &target.Location
	if err := host.save(); err != nil {
		t.Fatal(err)
	}

	// A cancelled candidate cannot be prepared or activated by a late request.
	peer, err := remote.Client(target.Location, &sourceIdentity)
	if err != nil {
		t.Fatal(err)
	}
	cancelledID := connectionID()
	for _, action := range []string{"prepare", "cancel"} {
		if err := remoteCall(ctx, peer, target.Location, "/remote/receive/"+action, "", map[string]string{"id": cancelledID}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if remoteCall(ctx, peer, target.Location, "/remote/receive/prepare", "", map[string]string{"id": cancelledID}, nil) == nil {
		t.Fatal("cancelled operation resumed")
	}
	untrusted, _ := remote.Client(target.Location, nil)
	if remoteCall(ctx, untrusted, target.Location, "/remote/receive/prepare", "", map[string]string{"id": connectionID()}, nil) == nil {
		t.Fatal("unbound source could transfer")
	}
	id := connectionID()
	var handoff contract.Handoff
	if err := remoteCall(ctx, client, source.Location, "/remote/migration/prepare", ownerToken, map[string]any{"id": id, "target": target.Location}, &handoff); err != nil {
		t.Fatal(err)
	}
	moving := time.Now()
	var result receiverState
	for {
		err = remoteCall(ctx, client, source.Location, "/remote/migration/start", ownerToken, map[string]any{"id": id, "location_saved": true}, &result)
		if err != nil {
			t.Fatal("handoff:", err)
		}
		if result.Status == "active" {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
	}
	migrationTime := time.Since(moving)
	if err := host.refreshRemote(ctx, &session); err != nil {
		t.Fatal(err)
	}
	if host.remote.Material.Location != target.Location {
		t.Fatal("connector did not switch")
	}
	replay, err := session.CallTool(ctx, mutation)
	if err != nil || replay.IsError {
		t.Fatal("migrated replay", replay, err)
	}
	var repeated mcpserver.CreateOutput
	if err := decodeTool(replay, &repeated); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(created)
	b, _ := json.Marshal(repeated)
	if string(a) != string(b) {
		t.Fatal("migration repeated creation")
	}
	out, err := session.CallTool(ctx, read)
	if err != nil || out.IsError {
		t.Fatal(out, err)
	}

	// Crash/restart each process; only target may reopen the business authority.
	stopSource()
	stopSource = start(source)
	defer stopSource()
	if err := remoteCall(ctx, client, source.Location, "/control/self", ownerToken, nil, nil); err == nil {
		t.Fatal("retired source accepted data access")
	}
	stopTarget()
	stopTarget = start(target)
	defer stopTarget()
	restarted := time.Now()
	if err := host.reconnectRemote(ctx, &session); err != nil {
		t.Fatal(err)
	}
	out, err = session.CallTool(ctx, read)
	if err != nil || out.IsError {
		t.Fatal("target restart", out, err)
	}
	reconnectTime := time.Since(restarted)
	reloaded, err := newRemoteHost(ctx, connectionMaterial{Location: source.Location, EnrollmentID: "same-host"}, vault)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.credential() != ownerToken || reloaded.remote.Material.Location != target.Location {
		t.Fatal("lost original identity or trusted handoff")
	}
	// Compare retained semantic work directly after orderly process removal.
	session.Close()
	stopTarget()
	moved, err := assembly.Open(assembly.Request{DataDir: target.DataDir, ProductSemantics: assembly.Collaborative, VectorBundleDir: bundle})
	if err != nil {
		t.Fatal(err)
	}
	defer moved.Close()
	workAfter, err := moved.Product().SemanticWork(owner, 10)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(workBefore)
	after, _ := json.Marshal(workAfter)
	if len(workAfter) < len(workBefore) {
		t.Fatalf("semantic work lost: %s / %s", before, after)
	}
	if _, err := os.Stat(filepath.Join(source.DataDir, "assets")); !os.IsNotExist(err) {
		t.Fatal("source retained asset copy")
	}
	t.Logf("local mean=%s; remote mean=%s; connection=%s; migration=%s; reconnect and read=%s; model calls=0", localMean, remoteMean, setupTime, migrationTime, reconnectTime)
}

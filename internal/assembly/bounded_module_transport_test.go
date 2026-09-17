package assembly

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/rpcstream"
)

func TestBoundedForgetCleansTransportCopyWithoutDeletingOtherSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, e := openBoundedWith(Request{DataDir: t.TempDir(), ProductSemantics: Basic}, testManifest(t, Basic), productionResources)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	token, e := r.UserControl().InitializeOwner("test owner")
	if e != nil {
		t.Fatal(e)
	}
	ctx = informationcontrol.Authenticate(ctx, token)
	a, e := r.Product().Create(ctx, contract.CreateInput{Content: strings.Repeat("forgotten original ", 1000)})
	if e != nil {
		t.Fatal(e)
	}
	b, e := r.Product().Create(ctx, contract.CreateInput{Content: strings.Repeat("unrelated original ", 1000)})
	if e != nil {
		t.Fatal(e)
	}
	read := func(id string) *contract.StreamResult {
		args, _ := json.Marshal(map[string]string{"id": id})
		v, e := r.Streaming().ExecuteStream(ctx, contract.StreamRequest{Operation: "ownward_read", Arguments: boundedstore.StringSource(args)})
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	x, y := read(a.Information.ID), read(b.Information.ID)
	defer x.Close()
	defer y.Close()
	budget, _ := resourcebudget.New(2*resourcebudget.MiB, 0)
	scope := rpcstream.New(t.TempDir(), budget, 4*resourcebudget.MiB)
	defer scope.Close()
	args, _ := json.Marshal(map[string]string{"id": a.Information.ID})
	envelope := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ownward_read","arguments":` + string(args) + `}}`
	_, call, e := scope.Project(ctx, strings.NewReader(envelope))
	if e != nil {
		t.Fatal(e)
	}
	result, e := call.Result(ctx, func(w io.Writer) error {
		v, e := x.Value.Open(ctx)
		if e != nil {
			return e
		}
		defer v.Close()
		_, e = io.Copy(w, v)
		return e
	}, func() error { return x.Check(ctx) }, x.Retain)
	if e != nil {
		t.Fatal(e)
	}
	// The core result closes as in the real MCP adapter; the transport retains
	// independent ownership until network delivery or invalidation.
	x.Close()
	if scope.Disk.Used() < 10000 {
		t.Fatal("transport fixture not retained")
	}
	_, e = r.Management().Manage(ctx, contract.ManagementRequest{ID: "forget-copy", Operation: "forget", Targets: []contract.AssetVersion{{ID: a.Information.ID, Revision: 1}}})
	if e != nil {
		t.Fatal(e)
	}
	for {
		receipt, e := r.Management().Receipt(ctx, "forget-copy")
		if e != nil {
			t.Fatal(e)
		}
		if receipt.Status == "completed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond * 10):
		}
	}
	if scope.Disk.Used() != 0 {
		t.Fatal("forgotten transport bytes remain", scope.Disk.Used())
	}
	// Forgetting changes the control epoch, invalidating earlier delivery leases.
	// A fresh read of an unrelated source must remain available and exact.
	other, e := r.Product().Read(ctx, b.Information.ID)
	if e != nil || other.Content != b.Information.Content {
		t.Fatal("unrelated source unavailable", e)
	}
	response, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": result})
	var output bytes.Buffer
	if e = call.Expand(ctx, &output, response); e == nil || output.Len() != 0 {
		t.Fatal("invalid transport copy delivered", e)
	}
}

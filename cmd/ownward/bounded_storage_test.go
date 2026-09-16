package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 资源验收以独立进程运行同一生产实现，测量器不计入产品进程树。
// 无环境参数时跳过，不下载模型、不触发外部智能调用。
func TestBoundedStorageProcess(t *testing.T) {
	role := os.Getenv("OWNWARD_BOUND_ROLE")
	if role == "" {
		t.Skip("仅用于带本地制品的单元一资源验收")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := os.Getenv("OWNWARD_BOUND_DIR")
	if role == "service" {
		budget, err := resourcebudget.New(12*resourcebudget.MiB, resourcebudget.MiB)
		check(err)
		store, err := boundedstore.Open(ctx, filepath.Join(dir, "ownward.sqlite"), boundedstore.Options{Budget: budget})
		check(err)
		defer store.Close()
		hash := sha256.Sum256([]byte("bounded-local-acceptance"))
		check(store.PublishAccess(ctx, boundedstore.AccessHeader{System: "bounded-system", Revision: 1}, 0, []contract.Principal{{ID: "bounded-principal", Revision: 1, CredentialDigest: hex.EncodeToString(hash[:]), Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission}}}))
		model, err := embedding.OpenManaged(os.Getenv("OWNWARD_BOUND_BUNDLE"))
		check(err)
		defer model.Close()
		product := &core.StreamingAssets{Store: store, Budget: budget, Scratch: dir, DiskBytes: 256 * resourcebudget.MiB}
		if os.Getenv("OWNWARD_BOUND_RETRIEVAL") == "1" {
			product.Embedder = model
		}
		server := mcpserver.NewStreamingStorage(product, "unit-one", dir, budget, 256*resourcebudget.MiB)
		mux := http.NewServeMux()
		mux.Handle("/capabilities", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			server.HTTPHandler().ServeHTTP(w, r.WithContext(informationcontrol.Authenticate(r.Context(), r.Header.Get("X-Ownward-Principal"))))
		}))
		mux.HandleFunc("/vectors", func(w http.ResponseWriter, r *http.Request) {
			for _, query := range []string{"Which source explains the project conditions?", strings.Repeat("reason ", 450)} {
				if _, err := model.EmbedQuery(r.Context(), query); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
			}
			batch := make([]string, 32)
			for i := range batch {
				batch[i] = fmt.Sprintf("fact %02d", i)
			}
			if _, err := model.EmbedDocuments(r.Context(), batch); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			fmt.Fprint(w, "ok")
		})
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		check(err)
		h := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go h.Serve(listener)
		defer h.Close()
		check(os.WriteFile(filepath.Join(dir, "endpoint"), []byte("http://"+listener.Addr().String()), 0600))
		var one [1]byte
		_, _ = os.Stdin.Read(one[:])
		return
	}
	if role != "connector" {
		t.Fatal("unknown process role")
	}
	transport := &connectorTransport{base: boundedTestAuth{}}
	client := mcp.NewClient(&mcp.Implementation{Name: "ownward-connector", Version: "unit-one"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: os.Getenv("OWNWARD_BOUND_ENDPOINT") + "/capabilities", HTTPClient: &http.Client{Transport: transport}, DisableStandaloneSSE: true}, nil)
	check(err)
	defer session.Close()
	scope, err := configureStreamingConnector(transport, session.InitializeResult(), dir)
	check(err)
	if scope == nil {
		t.Fatal("bounded negotiation missing")
	}
	proxy := mcp.NewServer(&mcp.Implementation{Name: "ownward", Version: "unit-one"}, nil)
	for tool, err := range session.Tools(ctx, nil) {
		check(err)
		copy := *tool
		proxy.AddTool(&copy, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var args json.RawMessage = r.Params.Arguments
			if r.Params.Name == "ownward_create" || r.Params.Name == "ownward_create_batch" || r.Params.Name == "ownward_update" {
				if _, err := requestMutationDigest(ctx, r.Params.Name, args); err != nil {
					return nil, err
				}
			}
			return session.CallTool(ctx, &mcp.CallToolParams{Name: r.Params.Name, Arguments: args, Meta: r.Params.Meta})
		})
	}
	check(runConnectorIO(ctx, proxy, scope, os.Stdin, os.Stdout))
}

type boundedTestAuth struct{}

func (boundedTestAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-Ownward-Principal", "bounded-local-acceptance")
	return http.DefaultTransport.RoundTrip(r)
}

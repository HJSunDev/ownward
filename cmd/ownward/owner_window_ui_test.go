package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/ownerwindow"
	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/semantics"
)

type ownerBrowserFaults struct {
	mu              sync.Mutex
	flags           map[string]bool
	counts          map[string]int
	internalSession string
}

func (b *ownerBrowserFaults) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		internalSession := b.internalSession
		b.mu.Unlock()
		if !strings.Contains(r.URL.Path, ownerwindow.Prefix+"v1/") || r.Header.Get("Authorization") == "Bearer "+internalSession {
			next.ServeHTTP(w, r)
			return
		}
		kind := strings.TrimPrefix(r.URL.Path, ownerwindow.Prefix+"v1/")
		if kind == "query" || kind == "action" {
			raw, _ := io.ReadAll(io.LimitReader(r.Body, contract.OwnerRequestBytes+1))
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var q struct{ View, Action string }
			_ = json.Unmarshal(raw, &q)
			kind += "/" + q.View + q.Action
		}
		b.mu.Lock()
		b.counts[kind]++
		fail := b.flags["offline"] || b.flags["uploads"] && kind == "draft-text" || b.flags["draft-reads"] && kind == "query/draft_content" || b.flags["receipts"] && kind == "query/publish_receipt" || b.flags["pending"] && kind == "query/pending"
		auth := b.flags["auth"] || b.flags["logout-auth"] && kind == "logout"
		delay := b.flags["delay-source"] && kind == "query/source" || b.flags["delay-rebase"] && kind == "action/create_draft" || b.flags["delay-discard"] && kind == "action/discard_draft"
		if b.flags["resolve-once"] && kind == "query/resolve" {
			fail = true
			delete(b.flags, "resolve-once")
		}
		drop := b.flags["publish-reply"] && kind == "action/publish_draft" || b.flags["discard-reply"] && kind == "action/discard_draft"
		if drop {
			delete(b.flags, "publish-reply")
			delete(b.flags, "discard-reply")
		}
		b.mu.Unlock()
		if delay {
			time.Sleep(3 * time.Second)
		}
		if auth {
			http.Error(w, "fixture expired session", 401)
			return
		}
		if fail {
			http.Error(w, "fixture disconnected", 503)
			return
		}
		if drop {
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, r)
			http.Error(w, "fixture lost reply", 503)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Opt-in fixture: real browser transport and MCP, synthetic data only. The
// bounded file mailbox is test-only and never linked into the product binary.
func TestOwnerWindowUnitThreeBrowser(t *testing.T) {
	path := os.Getenv("OWNWARD_UI_FIXTURE")
	if path == "" {
		t.Skip("opt-in real-browser acceptance")
	}
	faults := &ownerBrowserFaults{flags: map[string]bool{}, counts: map[string]int{}}
	f := ownerHTTPWithHandler(t, faults.wrap)
	faults.mu.Lock()
	faults.internalSession = f.session
	faults.mu.Unlock()
	principal, _, agent := f.agent(t, "写作伙伴")
	f.agent(t, "写作伙伴")
	count := 28
	if n, e := strconv.Atoi(os.Getenv("OWNWARD_UI_ASSETS")); e == nil && n > 0 && n <= 10000 {
		count = n
	}
	titles := []string{"关于留白的工作笔记", "一次有头有尾的积累", "阅读，是与过去的自己重逢", "把事情讲清楚，比讲复杂更难"}
	var ids []string
	for i := 0; i < count; i++ {
		body := titles[i%len(titles)] + "\n\n" + "想法可以零散地发生，但值得被完整地留下。每一次回看，都是重新建立联系的机会。\n\n" + "从原文出发，核对来源，再继续自己的文字。" + strconv.Itoa(i)
		source := domain.Source{Actor: "我保存的阅读笔记", Ref: "读书记录 · 第三章"}
		if i == 0 {
			source = domain.Source{Actor: "ownward:owner"}
		}
		a, e := f.k.Create(f.ctx, contract.CreateInput{Content: body, Source: source})
		if e != nil {
			t.Fatal(e)
		}
		ids = append(ids, a.Information.ID)
	}
	if e := f.s.CreateGeneration(f.ctx, "browser-organized", "fixture-space"); e != nil {
		t.Fatal(e)
	}
	for i, id := range ids {
		r := derived.Record{AssetID: id, AssetRevision: 1, Status: "ready", InputsKnown: true, Analysis: semantics.Analysis{Summary: "synthetic fixture"}}
		if i == 0 {
			r.Analysis.Organization = &semantics.Organization{Schema: semantics.OrganizationSchema, Snapshot: "fixture", Links: []semantics.GroundedLink{{ID: "related", Type: "supports", Meaning: "两篇笔记都强调保留原文，让后来形成的判断有据可查。", Source: semantics.GraphEndpoint{AssetID: id, Revision: 1}, Target: semantics.GraphEndpoint{AssetID: ids[1], Revision: 1}}}}
		}
		b, _ := json.Marshal(r)
		v, e := f.s.StageOrganization(f.ctx, "browser-organized", boundedstore.StringSource(b))
		if e != nil {
			t.Fatal(e)
		}
		if e = f.s.PublishOrganization(f.ctx, v); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.s.ActivateGeneration(f.ctx, "browser-organized", ""); e != nil {
		t.Fatal(e)
	}
	f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("给未来自己的几句话\n\n今天的想法，先在这里慢慢成形。")})
	f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("一个值得继续的问题\n\n我们如何让积累真正变成自己的东西？")})
	write := func(file string, value any) {
		b, _ := json.Marshal(value)
		if e := os.WriteFile(file, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	entry := func() string { return f.server.URL + ownerwindow.Prefix + "#" + f.bootstrap(t) }
	write(path, map[string]any{"entry": entry(), "origin": f.server.URL, "assets": count})
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(30 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser acceptance deadline reached")
		case <-ticker.C:
			if _, e := os.Stat(path + ".stop"); e == nil {
				return
			}
			b, e := os.ReadFile(path + ".command")
			if e != nil {
				continue
			}
			if e = os.Remove(path + ".command"); e != nil {
				t.Fatal(e)
			}
			var command struct {
				Action, Reference, Draft, Grant, Text string
				Flags                                 map[string]bool
			}
			if e = json.Unmarshal(b, &command); e != nil {
				t.Fatal(e)
			}
			out := map[string]any{"ok": true}
			switch command.Action {
			case "faults":
				faults.mu.Lock()
				faults.flags = command.Flags
				faults.mu.Unlock()
			case "snapshot":
				out["drafts"] = f.freshQuery(t, contract.OwnerQuery{View: "drafts", Limit: 100}).Drafts
				out["assets"] = f.freshQuery(t, contract.OwnerQuery{View: "assets", Limit: 12}).Assets
				faults.mu.Lock()
				copyCounts := map[string]int{}
				for k, v := range faults.counts {
					copyCounts[k] = v
				}
				faults.mu.Unlock()
				out["browser_requests"] = copyCounts
			case "export":
				archive, e := f.w.Archives.Backup(f.ctx)
				if e != nil {
					t.Fatal(e)
				}
				b, e := os.ReadFile(archive)
				if e != nil {
					t.Fatal(e)
				}
				dest := filepath.Join(filepath.Dir(path), "browser-backup.ownward")
				if e = os.WriteFile(dest, b, 0600); e != nil {
					t.Fatal(e)
				}
				os.Remove(archive)
				out["file"] = dest
			case "entry":
				out["entry"] = entry()
			case "pending":
				if _, e = f.c.Invite(f.ctx, "browser-new-device"); e != nil {
					t.Fatal(e)
				}
				if _, e = f.c.Join("browser-new-device", strings.Repeat("synthetic-proof", 4), "新的阅读伙伴", []contract.Permission{contract.ReadPermission}); e != nil {
					t.Fatal(e)
				}
			case "agent_append", "agent_latest":
				if command.Action == "agent_latest" {
					grants, _, e := f.s.OwnerGrants(f.ctx, "", 100)
					if e != nil {
						t.Fatal(e)
					}
					for _, g := range grants {
						if g.Principal == principal.ID {
							command.Draft = g.DraftID
							command.Grant = g.ID
							break
						}
					}
				}
				var read contract.AgentDraftResult
				if e = decodeTool(hostCall(t, agent, "ownward_draft_work", contract.AgentDraftRequest{Action: "read", Draft: command.Draft, Grant: command.Grant}, true), &read); e != nil {
					t.Fatal(e)
				}
				hostCall(t, agent, "ownward_draft_work", contract.AgentDraftRequest{Action: "append", Draft: command.Draft, Grant: command.Grant, Handle: read.Handle, Text: command.Text}, true)
			case "external_draft":
				p := f.freshQuery(t, contract.OwnerQuery{View: "resolve", Reference: command.Reference})
				f.act(t, contract.OwnerAction{Action: "replace_draft", Handle: p.Drafts[0].Handle, Text: &command.Text})
			case "external_asset":
				p := f.freshQuery(t, contract.OwnerQuery{View: "resolve", Reference: command.Reference})
				d := f.act(t, contract.OwnerAction{Action: "create_draft", Target: p.Assets[0].Handle, Text: &command.Text})
				f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "fixture-publish-" + strconv.FormatInt(time.Now().UnixNano(), 10)})
			case "forget":
				p := f.freshQuery(t, contract.OwnerQuery{View: "resolve", Reference: command.Reference})
				out["result"] = f.act(t, contract.OwnerAction{Action: "forget", Handle: p.Assets[0].Handle, OperationID: "fixture-forget-" + strconv.FormatInt(time.Now().UnixNano(), 10)})
			case "rebuild":
				if e = f.s.CreateGeneration(f.ctx, "browser-rebuilding", "fixture-space"); e != nil {
					t.Fatal(e)
				}
			case "large_asset":
				a, e := f.k.Create(f.ctx, contract.CreateInput{Content: "跨页完整文本\n" + strings.Repeat("中文🙂引号\"与反斜线\\。\n", 60000)})
				if e != nil {
					t.Fatal(e)
				}
				out["id"] = a.Information.ID
			default:
				t.Fatalf("unknown fixture action %q", command.Action)
			}
			write(path+".result", out)
		}
	}
}

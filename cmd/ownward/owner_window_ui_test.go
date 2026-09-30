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
		delay := b.flags["delay-quit"] && kind == "quit" || b.flags["delay-source"] && kind == "query/source" || b.flags["delay-rebase"] && kind == "action/create_draft" || b.flags["delay-discard"] && kind == "action/discard_draft"
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
	var f *ownerHTTPFixture
	f = ownerHTTPWithHandler(t, func(next http.Handler) http.Handler {
		handler := faults.wrap(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Opt-in synthetic fixture only: open without exposing test credentials
			// to browser automation logs. No such route exists in product builds.
			if r.URL.Path == "/__test/open" && f != nil {
				http.Redirect(w, r, f.server.URL+ownerwindow.Prefix+"#"+f.bootstrap(t), http.StatusFound)
				return
			}
			handler.ServeHTTP(w, r)
		})
	})
	f.w.SetQuit(func() { _ = os.WriteFile(path+".stop", []byte("browser quit"), 0600) })
	faults.mu.Lock()
	faults.internalSession = f.session
	faults.mu.Unlock()
	principal, _, agent := f.agent(t, "写作伙伴")
	f.agent(t, "写作伙伴")
	count := 28
	if n, e := strconv.Atoi(os.Getenv("OWNWARD_UI_ASSETS")); e == nil && n >= 0 && n <= 10000 {
		count = n
	}
	titles := []string{"个人知识库的组织方式", "从问题出发做笔记", "检索与回忆的区别", "为什么保留原始来源", "写作提纲：把想法连起来", "周末去京都", "京都步行路线", "旅行轻装清单", "雨天的备用安排", "产品访谈记录", "访谈后的三个观察", "本周待读书目"}
	bodies := []string{
		"资料值得被保存，也值得被重新找到。\n\n以问题作为入口，保留原始来源，用关联把分散的笔记连起来。组织的目的，是让下一次阅读有一个清楚的起点。",
		"先写下真正想回答的问题，再收集相关材料。\n\n一条笔记可以对应多个问题。写作时，从问题展开相关资料，比依赖文件夹路径更容易找到思路。",
		"检索帮助我们找回已知内容，浏览关联帮助我们发现原本没有想到的联系。\n\n两种方式需要不同的界面：清晰的列表适合精确查找，空间里的连线适合观察上下文。",
		"解释会变化，来源应该仍然可以查看。\n\n在引用观点时保留原始资料，可以回到当时的语境，检查结论是否适用。",
		"开头：为什么我们存了很多资料，却仍然找不到思路？\n\n正文：从具体问题开始；沿关联收集依据；回到原文检查；最后写成自己的表达。",
		"周六上午抵达，住在三条附近。\n\n这次以步行为主，每天安排两到三个地点，中间留出休息时间。下雨时切换到室内行程。",
		"第一天：鸭川 → 哲学之道 → 银阁寺。\n\n午餐安排在路线上，不为了打卡反复折返。第二天去清水寺，傍晚回鸭川散步。",
		"随身物品：证件、充电器、水杯、轻便外套。\n\n带一把小伞和容易步行的鞋，白天只背一个轻便的包。",
		"大雨时调整户外路线，去博物馆和书店。\n\n预约事项保持不变，其余地点按当天交通和天气再决定。",
		"受访者希望快速找回正在使用的材料，不想每次重新整理。\n\n当关系很多时，他首先关注的是：这一条与我当前的问题有什么关系？",
		"一、先帮助用户辨认对象，再展示操作。\n二、展开关联时保留原来的位置，让用户知道自己从哪里来。\n三、把依据放在关系旁边，减少来回切换。",
		"《设计心理学》\n《思考，快与慢》\n\n读完后各留下一条值得继续追问的问题。",
	}
	var ids []string
	for i := 0; i < count; i++ {
		body := titles[i%len(titles)] + "\n\n" + bodies[i%len(bodies)]
		source := domain.Source{Actor: "我的笔记", Ref: "个人记录"}
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
		links := map[int][]int{0: {1, 2, 3}, 1: {4}, 2: {4}, 5: {6, 7, 8}, 6: {8}, 9: {10}, 10: {0}}
		for _, target := range links[i] {
			if target >= len(ids) {
				continue
			}
			if r.Analysis.Organization == nil {
				r.Analysis.Organization = &semantics.Organization{Schema: semantics.OrganizationSchema, Snapshot: "fixture"}
			}
			reason := "这两份资料讨论同一个问题的不同侧面，可以对照阅读。"
			if i == 0 {
				reason = "知识库的组织方式以问题为入口，并通过检索、关联与原始来源帮助理解资料。"
			}
			if i == 5 || i == 6 {
				reason = "行程中的步行路线、随身准备与雨天替代安排相互补充。"
			}
			if i == 9 || i == 10 {
				reason = "访谈观察关注资料查找和关联探索，为知识库的使用方式提供依据。"
			}
			r.Analysis.Organization.Links = append(r.Analysis.Organization.Links, semantics.GroundedLink{ID: "related-" + strconv.Itoa(target), Type: "supports", Meaning: reason, Source: semantics.GraphEndpoint{AssetID: id, Revision: 1}, Target: semantics.GraphEndpoint{AssetID: ids[target], Revision: 1}})
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
	draftTexts := []string{"文章草稿：让资料重新参与思考\n\n保存只是开始。一个有用的知识库，应当帮助我们沿着问题重新找到内容、理解联系，并继续写作。", "下次访谈的问题\n\n你最近一次找资料是为了什么？\n哪些线索帮助你找到了它？\n找不到的时候，你会怎么做？"}
	draftCount := len(draftTexts)
	if n, e := strconv.Atoi(os.Getenv("OWNWARD_UI_DRAFTS")); e == nil && n >= 0 && n <= len(draftTexts) {
		draftCount = n
	}
	for _, content := range draftTexts[:draftCount] {
		f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr(content)})
	}
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

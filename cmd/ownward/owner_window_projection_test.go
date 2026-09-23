package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/ownerwindow"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/ownerview"
)

func TestOwnerWindowReferenceReconnectAndForget(t *testing.T) {
	f := ownerHTTP(t)
	created := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("input before restart")})
	first := f.query(t, contract.OwnerQuery{View: "resolve", Reference: created.Reference})
	other, e := ownerview.New(f.s, f.c, f.p)
	if e != nil {
		t.Fatal(e)
	}
	p, e := other.Query(f.ctx, contract.OwnerQuery{View: "resolve", Reference: created.Reference})
	if e != nil || len(p.Drafts) != 1 {
		t.Fatal(p, e)
	}
	if p.Drafts[0].Version != first.Drafts[0].Version || p.Drafts[0].Handle == first.Drafts[0].Handle {
		t.Fatal("reference/version must survive codec rotation, not handles")
	}
	if _, e = other.Query(f.ctx, contract.OwnerQuery{View: "draft_content", Handle: created.Handle}); e == nil {
		t.Fatal("old codec handle accepted")
	}
	if _, e = other.Query(context.Background(), contract.OwnerQuery{View: "resolve", Reference: created.Reference}); e == nil {
		t.Fatal("unauthenticated reference accepted")
	}
	if _, e = other.Act(f.ctx, contract.OwnerAction{Action: "discard_draft", Handle: created.Reference}); e == nil {
		t.Fatal("locator accepted as write permission")
	}
	bad, _ := json.Marshal(map[string]string{"System": "different-library", "Kind": "draft", "ID": "anything"})
	if _, e = other.Query(f.ctx, contract.OwnerQuery{View: "resolve", Reference: base64.RawURLEncoding.EncodeToString(bad)}); e == nil {
		t.Fatal("foreign authority reference accepted")
	}
	f.act(t, contract.OwnerAction{Action: "discard_draft", Handle: created.Handle})
	if p := f.query(t, contract.OwnerQuery{View: "resolve", Reference: created.Reference}); !p.Unavailable {
		t.Fatal("discard not distinguishable from transient error", p)
	}
	a := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("forget bound text")})
	published := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: a.Handle, OperationID: "reference-publish"})
	asset := f.query(t, contract.OwnerQuery{View: "resolve", Handle: published.Handle}).Assets[0]
	draft := f.act(t, contract.OwnerAction{Action: "create_draft", Target: asset.Handle})
	cp := f.query(t, contract.OwnerQuery{View: "health"})
	f.act(t, contract.OwnerAction{Action: "forget", Handle: asset.Handle, OperationID: "reference-forget"})
	for _, ref := range []string{asset.Reference, draft.Reference} {
		if p := f.freshQuery(t, contract.OwnerQuery{View: "resolve", Reference: ref}); !p.Unavailable {
			t.Fatal("forgotten reference reopened", p)
		}
	}
	if p := f.freshQuery(t, contract.OwnerQuery{View: "changes", Cursor: cp.Cursor}); !p.Reset {
		t.Fatal("forget did not require browser cache reset")
	}
}

func TestOwnerWindowRecentAndSourceProjection(t *testing.T) {
	f := ownerHTTP(t)
	for i := 0; i < 15; i++ {
		if _, e := f.k.Create(f.ctx, contract.CreateInput{Content: fmt.Sprintf("recent item %02d", i)}); e != nil {
			t.Fatal(e)
		}
	}
	p := f.freshQuery(t, contract.OwnerQuery{View: "recent", Limit: 3})
	if len(p.Activity) != 3 {
		t.Fatal(p)
	}
	for i, a := range p.Activity {
		if body := f.query(t, contract.OwnerQuery{View: "content", Handle: a.Asset}).Text.Text; body != fmt.Sprintf("recent item %02d", 14-i) {
			t.Fatal("not actual newest page", body)
		}
	}
	next := f.query(t, contract.OwnerQuery{View: "recent", Limit: 3, After: p.Next})
	if got := f.query(t, contract.OwnerQuery{View: "content", Handle: next.Activity[0].Asset}).Text.Text; got != "recent item 11" {
		t.Fatal(got)
	}
	a, e := f.k.Create(f.ctx, contract.CreateInput{Content: "source body", Source: domain.Source{Actor: "original author", Ref: "original source"}})
	if e != nil {
		t.Fatal(e)
	}
	assets := f.query(t, contract.OwnerQuery{View: "assets", Query: "source body"})
	var handle string
	for _, v := range assets.Assets {
		p := f.query(t, contract.OwnerQuery{View: "content", Handle: v.Handle})
		if p.Text.Text == a.Information.Content {
			handle = v.Handle
		}
	}
	source := f.query(t, contract.OwnerQuery{View: "source", Handle: handle}).Source
	if source.Authored || !source.PreserveOriginal || source.Actor != "original author" {
		t.Fatal(source)
	}
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Target: handle, Text: textPtr("new current")})
	result := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "source-preview"})
	source = f.query(t, contract.OwnerQuery{View: "source", Handle: result.Handle}).Source
	if !source.Authored || !source.PreserveOriginal {
		t.Fatal(source)
	}
	if got := f.query(t, contract.OwnerQuery{View: "original", Handle: result.Handle}).Text.Text; got != "source body" {
		t.Fatal(got)
	}
}

func uploadOwnerText(t *testing.T, f *ownerHTTPFixture, handle string, body []byte, authorized bool) (int, contract.OwnerResult) {
	t.Helper()
	r, e := http.NewRequest("POST", f.server.URL+ownerwindow.Prefix+"v1/draft-text", bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Content-Type", "text/plain; charset=utf-8")
	r.Header.Set("Origin", f.server.URL)
	r.Header.Set("X-Ownward-View", contract.OwnerViewSchema)
	r.Header.Set("X-Ownward-Handle", handle)
	if authorized {
		r.Header.Set("Authorization", "Bearer "+f.session)
	}
	response, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	var result contract.OwnerResult
	if response.StatusCode == 200 {
		if e = json.NewDecoder(response.Body).Decode(&result); e != nil {
			t.Fatal(e)
		}
	} else {
		io.Copy(io.Discard, response.Body)
	}
	return response.StatusCode, result
}

func TestOwnerWindowLargeAtomicTextUpload(t *testing.T) {
	f := ownerHTTP(t)
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("whole previous draft")})
	body := strings.Repeat("正文中文🙂引号\"反斜线\\\n", 48000)
	if len(body) <= contract.OwnerRequestBytes {
		t.Fatal("fixture below JSON boundary")
	}
	if code, _ := uploadOwnerText(t, f, d.Handle, []byte(body), false); code != 401 {
		t.Fatal("unauthenticated upload", code)
	}
	code, result := uploadOwnerText(t, f, d.Handle, []byte(body), true)
	if code != 200 {
		t.Fatal("large write", code)
	}
	var whole strings.Builder
	offset := int64(0)
	for {
		page := f.query(t, contract.OwnerQuery{View: "draft_content", Handle: result.Handle, Offset: offset})
		whole.WriteString(page.Text.Text)
		if !page.Text.More {
			break
		}
		offset = page.Text.NextOffset
	}
	if whole.String() != body {
		t.Fatal("paged complete text changed")
	}
	// A different device can send immediately; pacing is not the safety proof.
	f.session = f.login(t)
	if code, _ = uploadOwnerText(t, f, d.Handle, []byte("stale overwrite"), true); code != 409 {
		t.Fatal("stale stream accepted", code)
	}
	f.session = f.login(t)
	if code, _ = uploadOwnerText(t, f, result.Handle, []byte{0xff, 0xfe}, true); code == 200 {
		t.Fatal("invalid UTF-8 accepted")
	}
	f.session = f.login(t)
	u, _ := url.Parse(f.server.URL)
	c, e := net.Dial("tcp", u.Host)
	if e != nil {
		t.Fatal(e)
	}
	fmt.Fprintf(c, "POST %sv1/draft-text HTTP/1.1\r\nHost: %s\r\nOrigin: %s\r\nContent-Type: text/plain; charset=utf-8\r\nX-Ownward-View: %s\r\nAuthorization: Bearer %s\r\nX-Ownward-Handle: %s\r\nContent-Length: 300\r\nConnection: close\r\n\r\ntruncated", ownerwindow.Prefix, u.Host, f.server.URL, contract.OwnerViewSchema, f.session, result.Handle)
	c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		p := f.freshQuery(t, contract.OwnerQuery{View: "resolve", Reference: d.Reference})
		if p.Drafts[0].Bytes != int64(len(body)) {
			t.Fatal("interrupted stream replaced old draft")
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	current := f.freshQuery(t, contract.OwnerQuery{View: "resolve", Reference: d.Reference}).Drafts[0]
	if got := f.query(t, contract.OwnerQuery{View: "draft_content", Handle: current.Handle}).Text.Text; !strings.HasPrefix(body, got) {
		t.Fatal("previous full body lost")
	}
}

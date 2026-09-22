package main

import (
	"net/http"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
)

func TestOwnerEntryCapacityPreservesActiveSessionAndDraft(t *testing.T) {
	f := ownerHTTP(t)
	draft := f.act(t, contract.OwnerAction{Action: "create_draft", Text: textPtr("keep my work")})
	sessions := []string{f.session}
	for i := 1; i < 32; i++ {
		sessions = append(sessions, f.login(t))
	}
	// Ordinary content reads, not just polling/saving, keep this tab active.
	f.query(t, contract.OwnerQuery{View: "draft_content", Handle: draft.Handle})
	newest := f.login(t)
	for _, item := range []struct {
		key  string
		want int
	}{{sessions[0], 200}, {sessions[1], 401}, {newest, 200}} {
		f.session = item.key
		response, _ := f.request(t, "query", contract.OwnerQuery{View: "drafts"}, nil)
		if response.StatusCode != item.want {
			t.Fatalf("session status = %d, want %d", response.StatusCode, item.want)
		}
	}
	if text := f.query(t, contract.OwnerQuery{View: "draft_content", Handle: draft.Handle}).Text.Text; text != "keep my work" {
		t.Fatalf("entry replacement changed draft: %q", text)
	}
	// More than a full capacity of closed tabs remains recoverable.
	for i := 0; i < 40; i++ {
		f.session = f.login(t)
	}
	if len(f.query(t, contract.OwnerQuery{View: "drafts"}).Drafts) != 1 {
		t.Fatal("work lost on reopening")
	}
}

func TestOwnerBootstrapCapacityAndInvalidExchangeDoNotEvictSessions(t *testing.T) {
	f := ownerHTTP(t)
	sessions := []string{f.session}
	for i := 1; i < 32; i++ {
		sessions = append(sessions, f.login(t))
	}
	oldest := f.bootstrap(t)
	var newest string
	for i := 0; i < 32; i++ {
		newest = f.bootstrap(t)
	}
	for _, value := range []string{"invalid", oldest} {
		response, _ := f.request(t, "bootstrap", map[string]string{"token": value}, nil)
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatal("invalid bootstrap accepted", response.StatusCode)
		}
	}
	for _, key := range sessions {
		f.session = key
		response, _ := f.request(t, "query", contract.OwnerQuery{View: "drafts"}, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatal("invalid exchange evicted session", response.StatusCode)
		}
	}
	response, _ := f.request(t, "bootstrap", map[string]string{"token": newest}, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatal("new valid entry cannot recover", response.StatusCode)
	}
	response, _ = f.request(t, "bootstrap", map[string]string{"token": newest}, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("bootstrap replay accepted", response.StatusCode)
	}
}

func TestOwnerRecoveredCredentialCannotBeDisplacedByOldBootstrap(t *testing.T) {
	f := ownerHTTP(t)
	stale := f.bootstrap(t)
	var err error
	f.owner, err = f.c.RecoverOwner()
	if err != nil {
		t.Fatal(err)
	}
	sessions := make([]string, 32)
	for i := range sessions {
		sessions[i] = f.login(t)
	}
	response, _ := f.request(t, "bootstrap", map[string]string{"token": stale}, nil)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("stale owner bootstrap accepted", response.StatusCode)
	}
	for _, key := range sessions {
		f.session = key
		response, _ := f.request(t, "query", contract.OwnerQuery{View: "drafts"}, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatal("stale owner bootstrap displaced valid session", response.StatusCode)
		}
	}
}

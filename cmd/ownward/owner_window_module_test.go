package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/ownerwindow"
	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

func TestOwnerModuleOriginalAcrossWindowAndAgent(t *testing.T) {
	f := ownerHTTP(t)
	source := domain.Source{Ref: "local:original", Actor: "source author"}
	original := strings.Repeat("source evidence🙂\n", 8000)
	a, err := f.k.Create(f.ctx, contract.CreateInput{Content: original, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	p, credential, agent := f.agent(t, "reader")
	if err = f.c.SetPermissions(f.ctx, p.ID, []contract.Permission{contract.ReadPermission}); err != nil {
		t.Fatal(err)
	}
	read := func(include bool) contract.InformationRead {
		t.Helper()
		var result contract.InformationRead
		if e := decodeTool(hostCall(t, agent, "ownward_read", map[string]any{"id": a.Information.ID, "include_original": include}, true), &result); e != nil {
			t.Fatal(e)
		}
		return result
	}
	if read(true).Original != nil {
		t.Fatal("unedited source falsely has retained original")
	}
	asset := f.query(t, contract.OwnerQuery{View: "assets"}).Assets[0]
	current := strings.Repeat("owner revision one. ", 30)
	d := f.act(t, contract.OwnerAction{Action: "create_draft", Target: asset.Handle, Text: textPtr(current)})
	revised := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "module-first-edit"})
	plain, full := read(false), read(true)
	if plain.Original == nil || plain.Original.Revision != 1 || plain.Original.Content != nil || plain.Original.Source != nil {
		t.Fatal("default must discover without repeating payload", plain.Original)
	}
	if full.Original == nil || full.Original.Content == nil || *full.Original.Content != original || full.Original.Source == nil || *full.Original.Source != source {
		t.Fatal("wrong retained evidence")
	}
	if full.Information.Content != current || full.Basis != plain.Basis {
		t.Fatal("current and historical evidence mixed")
	}
	if raw, _ := json.Marshal(plain); len(raw) > 4096 {
		t.Fatal("default read copied original payload")
	}
	typed, err := f.p.ReadInformation(informationcontrol.Authenticate(context.Background(), credential), a.Information.ID, contract.ReadOptions{IncludeOriginal: true})
	if err != nil || typed.Original == nil || typed.Original.Content == nil || *typed.Original.Content != original {
		t.Fatal("typed product lost option", err)
	}
	// Short documents correctly yield no fragments; the current text above is
	// long enough to exercise actual evidence discovery and reading together.
	var refs struct {
		Evidence []domain.EvidenceReference `json:"evidence"`
	}
	if err = decodeTool(hostCall(t, agent, "ownward_evidence_search", map[string]any{"source_id": a.Information.ID, "query": "owner", "limit": 1}, true), &refs); err != nil {
		t.Fatal(err)
	}
	if len(refs.Evidence) == 0 {
		t.Fatal("missing current evidence")
	}
	ref := refs.Evidence[0]
	var fragment contract.EvidenceRead
	if err = decodeTool(hostCall(t, agent, "ownward_evidence_read", map[string]any{"id": ref.ID}, true), &fragment); err != nil {
		t.Fatal(err)
	}
	if fragment.Original == nil || fragment.Original.Revision != 1 || fragment.Original.Content != nil {
		t.Fatal("fragment hides original")
	}
	d = f.act(t, contract.OwnerAction{Action: "create_draft", Target: revised.Handle, Text: textPtr("owner revision two")})
	revised = f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "module-second-edit"})
	full = read(true)
	if full.Information.Content != "owner revision two" || full.Original.Revision != 1 || *full.Original.Content != original {
		t.Fatal("later edit replaced retained source")
	}
	checks, err := f.p.CheckInformation(f.ctx, []string{plain.Basis})
	if err != nil || len(checks) != 1 || checks[0].Status != "changed" {
		t.Fatal("old basis survived edit", checks, err)
	}
	if err = f.c.SetPermissions(f.ctx, p.ID, nil); err != nil {
		t.Fatal(err)
	}
	hostCall(t, agent, "ownward_read", map[string]any{"id": a.Information.ID, "include_original": true}, false)
	if err = f.c.SetPermissions(f.ctx, p.ID, []contract.Permission{contract.ReadPermission}); err != nil {
		t.Fatal(err)
	}
	f.act(t, contract.OwnerAction{Action: "forget", Handle: revised.Handle, OperationID: "module-forget"})
	hostCall(t, agent, "ownward_read", map[string]any{"id": a.Information.ID, "include_original": true}, false)
	hostCall(t, agent, "ownward_evidence_read", map[string]any{"id": ref.ID}, false)
}

func TestOwnerModuleOriginalDeliveryInvalidation(t *testing.T) {
	for _, action := range []string{"revoke", "forget"} {
		t.Run(action, func(t *testing.T) {
			f := ownerHTTP(t)
			a, err := f.k.Create(f.ctx, contract.CreateInput{Content: "retained original", Source: domain.Source{Ref: "local:source"}})
			if err != nil {
				t.Fatal(err)
			}
			asset := f.query(t, contract.OwnerQuery{View: "assets"}).Assets[0]
			d := f.act(t, contract.OwnerAction{Action: "create_draft", Target: asset.Handle, Text: textPtr("current")})
			r := f.act(t, contract.OwnerAction{Action: "publish_draft", Handle: d.Handle, OperationID: "delivery-edit"})
			p, credential, _ := f.agent(t, "delivery-reader")
			if err = f.c.SetPermissions(f.ctx, p.ID, []contract.Permission{contract.ReadPermission}); err != nil {
				t.Fatal(err)
			}
			args, _ := json.Marshal(map[string]any{"id": a.Information.ID, "include_original": true})
			result, err := f.k.ExecuteStream(informationcontrol.Authenticate(context.Background(), credential), contract.StreamRequest{Operation: "ownward_read", Arguments: boundedstore.StringSource(args)})
			if err != nil {
				t.Fatal(err)
			}
			defer result.Close()
			if err = result.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			if action == "revoke" {
				if err = f.c.SetPermissions(f.ctx, p.ID, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				f.act(t, contract.OwnerAction{Action: "forget", Handle: r.Handle, OperationID: "delivery-forget"})
			}
			if err = result.Check(context.Background()); err == nil {
				t.Fatal("stale original delivered")
			}
			if action == "forget" {
				deadline := time.Now().Add(5 * time.Second)
				for {
					receipt, e := f.p.Receipt(f.ctx, "delivery-forget")
					if e != nil {
						t.Fatal(e)
					}
					if receipt.Status == "completed" {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("forget cleanup did not complete", receipt)
					}
					time.Sleep(10 * time.Millisecond)
				}
				reader, e := result.Value.Open(context.Background())
				if e == nil {
					_, e = io.ReadAll(reader)
					reader.Close()
				}
				if e == nil {
					t.Fatal("undelivered forgotten copy survives")
				}
			}
		})
	}
}

func TestOwnerModuleDamagedHintStillOpensAuthenticatedEntry(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "owner-window.json"), 0700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	called := false
	err := launchOwnerWindow(root, "system", "http://127.0.0.1:1", "http://127.0.0.1:1/owner/#synthetic", &stdout, &stderr, func(entry string) error {
		called = true
		if entry != "http://127.0.0.1:1/owner/#synthetic" {
			t.Fatal("cache changed trusted entry")
		}
		return nil
	})
	if err != nil || !called || stdout.Len() == 0 || stderr.Len() == 0 {
		t.Fatal("valid entry blocked", err, called)
	}
	if strings.Contains(stderr.String(), "synthetic") {
		t.Fatal("bootstrap exposed in warning")
	}
}

// Opt-in contract-only cold execution server: isolated synthetic library and
// credentials, no access to the user's installation or real information.
func TestOwnerModuleColdHarness(t *testing.T) {
	path := os.Getenv("OWNWARD_MODULE_COLD_FIXTURE")
	if path == "" {
		t.Skip("opt-in independent interface acceptance")
	}
	f := ownerHTTP(t)
	a, err := f.k.Create(f.ctx, contract.CreateInput{Content: strings.Repeat("Historical source record. ", 30), Source: domain.Source{Actor: "fixture author", Ref: "local:cold-source"}})
	if err != nil {
		t.Fatal(err)
	}
	p, credential, _ := f.agent(t, "cold-reader")
	if err = f.c.SetPermissions(f.ctx, p.ID, []contract.Permission{contract.ReadPermission}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"entry": f.server.URL + ownerwindow.Prefix + "#" + f.bootstrap(t), "entry2": f.server.URL + ownerwindow.Prefix + "#" + f.bootstrap(t), "origin": f.server.URL, "transport": "transport-test", "reader": credential, "reader_id": p.ID, "source_id": a.Information.ID, "source_content": a.Information.Content})
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("independent execution deadline")
		case <-tick.C:
			if _, err := os.Stat(path + ".stop"); err == nil {
				return
			}
		}
	}
}

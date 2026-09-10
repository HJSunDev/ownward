package informationcontrol_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/authoritysubstrate"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

type interruptedKernel struct {
	*core.Service
	stage string
}

func TestForgetDuringFrozenMigrationStopsUseBeforeOfflineCopyCleanup(t *testing.T) {
	a, c, owner, _ := setup(t)
	kernel, err := core.NewWithAuthority(a.Assets())
	if err != nil {
		t.Fatal(err)
	}
	defer kernel.Close()
	product := informationcontrol.NewProduct(kernel, c)
	defer product.Close()
	value, err := product.Create(owner, contract.CreateInput{Content: "must stop being used during migration"})
	if err != nil {
		t.Fatal(err)
	}
	target := contract.Location{SystemID: c.SystemID(), ServiceID: "target", Endpoint: "https://target.test", Certificate: "test", Composition: "test"}
	if _, err := c.PrepareHandoff(owner, "migration", target); err != nil {
		t.Fatal(err)
	}
	frozen, err := c.FreezeHandoff(owner, "migration", true)
	if err != nil {
		t.Fatal(err)
	}
	product.SetRelatedCleanup(func() error { return errors.New("target temporarily offline") })
	receipt, err := product.Manage(owner, contract.ManagementRequest{ID: "forget-frozen", Operation: "forget", Targets: []contract.AssetVersion{{ID: value.Information.ID, Revision: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := product.Read(owner, value.Information.ID); err == nil {
		t.Fatal("forgotten data remained usable")
	}
	if c.State().Access.Handoff != nil {
		t.Fatal("forget did not cancel snapshot")
	}
	if _, err := c.RetireHandoff(owner, frozen.ID, strings.Repeat("a", 64), frozen.Revision); err == nil {
		t.Fatal("stale snapshot retired source")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		receipt, err = product.Receipt(owner, receipt.Request.ID)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Error != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if receipt.Status == "completed" || receipt.Error == "" {
		t.Fatal("offline copy falsely reported cleaned", receipt.Status)
	}
	product.SetRelatedCleanup(func() error { return c.MarkHandoffClean("migration") })
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		receipt, err = product.Receipt(owner, receipt.Request.ID)
		if err != nil {
			t.Fatal(err)
		}
		if receipt.Status == "completed" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if receipt.Status != "completed" {
		t.Fatal("cleanup did not resume", receipt.Status)
	}
}

func (k interruptedKernel) StopUsing(targets, affected []contract.AssetVersion, id string, persist func([]contract.AssetVersion) error) error {
	return k.Service.StopUsing(targets, affected, id, func(v []contract.AssetVersion) error {
		if err := persist(v); err != nil {
			return err
		}
		if k.stage == "stopping" {
			return errors.New("simulated power loss after durable intent")
		}
		return nil
	})
}
func (k interruptedKernel) CleanForgotten() error {
	if k.stage == "cleaning" {
		return os.ErrPermission
	}
	return k.Service.CleanForgotten()
}

func TestForgetRecoversDurableBarrierAndCleanupAfterRestart(t *testing.T) {
	for _, stage := range []string{"stopping", "cleaning"} {
		t.Run(stage, func(t *testing.T) {
			a, c, owner, root := setup(t)
			s, err := core.NewWithAuthority(a.Assets())
			if err != nil {
				t.Fatal(err)
			}
			p := informationcontrol.NewProduct(interruptedKernel{s, stage}, c)
			value, err := p.Create(owner, contract.CreateInput{Content: "forget after interruption"})
			if err != nil {
				t.Fatal(err)
			}
			op, manageErr := p.Manage(owner, contract.ManagementRequest{ID: "interrupted", Operation: "forget", Targets: []contract.AssetVersion{{ID: value.Information.ID, Revision: 1}}})
			if stage == "stopping" && manageErr == nil {
				t.Fatal("fault did not interrupt")
			}
			if stage == "cleaning" && manageErr != nil {
				t.Fatal(manageErr)
			}
			if _, err := p.Read(owner, value.Information.ID); err == nil {
				t.Fatal("durable barrier not enforced")
			}
			if stage == "cleaning" {
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					op, _ = p.Receipt(owner, "interrupted")
					if op.Error != "" {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if op.Error == "" {
					t.Fatal("cleanup failure invisible")
				}
			}
			p.Close()
			s.Close()
			a.Close()
			reopened, err := authoritysubstrate.Open(root, initial)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			control := informationcontrol.New(reopened.Control())
			service, err := core.NewWithAuthority(reopened.Assets())
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			product := informationcontrol.NewProduct(service, control)
			defer product.Close()
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				op, err = product.Receipt(owner, "interrupted")
				if err != nil {
					t.Fatal(err)
				}
				if op.Status == "completed" {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if op.Status != "completed" {
				t.Fatalf("restart did not finish: %+v", op)
			}
			if _, err := product.Read(owner, value.Information.ID); err == nil {
				t.Fatal("restart resurrected content")
			}
			if _, err := product.Manage(owner, op.Request); err != nil {
				t.Fatal("completed retry failed", err)
			}
		})
	}
}

func TestConfirmationTargetChangeAndReceiptIsolation(t *testing.T) {
	a, c, owner, _ := setup(t)
	s, err := core.NewWithAuthority(a.Assets())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := informationcontrol.NewProduct(s, c)
	defer p.Close()
	_, token, err := c.Enroll(owner, "requester")
	if err != nil {
		t.Fatal(err)
	}
	caller := informationcontrol.Authenticate(context.Background(), token)
	_, otherToken, err := c.Enroll(owner, "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	value, err := p.Create(owner, contract.CreateInput{Content: "before confirmation"})
	if err != nil {
		t.Fatal(err)
	}
	request := contract.ManagementRequest{ID: "version-bound", Operation: "forget", Targets: []contract.AssetVersion{{ID: value.Information.ID, Revision: 1}}}
	if _, err := p.Manage(caller, request); err != nil {
		t.Fatal(err)
	}
	changed := "after confirmation material"
	if _, err := p.Update(owner, contract.UpdateInput{ID: value.Information.ID, ExpectedRevision: 1, Content: &changed}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Decide(owner, request.ID, true); err == nil {
		t.Fatal("changed target deleted")
	}
	if _, err := p.Read(owner, value.Information.ID); err != nil {
		t.Fatal("target was lost", err)
	}
	if _, err := p.Receipt(informationcontrol.Authenticate(context.Background(), otherToken), request.ID); err == nil {
		t.Fatal("other subject read receipt")
	}
}

func TestUserControlPreservesSearchResultsAndHasNoModelRound(t *testing.T) {
	a, c, owner, _ := setup(t)
	s, err := core.NewWithAuthority(a.Assets())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := informationcontrol.NewProduct(s, c)
	defer p.Close()
	for _, text := range []string{"travel train booking", "travel hotel address", "recipe vegetables"} {
		if _, err := p.Create(owner, contract.CreateInput{Content: text}); err != nil {
			t.Fatal(err)
		}
	}
	input := contract.SearchInput{Query: "travel", Limit: 10}
	raw, err := s.Search(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	for i := 0; i < 100; i++ {
		got, err := p.Search(owner, input)
		if err != nil || len(got) != len(raw) {
			t.Fatal("search changed", err)
		}
		for j := range got {
			if got[j].ID != raw[j].ID {
				t.Fatal("ranking changed")
			}
		}
	}
	t.Logf("100 guarded searches: %s; per call: %s; external model calls: 0", time.Since(started), time.Since(started)/100)
	if err := a.Backup(filepath.Join(t.TempDir(), "authorized.zip")); err != nil {
		t.Fatal(err)
	}
}

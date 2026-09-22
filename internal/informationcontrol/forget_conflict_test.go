package informationcontrol_test

import (
	"fmt"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
)

func TestForgetConflictEndsWithLegacyKernel(t *testing.T) {
	a, c, owner, _ := setup(t)
	k, e := core.NewWithAuthority(a.Assets())
	if e != nil {
		t.Fatal(e)
	}
	defer k.Close()
	p := informationcontrol.NewProduct(k, c)
	defer p.Close()
	value, e := p.Create(owner, contract.CreateInput{Content: "before edit"})
	if e != nil {
		t.Fatal(e)
	}
	newText := "after edit"
	if _, e = p.Update(owner, contract.UpdateInput{ID: value.Information.ID, ExpectedRevision: value.Information.Revision, Content: &newText}); e != nil {
		t.Fatal(e)
	}
	request := contract.ManagementRequest{ID: "stale-forget", Operation: "forget", Targets: []contract.AssetVersion{{ID: value.Information.ID, Revision: value.Information.Revision}}}
	before := c.State().InformationControl.DeletionRevision
	for i := 0; i < 2; i++ {
		if op, e := p.Manage(owner, request); e != nil || op.Status != "superseded" {
			t.Fatal(op, e)
		}
	}
	got, e := p.Read(owner, value.Information.ID)
	if e != nil || got.Content != newText || c.State().InformationControl.DeletionRevision != before {
		t.Fatal("failed scope altered current authority", got, e)
	}
}

func TestPermanentlyInvalidManagementRequestsAreNotQueued(t *testing.T) {
	_, c, owner, _ := setup(t)
	requests := []contract.ManagementRequest{{ID: "owner-rights", Operation: "permissions", SubjectID: c.State().InformationControl.OwnerID}, {ID: "oversized-forget", Operation: "forget"}}
	for i := 0; i <= contract.MaxForgetTargets; i++ {
		requests[1].Targets = append(requests[1].Targets, contract.AssetVersion{ID: fmt.Sprintf("asset-%d", i), Revision: 1})
	}
	before := c.State().Revision
	for _, r := range requests {
		if _, e := c.Propose(owner, r); e == nil {
			t.Fatal("permanently invalid request accepted", r.ID)
		}
		if _, e := c.Receipt(owner, r.ID); e == nil {
			t.Fatal("invalid request retained", r.ID)
		}
	}
	if c.State().Revision != before {
		t.Fatal("invalid requests mutated authority")
	}
}

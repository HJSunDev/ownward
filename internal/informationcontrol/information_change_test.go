package informationcontrol_test

import (
	"context"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"testing"
)

func TestInformationBasisUsesCurrentPrincipalAndForgetBarrier(t *testing.T) {
	a, c, owner, _ := setup(t)
	k, err := core.NewWithAuthority(a.Assets())
	if err != nil {
		t.Fatal(err)
	}
	p := informationcontrol.NewProduct(k, c)
	defer p.Close()
	created, err := p.Create(owner, contract.CreateInput{Content: "可授权的信息"})
	if err != nil {
		t.Fatal(err)
	}
	reader, token, err := c.Enroll(owner, "reader")
	if err != nil {
		t.Fatal(err)
	}
	ctx := informationcontrol.Authenticate(context.Background(), token)
	if err := c.SetPermissions(owner, reader.ID, []contract.Permission{contract.ReadPermission}); err != nil {
		t.Fatal(err)
	}
	r, err := p.ReadInformation(ctx, created.Information.ID)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := p.CheckInformation(ctx, []string{r.Basis})
	if err != nil || checks[0].Status != "unchanged" {
		t.Fatal(checks, err)
	}
	if err := c.SetPermissions(owner, reader.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CheckInformation(ctx, []string{r.Basis}); err == nil {
		t.Fatal("依据绕过撤销")
	}
	if _, err := p.ReadInformation(ctx, created.Information.ID); err == nil {
		t.Fatal("撤销后读取")
	}
	if err := c.SetPermissions(owner, reader.ID, []contract.Permission{contract.ReadPermission}); err != nil {
		t.Fatal(err)
	}
	request := contract.ManagementRequest{ID: "forget-source", Operation: "forget", Targets: []contract.AssetVersion{{ID: created.Information.ID, Revision: 1}}}
	if _, err := p.Manage(owner, request); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Decide(owner, request.ID, true); err != nil {
		t.Fatal(err)
	}
	checks, err = p.CheckInformation(ctx, []string{r.Basis})
	if err != nil || checks[0].Status != "unavailable" || checks[0].SourceID != "" {
		t.Fatal(checks, err)
	}
}

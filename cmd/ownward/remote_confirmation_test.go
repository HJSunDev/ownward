package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServiceApprovalKeepsExplicitPreviewAcrossOwnerRecovery(t *testing.T) {
	for _, accept := range []bool{true, false} {
		name := "approve"
		if !accept {
			name = "decline"
		}
		t.Run(name, func(t *testing.T) {
			f := newRemoteFixture(t)
			ctx := context.Background()
			owner := informationcontrol.Authenticate(ctx, f.owner)
			root := t.TempDir()
			installed := installation{Root: root, DataDir: root, Location: f.identity.Location}
			if err := installed.save(); err != nil {
				t.Fatal(err)
			}
			identity, _ := json.Marshal(f.identity)
			if err := installed.vault().Save("installation", "identity", string(identity)); err != nil {
				t.Fatal(err)
			}
			if err := installed.vault().Save(f.c.SystemID(), "owner", f.owner); err != nil {
				t.Fatal(err)
			}
			if _, err := f.c.Invite(owner, "cli-entry"); err != nil {
				t.Fatal(err)
			}
			if _, err := f.c.Join("cli-entry", "fixture-proof-with-at-least-32-characters", "CLI entrant", []contract.Permission{contract.ReadPermission}); err != nil {
				t.Fatal(err)
			}
			preview := func() (contract.Enrollment, string) {
				var output bytes.Buffer
				if err := runAccessCommand(ctx, []string{"service-approve", "--data-dir", root}, &output, io.Discard); err != nil {
					t.Fatal(err)
				}
				var p struct {
					Enrollment contract.Enrollment `json:"enrollment"`
					Marker     string              `json:"marker"`
				}
				if err := json.Unmarshal(output.Bytes(), &p); err != nil {
					t.Fatal(err)
				}
				if p.Enrollment.Decision == "" {
					t.Fatal("preview has no decision")
				}
				return p.Enrollment, p.Marker
			}
			initial, marker := preview()
			call := func(decision string, approve bool) error {
				value := "false"
				if approve {
					value = "true"
				}
				args := []string{"service-approve", "--data-dir", root, "--id", "cli-entry", "--marker", marker, "--approve=" + value}
				if decision != "" {
					args = append(args, "--decision", decision)
				}
				return runAccessCommand(ctx, args, io.Discard, io.Discard)
			}
			if err := call("", true); err == nil {
				t.Fatal("approval without presented decision accepted")
			}
			if err := call(initial.Decision, true); err != nil {
				t.Fatal(err)
			}
			credential, err := f.c.RecoverOwner()
			if err != nil {
				t.Fatal(err)
			}
			if err = installed.vault().Save(f.c.SystemID(), "owner", credential); err != nil {
				t.Fatal(err)
			}
			if err = call(initial.Decision, accept); err == nil {
				t.Fatal("stale CLI preview renewed an invalid approval")
			}
			fresh, _ := preview()
			if err = call(fresh.Decision, accept); err != nil {
				t.Fatal("fresh CLI decision failed", err)
			}
			current, _, err := f.c.EnrollmentPreview(informationcontrol.Authenticate(ctx, credential), "cli-entry")
			expected := "declined"
			if accept {
				expected = "approved"
			}
			if err != nil || current.Status != expected {
				t.Fatal(current.Status, err)
			}
		})
	}
}

func TestRemoteConfirmationRejectsStalePresentationAndResumesApproval(t *testing.T) {
	f := newRemoteFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	owner := informationcontrol.Authenticate(ctx, f.owner)
	actor, actorToken, err := f.c.Enroll(owner, "requester")
	if err != nil {
		t.Fatal(err)
	}
	manager, managerToken, err := f.c.Enroll(owner, "other manager")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.c.SetPermissions(owner, manager.ID, []contract.Permission{contract.ManagePermission}); err != nil {
		t.Fatal(err)
	}
	request := contract.ManagementRequest{ID: "remote-stale", Operation: "permissions", SubjectID: actor.ID, Permissions: []contract.Permission{contract.ReadPermission}}
	if _, err = f.p.Manage(informationcontrol.Authenticate(ctx, actorToken), request); err != nil {
		t.Fatal(err)
	}
	host := &hostConnector{remote: &remoteConnection{Client: f.client, Material: connectionMaterial{Location: f.identity.Location}}}
	proxy := mcp.NewServer(&mcp.Implementation{Name: "confirmation-test"}, nil)
	proxy.AddTool(&mcp.Tool{Name: "confirm_pending", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, err := host.confirmManagement(ctx, r.Session, f.owner, request.ID)
		return &mcp.CallToolResult{}, err
	})
	ct, st := mcp.NewInMemoryTransports()
	server, err := proxy.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	confirmations := 0
	client := mcp.NewClient(&mcp.Implementation{Name: "owner-host"}, &mcp.ClientOptions{ElicitationHandler: func(ctx context.Context, _ *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
		confirmations++
		if confirmations == 1 {
			op, err := f.p.Receipt(owner, request.ID)
			if err != nil {
				return nil, err
			}
			if _, err = f.c.DecideVersion(informationcontrol.Authenticate(ctx, managerToken), request.ID, op.Decision, true); err != nil {
				return nil, err
			}
			if err = f.c.SetPermissions(owner, manager.ID, []contract.Permission{contract.ReadPermission}); err != nil {
				return nil, err
			}
		}
		return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"confirm": true}}, nil
	}})
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_pending", Arguments: struct{}{}}); err == nil && !result.IsError {
		t.Fatal("old remote presentation accepted")
	}
	if result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_pending", Arguments: struct{}{}}); err != nil || result.IsError {
		t.Fatal("fresh remote presentation failed", err)
	}
	op, err := f.p.Receipt(owner, request.ID)
	if err != nil || op.Status != "completed" || confirmations != 2 {
		t.Fatal(op.Status, confirmations, err)
	}
	// A surviving valid approval resumes without presenting a new form.
	request.ID = "remote-resume"
	if _, err = f.p.Manage(informationcontrol.Authenticate(ctx, actorToken), request); err != nil {
		t.Fatal(err)
	}
	if _, err = f.c.Decide(owner, request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "confirm_pending", Arguments: struct{}{}}); err != nil || result.IsError {
		t.Fatal("approved remote task did not resume", err)
	}
	op, err = f.p.Receipt(owner, request.ID)
	if err != nil || op.Status != "completed" || confirmations != 2 {
		t.Fatal(op.Status, confirmations, err)
	}
}

func TestRemoteManagerDecidesPendingWithPresentedVersion(t *testing.T) {
	for _, accept := range []bool{true, false} {
		name := "approve"
		if !accept {
			name = "decline"
		}
		t.Run(name, func(t *testing.T) {
			f := newRemoteFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			owner := informationcontrol.Authenticate(ctx, f.owner)
			manager, managerToken, err := f.c.Enroll(owner, "trusted manager")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.c.SetPermissions(owner, manager.ID, []contract.Permission{contract.ReadPermission, contract.MaintainPermission, contract.ManagePermission}); err != nil {
				t.Fatal(err)
			}
			requester, requesterToken, err := f.c.Enroll(owner, "requester")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.c.SetPermissions(owner, requester.ID, []contract.Permission{contract.ReadPermission}); err != nil {
				t.Fatal(err)
			}
			request := contract.ManagementRequest{ID: "remote-review-" + name, Operation: "permissions", SubjectID: requester.ID, Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission}}
			op, err := f.p.Manage(informationcontrol.Authenticate(ctx, requesterToken), request)
			if err != nil || op.Status != "awaiting_approval" {
				t.Fatal(op.Status, err)
			}
			host, err := newRemoteHost(ctx, connectionMaterial{Location: f.identity.Location, EnrollmentID: "review-manager"}, localowner.Vault{Root: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			host.record = connectorRecord{Credential: managerToken, Principal: manager.ID, Connected: true}
			if err = host.save(); err != nil {
				t.Fatal(err)
			}
			session := f.hostSession(t, host, "manager", accept)
			result, callErr := session.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_rules", Arguments: struct{}{}})
			receipt, err := f.p.Receipt(owner, request.ID)
			if err != nil {
				t.Fatal(err)
			}
			if callErr != nil || result == nil || result.IsError {
				t.Fatal("management decision blocked the original tool", callErr)
			}
			t.Logf("decision=%s durableStatus=%s; original tool continued", name, receipt.Status)
			expected := "completed"
			if !accept {
				expected = "declined"
			}
			if receipt.Status != expected {
				t.Fatalf("confirmed remote %s must become %s, got %s", name, expected, receipt.Status)
			}
		})
	}
}

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/rpcstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The foreground caller owns the route lock and reconnect policy. Do not call
// the background wrapper here: a queued route writer would deadlock a nested read lock.
func (o *organizationHost) statusRequest(ctx context.Context, request *mcp.CallToolRequest, self contract.Principal, session *mcp.ClientSession) (*mcp.CallToolResult, error) {
	var input struct {
		ID string `json:"id"`
	}
	scope := rpcstream.FromContext(ctx)
	if scope != nil {
		call, err := scope.Resolve("ownward_status", request.Params.Arguments)
		if err != nil {
			return nil, err
		}
		if err = call.Arguments.DecodeSmall(&input, 4096); err != nil {
			return nil, err
		}
	} else if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
		return nil, err
	}
	r, err := organizationToolCall(ctx, scope, session, "ownward_status", input)
	if err != nil {
		if o.host.remote != nil {
			return nil, errRemoteUnavailable
		}
		return nil, err
	}
	return o.annotateStatus(ctx, r, self)
}

func (o *organizationHost) statusScope(self contract.Principal) string {
	o.host.mu.Lock()
	defer o.host.mu.Unlock()
	return fmt.Sprintf("%s:%s:%d", o.host.system, self.ID, self.Revision)
}

func (o *organizationHost) executionIdentity(ctx context.Context, id string) (string, error) {
	r, err := o.call(ctx, "ownward_status", map[string]any{"id": id})
	if err != nil {
		return "", err
	}
	if r.IsError {
		return "", errors.New("组织状态不可核对")
	}
	var value struct{ Organization contract.OrganizationState }
	if err = decodeTool(r, &value); err != nil || value.Organization.ExecutionIdentity == "" {
		return "", errors.New("组织状态缺少执行身份")
	}
	return value.Organization.ExecutionIdentity, nil
}

func (o *organizationHost) blockExecution(ctx context.Context, scope, identity, reason string, lease contract.OrganizationLease) {
	// An obsolete attempt must not label a newer source or generation blocked.
	if _, err := o.jobs(ctx, contract.OrganizationJobRequest{Action: "renew", AssetID: lease.AssetID, Lease: lease.Lease, LeaseSeconds: 60}); err != nil {
		return
	}
	if identity == "" {
		o.broken.Store(true)
		o.ready.Store(false)
		return
	}
	err := o.journal.Block(ctx, scope, identity, reason)
	if err != nil {
		o.broken.Store(true)
		o.ready.Store(false)
	}
}

// Kernel completion remains authoritative; local execution limits are separate.
func (o *organizationHost) annotateStatus(ctx context.Context, r *mcp.CallToolResult, self contract.Principal) (*mcp.CallToolResult, error) {
	if r == nil || r.IsError {
		return r, nil
	}
	var value struct {
		Organization contract.OrganizationState `json:"organization"`
	}
	if err := decodeTool(r, &value); err != nil {
		return nil, err
	}
	s := &value.Organization
	if s.Status != "pending" || s.ExecutionIdentity == "" {
		return r, nil
	}
	reason, err := o.journal.BlockReason(ctx, o.statusScope(self), s.ExecutionIdentity)
	if errors.Is(err, sql.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	s.Error = reason
	s.RequiredAction = "restore_organization_execution"
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, StructuredContent: value}, nil
}

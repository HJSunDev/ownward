package main

import (
	"context"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type genericOrganizationExecutorFixture struct{}

func (genericOrganizationExecutorFixture) Descriptor() contract.OrganizationExecutorDescriptor {
	return contract.OrganizationExecutorDescriptor{ID: "fixture-host", Version: "v1", Kind: "deterministic"}
}

func (genericOrganizationExecutorFixture) Policy() contract.OrganizationExecutionPolicy {
	return contract.OrganizationExecutionPolicy{TimeoutSeconds: 10, MaxSubmissions: 1, MaxAttempts: 1, MaxTokens: 100}
}

func (genericOrganizationExecutorFixture) Validate(context.Context) error { return nil }

func (genericOrganizationExecutorFixture) Capacity(context.Context) error { return nil }

func (genericOrganizationExecutorFixture) Execute(context.Context, contract.OrganizationTask, contract.OrganizationSubmit, func(contract.OrganizationUsage) error) (contract.OrganizationUsage, error) {
	return contract.OrganizationUsage{}, nil
}

func TestOrganizationCapabilityUsesRegisteredGenericExecutor(t *testing.T) {
	result := &mcp.InitializeResult{Capabilities: &mcp.ServerCapabilities{Experimental: map[string]any{
		"ownward.deferred-organization": map[string]any{"version": 1, "mode": contract.DeferredOrganizationV1},
	}}}
	if got := organizationProxyCapabilities(result); got.Experimental != nil {
		t.Fatal("deferred organization advertised without a registered executor")
	}
	fixture := genericOrganizationExecutorFixture{}
	got := organizationProxyCapabilities(result, fixture)
	if _, ok := got.Experimental["ownward.deferred-organization"]; !ok {
		t.Fatal("deferred organization not advertised for a registered generic executor")
	}
}

func TestAttachOrganizationExecutorRegistersWithoutCodexProfile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	host := &hostConnector{}
	initialize := &mcp.InitializeResult{Capabilities: &mcp.ServerCapabilities{Experimental: map[string]any{
		"ownward.deferred-organization": map[string]any{"version": 1, "mode": contract.DeferredOrganizationV1},
	}}}
	stop := host.attachOrganizationExecutor(ctx, initialize, nil, genericOrganizationExecutorFixture{}, func(context.Context, string, any) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	if host.organizationExecutor == nil || host.organizationExecutor.Descriptor().ID != "fixture-host" {
		t.Fatal("generic executor was not registered")
	}
	cancel()
	stop()
}

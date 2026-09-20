package codexplugin

import (
	"context"
	"encoding/json"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// OrganizationExecutor is the Codex App Server adapter. Scheduling,
// persistence, leases and recovery live outside this package; this adapter
// only translates the generic executor port to the official Codex protocol.
type OrganizationExecutor struct {
	profile OrganizationProfile
	tool    *mcp.Tool
}

func NewOrganizationExecutor(profile OrganizationProfile, tool *mcp.Tool) *OrganizationExecutor {
	return &OrganizationExecutor{profile: profile, tool: tool}
}

func (e *OrganizationExecutor) Descriptor() contract.OrganizationExecutorDescriptor {
	return contract.OrganizationExecutorDescriptor{ID: "codex-app-server", Version: "v1", Kind: "local-process"}
}

func (e *OrganizationExecutor) Policy() contract.OrganizationExecutionPolicy {
	return e.profile.Policy()
}

func (e *OrganizationExecutor) Validate(context.Context) error { return e.profile.Validate() }

func (e *OrganizationExecutor) Capacity(context.Context) error {
	return OrganizationCapacity(e.profile)
}

func (e *OrganizationExecutor) Probe(ctx context.Context) error {
	return ProbeOrganization(ctx, e.profile, e.tool)
}

func (e *OrganizationExecutor) Execute(ctx context.Context, task contract.OrganizationTask, submit contract.OrganizationSubmit, progress func(contract.OrganizationUsage) error) (contract.OrganizationUsage, error) {
	usage, err := RunOrganization(ctx, e.profile, json.RawMessage(task.Work), e.tool, func(ctx context.Context, args json.RawMessage) (*mcp.CallToolResult, bool, error) {
		accepted, response, err := submit(ctx, append([]byte(nil), args...))
		if err != nil {
			return nil, false, err
		}
		var result mcp.CallToolResult
		if len(response) > 0 {
			if err := json.Unmarshal(response, &result); err != nil {
				return nil, false, err
			}
		}
		return &result, accepted, nil
	}, progress)
	usage.Executor = e.Descriptor()
	return usage, err
}

var _ contract.OrganizationExecutor = (*OrganizationExecutor)(nil)

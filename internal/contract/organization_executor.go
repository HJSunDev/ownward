package contract

import (
	"context"
	"errors"
	"time"
)

// OrganizationExecutionPolicy is the host-independent execution budget. It
// contains no process, model-provider, filesystem, or protocol settings.
// Those belong to an adapter and are never part of the generic runtime port.
type OrganizationExecutionPolicy struct {
	TimeoutSeconds int   `json:"timeout_seconds"`
	MaxSubmissions int   `json:"max_submissions"`
	MaxAttempts    int   `json:"max_attempts"`
	MaxTokens      int64 `json:"max_tokens"`
}

func (p OrganizationExecutionPolicy) Validate() error {
	if p.TimeoutSeconds < 1 || p.TimeoutSeconds > 1800 || p.MaxAttempts < 1 || p.MaxAttempts > 3 || p.MaxSubmissions < 1 || p.MaxSubmissions > 10 || p.MaxTokens < 1 {
		return errors.New("组织资源或原恢复预算无效")
	}
	return nil
}

// OrganizationExecutorDescriptor is stable identity for an execution
// backend. It is recorded with runtime evidence and does not select answers.
type OrganizationExecutorDescriptor struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
}

// OrganizationTask carries opaque, kernel-produced semantic work. The
// runtime never interprets its contents.
type OrganizationTask struct {
	AssetID    string
	Revision   uint64
	Generation string
	Work       []byte
}

type OrganizationUsage struct {
	Executor        OrganizationExecutorDescriptor `json:"executor"`
	Thread          string                         `json:"thread,omitempty"`
	Turn            string                         `json:"turn,omitempty"`
	Submissions     int                            `json:"submissions"`
	InputTokens     int64                          `json:"input_tokens"`
	OutputTokens    int64                          `json:"output_tokens"`
	TotalTokens     int64                          `json:"total_tokens"`
	ModelRequests   int                            `json:"model_requests"`
	UsageIncomplete bool                           `json:"usage_incomplete"`
	PeakBytes       uint64                         `json:"peak_bytes"`
	ProcessCount    uint32                         `json:"process_count"`
	Seconds         float64                        `json:"seconds"`
}

// OrganizationSubmit accepts opaque executor output and returns whether the
// kernel accepted the candidate. The callback owns semantic validation.
type OrganizationSubmit func(context.Context, []byte) (accepted bool, response []byte, err error)

// OrganizationExecutor is the only runtime boundary an organization backend
// must implement. Adapters may use MCP, a local model, a remote service, or a
// deterministic fixture behind this port.
type OrganizationExecutor interface {
	Descriptor() OrganizationExecutorDescriptor
	Policy() OrganizationExecutionPolicy
	Validate(context.Context) error
	Capacity(context.Context) error
	Execute(context.Context, OrganizationTask, OrganizationSubmit, func(OrganizationUsage) error) (OrganizationUsage, error)
}

func (p OrganizationExecutionPolicy) Timeout() time.Duration {
	if p.TimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(p.TimeoutSeconds) * time.Second
}

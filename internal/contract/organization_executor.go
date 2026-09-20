package contract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// OrganizationExecutorProfile describes an approved execution environment.
// It is configuration owned by the host, never semantic input produced by an
// external model. The contract deliberately contains no MCP or vendor types.
type OrganizationExecutorProfile struct {
	Executable     string `json:"executable"`
	Home           string `json:"home"`
	Model          string `json:"model"`
	Provider       string `json:"provider"`
	Effort         string `json:"effort"`
	Reservation    string `json:"reservation"`
	MemoryMiB      int    `json:"memory_mib"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxSubmissions int    `json:"max_submissions"`
	MaxAttempts    int    `json:"max_attempts"`
	MaxTokens      int64  `json:"max_tokens"`
}

func (p OrganizationExecutorProfile) Validate() error {
	if !filepath.IsAbs(p.Executable) || !filepath.IsAbs(p.Home) || p.Model == "" || p.Provider == "" || p.Effort == "" || p.Reservation == "" {
		return errors.New("后台组织须明确执行器、独立宿主配置、原模型档位及已验证资源预留")
	}
	if p.MemoryMiB < 64 || p.MemoryMiB > 2048 || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 1800 || p.MaxAttempts < 1 || p.MaxAttempts > 3 || p.MaxSubmissions < 1 || p.MaxSubmissions > 10 || p.MaxTokens < 1 {
		return errors.New("组织资源或原恢复预算无效")
	}
	if _, err := os.Stat(p.Executable); err != nil {
		return err
	}
	if info, err := os.Stat(p.Home); err != nil {
		return err
	} else if !info.IsDir() {
		return errors.New("组织宿主目录无效")
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
	Profile() OrganizationExecutorProfile
	Validate(context.Context) error
	Capacity(context.Context) error
	Execute(context.Context, OrganizationTask, OrganizationSubmit, func(OrganizationUsage) error) (OrganizationUsage, error)
}

func (p OrganizationExecutorProfile) Timeout() time.Duration {
	if p.TimeoutSeconds <= 0 {
		return 0
	}
	return time.Duration(p.TimeoutSeconds) * time.Second
}

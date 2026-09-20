package organization

import (
	"context"

	"github.com/HJSunDev/ownward/internal/contract"
)

// Execution describes one immutable kernel work item. Runtime owns the
// durable budget and idempotency boundary; the executor owns only model/tool
// protocol translation.
type Execution struct {
	Scope  string
	Key    string
	Task   contract.OrganizationTask
	Policy contract.OrganizationExecutionPolicy
	Prior  int64
}

type RunResult struct {
	Usage    contract.OrganizationUsage
	Allowed  bool
	Accepted bool
}

type Runtime struct {
	Journal  *Journal
	Executor contract.OrganizationExecutor
}

func (r *Runtime) Run(ctx context.Context, execution Execution, submit contract.OrganizationSubmit) (RunResult, error) {
	if r == nil || r.Journal == nil || r.Executor == nil {
		return RunResult{}, ErrUnavailable
	}
	allowed, prior, err := r.Journal.Begin(ctx, execution.Scope, execution.Key, execution.Policy)
	if err != nil || !allowed {
		return RunResult{Allowed: allowed}, err
	}
	if execution.Prior > 0 {
		prior = execution.Prior
	}
	return r.runAllowed(ctx, execution, prior, submit)
}

// RunReserved executes after the caller has atomically claimed a budget entry
// with Journal.Begin. This keeps retry accounting and the executor protocol in
// one reusable runtime while allowing callers to retain a precise admission
// decision for status and recovery handling.
func (r *Runtime) RunReserved(ctx context.Context, execution Execution, prior int64, submit contract.OrganizationSubmit) (RunResult, error) {
	if r == nil || r.Journal == nil || r.Executor == nil {
		return RunResult{}, ErrUnavailable
	}
	return r.runAllowed(ctx, execution, prior, submit)
}

func (r *Runtime) runAllowed(ctx context.Context, execution Execution, prior int64, submit contract.OrganizationSubmit) (RunResult, error) {
	progress := func(usage contract.OrganizationUsage) error {
		usage.Executor = r.Executor.Descriptor()
		return r.Journal.Save(ctx, execution.Scope, execution.Key, "running", prior, usage)
	}
	acceptedByKernel := false
	wrappedSubmit := func(submitCtx context.Context, payload []byte) (bool, []byte, error) {
		accepted, response, submitErr := submit(submitCtx, payload)
		acceptedByKernel = acceptedByKernel || accepted
		return accepted, response, submitErr
	}
	usage, execErr := r.Executor.Execute(ctx, execution.Task, wrappedSubmit, progress)
	usage.Executor = r.Executor.Descriptor()
	status := "incomplete"
	accepted := acceptedByKernel && execErr == nil
	if accepted {
		status = "accepted"
	} else if execErr != nil {
		status = "failed"
	}
	if err := r.Journal.Save(ctx, execution.Scope, execution.Key, status, prior, usage); err != nil {
		return RunResult{Usage: usage, Allowed: true, Accepted: accepted}, err
	}
	return RunResult{Usage: usage, Allowed: true, Accepted: accepted}, execErr
}

var ErrUnavailable = &runtimeError{"organization executor unavailable"}

type runtimeError struct{ message string }

func (e *runtimeError) Error() string { return e.message }

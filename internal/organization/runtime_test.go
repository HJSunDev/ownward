package organization

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/HJSunDev/ownward/internal/contract"
)

type fixtureExecutor struct{}

func (fixtureExecutor) Descriptor() contract.OrganizationExecutorDescriptor {
	return contract.OrganizationExecutorDescriptor{ID: "fixture", Version: "v1", Kind: "deterministic"}
}
func (fixtureExecutor) Policy() contract.OrganizationExecutionPolicy {
	return contract.OrganizationExecutionPolicy{TimeoutSeconds: 10, MaxSubmissions: 2, MaxAttempts: 2, MaxTokens: 100}
}
func (fixtureExecutor) Validate(context.Context) error { return nil }
func (fixtureExecutor) Capacity(context.Context) error { return nil }
func (fixtureExecutor) Execute(_ context.Context, task contract.OrganizationTask, submit contract.OrganizationSubmit, progress func(contract.OrganizationUsage) error) (contract.OrganizationUsage, error) {
	usage := contract.OrganizationUsage{TotalTokens: 3}
	if err := progress(usage); err != nil {
		return usage, err
	}
	accepted, _, err := submit(context.Background(), task.Work)
	if accepted {
		usage.TotalTokens = 5
	}
	return usage, err
}

func TestRuntimeOwnsDurableExecutionAndUsesGenericExecutor(t *testing.T) {
	j, err := OpenJournal(t.TempDir(), "fixture-client")
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	runtime := &Runtime{Journal: j, Executor: fixtureExecutor{}}
	profile := contract.OrganizationExecutionPolicy{TimeoutSeconds: 10, MaxSubmissions: 2, MaxAttempts: 2, MaxTokens: 100}
	result, err := runtime.Run(context.Background(), Execution{
		Scope:  "system:principal",
		Key:    "work-1",
		Policy: profile,
		Task:   contract.OrganizationTask{AssetID: "asset-1", Work: []byte(`{"candidate":true}`)},
	}, func(context.Context, []byte) (bool, []byte, error) {
		return true, []byte(`{"accepted":true}`), nil
	})
	if err != nil || !result.Accepted || result.Usage.Executor.ID != "fixture" {
		t.Fatalf("generic runtime did not accept fixture execution: %#v %v", result, err)
	}
	var status string
	if err := j.DB().QueryRow(`SELECT status FROM executions WHERE scope=? AND work=?`, "system:principal", "work-1").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "accepted" {
		t.Fatalf("durable status = %q", status)
	}
	var encoded string
	if err := j.DB().QueryRow(`SELECT usage FROM executions WHERE scope=? AND work=?`, "system:principal", "work-1").Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var usage contract.OrganizationUsage
	if err := json.Unmarshal([]byte(encoded), &usage); err != nil || usage.Executor.ID != "fixture" {
		t.Fatalf("durable executor identity missing: %q %v", encoded, err)
	}
}

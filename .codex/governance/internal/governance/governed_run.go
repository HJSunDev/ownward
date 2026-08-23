package governance

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type GovernedRunOptions struct {
	Command       []string
	HeartbeatPath string
	StaleAfter    time.Duration
	StartupGrace  time.Duration
	WorkingDir    string
}

// GovernedRun remains available for isolated process-control tests. The CLI
// uses Runtime.GovernedRun so a real governed task can bind failure events.
func GovernedRun(options GovernedRunOptions) error {
	return (&Runtime{}).GovernedRun(options)
}

func (runtime *Runtime) GovernedRun(options GovernedRunOptions) error {
	if len(options.Command) == 0 || options.HeartbeatPath == "" {
		return errors.New("governed-run requires a command and heartbeat path")
	}
	if options.StaleAfter <= 0 || options.StartupGrace <= 0 {
		return errors.New("governed-run requires positive heartbeat staleness and startup grace")
	}
	heartbeat, err := filepath.Abs(options.HeartbeatPath)
	if err != nil {
		return err
	}
	command := exec.Command(options.Command[0], options.Command[1:]...)
	command.Dir = options.WorkingDir
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.Stdin = os.Stdin
	configureProcess(command)
	startedAt := time.Now()
	executionID := newID("execution")
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	ticker := time.NewTicker(minDuration(options.StaleAfter/3, 5*time.Second))
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				if recordErr := runtime.recordGovernedRunFailure(executionID, "governed process failed: "+err.Error(), options); recordErr != nil {
					return errors.Join(err, recordErr)
				}
			}
			return err
		case <-ticker.C:
			info, statErr := os.Stat(heartbeat)
			if statErr == nil && time.Since(info.ModTime()) <= options.StaleAfter {
				continue
			}
			if time.Since(startedAt) <= options.StartupGrace {
				continue
			}
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				_ = stopProcessTree(command)
				failure := fmt.Errorf("cannot inspect governed heartbeat: %w", statErr)
				if recordErr := runtime.recordGovernedRunFailure(executionID, failure.Error(), options); recordErr != nil {
					return errors.Join(failure, recordErr)
				}
				return failure
			}
			_ = stopProcessTree(command)
			<-done
			failure := fmt.Errorf("governed process lost heartbeat %s; existing checkpoints were preserved", heartbeat)
			if recordErr := runtime.recordGovernedRunFailure(executionID, failure.Error(), options); recordErr != nil {
				return errors.Join(failure, recordErr)
			}
			return failure
		}
	}
}

func (runtime *Runtime) recordGovernedRunFailure(executionID, message string, options GovernedRunOptions) error {
	if !runtime.StateExists() {
		return nil
	}
	evidence, err := hashJSON(map[string]any{"execution_id": executionID, "message": message, "command": options.Command, "working_dir": options.WorkingDir})
	if err != nil {
		return err
	}
	_, err = runtime.RecordFailureEvent(FailureEventInput{
		Signature: message, SourceKind: "governed_run", SourceExecution: executionID,
		ToolUseID: executionID, EvidenceHash: evidence,
	})
	if err == nil {
		return nil
	}
	if reviewErr := runtime.ensureFailureRecordingReview(); reviewErr != nil {
		return errors.Join(fmt.Errorf("governance failure event was not recorded: %w", err), reviewErr)
	}
	return fmt.Errorf("governance failure event was not recorded; an integrity review was requested: %w", err)
}

func minDuration(left, right time.Duration) time.Duration {
	if left <= 0 {
		return time.Second
	}
	if left < right {
		return left
	}
	return right
}

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

func GovernedRun(options GovernedRunOptions) error {
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
				return fmt.Errorf("cannot inspect governed heartbeat: %w", statErr)
			}
			_ = stopProcessTree(command)
			<-done
			return fmt.Errorf("governed process lost heartbeat %s; existing checkpoints were preserved", heartbeat)
		}
	}
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

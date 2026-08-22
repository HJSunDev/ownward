//go:build windows

package governance

import (
	"fmt"
	"os/exec"
)

func configureProcess(command *exec.Cmd) {}

func stopProcessTree(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprintf("%d", command.Process.Pid))
	return kill.Run()
}

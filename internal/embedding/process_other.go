//go:build !windows

package embedding

import (
	"io"
	"os/exec"
)

func hideProcessWindow(_ *exec.Cmd) {}

func attachProcessLifetime(_ *exec.Cmd) (io.Closer, error) { return nil, nil }

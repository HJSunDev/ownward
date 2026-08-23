//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package governance

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

type stateLock struct{ file *os.File }

func acquireStateLock(path string, timeout time.Duration) (*stateLock, error) {
	deadline := time.Now().Add(timeout)
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			_ = file.Truncate(0)
			_, _ = file.Seek(0, 0)
			_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
			_ = file.Sync()
			return &stateLock{file: file}, nil
		}
		_ = file.Close()
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("governance state is busy; another live writer holds the operating-system lock: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (lock *stateLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

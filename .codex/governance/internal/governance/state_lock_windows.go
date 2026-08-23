//go:build windows

package governance

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

type stateLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquireStateLock(path string, timeout time.Duration) (*stateLock, error) {
	deadline := time.Now().Add(timeout)
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		lock := &stateLock{file: file}
		err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &lock.overlapped)
		if err == nil {
			if truncateErr := file.Truncate(0); truncateErr != nil {
				_ = lock.release()
				return nil, truncateErr
			}
			_, _ = file.Seek(0, 0)
			_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
			_ = file.Sync()
			return lock, nil
		}
		_ = file.Close()
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) || time.Now().After(deadline) {
			return nil, fmt.Errorf("governance state is busy; another live writer holds the operating-system lock: %w", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (lock *stateLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := windows.UnlockFileEx(windows.Handle(lock.file.Fd()), 0, 1, 0, &lock.overlapped)
	closeErr := lock.file.Close()
	lock.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

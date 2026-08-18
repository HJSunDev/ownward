//go:build windows

package assetlog

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type directoryLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquireDirectoryLock(path string) (*directoryLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	lock := &directoryLock{file: file}
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &lock.overlapped,
	)
	if err != nil {
		_ = file.Close()
		return nil, errors.New("目录正由另一个 Ownward 进程使用")
	}
	return lock, nil
}

func (l *directoryLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &l.overlapped)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

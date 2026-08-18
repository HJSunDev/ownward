//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package assetlog

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type directoryLock struct {
	file *os.File
}

func acquireDirectoryLock(path string) (*directoryLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, errors.New("目录正由另一个 Ownward 进程使用")
	}
	return &directoryLock{file: file}, nil
}

func (l *directoryLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

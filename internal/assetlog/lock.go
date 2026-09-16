package assetlog

import "io"

// LockDirectory 使新存储与旧格式共享同一文件锁；调用者负责目录与生命周期。
func LockDirectory(path string) (io.Closer, error) {
	l, err := acquireDirectoryLock(path)
	if err != nil {
		return nil, err
	}
	return directoryLease{l}, nil
}

type directoryLease struct{ lock *directoryLock }

func (l directoryLease) Close() error { return l.lock.release() }

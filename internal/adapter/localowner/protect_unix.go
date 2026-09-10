//go:build !windows

package localowner

import "os"

func protect(data []byte) ([]byte, error)        { return data, nil }
func protectMachine(data []byte) ([]byte, error) { return protect(data) }
func ProtectServiceDirectory(path string) error  { return os.Chmod(path, 0700) }
func unprotect(data []byte) ([]byte, error)      { return data, nil }
func replace(from, to string) error              { return os.Rename(from, to) }

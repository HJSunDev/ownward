//go:build !windows

package localowner

import "os"

func protect(data []byte) ([]byte, error)   { return data, nil }
func unprotect(data []byte) ([]byte, error) { return data, nil }
func replace(from, to string) error         { return os.Rename(from, to) }

//go:build !windows

package main

import "golang.org/x/sys/unix"

func availableSpace(path string) (uint64, error) {
	var state unix.Statfs_t
	if err := unix.Statfs(path, &state); err != nil {
		return 0, err
	}
	return uint64(state.Bavail) * uint64(state.Bsize), nil
}

//go:build windows

package main

import "golang.org/x/sys/windows"

func availableSpace(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, allFree uint64
	err = windows.GetDiskFreeSpaceEx(p, &free, &total, &allFree)
	return free, err
}

package resourcebudget

import (
	"golang.org/x/sys/windows"
	"path/filepath"
)

func freeBytes(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(filepath.Dir(path))
	if err != nil {
		return 0, err
	}
	var available uint64
	err = windows.GetDiskFreeSpaceEx(p, &available, nil, nil)
	return available, err
}

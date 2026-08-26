//go:build !windows

package assetlog

import "os"

func replaceLogFile(source, target string) error {
	return os.Rename(source, target)
}

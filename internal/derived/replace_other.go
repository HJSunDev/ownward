//go:build !windows

package derived

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}

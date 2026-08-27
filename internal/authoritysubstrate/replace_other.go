//go:build !windows

package authoritysubstrate

import "os"

func replaceControlFile(source, target string) error {
	return os.Rename(source, target)
}

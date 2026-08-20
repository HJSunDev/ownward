//go:build !windows

package embedding

import "os"

func replaceAcceptanceFile(source, target string) error {
	return os.Rename(source, target)
}

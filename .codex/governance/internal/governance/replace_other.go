//go:build !windows

package governance

import "os"

func replaceFile(source, target string) error { return os.Rename(source, target) }

//go:build !windows

package boundedstore

import (
	"errors"
	"os"
	"path/filepath"
)

func replaceDurable(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

package resourcebudget

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// RecoverScratch requires exclusive ownership of this directory. It removes
// only known legacy scratch files, never user documents, subdirectories or links.
func RecoverScratch(dir string) error {
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	for {
		entries, e := d.ReadDir(64)
		if e != nil && !errors.Is(e, io.EOF) {
			return e
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				continue
			}
			known := false
			for _, prefix := range []string{"rpc-body-", "rpc-index-", "selector-", "basis-order-", "semantic-source-", "evidence-", "name-word-"} {
				if !strings.HasPrefix(entry.Name(), prefix) {
					continue
				}
				suffix := strings.TrimPrefix(entry.Name(), prefix)
				if suffix != "" && strings.Trim(suffix, "0123456789") == "" {
					known = true
				}
				break
			}
			if known {
				if e := os.Remove(filepath.Join(dir, entry.Name())); e != nil && !errors.Is(e, os.ErrNotExist) {
					return e
				}
			}
		}
		if errors.Is(e, io.EOF) {
			return nil
		}
	}
}

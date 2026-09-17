//go:build !windows

package resourcebudget

import "os"

func ephemeralFile(dir, prefix string) (*os.File, error) {
	f, e := os.CreateTemp(dir, prefix)
	if e != nil {
		return nil, e
	}
	if e = os.Remove(f.Name()); e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}

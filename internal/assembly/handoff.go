package assembly

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/derived"
)

// Verify the selected kernel's derived format at the composition boundary;
// the authority store does not depend on any particular kernel implementation.
func ValidateHandoffData(destination string) error {
	path := filepath.Join(destination, "state")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	d, err := derived.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	if d.RecoveredCorruption() {
		return errors.New("迁移派生状态不完整")
	}
	_, err = d.AllWithEmbeddings()
	return err
}

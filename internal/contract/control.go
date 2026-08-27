package contract

import (
	"errors"
	"strings"
)

const ControlStateSchema = "ownward.control-state/v1"

// ControlState is the minimum future authority decision. This work package
// defines and validates it but does not persist or activate it.
type ControlState struct {
	Schema                 string `json:"schema"`
	Revision               uint64 `json:"revision"`
	ActiveComposition      string `json:"active_composition"`
	ActiveKernelGeneration string `json:"active_kernel_generation"`
}

func (s ControlState) Validate() error {
	if s.Schema != ControlStateSchema || s.Revision == 0 || strings.TrimSpace(s.ActiveComposition) == "" || strings.TrimSpace(s.ActiveKernelGeneration) == "" {
		return errors.New("权威控制状态无效")
	}
	return nil
}

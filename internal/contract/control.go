package contract

import (
	"errors"
	"strings"
)

const ControlStateSchema = "ownward.control-state/v1"

// ControlState is the minimum durable authority decision. It deliberately does
// not contain process, network, candidate-promotion or Acceptance state. The
// current product has no established authorization decisions, so v1 does not
// invent an authorization field; if such a product decision is established,
// only the authority substrate may persist and execute it through a versioned
// control contract.
type ControlState struct {
	Schema                 string `json:"schema"`
	Revision               uint64 `json:"revision"`
	ActiveComposition      string `json:"active_composition"`
	ActiveKernelGeneration string `json:"active_kernel_generation"`
}

// ControlAuthority owns the one durable control decision. Mutations use a
// compare-and-swap revision so concurrent or stale decisions fail explicitly.
type ControlAuthority interface {
	ReadControl() ControlState
	CompareAndSwapControl(expectedRevision uint64, next ControlState) (ControlState, error)
}

func (s ControlState) Validate() error {
	if s.Schema != ControlStateSchema || s.Revision == 0 || strings.TrimSpace(s.ActiveComposition) == "" || strings.TrimSpace(s.ActiveKernelGeneration) == "" {
		return errors.New("权威控制状态无效")
	}
	return nil
}

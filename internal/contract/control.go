package contract

import (
	"errors"
	"strings"
)

const ControlStateSchema = "ownward.control-state/v1"
const UserControlStateSchema = "ownward.control-state/v2"

// ControlState 保存唯一权威决定；启用用户控制后使用 v2，旧程序不能忽略权限继续打开。
type ControlState struct {
	ReadError              error                    `json:"-"`
	Stopping               bool                     `json:"-"`
	Schema                 string                   `json:"schema"`
	Revision               uint64                   `json:"revision"`
	ActiveComposition      string                   `json:"active_composition"`
	ActiveKernelGeneration string                   `json:"active_kernel_generation"`
	InformationControl     *InformationControlState `json:"information_control,omitempty"`
	Access                 *AccessState             `json:"access,omitempty"`
}

// ControlSelection selects the identities used by one control decision.
// Omitted records remain authoritative and are not deleted by a scoped commit.
type ControlSelection struct {
	Credential, Principal, Operation, Handoff, Enrollment string
	Principals                                            bool
	Enrollments                                           bool // load only the bounded enrollment identities
	Pending                                               string
	After                                                 string
	Limit                                                 int
	RelatedPrincipals                                     []string // bounded dependencies of an in-flight management commit
}
type SelectedControlAuthority interface {
	ReadSelectedControl(ControlSelection) (ControlState, error)
}

// ControlAuthority owns the one durable control decision. Mutations use a
// compare-and-swap revision so concurrent or stale decisions fail explicitly.
type ControlAuthority interface {
	ReadControl() ControlState
	CompareAndSwapControl(expectedRevision uint64, next ControlState) (ControlState, error)
}

func (s ControlState) Validate() error {
	if (s.Schema != ControlStateSchema && s.Schema != UserControlStateSchema && s.Schema != AccessControlStateSchema) || s.Revision == 0 || strings.TrimSpace(s.ActiveComposition) == "" || strings.TrimSpace(s.ActiveKernelGeneration) == "" {
		return errors.New("权威控制状态无效")
	}
	if s.InformationControl != nil {
		if s.Schema != UserControlStateSchema && s.Schema != AccessControlStateSchema {
			return errors.New("用户控制状态必须采用 v2 契约")
		}
		if s.Schema == AccessControlStateSchema {
			if s.Access == nil {
				return errors.New("跨地点控制状态缺失")
			}
			if err := s.Access.Validate(); err != nil {
				return err
			}
		} else if s.Access != nil {
			return errors.New("跨地点状态必须采用 v3 契约")
		}
		return s.InformationControl.Validate()
	}
	if s.Schema == UserControlStateSchema || s.Schema == AccessControlStateSchema || s.Access != nil {
		return errors.New("用户控制状态缺失")
	}
	return nil
}

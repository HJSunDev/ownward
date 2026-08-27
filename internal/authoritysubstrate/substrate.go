package authoritysubstrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/authorityport"
	"github.com/HJSunDev/ownward/internal/contract"
)

const (
	controlEnvelopeSchema = "ownward.control-state-envelope/v1"
	controlDirectory      = "authority"
	controlFile           = "control.json"
)

// Substrate is the single runtime owner of the concrete asset store and the
// minimum durable control decision. Kernels receive only stable contracts.
type Substrate struct {
	assets  *assetlog.Store
	port    *authorityport.Current
	control *controlStore
	once    sync.Once
	err     error
}

var _ contract.ControlAuthority = (*controlStore)(nil)
var _ contract.AuthoritySubstrate = (*Substrate)(nil)

func Open(dataDir string, initialState contract.ControlState) (*Substrate, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, errors.New("权威基座数据目录必须是绝对路径")
	}
	if err := initialState.Validate(); err != nil {
		return nil, fmt.Errorf("初始化权威控制状态: %w", err)
	}
	controlPath := filepath.Join(filepath.Clean(dataDir), controlDirectory, controlFile)
	var control *controlStore
	var err error
	if _, statErr := os.Stat(controlPath); statErr == nil {
		control, err = openControl(filepath.Dir(controlPath), initialState)
		if err != nil {
			return nil, err
		}
		current := control.ReadControl()
		if current.ActiveComposition != initialState.ActiveComposition || current.ActiveKernelGeneration != initialState.ActiveKernelGeneration {
			return nil, errors.New("权威控制状态与当前已校验组合不一致")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("检查权威控制状态: %w", statErr)
	}
	assets, err := assetlog.Open(filepath.Join(filepath.Clean(dataDir), "assets"))
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Substrate, error) {
		_ = assets.Close()
		return nil, err
	}
	port, err := authorityport.Bind(assets)
	if err != nil {
		return fail(err)
	}
	if control == nil {
		control, err = openControl(filepath.Join(filepath.Clean(dataDir), controlDirectory), initialState)
		if err != nil {
			return fail(err)
		}
	}
	return &Substrate{assets: assets, port: port, control: control}, nil
}

func (s *Substrate) Assets() contract.AssetAuthority {
	if s == nil {
		return nil
	}
	return s.port
}

func (s *Substrate) Control() contract.ControlAuthority {
	if s == nil {
		return nil
	}
	return s.control
}

func (s *Substrate) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.assets != nil {
			s.err = s.assets.Close()
		}
	})
	return s.err
}

type controlEnvelope struct {
	Schema string                `json:"schema"`
	State  contract.ControlState `json:"state"`
	SHA256 string                `json:"sha256"`
}

type controlStore struct {
	mu    sync.RWMutex
	path  string
	state contract.ControlState
}

func openControl(dir string, initial contract.ControlState) (*controlStore, error) {
	path := filepath.Join(dir, controlFile)
	encoded, err := os.ReadFile(path)
	if err == nil {
		state, decodeErr := decodeControl(encoded)
		if decodeErr != nil {
			return nil, decodeErr
		}
		return &controlStore{path: path, state: state}, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取权威控制状态: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建权威控制目录: %w", err)
	}
	store := &controlStore{path: path, state: initial}
	if err := store.write(initial, true); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *controlStore) ReadControl() contract.ControlState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *controlStore) CompareAndSwapControl(expectedRevision uint64, next contract.ControlState) (contract.ControlState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedRevision != s.state.Revision {
		return contract.ControlState{}, fmt.Errorf("权威控制状态已更新，当前修订为 %d", s.state.Revision)
	}
	if next.Revision != expectedRevision+1 {
		return contract.ControlState{}, errors.New("权威控制状态修订不连续")
	}
	if err := next.Validate(); err != nil {
		return contract.ControlState{}, err
	}
	if err := s.write(next, false); err != nil {
		return contract.ControlState{}, err
	}
	s.state = next
	return next, nil
}

func (s *controlStore) write(state contract.ControlState, createOnly bool) error {
	encoded, err := encodeControl(state)
	if err != nil {
		return err
	}
	parent := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(parent, ".control-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时控制状态: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		return fmt.Errorf("写入权威控制状态: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("持久化权威控制状态: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if createOnly {
		if _, err := os.Stat(s.path); err == nil {
			return errors.New("权威控制状态已由并发初始化建立")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := replaceControlFile(temporaryPath, s.path); err != nil {
		return fmt.Errorf("提交权威控制状态: %w", err)
	}
	committed = true
	return nil
}

func encodeControl(state contract.ControlState) ([]byte, error) {
	if err := state.Validate(); err != nil {
		return nil, err
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(stateJSON)
	encoded, err := json.MarshalIndent(controlEnvelope{Schema: controlEnvelopeSchema, State: state, SHA256: hex.EncodeToString(digest[:])}, "", "  ")
	return append(encoded, '\n'), err
}

func decodeControl(encoded []byte) (contract.ControlState, error) {
	var envelope controlEnvelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return contract.ControlState{}, fmt.Errorf("解析权威控制状态: %w", err)
	}
	if envelope.Schema != controlEnvelopeSchema || envelope.State.Validate() != nil {
		return contract.ControlState{}, errors.New("权威控制状态格式无效")
	}
	stateJSON, err := json.Marshal(envelope.State)
	if err != nil {
		return contract.ControlState{}, err
	}
	digest := sha256.Sum256(stateJSON)
	if envelope.SHA256 != hex.EncodeToString(digest[:]) {
		return contract.ControlState{}, errors.New("权威控制状态完整性校验失败")
	}
	return envelope.State, nil
}

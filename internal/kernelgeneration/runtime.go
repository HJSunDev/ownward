package kernelgeneration

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
)

type Mode string

const (
	Basic         Mode = "basic"
	Organized     Mode = "organized"
	Collaborative Mode = "collaborative"
)

type OpenRequest struct {
	DataDir          string
	Mode             Mode
	Authority        contract.AssetAuthority
	SemanticProvider semantics.Provider
	VectorProvider   embedding.Provider
}

// Open selects one complete, already sealed kernel component and opens its
// organization, representation, retrieval, derived storage, execution,
// indexing and degradation behavior as one in-process unit. Assembly supplies
// only stable external ports; it cannot choose individual internal parts.
func Open(manifest composition.Manifest, request OpenRequest) (*core.Service, error) {
	if _, err := composition.VerifySealed(manifest); err != nil {
		return nil, fmt.Errorf("校验内核所属组合: %w", err)
	}
	kernel, exists := component(manifest, "kernel")
	if !exists {
		return nil, errors.New("组合缺少完整内核世代")
	}
	if mode, _ := kernel.Config["mode"].(string); strings.TrimSpace(mode) != string(request.Mode) {
		return nil, fmt.Errorf("内核世代语义与选择不一致: 需要 %s", request.Mode)
	}
	if request.Authority == nil {
		return nil, errors.New("内核世代缺少资产权威端口")
	}
	if strings.TrimSpace(request.DataDir) == "" || !filepath.IsAbs(request.DataDir) {
		return nil, errors.New("内核世代数据目录必须是明确绝对路径")
	}
	switch request.Mode {
	case Basic:
		if request.SemanticProvider != nil || request.VectorProvider != nil {
			return nil, errors.New("basic 内核不得拼接语义或向量能力")
		}
		return core.NewWithAuthority(request.Authority)
	case Organized:
		if request.SemanticProvider == nil || request.VectorProvider != nil {
			return nil, errors.New("organized 内核必须且只能绑定语义能力")
		}
		store, err := derived.Open(filepath.Join(request.DataDir, "state"))
		if err != nil {
			return nil, err
		}
		service, err := core.NewOrganizedWithAuthority(request.Authority, store, request.SemanticProvider)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		return service, nil
	case Collaborative:
		if request.SemanticProvider != nil || request.VectorProvider == nil {
			return nil, errors.New("collaborative 内核必须且只能绑定向量能力")
		}
		store, err := derived.Open(filepath.Join(request.DataDir, "state"))
		if err != nil {
			return nil, err
		}
		service, err := core.NewCollaborativeWithAuthority(request.Authority, store, request.VectorProvider)
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		return service, nil
	default:
		return nil, fmt.Errorf("未知内核产品语义: %q", request.Mode)
	}
}

func component(manifest composition.Manifest, role string) (composition.Component, bool) {
	for _, value := range manifest.Components {
		if value.Role == role {
			return value, true
		}
	}
	return composition.Component{}, false
}

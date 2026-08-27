package assembly

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/HJSunDev/ownward/internal/assetlog"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/derived"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/semantics"
	compositionv1 "github.com/HJSunDev/ownward/manifests/compositions/v1"
)

type ProductSemantics string

const (
	Basic         ProductSemantics = "basic"
	Organized     ProductSemantics = "organized"
	Collaborative ProductSemantics = "collaborative"
)

const (
	activeAssemblyEntry      = "internal/assembly.Open"
	activeAssemblyActivation = "unique-explicit-entry"
)

// Request contains every choice that can change the product assembled by
// Open. ProductSemantics is mandatory; providers cannot select a mode by being
// nil, and no boolean or constructor default participates in the decision.
type Request struct {
	DataDir           string
	RestoreBackup     string
	ProductSemantics  ProductSemantics
	OrganizedProvider semantics.Provider
	VectorBundleDir   string
}

type Runtime struct {
	service      *core.Service
	backup       func(string) error
	verification composition.Verification
	semantics    ProductSemantics
	closeOnce    sync.Once
	closeErr     error
}

func (r *Runtime) Service() *core.Service {
	if r == nil {
		return nil
	}
	return r.service
}

func (r *Runtime) Composition() composition.Verification {
	if r == nil {
		return composition.Verification{}
	}
	return r.verification
}

func (r *Runtime) ProductSemantics() ProductSemantics {
	if r == nil {
		return ""
	}
	return r.semantics
}

func (r *Runtime) Backup(destination string) error {
	if r == nil || r.backup == nil {
		return errors.New("装配运行时尚未打开资产权威")
	}
	return r.backup(destination)
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.service != nil {
			r.closeErr = r.service.Close()
		}
	})
	return r.closeErr
}

type resources struct {
	restore     func(string, string) error
	openAssets  func(string) (*assetlog.Store, error)
	openDerived func(string) (*derived.Store, error)
	openVector  func(string, composition.Manifest) (embedding.Provider, error)
}

var productionResources = resources{
	restore:     assetlog.Restore,
	openAssets:  assetlog.Open,
	openDerived: derived.Open,
	openVector:  openProductionVector,
}

// Open is the sole active product assembly entry. It validates the complete
// composition and its explicit product semantics before restoring or opening
// any product resource.
func Open(request Request) (*Runtime, error) {
	manifest, err := currentManifest()
	if err != nil {
		return nil, err
	}
	return openWith(request, manifest, productionResources)
}

// Verify validates the release-embedded composition without discovering or
// reading a source repository. The actual service still enters through Open.
func Verify(productSemantics ProductSemantics) (composition.Verification, error) {
	manifest, err := currentManifest()
	if err != nil {
		return composition.Verification{}, err
	}
	return verifyComposition(manifest, productSemantics)
}

// PreflightSharedConnector validates only the sealed release identity and the
// adjacent vector manifest needed to coordinate a shared service. It confirms
// every declared technical path exists, but deliberately leaves full artifact
// hashing to the service process that owns the product runtime through Open.
func PreflightSharedConnector(productSemantics ProductSemantics, vectorBundleDir string) (composition.Verification, error) {
	manifest, err := currentManifest()
	if err != nil {
		return composition.Verification{}, err
	}
	verification, err := verifyComposition(manifest, productSemantics)
	if err != nil {
		return composition.Verification{}, err
	}
	if productSemantics == Collaborative {
		vectorBundleDir, err = requireAbsolute("向量能力目录", vectorBundleDir)
		if err != nil {
			return composition.Verification{}, err
		}
		if _, err := inspectProductionVector(vectorBundleDir, manifest); err != nil {
			return composition.Verification{}, err
		}
	}
	return verification, nil
}

func openWith(request Request, manifest composition.Manifest, resource resources) (*Runtime, error) {
	normalized, err := validateRequest(request, resource)
	if err != nil {
		return nil, err
	}
	verification, err := verifyComposition(manifest, normalized.ProductSemantics)
	if err != nil {
		return nil, err
	}

	var vector embedding.Provider
	vectorOwned := false
	if normalized.ProductSemantics == Collaborative {
		vector, err = resource.openVector(normalized.VectorBundleDir, manifest)
		if err != nil {
			return nil, err
		}
		if vector == nil {
			return nil, errors.New("显式 collaborative 向量能力不能为空")
		}
		defer func() {
			if !vectorOwned {
				_ = vector.Close()
			}
		}()
	}

	assetDir := filepath.Join(normalized.DataDir, "assets")
	if normalized.RestoreBackup != "" {
		if err := resource.restore(normalized.RestoreBackup, assetDir); err != nil {
			return nil, err
		}
	}
	store, err := resource.openAssets(assetDir)
	if err != nil {
		return nil, err
	}
	closeStore := true
	defer func() {
		if closeStore {
			_ = store.Close()
		}
	}()

	var service *core.Service
	switch normalized.ProductSemantics {
	case Basic:
		service = core.New(store)
	case Organized, Collaborative:
		derivedStore, openErr := resource.openDerived(filepath.Join(normalized.DataDir, "state"))
		if openErr != nil {
			return nil, openErr
		}
		closeDerived := true
		defer func() {
			if closeDerived {
				_ = derivedStore.Close()
			}
		}()
		if normalized.ProductSemantics == Organized {
			service, err = core.NewOrganized(store, derivedStore, normalized.OrganizedProvider)
		} else {
			service, err = core.NewCollaborative(store, derivedStore, vector)
		}
		if err != nil {
			return nil, err
		}
		closeDerived = false
	default:
		panic("validated product semantics became invalid")
	}
	closeStore = false
	vectorOwned = normalized.ProductSemantics == Collaborative
	return &Runtime{service: service, backup: store.Backup, verification: verification, semantics: normalized.ProductSemantics}, nil
}

func currentManifest() (composition.Manifest, error) {
	manifest, err := composition.Parse(compositionv1.CurrentCollaborative())
	if err != nil {
		return composition.Manifest{}, fmt.Errorf("解析发布内置组合: %w", err)
	}
	return manifest, nil
}

func verifyComposition(manifest composition.Manifest, productSemantics ProductSemantics) (composition.Verification, error) {
	if productSemantics != Basic && productSemantics != Organized && productSemantics != Collaborative {
		return composition.Verification{}, fmt.Errorf("产品语义必须显式选择 basic、organized 或 collaborative，实际 %q", productSemantics)
	}
	verification, err := composition.VerifySealed(manifest)
	if err != nil {
		return composition.Verification{}, fmt.Errorf("校验产品组合: %w", err)
	}
	if err := validateManifestSemantics(manifest, productSemantics); err != nil {
		return composition.Verification{}, err
	}
	return verification, nil
}

func validateRequest(request Request, resource resources) (Request, error) {
	var err error
	request.DataDir, err = requireAbsolute("数据目录", request.DataDir)
	if err != nil {
		return Request{}, err
	}
	if request.RestoreBackup != "" {
		request.RestoreBackup, err = requireAbsolute("备份文件", request.RestoreBackup)
		if err != nil {
			return Request{}, err
		}
	}
	if resource.restore == nil || resource.openAssets == nil {
		return Request{}, errors.New("装配资源实现不完整")
	}
	switch request.ProductSemantics {
	case Basic:
		if request.OrganizedProvider != nil || strings.TrimSpace(request.VectorBundleDir) != "" {
			return Request{}, errors.New("basic 语义不得声明组织或向量能力")
		}
	case Organized:
		if resource.openDerived == nil || request.OrganizedProvider == nil || strings.TrimSpace(request.VectorBundleDir) != "" {
			return Request{}, errors.New("organized 语义必须且只能显式声明语义 Provider")
		}
	case Collaborative:
		if resource.openDerived == nil || resource.openVector == nil || request.OrganizedProvider != nil {
			return Request{}, errors.New("collaborative 语义不得声明内置语义 Provider")
		}
		request.VectorBundleDir, err = requireAbsolute("向量能力目录", request.VectorBundleDir)
		if err != nil {
			return Request{}, err
		}
	default:
		return Request{}, fmt.Errorf("产品语义必须显式选择 basic、organized 或 collaborative，实际 %q", request.ProductSemantics)
	}
	return request, nil
}

func validateManifestSemantics(manifest composition.Manifest, expected ProductSemantics) error {
	kernel, exists := component(manifest, "kernel")
	if !exists || configString(kernel.Config, "mode") != string(expected) {
		return fmt.Errorf("内核组合语义与显式选择不一致: 需要 %s", expected)
	}
	assembled, exists := component(manifest, "assembly")
	if !exists || configString(assembled.Config, "product_semantics") != string(expected) {
		return fmt.Errorf("装配组合语义与显式选择不一致: 需要 %s", expected)
	}
	if configString(assembled.Config, "entry") != activeAssemblyEntry || configString(assembled.Config, "activation") != activeAssemblyActivation {
		return errors.New("组合清单没有声明唯一活动装配入口")
	}
	return nil
}

func component(manifest composition.Manifest, role string) (composition.Component, bool) {
	for _, item := range manifest.Components {
		if item.Role == role {
			return item, true
		}
	}
	return composition.Component{}, false
}

func configString(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return strings.TrimSpace(value)
}

func requireAbsolute(name, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("%s必须是明确的绝对路径", name)
	}
	return filepath.Clean(trimmed), nil
}

func openProductionVector(root string, manifest composition.Manifest) (embedding.Provider, error) {
	bundle, err := embedding.LoadBundle(root)
	if err != nil {
		return nil, fmt.Errorf("校验发布向量能力包: %w", err)
	}
	if err := verifyProductionVectorBinding(bundle, manifest); err != nil {
		return nil, err
	}
	managed, err := embedding.OpenManagedBundle(bundle)
	if err != nil {
		return nil, fmt.Errorf("打开发布向量能力: %w", err)
	}
	return managed, nil
}

func inspectProductionVector(root string, manifest composition.Manifest) (embedding.Bundle, error) {
	bundle, err := embedding.InspectBundle(root)
	if err != nil {
		return embedding.Bundle{}, fmt.Errorf("预检发布向量能力包: %w", err)
	}
	if err := verifyProductionVectorBinding(bundle, manifest); err != nil {
		return embedding.Bundle{}, err
	}
	return bundle, nil
}

func verifyProductionVectorBinding(bundle embedding.Bundle, manifest composition.Manifest) error {
	vector, exists := component(manifest, "vector")
	if !exists {
		return errors.New("发布组合缺少向量组件")
	}
	expectedManifest := ""
	for _, item := range vector.Content {
		if item.Name == "manifest.json" {
			expectedManifest = item.SHA256
			break
		}
	}
	actualManifest, err := fileSHA256(bundle.ManifestPath)
	if err != nil || expectedManifest == "" || actualManifest != expectedManifest {
		return errors.New("发布向量能力清单与内置组合身份不一致")
	}
	if configString(vector.Config, "bundle_schema") != bundle.Manifest.Schema ||
		configString(vector.Config, "capability") != bundle.Manifest.Capability ||
		configString(vector.Config, "space") != bundle.Manifest.Space.ID ||
		configInt(vector.Config, "dimensions") != bundle.Manifest.Space.Dimensions {
		return errors.New("发布向量能力配置与内置组合身份不一致")
	}
	return nil
}

func configInt(config map[string]any, key string) int {
	switch value := config[key].(type) {
	case int:
		return value
	case json.Number:
		parsed, _ := strconv.Atoi(value.String())
		return parsed
	case float64:
		return int(value)
	default:
		return 0
	}
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

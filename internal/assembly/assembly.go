package assembly

import (
	"context"
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

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/core"
	"github.com/HJSunDev/ownward/internal/embedding"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
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
	OrganizedProvider contract.SemanticCapability
	OrganizedVector   contract.VectorCapability
	VectorBundleDir   string
}

type Runtime struct {
	streaming    *core.StreamingAssets
	vector       contract.VectorCapability
	product      *informationcontrol.Product
	userControl  *informationcontrol.Control
	backup       func(string) error
	verification composition.Verification
	semantics    ProductSemantics
	closeOnce    sync.Once
	closeErr     error
}

// Product exposes only the stable product waist to access adapters.
func (r *Runtime) Product() contract.ProductCapability {
	if r == nil {
		return nil
	}
	return r.product
}

func (r *Runtime) UserControl() *informationcontrol.Control { return r.userControl }
func (r *Runtime) Management() *informationcontrol.Product  { return r.product }

func (r *Runtime) OperationGeneration() uint64 {
	if r == nil || r.streaming == nil {
		return 0
	}
	g, _ := r.streaming.Store.OperationGeneration(context.Background())
	return g
}

// Kernel exposes only the stable lifecycle waist to operational commands.
func (r *Runtime) Kernel() contract.KernelLifecycle {
	if r == nil {
		return nil
	}
	return r.streaming
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

func (r *Runtime) ExportHandoff(destination string) error {
	if r == nil || r.streaming == nil {
		return errors.New("装配运行时尚未打开资产权威")
	}
	return r.streaming.Store.ExportDeploymentArchive(context.Background(), filepath.Join(destination, "authority.zip"))
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.product != nil {
			r.product.Close()
		}
		if r.streaming != nil {
			r.closeErr = errors.Join(r.closeErr, r.streaming.Close())
		}
		if r.vector != nil {
			r.closeErr = errors.Join(r.closeErr, r.vector.Close())
		}
	})
	return r.closeErr
}

type resources struct {
	restore    func(context.Context, string, string, boundedstore.Options) error
	openVector func(string, composition.Manifest) (contract.VectorCapability, error)
}

var productionResources = resources{
	restore:    boundedstore.RestoreLegacyAssetArchive,
	openVector: openProductionVector,
}

// Open is the sole active product assembly entry. It validates the complete
// composition and its explicit product semantics before restoring or opening
// any product resource.
func Open(request Request) (*Runtime, error) {
	manifest, err := currentManifest()
	if err != nil {
		return nil, err
	}
	return openBoundedWith(request, manifest, productionResources)
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
	if resource.restore == nil {
		return Request{}, errors.New("装配资源实现不完整")
	}
	switch request.ProductSemantics {
	case Basic:
		if request.OrganizedProvider != nil || request.OrganizedVector != nil || strings.TrimSpace(request.VectorBundleDir) != "" {
			return Request{}, errors.New("basic 语义不得声明组织或向量能力")
		}
	case Organized:
		if request.OrganizedProvider == nil || request.OrganizedVector == nil || strings.TrimSpace(request.VectorBundleDir) != "" {
			return Request{}, errors.New("organized 语义必须显式声明语义与向量能力")
		}
	case Collaborative:
		if resource.openVector == nil || request.OrganizedProvider != nil || request.OrganizedVector != nil {
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

func validateDeclaredCapabilities(manifest composition.Manifest, request Request) error {
	if request.ProductSemantics != Organized {
		return nil
	}
	semantic, exists := component(manifest, "semantic")
	if !exists {
		return errors.New("组合缺少语义能力声明")
	}
	expectedSemantic := semanticsCapabilityFromConfig(semantic.Config)
	if request.OrganizedProvider == nil || request.OrganizedProvider.Identity() != expectedSemantic || strings.TrimSpace(expectedSemantic.ID) == "" || strings.TrimSpace(expectedSemantic.Version) == "" {
		return errors.New("语义能力身份与组合声明不一致")
	}
	vector, exists := component(manifest, "vector")
	if !exists || request.OrganizedVector == nil {
		return errors.New("组合缺少向量能力声明")
	}
	space := request.OrganizedVector.Space()
	if configString(vector.Config, "capability") != request.OrganizedVector.Name() ||
		configString(vector.Config, "space") != space.ID || configInt(vector.Config, "dimensions") != space.Dimensions {
		return errors.New("向量能力身份或空间与组合声明不一致")
	}
	return nil
}

func semanticsCapabilityFromConfig(config map[string]any) semantics.Capability {
	return semantics.Capability{
		ID: configString(config, "provider"), Version: configString(config, "provider_version"),
		Execution: configString(config, "provider_execution"),
	}
}

func authorityInitial(manifest composition.Manifest) (contract.ControlState, error) {
	kernel, exists := component(manifest, "kernel")
	if !exists || strings.TrimSpace(kernel.Identity) == "" || strings.TrimSpace(manifest.Identity) == "" {
		return contract.ControlState{}, errors.New("组合缺少权威控制状态初始化身份")
	}
	return contract.ControlState{
		Schema: contract.ControlStateSchema, Revision: 1,
		ActiveComposition: manifest.Identity, ActiveKernelGeneration: kernel.Identity,
	}, nil
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

func openProductionVector(root string, manifest composition.Manifest) (contract.VectorCapability, error) {
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

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const principalHeader = "X-Ownward-Principal"
const controlPrefix = "/__ownward/control/"

// 只定位本机受保护恢复通道；不是体系或主体身份。
func ownerRecoveryScope(dataDir string) string {
	abs, _ := filepath.Abs(dataDir)
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(abs))))
	return "local-recovery:" + hex.EncodeToString(digest[:])
}

type controlHTTPServer struct {
	server     httpMCPServer
	control    *informationcontrol.Control
	product    *informationcontrol.Product
	kernel     contract.ProductCapability
	vault      localowner.Vault
	recovery   string
	generation func() uint64
}

// 独立恢复通道只在当前 OS 用户受保护的凭据区交付；服务启动令牌没有恢复权。
func (s *controlHTTPServer) prepareRecovery(scope string) error {
	vault, err := localowner.Default()
	if err != nil {
		return err
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	s.vault, s.recovery = vault, hex.EncodeToString(token[:])
	return vault.Save(scope, "owner-recovery", s.recovery)
}

func (s controlHTTPServer) HTTPHandler() http.Handler {
	// MCP 会话上下文来自初始化；身份必须取当前 HTTP 请求，不能缓存首个请求的授权。
	if server, ok := s.server.(interface{ MCP() *mcp.Server }); ok {
		server.MCP().AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				credential := ""
				if call, ok := req.(*mcp.CallToolRequest); ok && isAssetMutation(call.Params.Name) {
					if id, ok := call.Params.Meta["ownward/operation"].(string); ok {
						generation, _ := strconv.ParseUint(fmt.Sprint(call.Params.Meta["ownward/generation"]), 10, 64)
						ctx = contract.WithOperation(ctx, contract.OperationIdentity{ID: id, Generation: generation, Kind: call.Params.Name, Digest: mutationDigest(call.Params.Name, call.Params.Arguments)})
					}
				}
				if extra := req.GetExtra(); extra != nil {
					credential = extra.Header.Get(principalHeader)
					if call, ok := req.(*mcp.CallToolRequest); ok && isAssetMutation(call.Params.Name) && extra.Header.Get("X-Ownward-Operation") != "" {
						generation, _ := strconv.ParseUint(extra.Header.Get("X-Ownward-Generation"), 10, 64)
						ctx = contract.WithOperation(ctx, contract.OperationIdentity{ID: extra.Header.Get("X-Ownward-Operation"), Generation: generation, Kind: call.Params.Name, Digest: mutationDigest(call.Params.Name, call.Params.Arguments)})
					}
				}
				return next(informationcontrol.Authenticate(ctx, credential), method, req)
			}
		})
	}
	next := s.server.HTTPHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := informationcontrol.Authenticate(r.Context(), r.Header.Get(principalHeader))
		r = r.WithContext(ctx)
		if !strings.HasPrefix(r.URL.Path, controlPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var input struct {
			Name   string `json:"name"`
			ID     string `json:"id"`
			Accept bool   `json:"accept"`
		}
		if r.Method == http.MethodPost {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
		} else if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var value any
		var err error
		switch strings.TrimPrefix(r.URL.Path, controlPrefix) {
		case "identity":
			value = map[string]string{"system_id": s.control.SystemID()}
		case "generation":
			if _, err = s.control.Self(ctx); err == nil {
				if s.generation != nil {
					value = map[string]uint64{"generation": s.generation()}
				} else if k, ok := s.kernel.(interface{ OperationGeneration() uint64 }); ok {
					value = map[string]uint64{"generation": k.OperationGeneration()}
				}
			}
		case "recover":
			provided := r.Header.Get(principalHeader)
			if r.Method != http.MethodPost || s.recovery == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(s.recovery)) != 1 {
				http.Error(w, "recovery requires protected owner channel", http.StatusForbidden)
				return
			}
			var token string
			if s.control.SystemID() == "" {
				token, err = s.control.InitializeOwner("所有者")
			} else {
				token, err = s.control.RecoverOwner()
			}
			if err == nil {
				err = s.vault.Save(s.control.SystemID(), "owner", token)
			}
			value = map[string]string{"system_id": s.control.SystemID()}
		case "enroll":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", 405)
				return
			}
			var p contract.Principal
			var token string
			p, token, err = s.control.Enroll(ctx, input.Name)
			// 仅私密连接器端点接收凭据，永不作为模型工具输出。
			value = struct {
				Principal  contract.Principal `json:"principal"`
				Credential string             `json:"credential"`
			}{p, token}
		case "self":
			value, err = s.control.Self(ctx)
		case "reissue":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", 405)
				return
			}
			var token string
			token, err = s.control.Reissue(ctx, input.ID)
			value = map[string]string{"credential": token}
		case "decide":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", 405)
				return
			}
			value, err = s.product.Decide(ctx, input.ID, input.Accept)
		case "preview":
			if _, err = s.control.Principals(ctx); err == nil {
				var op contract.ManagementReceipt
				op, err = s.product.Receipt(ctx, input.ID)
				if err == nil {
					value, err = s.preview(ctx, op)
				}
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	})
}

func (s controlHTTPServer) preview(ctx context.Context, op contract.ManagementReceipt) (map[string]string, error) {
	bound, finish, err := s.control.Begin(ctx, contract.ManagePermission)
	if err != nil {
		return nil, err
	}
	ctx = bound
	var text string
	if op.Request.Operation == "permissions" {
		principals, err := s.control.Principals(ctx)
		if err != nil {
			return nil, err
		}
		name := ""
		for _, p := range principals {
			if p.ID == op.Request.SubjectID {
				name = p.Name
			}
		}
		if name == "" {
			return nil, errors.New("接入者已不存在")
		}
		text = "将“" + name + "”的权限调整为："
		for _, p := range op.Request.Permissions {
			switch p {
			case contract.ReadPermission:
				text += "查看全部资料；"
			case contract.MaintainPermission:
				text += "维护资料；"
			case contract.ManagePermission:
				text += "管理连接与遗忘资料；"
			}
		}
		if len(op.Request.Permissions) == 0 {
			text = "收回“" + name + "”的全部访问权限。"
		} else {
			text += "权限持续到你收回。"
		}
	} else {
		text = "忘掉以下完整资料，并清理本体系中的相关副本：\n"
		for _, target := range op.Request.Targets {
			asset, err := s.kernel.Read(ctx, target.ID)
			if err != nil || asset.Revision != target.Revision {
				return nil, errors.New("资料已变化，请重新核对删除范围")
			}
			runes := []rune(asset.Content)
			if len(runes) > 240 {
				runes = append(runes[:240], []rune("…（整项删除）")...)
			}
			text += fmt.Sprintf("- %s\n  来源：%s %s；保存于 %s\n", string(runes), asset.Source.Actor, asset.Source.Ref, asset.CreatedAt.Format("2006-01-02 15:04"))
		}
		text += "已交付给其他系统或独立旧备份中的副本不会被远程删除。"
	}
	if err := finish(); err != nil {
		return nil, err
	}
	return map[string]string{"message": text}, nil
}

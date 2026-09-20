package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/rpcstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func isAssetMutation(name string) bool {
	return name == "ownward_create" || name == "ownward_create_batch" || name == "ownward_update"
}
func mutationDigest(name string, args json.RawMessage) string {
	var value any
	_ = json.Unmarshal(args, &value)
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(append([]byte(name+":"), data...))
	return hex.EncodeToString(sum[:])
}

func requestMutationDigest(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if scope := rpcstream.FromContext(ctx); scope != nil && rpcstream.StorageTool(name) {
		call, err := scope.Resolve(name, args)
		if err != nil {
			return "", err
		}
		return call.Digest(ctx)
	}
	return mutationDigest(name, args), nil
}
func connectionID() string {
	var data [24]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data[:])
}

func (h *hostConnector) callProduct(ctx context.Context, request *mcp.CallToolRequest, session *mcp.ClientSession) (*mcp.CallToolResult, error) {
	if !isAssetMutation(request.Params.Name) {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: request.Params.Name, Arguments: request.Params.Arguments})
		if err != nil && h.remote != nil {
			return nil, errRemoteUnavailable
		}
		return result, err
	}
	h.operationMu.Lock()
	defer h.operationMu.Unlock()
	key, err := requestMutationDigest(ctx, request.Params.Name, request.Params.Arguments)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	op, exists := h.record.Mutations[key]
	deferred := h.record.Deferred[key]
	h.mu.Unlock()
	if !exists {
		var state struct {
			Generation uint64 `json:"generation"`
		}
		if err := h.controlCall(ctx, "generation", h.credential(), nil, &state); err != nil {
			return nil, err
		}
		if state.Generation == 0 {
			return nil, errors.New("服务未提供提交接续能力")
		}
		op = contract.OperationIdentity{ID: connectionID(), Generation: state.Generation, Kind: request.Params.Name, Digest: key}
		h.mu.Lock()
		if h.record.Mutations == nil {
			h.record.Mutations = map[string]contract.OperationIdentity{}
		}
		h.record.Mutations[key] = op
		deferred = h.organization != nil && h.organization.ready.Load()
		if deferred {
			if h.record.Deferred == nil {
				h.record.Deferred = map[string]bool{}
			}
			h.record.Deferred[key] = true
		}
		h.mu.Unlock()
		if err := h.save(); err != nil {
			return nil, err
		}
	}
	restore, err := h.deferOrganization(ctx, request, deferred)
	if err != nil {
		return nil, err
	}
	defer restore()
	result, err := session.CallTool(contract.WithOperation(ctx, op), &mcp.CallToolParams{Meta: mcp.Meta{"ownward/operation": op.ID, "ownward/generation": op.Generation}, Name: request.Params.Name, Arguments: request.Params.Arguments})
	if err != nil {
		return nil, errRemoteUnavailable
	}
	if !result.IsError {
		h.mu.Lock()
		delete(h.record.Mutations, key)
		delete(h.record.Deferred, key)
		h.mu.Unlock()
		if err := h.save(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

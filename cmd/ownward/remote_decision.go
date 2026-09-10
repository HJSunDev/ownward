package main

import (
	"context"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type savedDecision struct {
	Binding string `json:"binding"`
	Accept  bool   `json:"accept"`
}

func (h *hostConnector) durableConfirm(ctx context.Context, session *mcp.ServerSession, id, binding, message string) (bool, error) {
	h.mu.Lock()
	previous, found := h.record.Decisions[id]
	h.mu.Unlock()
	if found {
		if previous.Binding != binding {
			return false, errors.New("待确认内容已变化，不能复用旧决定")
		}
		return previous.Accept, nil
	}
	accept, err := hostConfirm(ctx, session, message)
	if err != nil {
		return false, err
	}
	h.mu.Lock()
	if h.record.Decisions == nil {
		h.record.Decisions = map[string]savedDecision{}
	}
	h.record.Decisions[id] = savedDecision{Binding: binding, Accept: accept}
	h.mu.Unlock()
	if err := h.save(); err != nil {
		return false, err
	}
	return accept, nil
}

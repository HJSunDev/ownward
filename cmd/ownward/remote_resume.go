package main

import (
	"context"
	"errors"
	"strings"

	"github.com/HJSunDev/ownward/internal/adapter/remote"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type remoteIdentity struct {
	Location contract.Location `json:"location"`
	Handoff  *contract.Handoff `json:"handoff,omitempty"`
}

// Called between tool calls. Existing calls finish against their pinned service;
// a new session is opened only after the trusted successor proves activation.
func (h *hostConnector) refreshRemote(ctx context.Context, session **mcp.ClientSession) error {
	if h.remote == nil {
		return nil
	}
	var current remoteIdentity
	err := remoteCall(ctx, h.remote.Client, h.remote.Material.Location, "/remote/identity", "", nil, &current)
	if err == nil {
		if current.Location.SystemID != h.system || current.Location.ServiceID != h.remote.Material.Location.ServiceID {
			return errors.New("当前服务身份发生变化")
		}
		if current.Handoff != nil && current.Handoff.Phase != "active" {
			h.mu.Lock()
			h.record.NextLocation = &current.Handoff.Target
			h.mu.Unlock()
			if err := h.save(); err != nil {
				return err
			}
		}
	}
	h.mu.Lock()
	target := h.record.NextLocation
	h.mu.Unlock()
	if target == nil || target.ServiceID == h.remote.Material.Location.ServiceID {
		return err
	}
	client, connectErr := remote.Client(*target, nil)
	if connectErr != nil {
		return connectErr
	}
	var next remoteIdentity
	if connectErr = remoteCall(ctx, client, *target, "/remote/identity", "", nil, &next); connectErr != nil {
		if err != nil {
			return errors.New("交接目的地暂不可用，原请求已保留")
		}
		return nil
	}
	if next.Location.SystemID != h.system || next.Location.ServiceID != target.ServiceID || next.Handoff == nil || next.Handoff.Phase != "active" || next.Handoff.Target != *target {
		return err
	}
	transport := *client
	transport.Transport = remoteBearer{base: client.Transport, credential: h.credential}
	consumer := mcp.NewClient(&mcp.Implementation{Name: "ownward-remote", Version: version}, nil)
	nextSession, connectErr := consumer.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: strings.TrimRight(target.Endpoint, "/") + "/capabilities", HTTPClient: &transport, MaxRetries: 1, DisableStandaloneSSE: true}, nil)
	if connectErr != nil {
		return connectErr
	}
	old := *session
	oldClient, oldLocation, oldEndpoint := h.remote.Client, h.remote.Material.Location, h.descriptor.Endpoint
	*session = nextSession
	h.remote.Client = client
	h.remote.Material.Location = *target
	h.descriptor.Endpoint = strings.TrimRight(target.Endpoint, "/") + "/capabilities"
	if err := h.save(); err != nil {
		nextSession.Close()
		*session = old
		h.remote.Client, h.remote.Material.Location, h.descriptor.Endpoint = oldClient, oldLocation, oldEndpoint
		return err
	}
	_ = old.Close()
	return nil
}

func (h *hostConnector) reconnectRemote(ctx context.Context, session **mcp.ClientSession) error {
	if err := h.refreshRemote(ctx, session); err != nil {
		return err
	}
	client := *h.remote.Client
	client.Transport = remoteBearer{base: client.Transport, credential: h.credential}
	consumer := mcp.NewClient(&mcp.Implementation{Name: "ownward-remote", Version: version}, nil)
	next, err := consumer.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.descriptor.Endpoint, HTTPClient: &client, MaxRetries: 1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return errRemoteUnavailable
	}
	old := *session
	*session = next
	_ = old.Close()
	return nil
}

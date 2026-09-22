package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/adapter/remote"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type connectionMaterial struct {
	Location     contract.Location     `json:"location"`
	EnrollmentID string                `json:"enrollment_id"`
	Permissions  []contract.Permission `json:"permissions,omitempty"`
}
type remoteConnection struct {
	Material connectionMaterial
	Client   *http.Client
}

var errRemoteUnavailable = errors.New("受保护连接暂不可用，原操作保留待接续")

// Only this private connector API is exposed remotely. The local startup token,
// shutdown and owner recovery endpoints are never registered here.
func remoteHandler(location contract.Location, server controlHTTPServer) http.Handler {
	product := server.HTTPHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Origin") != "" {
			http.Error(w, "origin not supported", 403)
			return
		}
		credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			credential = ""
		}
		r = r.Clone(r.Context())
		r.Header = r.Header.Clone()
		r.Header.Del(principalHeader)
		r.Header.Set(principalHeader, credential)
		if r.URL.Path == "/remote/identity" && r.Method == "GET" {
			state := server.control.State()
			location.SystemID = server.control.SystemID()
			_ = json.NewEncoder(w).Encode(struct {
				Location contract.Location `json:"location"`
				Handoff  *contract.Handoff `json:"handoff,omitempty"`
			}{location, func() *contract.Handoff {
				if state.Access != nil {
					return state.Access.Handoff
				}
				return nil
			}()})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/remote/enrollment/") {
			var in struct {
				ID          string                `json:"id"`
				Proof       string                `json:"proof,omitempty"`
				Name        string                `json:"name,omitempty"`
				Permissions []contract.Permission `json:"permissions,omitempty"`
				Marker      string                `json:"marker,omitempty"`
				Decision    string                `json:"decision,omitempty"`
				Accept      bool                  `json:"accept,omitempty"`
			}
			if r.Method != "POST" {
				http.Error(w, "method not allowed", 405)
				return
			}
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
			d.DisallowUnknownFields()
			if d.Decode(&in) != nil {
				http.Error(w, "invalid request", 400)
				return
			}
			ctx := informationcontrol.Authenticate(r.Context(), credential)
			var out any
			var err error
			switch strings.TrimPrefix(r.URL.Path, "/remote/enrollment/") {
			case "invite":
				out, err = server.control.Invite(ctx, in.ID)
			case "join":
				out, err = server.control.Join(in.ID, in.Proof, in.Name, in.Permissions)
			case "list":
				out, err = server.control.Enrollments(ctx)
			case "management":
				out, err = server.control.PendingManagement(ctx)
			case "preview":
				var e contract.Enrollment
				var marker string
				e, marker, err = server.control.EnrollmentPreview(ctx, in.ID)
				out = struct {
					Enrollment contract.Enrollment `json:"enrollment"`
					Marker     string              `json:"marker"`
				}{e, marker}
			case "decide":
				out, err = server.control.DecideEnrollmentVersion(ctx, in.ID, in.Marker, in.Decision, in.Accept)
			case "claim":
				var e contract.Enrollment
				var token string
				e, token, err = server.control.ClaimEnrollment(in.ID, in.Proof)
				out = struct {
					Enrollment contract.Enrollment `json:"enrollment"`
					Credential string              `json:"credential,omitempty"`
				}{e, token}
			case "ack":
				err = server.control.AcknowledgeEnrollment(ctx, in.ID, in.Proof)
				out = map[string]bool{"saved": err == nil}
			default:
				http.NotFound(w, r)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), 403)
				return
			}
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		if r.URL.Path == "/capabilities" {
			// Initialization and static tool definitions contain no user data.
			// Each product call authenticates the current request separately.
			product.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, controlPrefix) {
			switch strings.TrimPrefix(r.URL.Path, controlPrefix) {
			case "self", "generation", "preview", "decide", "receipt", "reissue":
				product.ServeHTTP(w, r)
				return
			}
		}
		http.NotFound(w, r)
	})
}

func remoteCall(ctx context.Context, client *http.Client, location contract.Location, path, credential string, input, output any) error {
	var body io.Reader
	method := "GET"
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
		method = "POST"
	}
	r, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(location.Endpoint, "/")+path, body)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	if credential != "" {
		r.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := client.Do(r)
	if err != nil {
		return errRemoteUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return &controlError{Status: response.StatusCode, Message: strings.TrimSpace(string(message))}
	}
	if output == nil {
		_, err = io.Copy(io.Discard, response.Body)
		return err
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
}

func newRemoteHost(ctx context.Context, material connectionMaterial, vault localowner.Vault) (*hostConnector, error) {
	if material.Location.SystemID == "" || material.EnrollmentID == "" {
		return nil, errors.New("连接材料缺少体系或接入操作")
	}
	client, err := remote.Client(material.Location, nil)
	if err != nil {
		return nil, err
	}
	h := &hostConnector{vault: vault, system: material.Location.SystemID, profile: "remote:" + material.EnrollmentID, remote: &remoteConnection{Material: material, Client: client}}
	if data, err := vault.Load(h.system, h.profile); err == nil {
		if err := json.Unmarshal([]byte(data), &h.record); err != nil {
			return nil, errors.New("连接状态损坏")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// A previously trusted location handoff survives losing the old host.
	if h.record.NextLocation != nil {
		if next, err := remote.Client(*h.record.NextLocation, nil); err == nil {
			var identity struct {
				Location contract.Location `json:"location"`
				Handoff  *contract.Handoff `json:"handoff"`
			}
			if remoteCall(ctx, next, *h.record.NextLocation, "/remote/identity", "", nil, &identity) == nil && identity.Location.SystemID == h.system && identity.Handoff != nil && identity.Handoff.Phase == "active" && identity.Handoff.Target == *h.record.NextLocation {
				h.remote.Client = next
				h.remote.Material.Location = *h.record.NextLocation
			}
		}
	}
	var identity struct {
		Location contract.Location `json:"location"`
		Handoff  *contract.Handoff `json:"handoff"`
	}
	if err := remoteCall(ctx, h.remote.Client, h.remote.Material.Location, "/remote/identity", "", nil, &identity); err != nil {
		return nil, err
	}
	if identity.Location.SystemID != h.system || identity.Location.ServiceID != h.remote.Material.Location.ServiceID || identity.Location.Composition != h.remote.Material.Location.Composition {
		return nil, errors.New("连接目标不是已确认的信息体系")
	}
	if identity.Handoff != nil && identity.Handoff.Phase != "active" {
		h.record.NextLocation = &identity.Handoff.Target
		if err := h.save(); err != nil {
			return nil, err
		}
	}
	h.descriptor = &sharedMCPDescriptor{Endpoint: strings.TrimRight(h.remote.Material.Location.Endpoint, "/") + "/capabilities"}
	return h, nil
}

func (h *hostConnector) remoteInitialize(ctx context.Context, request *mcp.CallToolRequest) error {
	if h.credential() != "" {
		return nil
	}
	params := request.Session.InitializeParams()
	if params == nil || params.ClientInfo == nil {
		return errors.New("宿主名称缺失")
	}
	if h.record.JoinProof == "" {
		h.record.JoinProof = connectionID()
		if err := h.save(); err != nil {
			return err
		}
	}
	material := h.remote.Material
	permissions := material.Permissions
	if len(permissions) == 0 {
		permissions = []contract.Permission{contract.ReadPermission, contract.MaintainPermission}
	}
	var joined contract.Enrollment
	if err := remoteCall(ctx, h.remote.Client, material.Location, "/remote/enrollment/join", "", map[string]any{"id": material.EnrollmentID, "proof": h.record.JoinProof, "name": params.ClientInfo.Name, "permissions": permissions}, &joined); err != nil {
		return err
	}
	for attempt := 0; attempt < 15; attempt++ {
		var result struct {
			Enrollment contract.Enrollment `json:"enrollment"`
			Credential string              `json:"credential"`
		}
		if err := remoteCall(ctx, h.remote.Client, material.Location, "/remote/enrollment/claim", "", map[string]string{"id": material.EnrollmentID, "proof": h.record.JoinProof}, &result); err != nil {
			return err
		}
		if result.Credential != "" {
			h.mu.Lock()
			h.record.Credential = result.Credential
			h.record.Principal = result.Enrollment.Principal
			h.record.Connected = true
			h.mu.Unlock()
			if err := h.save(); err != nil {
				return err
			}
			var ack any
			return remoteCall(ctx, h.remote.Client, material.Location, "/remote/enrollment/ack", h.credential(), map[string]string{"id": material.EnrollmentID, "proof": h.record.JoinProof}, &ack)
		}
		if result.Enrollment.Status == "declined" || result.Enrollment.Status == "cancelled" {
			return errors.New("管理宿主未批准此连接")
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("接入申请已保留，等待可信管理宿主批准；核对标记 %s，获准后接续原任务", informationcontrol.EnrollmentMarker(material.EnrollmentID, h.record.JoinProof))
}

func (h *hostConnector) processEnrollments(ctx context.Context, session *mcp.ServerSession) error {
	if h.remote == nil {
		return nil
	}
	var self contract.Principal
	if err := h.controlCall(ctx, "self", h.credential(), nil, &self); err != nil {
		return err
	}
	if !slices.Contains(self.Permissions, contract.ManagePermission) {
		return nil
	}
	var entries []contract.Enrollment
	if err := remoteCall(ctx, h.remote.Client, h.remote.Material.Location, "/remote/enrollment/list", h.credential(), struct{}{}, &entries); err != nil {
		return err
	}
	for _, e := range entries {
		if e.Status != "pending" {
			continue
		}
		var preview struct {
			Enrollment contract.Enrollment `json:"enrollment"`
			Marker     string              `json:"marker"`
		}
		if err := remoteCall(ctx, h.remote.Client, h.remote.Material.Location, "/remote/enrollment/preview", h.credential(), map[string]string{"id": e.ID}, &preview); err != nil {
			return err
		}
		message := fmt.Sprintf("允许“%s”接入此信息体系吗？请核对目标宿主上的标记 %s；允许的行为：%v。", preview.Enrollment.Name, preview.Marker, preview.Enrollment.Permissions)
		decisionToken, marker := preview.Enrollment.Decision, preview.Marker
		_, err := awaitOwnerDecision(ctx, session, message, func(c context.Context) (string, error) {
			err := remoteCall(c, h.remote.Client, h.remote.Material.Location, "/remote/enrollment/preview", h.credential(), map[string]string{"id": e.ID}, &preview)
			return preview.Enrollment.Status, err
		}, func(c context.Context, accept bool) (string, error) {
			var decision contract.Enrollment
			err := remoteCall(c, h.remote.Client, h.remote.Material.Location, "/remote/enrollment/decide", h.credential(), map[string]any{"id": e.ID, "marker": marker, "decision": decisionToken, "accept": accept}, &decision)
			return decision.Status, err
		})
		if err != nil {
			return err
		}
	}
	var pending []contract.ManagementReceipt
	if err := remoteCall(ctx, h.remote.Client, h.remote.Material.Location, "/remote/enrollment/management", h.credential(), struct{}{}, &pending); err != nil {
		return err
	}
	for _, op := range pending {
		_, err := h.confirmManagement(ctx, session, h.credential(), op.Request.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

type remoteBearer struct {
	base       http.RoundTripper
	credential func() string
}

func (t remoteBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Del(principalHeader)
	if credential := t.credential(); credential != "" {
		copy.Header.Set("Authorization", "Bearer "+credential)
	}
	return t.base.RoundTrip(copy)
}

func runRemoteConnector(ctx context.Context, material connectionMaterial) error {
	vault, err := localowner.Default()
	if err != nil {
		return err
	}
	host, err := newRemoteHost(ctx, material, vault)
	if err != nil {
		return err
	}
	client := *host.remote.Client
	streamTransport := &connectorTransport{base: remoteBearer{base: client.Transport, credential: host.credential}}
	client.Transport = streamTransport
	consumer := mcp.NewClient(&mcp.Implementation{Name: "ownward-remote", Version: version}, nil)
	session, err := consumer.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: host.descriptor.Endpoint, HTTPClient: &client, MaxRetries: 1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return err
	}
	defer func() { session.Close() }()
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	host.streaming, err = configureStreamingConnector(streamTransport, session.InitializeResult(), filepath.Join(cache, "Ownward"))
	if err != nil {
		return err
	}
	if host.streaming != nil {
		defer host.streaming.Close()
	}
	var organizationTools []*mcp.Tool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return err
		}
		copy := connectorTool(tool)
		organizationTools = append(organizationTools, &copy)
	}
	stopOrganization := host.attachOrganization(ctx, session.InitializeResult(), organizationTools, func(ctx context.Context, name string, args any) (*mcp.CallToolResult, error) {
		return host.remoteOrganizationCall(ctx, &session, name, args)
	}, func(ctx context.Context) error {
		return host.refreshOrganizationRoute(ctx, &session)
	})
	defer stopOrganization()
	proxy := newConnectorServer(version, session.InitializeResult(), host.organizationExecutor)
	host.addMaterialTool(proxy, func(ctx context.Context, refs []string) ([]contract.InformationCheck, error) {
		host.routeMu.Lock()
		err := host.refreshRemote(ctx, &session)
		host.routeMu.Unlock()
		if err != nil {
			return nil, err
		}
		host.routeMu.RLock()
		defer host.routeMu.RUnlock()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "ownward_check", Arguments: map[string]any{"bases": refs}})
		if err != nil {
			return nil, err
		}
		if result.IsError {
			return nil, errors.New("来源核对未完成")
		}
		var out struct {
			Results []contract.InformationCheck `json:"results"`
		}
		err = decodeTool(result, &out)
		return out.Results, err
	})
	for _, tool := range organizationTools {
		copy := *tool
		proxy.AddTool(&copy, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			host.routeMu.Lock()
			err := host.refreshRemote(ctx, &session)
			host.routeMu.Unlock()
			if err != nil {
				return nil, err
			}
			run := func() (*mcp.CallToolResult, error) {
				host.routeMu.RLock()
				defer host.routeMu.RUnlock()
				if err := host.initialize(ctx, request); err != nil {
					return nil, err
				}
				if err := host.processEnrollments(ctx, request.Session); err != nil {
					return nil, err
				}
				return host.call(ctx, request, session)
			}
			result, err := run()
			if errors.Is(err, errRemoteUnavailable) && ctx.Err() == nil {
				host.routeMu.Lock()
				reconnectErr := host.reconnectRemote(ctx, &session)
				host.routeMu.Unlock()
				if reconnectErr != nil {
					return nil, reconnectErr
				}
				return run()
			}
			return result, err
		})
	}
	host.addOrganizationDemand(proxy, func(ctx context.Context, request *mcp.CallToolRequest, name string, args any) (*mcp.CallToolResult, error) {
		host.routeMu.Lock()
		err := host.refreshRemote(ctx, &session)
		host.routeMu.Unlock()
		if err != nil {
			return nil, err
		}
		host.routeMu.RLock()
		defer host.routeMu.RUnlock()
		if err := host.initialize(ctx, request); err != nil {
			return nil, err
		}
		if err := host.processEnrollments(ctx, request.Session); err != nil {
			return nil, err
		}
		return session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	})
	mcp.AddTool(proxy, &mcp.Tool{Name: "ownward_connect", Description: "按用户需求连接另一个智能体。生成公开连接材料，在目标宿主打开；批准由本可信宿主办理。"}, func(ctx context.Context, request *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, connectionMaterial, error) {
		host.routeMu.Lock()
		err := host.refreshRemote(ctx, &session)
		host.routeMu.Unlock()
		if err != nil {
			return nil, connectionMaterial{}, err
		}
		host.routeMu.RLock()
		defer host.routeMu.RUnlock()
		if err := host.initialize(ctx, request); err != nil {
			return nil, connectionMaterial{}, err
		}
		if err := host.processEnrollments(ctx, request.Session); err != nil {
			return nil, connectionMaterial{}, err
		}
		id := connectionID()
		var e contract.Enrollment
		if err := remoteCall(ctx, host.remote.Client, host.remote.Material.Location, "/remote/enrollment/invite", host.credential(), map[string]string{"id": id}, &e); err != nil {
			return nil, connectionMaterial{}, err
		}
		return nil, connectionMaterial{Location: host.remote.Material.Location, EnrollmentID: id}, nil
	})
	addMigrationTool(proxy, host)
	return runConnectorIO(ctx, proxy, host.streaming, os.Stdin, os.Stdout, stopOrganization)
}

func addMigrationTool(proxy *mcp.Server, host *hostConnector) {
	mcp.AddTool(proxy, &mcp.Tool{Name: "ownward_migrate", Description: "按用户需求将信息体系迁往已准备的目的地。使用目的地正式安装入口提供的公开描述，确认后自动搬迁和接续；重复调用接续同一次搬迁。"}, func(ctx context.Context, request *mcp.CallToolRequest, input struct {
		Target contract.Location `json:"target"`
	}) (*mcp.CallToolResult, receiverState, error) {
		if err := host.initialize(ctx, request); err != nil {
			return nil, receiverState{}, err
		}
		host.routeMu.RLock()
		defer host.routeMu.RUnlock()
		host.operationMu.Lock()
		defer host.operationMu.Unlock()
		id := host.record.MigrationID
		if id == "" {
			id = connectionID()
			host.record.NextLocation = &input.Target
			host.record.MigrationID = id
			if err := host.save(); err != nil {
				return nil, receiverState{}, err
			}
		} else if host.record.NextLocation == nil || *host.record.NextLocation != input.Target {
			return nil, receiverState{}, errors.New("已有搬迁尚未完成，请接续原目的地")
		}
		// Persist identity before preparing either side or asking for a decision.
		// A disconnected form resumes the same operation, never an orphaned new one.
		var identity struct {
			Handoff *contract.Handoff `json:"handoff"`
		}
		if err := remoteCall(ctx, host.remote.Client, host.remote.Material.Location, "/remote/identity", "", nil, &identity); err != nil {
			return nil, receiverState{}, err
		}
		var handoff contract.Handoff
		if identity.Handoff != nil && identity.Handoff.ID == id && identity.Handoff.Phase != "prepared" {
			handoff = *identity.Handoff
		} else {
			if err := remoteCall(ctx, host.remote.Client, host.remote.Material.Location, "/remote/migration/prepare", host.credential(), map[string]any{"id": id, "target": input.Target}, &handoff); err != nil {
				return nil, receiverState{}, err
			}
		}
		// The form approves the version it presented. Polling may discover that
		// approval was invalidated, but cannot lend a new revision to this form.
		decisionRevision := handoff.Revision
		state, err := awaitOwnerDecision(ctx, request.Session, "将信息体系搬迁到你选择的目的地吗？搬迁期间暂停普通修改，接管时暂不可用；等待取决于资料量和两端网络。信息、权限与连接关系保留。源退出前可取消，退出后继续完成交接。", func(c context.Context) (string, error) {
			if handoff.Phase == "cancelled" {
				return handoff.ApprovalStatus, nil
			}
			if handoff.Phase != "prepared" {
				return "approved", nil
			}
			err := remoteCall(c, host.remote.Client, host.remote.Material.Location, "/remote/migration/status", host.credential(), map[string]string{"id": id}, &handoff)
			status := handoff.ApprovalStatus
			if status == "" {
				status = "awaiting_approval"
			}
			return status, err
		}, func(c context.Context, accept bool) (string, error) {
			err := remoteCall(c, host.remote.Client, host.remote.Material.Location, "/remote/migration/decide", host.credential(), map[string]any{"id": id, "revision": decisionRevision, "accept": accept}, &handoff)
			return handoff.ApprovalStatus, err
		})
		if err != nil {
			return nil, receiverState{}, err
		}
		if state == "declined" || state == "cancelled" {
			host.record.MigrationID, host.record.NextLocation = "", nil
			if err := host.save(); err != nil {
				return nil, receiverState{}, err
			}
			return nil, receiverState{}, errors.New("用户未批准搬迁")
		}
		var result receiverState
		err = remoteCall(ctx, host.remote.Client, host.remote.Material.Location, "/remote/migration/start", host.credential(), map[string]any{"id": id, "location_saved": true}, &result)
		if err == nil && result.Status == "active" {
			host.record.MigrationID = ""
			delete(host.record.Decisions, "move:"+id)
			err = host.save()
		}
		return nil, result, err
	})
}

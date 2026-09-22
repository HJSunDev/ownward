// Package ownerview projects the active authority. It has no model dependency,
// background worker, business cache or second store.
package ownerview

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/informationcontrol"
	"github.com/HJSunDev/ownward/internal/semantics"
)

type Service struct {
	Store      *boundedstore.Store
	Control    *informationcontrol.Control
	Management *informationcontrol.Product
	codec      cipher.AEAD
}

var _ contract.OwnerView = (*Service)(nil)
var _ contract.AgentDraftWork = (*Service)(nil)

func New(store *boundedstore.Store, control *informationcontrol.Control, management *informationcontrol.Product) (*Service, error) {
	var key [32]byte
	if _, e := rand.Read(key[:]); e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key[:])
	if e != nil {
		return nil, e
	}
	codec, e := cipher.NewGCMWithRandomNonce(block)
	if e != nil {
		return nil, e
	}
	return &Service{Store: store, Control: control, Management: management, codec: codec}, nil
}

type handle struct {
	Type, ID, System, Binding, Position string
	Revision, OwnerRevision             uint64
	Checkpoint                          *boundedstore.OwnerCheckpoint `json:",omitempty"`
	Meaning                             boundedstore.GraphText        `json:",omitempty"`
}

func (s *Service) seal(h handle) string {
	b, _ := json.Marshal(h)
	return base64.RawURLEncoding.EncodeToString(s.codec.Seal(nil, nil, b, []byte(contract.OwnerViewSchema)))
}
func (s *Service) open(value string) (h handle, err error) {
	if len(value) > 8192 {
		return h, contract.ErrOwnerRefresh
	}
	b, e := base64.RawURLEncoding.DecodeString(value)
	if e != nil {
		return h, contract.ErrOwnerRefresh
	}
	b, e = s.codec.Open(nil, nil, b, []byte(contract.OwnerViewSchema))
	if e != nil || json.Unmarshal(b, &h) != nil {
		return h, contract.ErrOwnerRefresh
	}
	return h, nil
}
func (s *Service) object(cp boundedstore.OwnerCheckpoint, kind, id string, revision uint64) string {
	return s.seal(handle{Type: kind, ID: id, Revision: revision, System: cp.System, OwnerRevision: cp.OwnerRevision})
}
func (s *Service) resolve(cp boundedstore.OwnerCheckpoint, token, kind string) (handle, error) {
	h, e := s.open(token)
	if e != nil || h.Type != kind || h.System != cp.System || h.OwnerRevision != cp.OwnerRevision {
		return handle{}, contract.ErrOwnerRefresh
	}
	return h, nil
}
func queryBinding(q contract.OwnerQuery) string {
	q.After = ""
	q.Cursor = ""
	q.Offset = 0
	b, _ := json.Marshal(q)
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}
func (s *Service) next(cp boundedstore.OwnerCheckpoint, q contract.OwnerQuery, position string) string {
	if position == "" {
		return ""
	}
	return s.seal(handle{Type: "page", Binding: queryBinding(q), Position: position, Checkpoint: &cp})
}
func (s *Service) cursor(cp boundedstore.OwnerCheckpoint) string {
	return s.seal(handle{Type: "checkpoint", Checkpoint: &cp})
}

func (s *Service) Query(ctx context.Context, q contract.OwnerQuery) (out contract.OwnerPage, err error) {
	defer func() {
		if errors.Is(err, boundedstore.ErrSnapshotInterrupted) || errors.Is(err, boundedstore.ErrNotFound) || errors.Is(err, boundedstore.ErrDraftConflict) || errors.Is(err, boundedstore.ErrNavigationExpired) {
			out, err = contract.OwnerPage{}, contract.ErrOwnerRefresh
		}
	}()
	cp, e := s.Store.OwnerCheckpoint(ctx)
	if e != nil {
		return out, e
	}
	if q.Limit == 0 {
		q.Limit = 30
	}
	if q.Limit < 1 || q.Limit > contract.OwnerPageLimit {
		return out, errors.New("每页须为 1 至 100 项")
	}
	out.Schema = contract.OwnerViewSchema
	out.Cursor = s.cursor(cp)
	out.Changed = true
	if q.Cursor != "" {
		h, e := s.open(q.Cursor)
		out.Changed = e != nil || h.Type != "checkpoint" || h.Checkpoint == nil || *h.Checkpoint != cp
	}
	position := ""
	if q.After != "" {
		h, e := s.open(q.After)
		valid := e == nil && h.Type == "page" && h.Checkpoint != nil && h.Binding == queryBinding(q)
		if valid && q.View == "events" {
			valid = h.Checkpoint.System == cp.System && h.Checkpoint.OwnerRevision == cp.OwnerRevision && h.Checkpoint.Deletion == cp.Deletion
		} else if valid {
			valid = *h.Checkpoint == cp
		}
		if !valid {
			return out, contract.ErrOwnerRefresh
		}
		position = h.Position
	}
	var next string
	switch q.View {
	case "changes":
	case "publish_receipt":
		var result boundedstore.OwnerPublicationRow
		result, e = s.Store.OwnerPublication(ctx, q.OperationID)
		if e == nil {
			out.Publication = &contract.OwnerPublication{State: result.State}
			if result.Asset.ID != "" {
				out.Publication.Asset = s.object(cp, "asset", result.Asset.ID, result.Asset.Revision)
			}
		}
	case "assets", "continuable", "overview":
		var rows []boundedstore.OwnerAssetRow
		rows, next, e = s.assets(ctx, q, position)
		if e != nil {
			break
		}
		for _, v := range rows {
			kind := "asset"
			if v.State == "stopped" {
				kind = "stopped"
			}
			out.Assets = append(out.Assets, contract.OwnerAsset{Handle: s.object(cp, kind, v.Meta.ID, v.Meta.Revision), Kind: v.Meta.Kind, State: v.State, CreatedAt: v.Meta.CreatedAt, UpdatedAt: v.Meta.UpdatedAt, Bytes: v.Meta.ContentBytes, HasOriginal: v.Original})
		}
		if q.View == "overview" {
			out.Overview, e = s.overview(ctx, cp, rows)
		}
	case "drafts":
		var page contract.DraftPage
		page, e = s.Store.ListDrafts(ctx, position, q.Limit)
		next = page.Next
		for _, d := range page.Items {
			v := contract.OwnerDraft{Handle: s.object(cp, "draft", d.ID, d.Revision), UpdatedAt: d.UpdatedAt, Bytes: d.ContentBytes}
			if d.Target.ID != "" {
				v.Target = s.object(cp, "asset", d.Target.ID, d.Target.Revision)
			}
			out.Drafts = append(out.Drafts, v)
		}
	case "connections":
		var rows []boundedstore.OwnerPrincipalRow
		rows, next, e = s.Store.OwnerPrincipals(ctx, position, q.Limit)
		for _, p := range rows {
			out.Connections = append(out.Connections, s.connection(cp, p))
		}
	case "draft_grants":
		var rows []boundedstore.OwnerGrantRow
		rows, next, e = s.Store.OwnerGrants(ctx, position, q.Limit)
		for _, g := range rows {
			p, readErr := s.Store.OwnerPrincipal(ctx, g.Principal)
			if readErr != nil {
				return out, readErr
			}
			out.Grants = append(out.Grants, contract.OwnerGrant{Handle: s.object(cp, "grant", g.ID, 0), Draft: s.object(cp, "draft", g.DraftID, g.DraftRevision), Connection: s.connection(cp, p), ExpiresAt: g.ExpiresAt})
		}
	case "pending", "history":
		out.Decisions, next, e = s.decisions(ctx, cp, position, q.Limit, q.View == "pending")
	case "events":
		var after uint64
		if position != "" {
			after, e = strconv.ParseUint(position, 10, 64)
			if e != nil {
				break
			}
		}
		var page contract.OwnerEventPage
		page, e = s.Store.OwnerEvents(ctx, after, q.Limit)
		out.Reset = page.Reset
		for _, v := range page.Items {
			a := contract.OwnerActivity{Kind: v.Kind, State: v.Status, At: v.At, InvalidatedRelations: v.InvalidatedRelations}
			if v.Access != nil {
				a.Subject, a.Permissions = v.Access.Subject, v.Access.Permissions
			}
			if v.Kind == "management" {
				fact, readErr := s.Store.OwnerManagementFact(ctx, v.Operation)
				if errors.Is(readErr, boundedstore.ErrNotFound) {
					a.Unavailable = true
				} else if readErr != nil {
					return out, readErr
				} else {
					a.Kind, a.TargetCount, a.UnavailableTargets = fact.Kind, fact.Targets, fact.Unavailable
					if fact.Subject != "" {
						p, readErr := s.Store.OwnerPrincipal(ctx, fact.Subject)
						if readErr != nil {
							return out, readErr
						}
						a.Subject, a.Distinction = p.Name, connectionDistinction(p.Order)
					}
				}
			}
			if v.Asset.ID != "" {
				m, readErr := s.Store.ReadAssetMeta(ctx, v.Asset.ID, 0)
				if readErr == nil {
					a.Asset = s.object(cp, "asset", m.ID, m.Revision)
				} else if errors.Is(readErr, boundedstore.ErrNotFound) {
					a.Unavailable = true
				} else {
					return out, readErr
				}
			}
			out.Activity = append(out.Activity, a)
		}
		// Event continuation is a stream position, including an empty tail.
		// Ordinary writes do not invalidate it; forget requires a fresh view.
		next = strconv.FormatUint(page.Next, 10)
	case "content", "details", "original", "original_details", "draft_content":
		kind := "asset"
		if q.View == "draft_content" {
			kind = "draft"
		}
		var h handle
		h, e = s.resolve(cp, q.Handle, kind)
		if e == nil {
			var page contract.OwnerText
			page, e = s.Store.OwnerText(ctx, h.ID, h.Revision, q.View, q.Offset, "")
			out.Text = &page
		}
	case "relations":
		var h handle
		h, e = s.resolve(cp, q.Handle, "asset")
		if e != nil {
			break
		}
		if _, e = s.Store.ReadAssetMeta(ctx, h.ID, h.Revision); e != nil {
			break
		}
		out.Relations, next, e = s.relations(ctx, cp, h.ID, position, q.Limit)
	case "relation_text":
		var h handle
		h, e = s.resolve(cp, q.Handle, "meaning")
		if e == nil {
			if h.Checkpoint == nil || h.Checkpoint.Assets != cp.Assets || h.Checkpoint.Derived != cp.Derived || h.Checkpoint.Generation != cp.Generation {
				e = contract.ErrOwnerRefresh
			} else {
				var page contract.OwnerText
				page, e = s.Store.OwnerMeaning(ctx, h.Meaning, q.Offset)
				out.Text = &page
			}
		}
	case "health":
		out.Health = "normal"
		if cp.Attention {
			out.Health = "attention"
		}
	default:
		e = errors.New("未知物主视图")
	}
	if e != nil {
		return out, e
	}
	out.Organization = "available"
	if cp.Rebuilding {
		out.Organization = "rebuilding"
	}
	if cp.Generation == "" {
		out.Organization = "unavailable"
	}
	current, e := s.Store.OwnerCheckpoint(ctx)
	if e != nil {
		return contract.OwnerPage{}, e
	}
	if current != cp {
		return contract.OwnerPage{}, contract.ErrOwnerRefresh
	}
	out.Next = s.next(cp, q, next)
	return out, nil
}

func (s *Service) Act(ctx context.Context, in contract.OwnerAction) (out contract.OwnerResult, err error) {
	defer func() {
		if errors.Is(err, boundedstore.ErrDraftConflict) || errors.Is(err, boundedstore.ErrNotFound) || errors.Is(err, boundedstore.ErrSnapshotInterrupted) {
			err = contract.ErrOwnerRefresh
		}
	}()
	cp, e := s.Store.OwnerCheckpoint(ctx)
	if e != nil {
		return out, e
	}
	out.Schema = contract.OwnerViewSchema
	var d contract.Draft
	switch in.Action {
	case "create_draft":
		input := contract.DraftInput{}
		if in.Target != "" {
			h, e := s.resolve(cp, in.Target, "asset")
			if e != nil {
				return out, e
			}
			input.Target = contract.AssetVersion{ID: h.ID, Revision: h.Revision}
		}
		if in.Text != nil {
			input.Content = boundedstore.StringSource(*in.Text)
		}
		d, e = s.Store.CreateDraft(ctx, input)
	case "replace_draft", "append_draft", "discard_draft", "publish_draft", "grant_draft":
		var h handle
		h, e = s.resolve(cp, in.Handle, "draft")
		if e != nil {
			return out, e
		}
		switch in.Action {
		case "replace_draft", "append_draft":
			if in.Text == nil {
				return out, errors.New("缺少输入文本")
			}
			d, e = s.Store.WriteDraft(ctx, contract.DraftWrite{ID: h.ID, ExpectedRevision: h.Revision, Append: in.Action == "append_draft", Content: boundedstore.StringSource(*in.Text)})
		case "discard_draft":
			e = s.Store.DiscardDraft(ctx, h.ID, h.Revision)
		case "publish_draft":
			if in.OperationID == "" {
				return out, errors.New("缺少发布操作标识")
			}
			var asset contract.AssetVersion
			asset, e = s.Store.PublishDraft(ctx, h.ID, h.Revision, contract.OperationIdentity{ID: in.OperationID})
			if e == nil {
				out.Handle = s.object(cp, "asset", asset.ID, asset.Revision)
			}
		case "grant_draft":
			var p handle
			p, e = s.resolve(cp, in.Target, "principal")
			if e != nil {
				break
			}
			var current contract.Principal
			current, e = s.Control.Principal(ctx, p.ID)
			if e != nil {
				break
			}
			if current.Revision != p.Revision {
				return out, contract.ErrOwnerRefresh
			}
			var currentDraft contract.Draft
			currentDraft, e = s.Store.DraftMetadata(ctx, h.ID, "")
			if e != nil {
				break
			}
			if currentDraft.Revision != h.Revision {
				return out, contract.ErrOwnerRefresh
			}
			var g contract.DraftGrant
			g, e = s.Store.GrantDraftVersion(ctx, h.ID, h.Revision, p.ID, p.Revision, time.Duration(in.Seconds)*time.Second)
			if e == nil {
				out.Grant = g.ID
				out.Handle = h.ID
			}
		}
	case "revoke_grant":
		var h handle
		h, e = s.resolve(cp, in.Handle, "grant")
		if e == nil {
			e = s.Management.RevokeDraftGrant(ctx, s.Store, h.ID)
		}
	case "permissions", "forget":
		if in.OperationID == "" {
			return out, errors.New("缺少操作标识")
		}
		r := contract.ManagementRequest{ID: in.OperationID, Operation: in.Action}
		kind := "asset"
		if in.Action == "permissions" {
			kind = "principal"
		}
		var h handle
		h, e = s.resolve(cp, in.Handle, kind)
		if e != nil {
			return out, e
		}
		if kind == "principal" {
			r.SubjectID = h.ID
			r.SubjectRevision = h.Revision
			r.Permissions = in.Permissions
		} else {
			r.Targets = []contract.AssetVersion{{ID: h.ID, Revision: h.Revision}}
		}
		var op contract.ManagementReceipt
		op, e = s.Management.Manage(ctx, r)
		if e == nil {
			out.Handle = s.object(cp, "operation", op.Request.ID, 0)
			out.State = op.Status
		}
	case "decide":
		e = s.decide(ctx, cp, in, &out)
	default:
		e = errors.New("未知物主操作")
	}
	if e != nil {
		return out, e
	}
	if d.ID != "" {
		out.Handle = s.object(cp, "draft", d.ID, d.Revision)
	}
	if out.State == "" {
		out.State = "completed"
	}
	if cp, e = s.Store.OwnerCheckpoint(ctx); e == nil {
		out.Cursor = s.cursor(cp)
	}
	return out, e
}

func connectionDistinction(order uint64) string {
	return fmt.Sprintf("第 %d 个登记的连接", order)
}

func (s *Service) connection(cp boundedstore.OwnerCheckpoint, p boundedstore.OwnerPrincipalRow) contract.OwnerConnection {
	return contract.OwnerConnection{Handle: s.object(cp, "principal", p.ID, p.Revision), Name: p.Name, Distinction: connectionDistinction(p.Order), Permissions: p.Permissions, Owner: p.ID == cp.Owner}
}

func (s *Service) DraftWork(ctx context.Context, in contract.AgentDraftRequest) (out contract.AgentDraftResult, err error) {
	if in.Grant == "" || in.Draft == "" {
		return out, errors.New("缺少物主授予的工作项")
	}
	d, e := s.Store.DraftMetadata(ctx, in.Draft, in.Grant)
	if e != nil {
		return out, e
	}
	binding := contract.AuthenticationDigest(ctx) + ":" + in.Grant
	if in.Handle != "" {
		h, e := s.open(in.Handle)
		if e != nil || h.Type != "agent-draft" || h.ID != d.ID || h.Revision != d.Revision || h.Binding != binding {
			return out, contract.ErrOwnerRefresh
		}
	}
	switch in.Action {
	case "read":
		if in.Offset != 0 && in.Handle == "" {
			return out, contract.ErrOwnerRefresh
		}
		var v contract.OwnerText
		v, e = s.Store.OwnerText(ctx, d.ID, d.Revision, "draft_content", in.Offset, in.Grant)
		out.Content = &v
	case "replace", "append":
		if in.Handle == "" {
			return out, contract.ErrOwnerRefresh
		}
		if len(in.Text) > contract.OwnerRequestBytes {
			return out, errors.New("单次文本超过提交上限")
		}
		d, e = s.Store.WriteDraft(ctx, contract.DraftWrite{ID: d.ID, ExpectedRevision: d.Revision, GrantID: in.Grant, Append: in.Action == "append", Content: boundedstore.StringSource(in.Text)})
	default:
		e = errors.New("工作项只允许读取、替换或追加")
	}
	if e != nil {
		return out, e
	}
	current, e := s.Store.DraftMetadata(ctx, d.ID, in.Grant)
	if e != nil {
		return out, e
	}
	if current.Revision != d.Revision {
		return out, contract.ErrOwnerRefresh
	}
	out.Handle = s.seal(handle{Type: "agent-draft", ID: d.ID, Revision: d.Revision, Binding: binding})
	return out, nil
}

func (s *Service) CheckDraftWork(ctx context.Context, in contract.AgentDraftRequest, value string) error {
	h, e := s.open(value)
	if e != nil || h.Type != "agent-draft" || h.ID != in.Draft || h.Binding != contract.AuthenticationDigest(ctx)+":"+in.Grant {
		return contract.ErrOwnerRefresh
	}
	d, e := s.Store.DraftMetadata(ctx, in.Draft, in.Grant)
	if e != nil {
		return e
	}
	if d.Revision != h.Revision {
		return contract.ErrOwnerRefresh
	}
	return ctx.Err()
}

func (s *Service) relations(ctx context.Context, cp boundedstore.OwnerCheckpoint, id, after string, limit int) (out []contract.OwnerRelation, next string, err error) {
	if cp.Generation == "" {
		return nil, "", nil
	}
	start := id
	if after != "" {
		start = after
	}
	// One evidence-bearing edge can itself have a bounded set of conditions.
	// Advance one edge per page to keep decoded relation memory independent of
	// the caller's requested item count; the existing continuation is preserved.
	page, e := s.Store.NavigatePage(ctx, cp.Generation, []string{start}, nil, 1, 1)
	if e != nil {
		return nil, "", e
	}
	for _, edge := range page.Edges {
		source, e := s.Store.ReadAssetMeta(ctx, edge.SourceID, 0)
		if e != nil {
			return nil, "", e
		}
		target, e := s.Store.ReadAssetMeta(ctx, edge.TargetID, 0)
		if e != nil {
			return nil, "", e
		}
		v := contract.OwnerRelation{Type: edge.Type, Source: s.object(cp, "asset", source.ID, source.Revision), Target: s.object(cp, "asset", target.ID, target.Revision)}
		v.Evidence = edge.Evidence
		if edge.Meaning.Organization != "" {
			h := handle{Type: "meaning", System: cp.System, OwnerRevision: cp.OwnerRevision, Meaning: edge.Meaning, Checkpoint: &cp}
			v.Meaning = s.seal(h)
		}
		if edge.Grounded != nil {
			for i, ep := range append([]semantics.GraphEndpoint{edge.Grounded.Source, edge.Grounded.Target}, edge.Grounded.Conditions...) {
				if i >= len(edge.Endpoints) {
					return nil, "", errors.New("关系依据缺失")
				}
				loc := edge.Endpoints[i]
				role := "condition"
				if i == 0 {
					role = "source"
				}
				if i == 1 {
					role = "target"
				}
				v.Basis = append(v.Basis, contract.OwnerBasis{Asset: s.object(cp, "asset", ep.AssetID, ep.Revision), StartRune: loc.Span.Start, EndRune: loc.Span.End, Role: role})
				for _, span := range loc.Context {
					v.Basis = append(v.Basis, contract.OwnerBasis{Asset: s.object(cp, "asset", ep.AssetID, ep.Revision), StartRune: span.Start, EndRune: span.End, Role: "context"})
				}
			}
		}
		out = append(out, v)
	}
	return out, page.Continuation, nil
}

func (s *Service) overview(ctx context.Context, cp boundedstore.OwnerCheckpoint, rows []boundedstore.OwnerAssetRow) (*contract.OwnerOverview, error) {
	out := &contract.OwnerOverview{Examined: len(rows), Types: map[string]int{}, Approximate: true}
	for _, v := range rows {
		if v.State != "ready" {
			out.Pending++
			continue
		}
		edges, next, e := s.relations(ctx, cp, v.Meta.ID, "", 1)
		if e != nil {
			return nil, e
		}
		if len(edges) == 0 {
			if next != "" {
				out.Unresolved++
			} else {
				out.Unconnected++
			}
		} else {
			out.Connected++
			out.Types[edges[0].Type]++
		}
	}
	return out, nil
}

// ScopeBinding makes a decision handle sensitive to the complete immutable
// request, not just its display label or destination service identifier.
func scopeBinding(value any) string {
	b, _ := json.Marshal(value)
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h[:])
}

func (s *Service) assets(ctx context.Context, q contract.OwnerQuery, position string) ([]boundedstore.OwnerAssetRow, string, error) {
	if q.State == "stopped" || strings.HasPrefix(position, "s:") {
		if q.Query != "" || q.Kind != "" {
			return nil, "", nil
		} // no forgotten text or metadata lookup
		rows, next, e := s.Store.OwnerStopped(ctx, strings.TrimPrefix(position, "s:"), q.Limit)
		if next != "" {
			next = "s:" + next
		}
		return rows, next, e
	}
	rows, next, e := s.Store.OwnerAssets(ctx, strings.TrimPrefix(position, "a:"), q.Query, q.State, q.Kind, q.Limit)
	if e != nil {
		return nil, "", e
	}
	if next != "" {
		return rows, "a:" + next, nil
	}
	if q.View == "assets" && q.State == "" && q.Query == "" && q.Kind == "" {
		if len(rows) == q.Limit {
			return rows, "s:", nil
		}
		stopped, next, e := s.Store.OwnerStopped(ctx, "", q.Limit-len(rows))
		if next != "" {
			next = "s:" + next
		}
		return append(rows, stopped...), next, e
	}
	return rows, "", nil
}

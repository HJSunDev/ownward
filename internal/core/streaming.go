package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/domain"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

// StreamingAssets 是新存储格式的资产执行端口；存储格式交付前不切换旧用户资料。
type StreamingAssets struct {
	Store     *boundedstore.Store
	Budget    *resourcebudget.Budget
	Scratch   string
	DiskBytes int64
}

var _ contract.StreamingProduct = (*StreamingAssets)(nil)

func (s *StreamingAssets) ExecuteStream(ctx context.Context, request contract.StreamRequest) (*contract.StreamResult, error) {
	// 一次任务先取得完整工作区，内部阶段再使用子预算，避免多个任务各占一部分后互相等待。
	done, err := s.Budget.Acquire(ctx, 4*resourcebudget.MiB, false)
	if err != nil {
		return nil, err
	}
	defer done()
	work, _ := resourcebudget.New(4*resourcebudget.MiB, 0)
	ctx = resourcebudget.WithContext(ctx, work)
	permission := contract.MaintainPermission
	if request.Operation == "ownward_read" {
		permission = contract.ReadPermission
	}
	ctx, err = s.Store.BeginAccess(ctx, contract.AuthenticationDigest(ctx), permission)
	if err != nil {
		return nil, err
	}
	r, err := request.Arguments.Open(ctx)
	if err != nil {
		return nil, err
	}
	args, err := streamjson.Parse(ctx, s.Scratch, r, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes)
	r.Close()
	if err != nil {
		return nil, err
	}
	defer args.Close()
	if request.Operation == "ownward_read" {
		id, err := fieldString(args.Root(), "id", 256)
		if err != nil {
			return nil, err
		}
		return s.deliver(ctx, []contract.MutationOutcome{{Asset: contract.AssetVersion{ID: strings.TrimSpace(id)}}}, true, false)
	}
	if request.Operation != "ownward_create" && request.Operation != "ownward_create_batch" && request.Operation != "ownward_update" {
		return nil, errors.New("该流式端口不承担此操作")
	}
	op, ok := contract.Operation(ctx)
	if !ok {
		return nil, errors.New("写入缺少连接器操作身份")
	}
	h := sha256.New()
	io.WriteString(h, request.Operation+":")
	if err = args.Root().Canonical(ctx, h); err != nil {
		return nil, err
	}
	if op.Kind != request.Operation || op.Digest != hex.EncodeToString(h.Sum(nil)) {
		return nil, errors.New("操作身份与真实参数不一致")
	}
	if receipt, found, err := s.Store.MutationReceipt(ctx, op); err != nil {
		return nil, err
	} else if found {
		return s.deliver(ctx, receipt.Results, false, request.Operation == "ownward_create_batch")
	}
	var inputs []streamjson.Node
	if request.Operation == "ownward_create_batch" {
		items, found, err := args.Root().Field("items")
		if err != nil || !found || items.Kind != '[' || items.Count < 1 || items.Count > 20 {
			return nil, errors.New("每批必须包含一到二十条信息")
		}
		cursor := items.Children()
		for {
			item, e := cursor.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, e
			}
			inputs = append(inputs, item)
		}
	} else {
		inputs = []streamjson.Node{args.Root()}
	}
	receipt := contract.MutationReceipt{Operation: op}
	var writes []boundedstore.AssetWrite
	defer func() {
		for _, write := range writes {
			_ = s.Store.Abandon(context.WithoutCancel(ctx), write.Payload)
		}
	}()
	for _, input := range inputs {
		write, e := s.prepare(ctx, op, input, request.Operation == "ownward_update")
		if e != nil {
			if request.Operation != "ownward_create_batch" {
				return nil, e
			}
			receipt.Results = append(receipt.Results, contract.MutationOutcome{Error: e.Error()})
			continue
		}
		writes = append(writes, write)
		receipt.Results = append(receipt.Results, contract.MutationOutcome{Asset: contract.AssetVersion{ID: write.Meta.ID, Revision: write.Meta.Revision}})
	}
	if err = s.Store.Publish(ctx, receipt, writes); err != nil {
		return nil, err
	}
	// 并发同操作只有一个回执获准发布，返回真正已经提交的结果。
	actual, found, err := s.Store.MutationReceipt(ctx, op)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("提交后缺少操作回执")
	}
	return s.deliver(ctx, actual.Results, false, request.Operation == "ownward_create_batch")
}

func fieldString(n streamjson.Node, key string, maximum int64) (string, error) {
	v, ok, err := n.Field(key)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("缺少字段 " + key)
	}
	return v.String(maximum)
}

type currentPart struct {
	store    *boundedstore.Store
	id       string
	revision uint64
	details  bool
}

func (p currentPart) Open(ctx context.Context) (io.ReadCloser, error) {
	if p.details {
		return p.store.OpenDetails(ctx, p.id, p.revision)
	}
	return p.store.OpenContent(ctx, p.id, p.revision)
}

func (s *StreamingAssets) prepare(ctx context.Context, op contract.OperationIdentity, input streamjson.Node, update bool) (boundedstore.AssetWrite, error) {
	var out boundedstore.AssetWrite
	now := time.Now().UTC()
	id, err := newID(now)
	if err != nil {
		return out, err
	}
	out.Meta = contract.AssetMeta{ID: id, Revision: 1, CreatedAt: now, UpdatedAt: now, Kind: domain.KindGeneral}
	var old *streamjson.Document
	if update {
		id, err = fieldString(input, "id", 256)
		if err != nil {
			return out, err
		}
		id = strings.TrimSpace(id)
		v, ok, e := input.Field("expected_revision")
		if e != nil || !ok {
			return out, errors.New("缺少预期版本")
		}
		if err = v.DecodeSmall(&out.ExpectedRevision, 32); err != nil || out.ExpectedRevision == 0 {
			return out, errors.New("预期版本无效")
		}
		out.Meta, err = s.Store.ReadAssetMeta(ctx, id, out.ExpectedRevision)
		if err != nil {
			return out, err
		}
		out.Meta.Revision++
		out.Meta.UpdatedAt = now
		r, err := s.Store.OpenDetails(ctx, id, out.ExpectedRevision)
		if err != nil {
			return out, err
		}
		old, err = streamjson.Parse(ctx, s.Scratch, r, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes)
		r.Close()
		if err != nil {
			return out, err
		}
		defer old.Close()
	}
	changed := false
	if kind, ok, e := input.Field("kind"); e != nil {
		return out, e
	} else if ok && kind.Kind != 'n' {
		value, e := kind.String(128)
		if e != nil {
			return out, e
		}
		if value != "" || update {
			out.Meta.Kind, e = domain.ParseKind(value)
			if e != nil {
				return out, e
			}
		}
		changed = true
	}
	var content contract.ContentSource
	if n, ok, e := input.Field("content"); e != nil {
		return out, e
	} else if ok && n.Kind != 'n' {
		if n.Kind != '"' {
			return out, errors.New("正文必须为字符串")
		}
		content = n
		changed = true
	} else if update {
		content = currentPart{s.Store, id, out.ExpectedRevision, false}
	} else {
		return out, errors.New("缺少正文")
	}
	fields := map[string]streamjson.Node{}
	for _, key := range []string{"contexts", "explicit_relations", "source"} {
		if v, ok, e := input.Field(key); e != nil {
			return out, e
		} else if ok && v.Kind != 'n' {
			fields[key] = v
			changed = true
		} else if old != nil {
			if v, ok, e := old.Root().Field(key); e != nil {
				return out, e
			} else if ok {
				fields[key] = v
			}
		}
	}
	if update && !changed {
		return out, errors.New("更新内容不能为空")
	}
	details, links, err := s.details(ctx, fields, id, content, !update)
	if err != nil {
		return out, err
	}
	defer details.Close()
	defer links.Close()
	key, err := boundedstore.OperationKey(op)
	if err != nil {
		return out, err
	}
	out.Payload, err = s.Store.Stage(ctx, key, content, streamjson.RawSource{Node: details.Root()})
	if err != nil {
		return out, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = s.Store.Abandon(context.WithoutCancel(ctx), out.Payload)
		}
	}()
	cursor := links.Root().Children()
	for {
		node, e := cursor.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return out, e
		}
		var link boundedstore.ExplicitLink
		if e = node.DecodeSmall(&link, 4096); e != nil {
			return out, e
		}
		if e = s.Store.StageLink(ctx, out.Payload, link); e != nil {
			return out, e
		}
	}
	complete = true
	return out, nil
}

func (s *StreamingAssets) deliver(ctx context.Context, outcomes []contract.MutationOutcome, read, batch bool) (*contract.StreamResult, error) {
	epochs := map[string]uint64{}
	doc, err := streamjson.Build(ctx, s.Scratch, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes, func(w io.Writer) error {
		if batch {
			if _, err := io.WriteString(w, `{"results":[`); err != nil {
				return err
			}
		}
		for i, outcome := range outcomes {
			if batch && i > 0 {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			if outcome.Error != "" {
				b, _ := json.Marshal(map[string]string{"error": outcome.Error})
				if _, err := w.Write(b); err != nil {
					return err
				}
				continue
			}
			meta, err := s.Store.ReadAssetMeta(ctx, outcome.Asset.ID, outcome.Asset.Revision)
			if err != nil {
				if batch && !read && errors.Is(err, boundedstore.ErrNotFound) {
					b, _ := json.Marshal(map[string]string{"error": "操作已提交，原结果已更新或遗忘"})
					if _, err := w.Write(b); err != nil {
						return err
					}
					continue
				}
				return err
			}
			epoch, err := s.Store.SourceEpoch(ctx, outcome.Asset.ID)
			if err != nil {
				return err
			}
			epochs[outcome.Asset.ID] = epoch
			if !read {
				if _, err = io.WriteString(w, `{"result":`); err != nil {
					return err
				}
			}
			if _, err = io.WriteString(w, `{"information":`); err != nil {
				return err
			}
			hash := sha256.New()
			runes, err := s.writeInformation(ctx, io.MultiWriter(w, hash), meta)
			if err != nil {
				return err
			}
			if read {
				if err = s.writeReadBasis(ctx, w, meta, hex.EncodeToString(hash.Sum(nil)), runes); err != nil {
					return err
				}
			} else {
				if _, err = io.WriteString(w, `,"organization":{"status":"pending","provider":"external-semantic-capability","required_action":"ownward_semantic_work"}}}`); err != nil {
					return err
				}
			}
		}
		if batch {
			_, err := io.WriteString(w, "]}")
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	check := func(delivery context.Context) error {
		// 上下文中的授权快照属于本操作，交付前按最新控制状态复核。
		checking, cancel := context.WithCancel(context.WithoutCancel(ctx))
		defer cancel()
		stop := context.AfterFunc(delivery, cancel)
		defer stop()
		if err := delivery.Err(); err != nil {
			return err
		}
		return s.Store.AuthorizeDelivery(checking, epochs)
	}
	return &contract.StreamResult{Value: streamjson.RawSource{Node: doc.RootContext(context.WithoutCancel(ctx))}, Check: check, Close: doc.Close}, nil
}

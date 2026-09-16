package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/HJSunDev/ownward/internal/boundedstore"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

func writeJSON(w io.Writer, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}
func (s *StreamingAssets) details(ctx context.Context, fields map[string]streamjson.Node, id string, content contract.ContentSource, selfAlias bool) (*streamjson.Document, *streamjson.Document, error) {
	set, err := s.Store.NewWorkSet(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer set.Close()
	var details *streamjson.Document
	links, err := streamjson.Build(ctx, s.Scratch, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes, func(linkOut io.Writer) error {
		if _, err := io.WriteString(linkOut, "["); err != nil {
			return err
		}
		linkFirst := true
		var e error
		details, e = streamjson.Build(ctx, s.Scratch, resourcebudget.FromContext(ctx, s.Budget), s.DiskBytes, func(w io.Writer) error {
			if _, err := io.WriteString(w, "{"); err != nil {
				return err
			}
			first := true
			if contexts, ok := fields["contexts"]; ok && contexts.Kind != 'n' {
				if contexts.Kind != '[' {
					return errors.New("场景必须为数组")
				}
				if contexts.Count > 0 {
					if _, err := io.WriteString(w, `"contexts":[`); err != nil {
						return err
					}
					first = false
					itemFirst := true
					cursor := contexts.Children()
					for {
						item, err := cursor.Next()
						if err == io.EOF {
							break
						}
						if err != nil {
							return err
						}
						key, ok, err := item.Field("key")
						if err != nil || !ok {
							return errors.New("场景缺少键")
						}
						value, ok, err := item.Field("value")
						if err != nil || !ok {
							return errors.New("场景缺少值")
						}
						k, err := key.Trimmed(ctx)
						if err != nil {
							return err
						}
						v, err := value.Trimmed(ctx)
						if err != nil {
							return err
						}
						if k.Length == 0 || v.Length == 0 {
							return errors.New("场景键和值均不能为空")
						}
						hash, err := streamjson.FoldedPairDigest(ctx, k, v)
						if err != nil {
							return err
						}
						add, err := set.Add(ctx, hash)
						if err != nil {
							return err
						}
						if !add {
							continue
						}
						if !itemFirst {
							if _, err = io.WriteString(w, ","); err != nil {
								return err
							}
						}
						itemFirst = false
						if _, err = io.WriteString(w, `{"key":`); err != nil {
							return err
						}
						if err = k.WriteJSON(ctx, w); err != nil {
							return err
						}
						if _, err = io.WriteString(w, `,"value":`); err != nil {
							return err
						}
						if err = v.WriteJSON(ctx, w); err != nil {
							return err
						}
						if _, err = io.WriteString(w, "}"); err != nil {
							return err
						}
					}
					if _, err := io.WriteString(w, "]"); err != nil {
						return err
					}
				}
			}
			if relations, ok := fields["explicit_relations"]; ok && relations.Kind != 'n' {
				if relations.Kind != '[' {
					return errors.New("明确关系必须为数组")
				}
				if relations.Count > 0 {
					if !first {
						if _, err := io.WriteString(w, ","); err != nil {
							return err
						}
					}
					first = false
					if _, err := io.WriteString(w, `"explicit_relations":[`); err != nil {
						return err
					}
					cursor := relations.Children()
					ordinal := int64(0)
					for {
						item, err := cursor.Next()
						if err == io.EOF {
							break
						}
						if err != nil {
							return err
						}
						typ, ok, err := item.Field("type")
						if err != nil || !ok {
							return errors.New("关系缺少类型")
						}
						trimmed, err := typ.Trimmed(ctx)
						if err != nil || trimmed.Length == 0 {
							return errors.New("关系类型不能为空")
						}
						target, err := fieldString(item, "target_id", 256)
						if err != nil {
							return err
						}
						if selfAlias && target == "$self" {
							target = id
						}
						if strings.TrimSpace(target) == "" {
							return errors.New("关系目标不能为空")
						}
						kind, _ := typ.String(128)
						selector, selected, err := item.Field("selector")
						if err != nil {
							return err
						}
						selected = selected && selector.Kind != 'n'
						if target == id && (kind != "qualifies" || !selected) {
							return errors.New("信息不能显式关联自身")
						}
						if target != id {
							if _, err = s.Store.ReadAssetMeta(ctx, target, 0); err != nil {
								return errors.New("明确关系的目标信息不存在")
							}
						}
						link := boundedstore.ExplicitLink{Ordinal: ordinal, Target: target, Qualifies: kind == "qualifies"}
						if selected {
							if kind != "qualifies" {
								return errors.New("原文定位仅用于明确说明")
							}
							exact, ok, err := selector.Field("exact")
							if err != nil || !ok {
								return errors.New("说明定位缺少原文")
							}
							trim, err := exact.Trimmed(ctx)
							if err != nil || trim.Length == 0 {
								return errors.New("说明定位的原文不能为空")
							}
							var prefix, suffix contract.ContentSource = boundedstore.StringSource(""), boundedstore.StringSource("")
							if p, ok, e := selector.Field("prefix"); e != nil {
								return e
							} else if ok {
								prefix = p
							}
							if p, ok, e := selector.Field("suffix"); e != nil {
								return e
							} else if ok {
								suffix = p
							}
							link.StartRune, link.EndRune, err = streamjson.ResolveSelector(ctx, s.Scratch, content, prefix, exact, suffix)
							if err != nil {
								return err
							}
						}
						if ordinal > 0 {
							if _, err = io.WriteString(w, ","); err != nil {
								return err
							}
						}
						if _, err = io.WriteString(w, `{"type":`); err != nil {
							return err
						}
						if err = typ.Canonical(ctx, w); err != nil {
							return err
						}
						if _, err = io.WriteString(w, `,"target_id":`); err != nil {
							return err
						}
						if err = writeJSON(w, target); err != nil {
							return err
						}
						if selected {
							if _, err = io.WriteString(w, `,"selector":`); err != nil {
								return err
							}
							if err = writeStringFields(ctx, w, selector, "exact", "prefix", "suffix"); err != nil {
								return err
							}
						}
						if _, err = io.WriteString(w, "}"); err != nil {
							return err
						}
						if !linkFirst {
							if _, err = io.WriteString(linkOut, ","); err != nil {
								return err
							}
						}
						linkFirst = false
						if err = writeJSON(linkOut, link); err != nil {
							return err
						}
						ordinal++
					}
					if _, err := io.WriteString(w, "]"); err != nil {
						return err
					}
				}
			}
			if !first {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			if _, err := io.WriteString(w, `"source":`); err != nil {
				return err
			}
			if source, ok := fields["source"]; ok && source.Kind != 'n' {
				if source.Kind != '{' {
					return errors.New("来源必须为对象")
				}
				if err := writeStringFields(ctx, w, source, "actor", "ref"); err != nil {
					return err
				}
			} else {
				if _, err := io.WriteString(w, "{}"); err != nil {
					return err
				}
			}
			_, err := io.WriteString(w, "}")
			return err
		})
		if e != nil {
			return e
		}
		_, e = io.WriteString(linkOut, "]")
		return e
	})
	if err != nil {
		if details != nil {
			details.Close()
		}
		return nil, nil, err
	}
	return details, links, nil
}

// 保持权威结构的字段顺序和 omitempty 规则，依据身份不因传输实现改变。
func writeStringFields(ctx context.Context, w io.Writer, node streamjson.Node, keys ...string) error {
	if _, err := io.WriteString(w, "{"); err != nil {
		return err
	}
	first := true
	for _, key := range keys {
		value, found, err := node.Field(key)
		if err != nil {
			return err
		}
		if !found || value.Kind == 'n' {
			continue
		}
		r, err := value.Open(ctx)
		if err != nil {
			return err
		}
		var probe [1]byte
		n, err := r.Read(probe[:])
		r.Close()
		if err != nil && err != io.EOF {
			return err
		}
		if n == 0 {
			continue
		}
		if !first {
			if _, err = io.WriteString(w, ","); err != nil {
				return err
			}
		}
		first = false
		if err = writeJSON(w, key); err != nil {
			return err
		}
		if _, err = io.WriteString(w, ":"); err != nil {
			return err
		}
		if err = value.Canonical(ctx, w); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "}")
	return err
}

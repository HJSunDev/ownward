package streamjson

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
)

// Validate 对 Ownward 工具的对象、数组与叶值逐项校验；原文从不转成完整字符串。
// 不支持的新增约束明确拒绝，不能默默略过Schema条件。
func (n Node) Validate(ctx context.Context, s *jsonschema.Schema) error { return n.validate(ctx, s, s) }
func (n Node) validate(ctx context.Context, s, root *jsonschema.Schema) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return nil
	}
	if s.Ref != "" {
		key := strings.TrimPrefix(s.Ref, "#/$defs/")
		target := root.Defs[key]
		if target == nil {
			return fmt.Errorf("未解析的Schema引用 %s", s.Ref)
		}
		return n.validate(ctx, target, root)
	}
	if len(s.AnyOf) > 0 {
		for _, part := range s.AnyOf {
			if n.validate(ctx, part, root) == nil {
				return nil
			}
		}
		return errors.New("参数不符合可选类型")
	}
	if len(s.OneOf) > 0 || len(s.AllOf) > 0 || s.If != nil || len(s.PatternProperties) > 0 || len(s.DependentSchemas) > 0 || s.Pattern != "" || s.UniqueItems || s.Contains != nil || s.PropertyNames != nil {
		return errors.New("工具新增Schema约束尚未提供流式校验")
	}
	if s.Not != nil {
		return errors.New("参数不允许该字段或值")
	}
	if len(s.Types) > 0 {
		for _, typ := range s.Types {
			copy := *s
			copy.Type = typ
			copy.Types = nil
			if n.validate(ctx, &copy, root) == nil {
				return nil
			}
		}
		return errors.New("参数类型不匹配")
	}
	if s.Type != "" {
		matches := false
		switch s.Type {
		case "object":
			matches = n.Kind == '{'
		case "array":
			matches = n.Kind == '['
		case "string":
			matches = n.Kind == '"'
		case "boolean":
			matches = n.Kind == 't' || n.Kind == 'f'
		case "null":
			matches = n.Kind == 'n'
		case "number", "integer":
			matches = n.Kind == '0'
		}
		if !matches {
			return fmt.Errorf("参数要求 %s", s.Type)
		}
	}
	if len(s.Enum) > 0 || s.Const != nil {
		var v any
		if err := n.DecodeSmall(&v, 4096); err != nil {
			return errors.New("参数不属于允许值")
		}
		small := &jsonschema.Schema{Enum: s.Enum, Const: s.Const}
		resolved, err := small.Resolve(nil)
		if err != nil {
			return err
		}
		if err = resolved.Validate(v); err != nil {
			return err
		}
	}
	switch n.Kind {
	case '{':
		if s.MinProperties != nil && n.Count < int64(*s.MinProperties) || s.MaxProperties != nil && n.Count > int64(*s.MaxProperties) {
			return errors.New("对象字段数量不符")
		}
		for _, key := range s.Required {
			if _, ok, err := n.Field(key); err != nil || !ok {
				return fmt.Errorf("缺少必要字段 %s", key)
			}
		}
		c := n.Children()
		for {
			v, err := c.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			key, err := v.Key()
			if err != nil {
				return err
			}
			part, ok := s.Properties[key]
			if !ok {
				part = s.AdditionalProperties
			}
			if err = v.validate(ctx, part, root); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	case '[':
		if s.MinItems != nil && n.Count < int64(*s.MinItems) || s.MaxItems != nil && n.Count > int64(*s.MaxItems) {
			return errors.New("数组条目数量不符")
		}
		c := n.Children()
		for {
			v, err := c.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return err
			}
			if err = v.validate(ctx, s.Items, root); err != nil {
				return err
			}
		}
	case '"':
		if s.MinLength != nil || s.MaxLength != nil {
			r, err := n.Open(ctx)
			if err != nil {
				return err
			}
			defer r.Close()
			count, err := countRunes(r)
			if err != nil {
				return err
			}
			if s.MinLength != nil && count < int64(*s.MinLength) || s.MaxLength != nil && count > int64(*s.MaxLength) {
				return errors.New("字符串长度不符")
			}
		}
	case '0':
		var value float64
		if err := n.DecodeSmall(&value, 1024); err != nil {
			return err
		}
		if s.Type == "integer" && math.Trunc(value) != value {
			return errors.New("参数必须为整数")
		}
		small := &jsonschema.Schema{Minimum: s.Minimum, Maximum: s.Maximum, ExclusiveMinimum: s.ExclusiveMinimum, ExclusiveMaximum: s.ExclusiveMaximum, MultipleOf: s.MultipleOf}
		resolved, err := small.Resolve(nil)
		if err != nil {
			return err
		}
		return resolved.Validate(value)
	}
	return nil
}

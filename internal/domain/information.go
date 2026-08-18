package domain

import (
	"errors"
	"strings"
	"time"
)

const AssetSchema = "ownward.information/v1"

type InformationKind string

const (
	KindGeneral    InformationKind = "information"
	KindExperience InformationKind = "experience"
	KindThought    InformationKind = "thought"
	KindSocial     InformationKind = "social"
	KindKnowledge  InformationKind = "knowledge"
	KindSkill      InformationKind = "skill"
	KindWork       InformationKind = "work"
	KindMethod     InformationKind = "method"
	KindLesson     InformationKind = "lesson"
	KindSolution   InformationKind = "solution"
	KindPath       InformationKind = "path"
)

var validKinds = map[InformationKind]struct{}{
	KindGeneral:    {},
	KindExperience: {},
	KindThought:    {},
	KindSocial:     {},
	KindKnowledge:  {},
	KindSkill:      {},
	KindWork:       {},
	KindMethod:     {},
	KindLesson:     {},
	KindSolution:   {},
	KindPath:       {},
}

type Context struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ExplicitRelation struct {
	Type     string `json:"type"`
	TargetID string `json:"target_id"`
}

type Source struct {
	Actor string `json:"actor,omitempty"`
	Ref   string `json:"ref,omitempty"`
}

type Information struct {
	Schema    string             `json:"schema"`
	ID        string             `json:"id"`
	Revision  uint64             `json:"revision"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	Kind      InformationKind    `json:"kind"`
	Content   string             `json:"content"`
	Contexts  []Context          `json:"contexts,omitempty"`
	Relations []ExplicitRelation `json:"explicit_relations,omitempty"`
	Source    Source             `json:"source,omitempty"`
}

func (i Information) Validate() error {
	if i.Schema != AssetSchema {
		return errors.New("不支持的信息资产格式")
	}
	if strings.TrimSpace(i.ID) == "" {
		return errors.New("信息标识不能为空")
	}
	if i.Revision == 0 {
		return errors.New("信息版本必须大于零")
	}
	if _, ok := validKinds[i.Kind]; !ok {
		return errors.New("不支持的信息类型")
	}
	if strings.TrimSpace(i.Content) == "" {
		return errors.New("信息内容不能为空")
	}
	if i.CreatedAt.IsZero() || i.UpdatedAt.IsZero() || i.UpdatedAt.Before(i.CreatedAt) {
		return errors.New("信息时间无效")
	}
	for _, context := range i.Contexts {
		if strings.TrimSpace(context.Key) == "" || strings.TrimSpace(context.Value) == "" {
			return errors.New("场景键和值均不能为空")
		}
	}
	for _, relation := range i.Relations {
		if strings.TrimSpace(relation.Type) == "" || strings.TrimSpace(relation.TargetID) == "" {
			return errors.New("关系类型和目标均不能为空")
		}
		if relation.TargetID == i.ID {
			return errors.New("信息不能显式关联自身")
		}
	}
	return nil
}

func ParseKind(value string) (InformationKind, error) {
	kind := InformationKind(strings.ToLower(strings.TrimSpace(value)))
	if _, ok := validKinds[kind]; !ok {
		return "", errors.New("不支持的信息类型")
	}
	return kind, nil
}

func Kinds() []InformationKind {
	return []InformationKind{
		KindGeneral,
		KindExperience,
		KindThought,
		KindSocial,
		KindKnowledge,
		KindSkill,
		KindWork,
		KindMethod,
		KindLesson,
		KindSolution,
		KindPath,
	}
}

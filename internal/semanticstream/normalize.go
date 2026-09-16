package semanticstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/HJSunDev/ownward/internal/semantics"
)

type Span struct{ Start, End int64 }
type Resolver interface {
	Resolve(string, Selector) (Span, error)
	Whole(string) (Text, error)
	Length(string) (int64, error)
	Mention(string, Unit, Text) (Selector, error)
	Candidate(string, Text) (uint64, *Organization, error)
}
type normalizer struct {
	ctx      context.Context
	r        Resolver
	err      error
	asset    string
	revision uint64
	org      *Organization
}

func (n *normalizer) check(e error) {
	if e != nil {
		n.err = errors.Join(n.err, e)
	}
}
func (n *normalizer) empty(t Text) bool { v, e := t.Empty(n.ctx); n.check(e); return v }
func (n *normalizer) blank(t Text) bool { v, e := t.Blank(n.ctx); n.check(e); return v }
func (n *normalizer) key(t Text) string { v, e := t.Digest(n.ctx); n.check(e); return v }
func (n *normalizer) span(id string, s Selector) (Span, bool) {
	v, e := n.r.Resolve(id, s)
	if e != nil {
		n.check(e)
		return v, false
	}
	return v, true
}

func Normalize(ctx context.Context, asset string, revision uint64, org *Organization, r Resolver) (*Organization, error) {
	if org == nil {
		return nil, nil
	}
	if !org.ValidSchema() {
		return nil, errors.New("关系组织格式或工作量无效")
	}
	n := &normalizer{ctx: ctx, r: r, asset: asset, revision: revision, org: org}
	seen := map[string]bool{}
	for i := range org.Units {
		u := &org.Units[i]
		key := n.key(u.ID)
		if n.blank(u.ID) || seen[key] {
			return nil, errors.New("证据单元身份无效")
		}
		seen[key] = true
		n.unit(u, len(org.Units) == 1)
	}
	if n.err != nil {
		return nil, n.err
	}
	sort.SliceStable(org.Units, func(i, j int) bool {
		cmp, e := Compare(ctx, org.Units[i].ID, org.Units[j].ID)
		n.check(e)
		return cmp < 0
	})
	if n.err != nil {
		return nil, n.err
	}
	hash, e := digestJSON(func(w io.Writer) error {
		return object(w, valueField("ID", asset, false), valueField("Revision", revision, false), field{"Units", func(w io.Writer) error {
			if org.Units == nil {
				_, e := io.WriteString(w, "null")
				return e
			}
			return array(w, len(org.Units), func(w io.Writer, i int) error { return org.Units[i].Write(ctx, w, false) })
		}, false})
	})
	if e != nil {
		return nil, e
	}
	org.Snapshot = "org_" + hash
	links := make([]Link, 0, len(org.Links))
	seen = map[string]bool{}
	for i := range org.Links {
		l := org.Links[i]
		for _, ep := range append([]Endpoint{l.Source, l.Target}, l.Conditions...) {
			if (!n.empty(ep.ObjectName) || !n.empty(ep.ObjectRole)) && org.Schema != semantics.OrganizationSchema {
				return nil, errors.New("source object endpoints require the organization/v2 schema")
			}
		}
		if l.Type != "same_object" && (!n.empty(l.Source.ObjectName) || !n.empty(l.Target.ObjectName)) {
			return nil, errors.New("source object declarations belong to same_object relations")
		}
		for _, ep := range l.Conditions {
			if !n.empty(ep.ObjectName) || !n.empty(ep.ObjectRole) {
				return nil, errors.New("relation conditions must locate source evidence or published units")
			}
		}
		if (!semantics.IsAllowedRelationType(l.Type) && l.Type != "same_object") || n.blank(l.Meaning) {
			return nil, errors.New("关系含义或条件无效")
		}
		if l.Source.AssetID != asset && l.Target.AssetID != asset {
			return nil, errors.New("关系没有绑定当前资产")
		}
		n.check(n.endpoint(&l.Source))
		n.check(n.endpoint(&l.Target))
		if n.err != nil {
			continue
		}
		if l.Type == "same_object" && ((n.empty(l.Source.MentionID) && n.empty(l.Source.ObjectName)) || (n.empty(l.Target.MentionID) && n.empty(l.Target.ObjectName))) {
			return nil, errors.New("同一对象必须连接对象提及")
		}
		a, e := digestJSON(func(w io.Writer) error { return l.Source.Write(ctx, w) })
		n.check(e)
		b, e := digestJSON(func(w io.Writer) error { return l.Target.Write(ctx, w) })
		n.check(e)
		if a == b {
			n.check(fmt.Errorf("links[%d]: source 与 target 指向同一端点", i))
			continue
		}
		for j := range l.Conditions {
			n.check(n.endpoint(&l.Conditions[j]))
		}
		if n.err != nil {
			continue
		}
		hash, e := digestJSON(func(w io.Writer) error { return l.Write(ctx, w, false) })
		n.check(e)
		l.ID = "rel_" + hash
		if !seen[l.ID] {
			links = append(links, l)
			seen[l.ID] = true
		}
	}
	if n.err != nil {
		return nil, n.err
	}
	org.Links = links
	return org, nil
}
func (n *normalizer) unit(u *Unit, whole bool) {
	if n.empty(u.Selector.Exact) {
		if !whole {
			n.check(errors.New("多个单元需要分别定位原文"))
			return
		}
		var e error
		u.Selector.Exact, e = n.r.Whole(n.asset)
		n.check(e)
	}
	extent, ok := n.span(n.asset, u.Selector)
	if !ok {
		return
	}
	contexts := make([]Selector, 0, len(u.Context))
	spans := []Span{extent}
	seen := map[Span]bool{}
	for _, s := range u.Context {
		span, ok := n.span(n.asset, s)
		if !ok {
			continue
		}
		if (span.Start < extent.Start || span.End > extent.End) && !seen[span] {
			contexts = append(contexts, s)
			spans = append(spans, span)
			seen[span] = true
		}
	}
	u.Context = contexts
	mentions := map[string]bool{}
	for i := range u.Mentions {
		m := &u.Mentions[i]
		if n.empty(m.Selector.Exact) {
			s, e := n.r.Mention(n.asset, *u, m.Name)
			if e != nil {
				n.check(e)
				continue
			}
			m.Selector = s
		}
		key := n.key(m.ID)
		if n.empty(m.ID) || n.empty(m.Name) || mentions[key] {
			n.check(errors.New("对象提及身份无效"))
			continue
		}
		mentions[key] = true
		span, e := n.r.Resolve(n.asset, m.Selector)
		if e != nil && n.empty(m.Selector.Prefix) && n.empty(m.Selector.Suffix) {
			located, le := n.r.Mention(n.asset, *u, m.Selector.Exact)
			if le == nil {
				equal, le := Compare(n.ctx, located.Exact, m.Selector.Exact)
				if le == nil && equal == 0 {
					m.Selector = located
					span, e = n.r.Resolve(n.asset, m.Selector)
				}
			}
		}
		if e != nil {
			n.check(e)
			continue
		}
		contained := false
		for _, s := range spans {
			contained = contained || span.Start >= s.Start && span.End <= s.End
		}
		if !contained {
			n.check(errors.New("对象提及不在该单元或必要上下文内"))
		}
	}
}
func (n *normalizer) endpoint(p *Endpoint) error {
	revision, org := n.revision, n.org
	if p.AssetID != n.asset {
		var e error
		revision, org, e = n.r.Candidate(p.AssetID, p.UnitID)
		if e != nil {
			return e
		}
	}
	if revision == 0 || (p.Revision != 0 && p.Revision != revision) {
		return errors.New("关联端点不在当前工作或版本已变化")
	}
	p.Revision = revision
	if !n.empty(p.ObjectName) || !n.empty(p.ObjectRole) {
		if n.blank(p.ObjectName) || n.empty(p.Selector.Exact) {
			return errors.New("a source object requires an explicit name and original evidence selector")
		}
		if !n.empty(p.UnitID) || !n.empty(p.MentionID) || p.Snapshot != "" {
			return errors.New("a source object cannot also claim a published unit or mention")
		}
		if _, e := n.r.Resolve(p.AssetID, p.Selector); e != nil {
			return e
		}
		p.Fingerprint = ""
		p.MentionFingerprint = ""
		return nil
	}
	if n.empty(p.UnitID) {
		if !n.empty(p.MentionID) || p.Snapshot != "" {
			return errors.New("资产端点不能冒充组织单元")
		}
		p.Fingerprint = ""
		p.MentionFingerprint = ""
		if !n.empty(p.Selector.Exact) {
			span, e := n.r.Resolve(p.AssetID, p.Selector)
			if e != nil {
				return e
			}
			length, e := n.r.Length(p.AssetID)
			if e != nil {
				return e
			}
			if span.Start == 0 && span.End == length {
				p.Selector = Selector{}
			}
		}
		return nil
	}
	if org == nil || (p.Snapshot != "" && p.Snapshot != org.Snapshot) {
		return errors.New("资产未提供匹配的组织快照，不能引用单元；请引用已提供单元或用原文 selector 定位")
	}
	p.Snapshot = org.Snapshot
	wanted := n.key(p.UnitID)
	for _, u := range org.Units {
		if n.key(u.ID) != wanted {
			continue
		}
		selector := u.Selector
		p.MentionFingerprint = ""
		if !n.empty(p.MentionID) {
			found := false
			mention := n.key(p.MentionID)
			for _, m := range u.Mentions {
				if n.key(m.ID) == mention {
					var e error
					p.MentionFingerprint, e = m.Fingerprint(n.ctx)
					if e != nil {
						return e
					}
					selector = m.Selector
					found = true
					break
				}
			}
			if !found {
				return errors.New("资产单元未声明该对象提及")
			}
		}
		if !n.empty(p.Selector.Exact) {
			a, e := n.r.Resolve(p.AssetID, p.Selector)
			if e != nil {
				return e
			}
			b, e := n.r.Resolve(p.AssetID, selector)
			if e != nil || a != b {
				return errors.New("关联定位与单元或提及不一致")
			}
		}
		p.Selector = Selector{}
		var e error
		p.Fingerprint, e = u.Fingerprint(n.ctx)
		return e
	}
	return errors.New("资产未声明该单元")
}

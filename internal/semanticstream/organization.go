package semanticstream

import (
	"context"
	"errors"
	"io"

	"github.com/HJSunDev/ownward/internal/semantics"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

type Selector struct{ Exact, Prefix, Suffix Text }
type Mention struct {
	ID, Name, Role Text
	Selector       Selector
}
type Unit struct {
	ID, Statement Text
	Selector      Selector
	Context       []Selector
	Mentions      []Mention
}
type Endpoint struct {
	ObjectName, ObjectRole          Text
	AssetID                         string
	Revision                        uint64
	Snapshot                        string
	UnitID, MentionID               Text
	Fingerprint, MentionFingerprint string
	Selector                        Selector
}
type Link struct {
	ID, Type       string
	Meaning        Text
	Source, Target Endpoint
	Conditions     []Endpoint
}
type Organization struct {
	Schema, Snapshot string
	Units            []Unit
	Links            []Link
}

func readSelector(n streamjson.Node) (s Selector, e error) {
	if n.Kind == 0 || n.Kind == 'n' {
		return s, nil
	}
	s.Exact, e = textField(n, "exact")
	if e != nil {
		return
	}
	s.Prefix, e = textField(n, "prefix")
	if e != nil {
		return
	}
	s.Suffix, e = textField(n, "suffix")
	return
}
func selectorField(n streamjson.Node) (Selector, error) {
	v, ok, e := n.Field("selector")
	if e != nil || !ok {
		return Selector{}, e
	}
	return readSelector(v)
}
func ReadUnit(n streamjson.Node) (u Unit, e error) {
	u.ID, e = textField(n, "id")
	if e != nil {
		return
	}
	u.Statement, e = textField(n, "statement")
	if e != nil {
		return
	}
	u.Selector, e = selectorField(n)
	if e != nil {
		return
	}
	e = each(n, "context", 16, func(n streamjson.Node) error {
		sel, e := readSelector(n)
		if e == nil {
			u.Context = append(u.Context, sel)
		}
		return e
	})
	if e != nil {
		return
	}
	e = each(n, "mentions", 32, func(n streamjson.Node) error {
		var m Mention
		var e error
		m.ID, e = textField(n, "id")
		if e != nil {
			return e
		}
		m.Name, e = textField(n, "name")
		if e != nil {
			return e
		}
		m.Role, e = textField(n, "role")
		if e != nil {
			return e
		}
		m.Selector, e = selectorField(n)
		if e == nil {
			u.Mentions = append(u.Mentions, m)
		}
		return e
	})
	return
}
func readEndpoint(n streamjson.Node, persisted bool) (p Endpoint, e error) {
	p.AssetID, e = smallField(n, "asset_id")
	if e != nil {
		return
	}
	p.Snapshot, e = smallField(n, "snapshot")
	if e != nil {
		return
	}
	if persisted {
		p.Fingerprint, e = smallField(n, "fingerprint")
		if e != nil {
			return
		}
		p.MentionFingerprint, e = smallField(n, "mention_fingerprint")
		if e != nil {
			return
		}
	}
	if v, ok, er := n.Field("revision"); er != nil {
		e = er
		return
	} else if ok {
		if e = v.DecodeSmall(&p.Revision, 32); e != nil {
			return
		}
	}
	for _, f := range []struct {
		key string
		out *Text
	}{{"object_name", &p.ObjectName}, {"object_role", &p.ObjectRole}, {"unit_id", &p.UnitID}, {"mention_id", &p.MentionID}} {
		*f.out, e = textField(n, f.key)
		if e != nil {
			return
		}
	}
	p.Selector, e = selectorField(n)
	return
}
func ReadLink(n streamjson.Node) (Link, error) { return readLink(n, true) }

func readLink(n streamjson.Node, persisted bool) (l Link, e error) {
	if persisted {
		l.ID, e = smallField(n, "id")
		if e != nil {
			return
		}
	}
	l.Type, e = smallField(n, "type")
	if e != nil {
		return
	}
	l.Meaning, e = textField(n, "meaning")
	if e != nil {
		return
	}
	a, ok, e := n.Field("source")
	if e != nil || !ok {
		return l, errors.New("关系缺少来源")
	}
	l.Source, e = readEndpoint(a, persisted)
	if e != nil {
		return
	}
	b, ok, e := n.Field("target")
	if e != nil || !ok {
		return l, errors.New("关系缺少目标")
	}
	l.Target, e = readEndpoint(b, persisted)
	if e != nil {
		return
	}
	e = each(n, "conditions", 16, func(n streamjson.Node) error {
		p, e := readEndpoint(n, persisted)
		if e == nil {
			l.Conditions = append(l.Conditions, p)
		}
		return e
	})
	return
}
func ReadOrganization(n streamjson.Node) (o *Organization, e error) {
	if n.Kind == 0 || n.Kind == 'n' {
		return nil, nil
	}
	o = &Organization{}
	if units, ok, er := n.Field("units"); er != nil {
		return nil, er
	} else if ok && units.Kind == '[' {
		o.Units = make([]Unit, 0)
	}
	o.Schema, e = smallField(n, "schema")
	if e != nil {
		return
	}
	// Normalization derives snapshot, link identities and fingerprints from
	// accepted content; supplied copies are not retained as trusted identities.
	e = each(n, "units", 128, func(n streamjson.Node) error {
		u, e := ReadUnit(n)
		if e == nil {
			o.Units = append(o.Units, u)
		}
		return e
	})
	if e != nil {
		return
	}
	e = each(n, "links", 128, func(n streamjson.Node) error {
		l, e := readLink(n, false)
		if e == nil {
			o.Links = append(o.Links, l)
		}
		return e
	})
	return
}

func (s Selector) Write(ctx context.Context, w io.Writer) error {
	fields := make([]field, 0, 3)
	for _, f := range []struct {
		key      string
		t        Text
		optional bool
	}{{"exact", s.Exact, false}, {"prefix", s.Prefix, true}, {"suffix", s.Suffix, true}} {
		v, e := textJSON(ctx, f.key, f.t, f.optional)
		if e != nil {
			return e
		}
		fields = append(fields, v)
	}
	return object(w, fields...)
}
func selectorJSON(ctx context.Context, s Selector) field {
	return field{"selector", func(w io.Writer) error { return s.Write(ctx, w) }, false}
}
func (m Mention) Write(ctx context.Context, w io.Writer, fingerprint bool) error {
	id := m.ID
	if fingerprint {
		id = Text{}
	}
	fields := make([]field, 0, 4)
	for _, f := range []struct {
		key string
		t   Text
		opt bool
	}{{"id", id, false}, {"name", m.Name, false}, {"role", m.Role, true}} {
		v, e := textJSON(ctx, f.key, f.t, f.opt)
		if e != nil {
			return e
		}
		fields = append(fields, v)
	}
	fields = append(fields, selectorJSON(ctx, m.Selector))
	return object(w, fields...)
}
func (u Unit) Write(ctx context.Context, w io.Writer, fingerprint bool) error {
	id := u.ID
	if fingerprint {
		id = Text{}
	}
	a, e := textJSON(ctx, "id", id, false)
	if e != nil {
		return e
	}
	b, e := textJSON(ctx, "statement", u.Statement, true)
	if e != nil {
		return e
	}
	return object(w, a, b, selectorJSON(ctx, u.Selector), field{"context", func(w io.Writer) error {
		return array(w, len(u.Context), func(w io.Writer, i int) error { return u.Context[i].Write(ctx, w) })
	}, len(u.Context) == 0}, field{"mentions", func(w io.Writer) error {
		return array(w, len(u.Mentions), func(w io.Writer, i int) error { return u.Mentions[i].Write(ctx, w, fingerprint) })
	}, len(u.Mentions) == 0})
}
func (u Unit) Fingerprint(ctx context.Context) (string, error) {
	return digestJSON(func(w io.Writer) error { return u.Write(ctx, w, true) })
}
func (m Mention) Fingerprint(ctx context.Context) (string, error) {
	return digestJSON(func(w io.Writer) error { return m.Write(ctx, w, true) })
}
func (p Endpoint) Write(ctx context.Context, w io.Writer) error {
	fields := []field{}
	for _, f := range []struct {
		key string
		t   Text
	}{{"object_name", p.ObjectName}, {"object_role", p.ObjectRole}} {
		v, e := textJSON(ctx, f.key, f.t, true)
		if e != nil {
			return e
		}
		fields = append(fields, v)
	}
	fields = append(fields, valueField("asset_id", p.AssetID, false), valueField("revision", p.Revision, p.Revision == 0), valueField("snapshot", p.Snapshot, p.Snapshot == ""))
	for _, f := range []struct {
		key string
		t   Text
	}{{"unit_id", p.UnitID}, {"mention_id", p.MentionID}} {
		v, e := textJSON(ctx, f.key, f.t, true)
		if e != nil {
			return e
		}
		fields = append(fields, v)
	}
	fields = append(fields, valueField("fingerprint", p.Fingerprint, p.Fingerprint == ""), valueField("mention_fingerprint", p.MentionFingerprint, p.MentionFingerprint == ""), selectorJSON(ctx, p.Selector))
	return object(w, fields...)
}
func (l Link) Write(ctx context.Context, w io.Writer, identity bool) error {
	meaning, e := textJSON(ctx, "meaning", l.Meaning, false)
	if e != nil {
		return e
	}
	return object(w, valueField("id", l.ID, !identity || l.ID == ""), valueField("type", l.Type, false), meaning, field{"source", func(w io.Writer) error { return l.Source.Write(ctx, w) }, false}, field{"target", func(w io.Writer) error { return l.Target.Write(ctx, w) }, false}, field{"conditions", func(w io.Writer) error {
		return array(w, len(l.Conditions), func(w io.Writer, i int) error { return l.Conditions[i].Write(ctx, w) })
	}, len(l.Conditions) == 0})
}
func (o *Organization) Write(ctx context.Context, w io.Writer) error {
	if o == nil {
		_, e := io.WriteString(w, "null")
		return e
	}
	return object(w, valueField("schema", o.Schema, false), valueField("snapshot", o.Snapshot, o.Snapshot == ""), field{"units", func(w io.Writer) error {
		if o.Units == nil {
			_, e := io.WriteString(w, "null")
			return e
		}
		return array(w, len(o.Units), func(w io.Writer, i int) error { return o.Units[i].Write(ctx, w, false) })
	}, false}, field{"links", func(w io.Writer) error {
		return array(w, len(o.Links), func(w io.Writer, i int) error { return o.Links[i].Write(ctx, w, true) })
	}, false})
}
func array(w io.Writer, n int, write func(io.Writer, int) error) error {
	if _, e := io.WriteString(w, "["); e != nil {
		return e
	}
	for i := 0; i < n; i++ {
		if i > 0 {
			if _, e := io.WriteString(w, ","); e != nil {
				return e
			}
		}
		if e := write(w, i); e != nil {
			return e
		}
	}
	_, e := io.WriteString(w, "]")
	return e
}

func (o *Organization) ValidSchema() bool {
	return o.Schema == semantics.OrganizationSchema || o.Schema == semantics.LegacyOrganizationSchema
}

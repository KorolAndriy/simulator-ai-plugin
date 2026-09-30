package sim

import (
	"fmt"
	"math/big"
	"strings"
)

const hierarchy = "hierarchy"

type Actor struct {
	ID, Type, Title string
	Data            *OMap
	OriginID        string // id in Simulator ("" if none)
	CreatedBy       string
}

type Link struct {
	ID, Source, Target, EdgeType, Mediator string
}

type Account struct {
	ActorID, Name, ValueType string
	Value                    *big.Rat
}

type accKey struct{ actor, name string }

// Graph keeps insertion order everywhere: every "for each actor" follows it.
type Graph struct {
	actors     []*Actor
	actorIdx   map[string]*Actor
	links      []*Link
	linkIdx    map[string]*Link
	accounts   []*Account
	accIdx     map[accKey]*Account
	valueTypes map[string]ValueType
	Source     *OMap
}

func newGraph() *Graph {
	return &Graph{actorIdx: map[string]*Actor{}, linkIdx: map[string]*Link{}, accIdx: map[accKey]*Account{},
		valueTypes: map[string]ValueType{}, Source: NewOMap()}
}

func (g *Graph) addActor(a *Actor) {
	g.actors = append(g.actors, a)
	g.actorIdx[a.ID] = a
}

func (g *Graph) removeActor(id string) {
	delete(g.actorIdx, id)
	for i, a := range g.actors {
		if a.ID == id {
			g.actors = append(g.actors[:i], g.actors[i+1:]...)
			return
		}
	}
}

func (g *Graph) addLink(l *Link) {
	g.links = append(g.links, l)
	g.linkIdx[l.ID] = l
}

func (g *Graph) removeLink(id string) {
	delete(g.linkIdx, id)
	for i, l := range g.links {
		if l.ID == id {
			g.links = append(g.links[:i], g.links[i+1:]...)
			return
		}
	}
}

func (g *Graph) addAccount(a *Account) {
	g.accounts = append(g.accounts, a)
	g.accIdx[accKey{a.ActorID, a.Name}] = a
}

func (g *Graph) removeAccount(k accKey) {
	delete(g.accIdx, k)
	for i, a := range g.accounts {
		if a.ActorID == k.actor && a.Name == k.name {
			g.accounts = append(g.accounts[:i], g.accounts[i+1:]...)
			return
		}
	}
}

func (g *Graph) vtype(name string) ValueType {
	if vt, ok := g.valueTypes[name]; ok {
		return vt
	}
	return defaultType
}

func (g *Graph) children(id string, edgeType string) []string {
	var out []string
	for _, l := range g.links {
		if l.Source == id && (edgeType == "" || l.EdgeType == edgeType) {
			out = append(out, l.Target)
		}
	}
	return out
}

func (g *Graph) parents(id string, edgeType string) []string {
	var out []string
	for _, l := range g.links {
		if l.Target == id && (edgeType == "" || l.EdgeType == edgeType) {
			out = append(out, l.Source)
		}
	}
	return out
}

// Totals sums account values per value type.
func (g *Graph) Totals() map[string]*big.Rat {
	out := map[string]*big.Rat{}
	for _, a := range g.accounts {
		if out[a.ValueType] == nil {
			out[a.ValueType] = new(big.Rat)
		}
		out[a.ValueType].Add(out[a.ValueType], a.Value)
	}
	return out
}

func (g *Graph) clone() *Graph {
	c := newGraph()
	for _, a := range g.actors {
		cp := *a
		cp.Data = deepCopy(a.Data).(*OMap)
		c.addActor(&cp)
	}
	for _, l := range g.links {
		cp := *l
		c.addLink(&cp)
	}
	for _, a := range g.accounts {
		cp := *a
		cp.Value = new(big.Rat).Set(a.Value)
		c.addAccount(&cp)
	}
	for k, v := range g.valueTypes {
		c.valueTypes[k] = v
	}
	c.Source = deepCopy(g.Source).(*OMap)
	return c
}

func getString(o *OMap, k string) string {
	v, ok := o.Get(k)
	if !ok || v == nil {
		return ""
	}
	return show(v)
}

func getMap(o *OMap, k string) *OMap {
	v, _ := o.Get(k)
	m, _ := v.(*OMap)
	return m
}

func getList(o *OMap, k string) []any {
	v, _ := o.Get(k)
	l, _ := v.([]any)
	return l
}

// LoadGraph reads the Simulator plugin's layer YAML (pullGraphFile / pushGraphFile) with
// optional sim: sections (spec §1).
func LoadGraph(src []byte) (*Graph, error) {
	raw, err := parseYAML(src)
	if err != nil {
		return nil, fmt.Errorf("graph file: %w", err)
	}
	d, ok := raw.(*OMap)
	if !ok {
		return nil, fmt.Errorf("graph file: a mapping with actors and edges expected")
	}
	g := newGraph()
	sim := getMap(d, "sim")
	if src := getMap(sim, "source"); src != nil {
		g.Source = src
	} else {
		g.Source.Set("kind", "simulator")
		g.Source.Set("layer", getString(d, "layerId"))
	}
	if vts := getMap(sim, "valueTypes"); vts != nil {
		for _, name := range vts.Keys() {
			spec, _ := vts.m[name].(*OMap)
			vt, err := valueTypeFrom(name, spec)
			if err != nil {
				return nil, err
			}
			g.valueTypes[name] = vt
		}
	}
	for i, it := range getList(d, "actors") {
		a, ok := it.(*OMap)
		if !ok {
			return nil, fmt.Errorf("actors[%d]: mapping expected", i)
		}
		s := getMap(a, "sim")
		data := NewOMap()
		if dm := getMap(a, "data"); dm != nil {
			data = deepCopy(dm).(*OMap)
		}
		if st := getMap(s, "state"); st != nil {
			for _, k := range st.Keys() {
				data.Set(k, deepCopy(st.m[k]))
			}
		}
		if s == nil {
			if f, ok := a.Get("formId"); ok && f != nil {
				data.Set("_form_id", f)
			}
			if p := getMap(a, "position"); p != nil {
				pos := NewOMap()
				x, _ := p.Get("x")
				y, _ := p.Get("y")
				if x == nil {
					x = ratZero
				}
				if y == nil {
					y = ratZero
				}
				pos.Set("x", x)
				pos.Set("y", y)
				data.Set("_position", pos)
			}
		}
		id := getString(a, "id")
		if id == "" {
			return nil, fmt.Errorf("actors[%d]: id is required", i)
		}
		typ := getString(s, "type")
		if typ == "" {
			typ = getString(a, "formName")
		}
		if typ == "" {
			typ = getString(a, "formId")
		}
		if typ == "" {
			typ = "actor"
		}
		origin := ""
		if s == nil {
			origin = id
		} else {
			origin = getString(s, "origin_id")
		}
		g.addActor(&Actor{ID: id, Type: typ, Title: getString(a, "title"), Data: data, OriginID: origin,
			CreatedBy: getString(s, "created_by")})
	}
	for i, it := range getList(d, "edges") {
		e, ok := it.(*OMap)
		if !ok {
			return nil, fmt.Errorf("edges[%d]: mapping expected", i)
		}
		s := getMap(e, "sim")
		id := getString(s, "id")
		if id == "" {
			id = fmt.Sprintf("edge-%d", i)
		}
		src, dst := getString(e, "source"), getString(e, "target")
		if g.actorIdx[src] == nil || g.actorIdx[dst] == nil {
			continue
		}
		et := getString(s, "edgeType")
		if et == "" {
			et = hierarchy
		}
		g.addLink(&Link{ID: id, Source: src, Target: dst, EdgeType: et, Mediator: getString(s, "mediator")})
	}
	for i, it := range getList(sim, "accounts") {
		a, ok := it.(*OMap)
		if !ok {
			return nil, fmt.Errorf("sim.accounts[%d]: mapping expected", i)
		}
		vt := getString(a, "value_type")
		if vt == "" {
			vt = "default"
		}
		raw, _ := a.Get("value")
		if raw == nil {
			raw = ratZero
		}
		n, err := toNum(raw)
		if err != nil {
			return nil, fmt.Errorf("sim.accounts[%d]: %w", i, err)
		}
		q, err := g.vtype(vt).quantize(n)
		if err != nil {
			return nil, err
		}
		g.addAccount(&Account{ActorID: getString(a, "actor_id"), Name: getString(a, "name"), ValueType: vt, Value: q})
	}
	return g, nil
}

// resolveActor finds exactly one actor by id, origin id or title, or by {id|title|type}.
func resolveActor(g *Graph, spec any) (string, error) {
	var matches []string
	if m, ok := spec.(*OMap); ok {
		if id, has := m.Get("id"); has {
			return resolveActor(g, id)
		}
		title, hasTitle := m.Get("title")
		typ, hasType := m.Get("type")
		for _, a := range g.actors {
			if (!hasTitle || a.Title == show(title)) && (!hasType || a.Type == show(typ)) {
				matches = append(matches, a.ID)
			}
		}
	} else {
		s := show(spec)
		if g.actorIdx[s] != nil {
			return s, nil
		}
		for _, a := range g.actors {
			if (a.OriginID != "" && a.OriginID == s) || a.Title == s {
				matches = append(matches, a.ID)
			}
		}
	}
	if len(matches) != 1 {
		return "", &RuleError{fmt.Sprintf("reference %s matches %d actors", repr(spec), len(matches))}
	}
	return matches[0], nil
}

func (g *Graph) summary() string {
	return fmt.Sprintf("%d actors, %d links, %d accounts", len(g.actors), len(g.links), len(g.accounts))
}

var _ = strings.TrimSpace

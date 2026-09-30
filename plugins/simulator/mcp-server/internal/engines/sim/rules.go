package sim

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

type RuleError struct{ msg string }

func (e *RuleError) Error() string { return e.msg }

func ruleErr(format string, a ...any) error { return &RuleError{fmt.Sprintf(format, a...)} }

// Model is a parsed model.yaml (spec §5).
type Model struct {
	ValueTypes    map[string]ValueType
	vtOrder       []string
	Refs          *OMap
	Params        *OMap
	Horizon       int64
	InitialEvents []*OMap
	Behaviors     *OMap // type -> (event -> []action)
	Metrics       *OMap // name -> expression
	Goals         *OMap
	TypeField     string
	Actors        []*OMap
	Raw           *OMap
}

func LoadModel(src []byte) (*Model, error) {
	raw, err := parseYAML(src)
	if err != nil {
		return nil, fmt.Errorf("model: %w", err)
	}
	d, _ := raw.(*OMap)
	if d == nil {
		d = NewOMap()
	}
	m := &Model{ValueTypes: map[string]ValueType{}, Raw: d}
	if vts := getMap(d, "value_types"); vts != nil {
		for _, name := range vts.Keys() {
			spec, _ := vts.m[name].(*OMap)
			vt, err := valueTypeFrom(name, spec)
			if err != nil {
				return nil, err
			}
			m.ValueTypes[name] = vt
			m.vtOrder = append(m.vtOrder, name)
		}
	}
	orEmpty := func(k string) *OMap {
		if v := getMap(d, k); v != nil {
			return v
		}
		return NewOMap()
	}
	m.Refs, m.Params, m.Behaviors, m.Metrics, m.Goals = orEmpty("refs"), orEmpty("params"), orEmpty("behaviors"), orEmpty("metrics"), orEmpty("goals")
	h, _ := d.Get("horizon")
	if h == nil {
		h = ratZero
	}
	if m.Horizon, err = parseDuration(h); err != nil {
		return nil, fmt.Errorf("horizon: %w", err)
	}
	for i, e := range getList(d, "initial_events") {
		em, ok := e.(*OMap)
		if !ok {
			return nil, fmt.Errorf("initial_events[%d]: mapping expected", i)
		}
		m.InitialEvents = append(m.InitialEvents, em)
	}
	for i, a := range getList(d, "actors") {
		am, ok := a.(*OMap)
		if !ok {
			return nil, fmt.Errorf("actors[%d]: mapping expected", i)
		}
		m.Actors = append(m.Actors, am)
	}
	m.TypeField = getString(d, "type_field")
	return m, nil
}

// stableUniform is U(parts) of spec §7: SHA-256 of the joined parts, first 8 bytes / 2^64.
func stableUniform(parts ...string) *big.Rat {
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	n := new(big.Int).SetUint64(binary.BigEndian.Uint64(h[:8]))
	return new(big.Rat).SetFrac(n, new(big.Int).Lsh(big.NewInt(1), 64))
}

// ---- views ---------------------------------------------------------------

type ActorView struct {
	s  *store
	id string
}

func (v *ActorView) actor() (*Actor, error) { return v.s.actor(v.id) }

func (v *ActorView) title() string {
	if a, err := v.actor(); err == nil {
		return a.Title
	}
	return ""
}

func (v *ActorView) label() string {
	if t := v.title(); t != "" {
		return t
	}
	return v.id
}

func (v *ActorView) attr(name string) (any, error) {
	a, err := v.actor()
	if err != nil {
		return nil, err
	}
	switch name {
	case "id":
		return v.id, nil
	case "type":
		return a.Type, nil
	case "title":
		return a.Title, nil
	case "data":
		return a.Data, nil
	case "parent":
		p := v.s.g.parents(v.id, "")
		if len(p) == 0 {
			return nil, nil
		}
		return &ActorView{v.s, p[0]}, nil
	}
	if val, ok := a.Data.Get(name); ok {
		return val, nil
	}
	return nil, exprErr("actor %s has no field %q", v.label(), name)
}

func (v *ActorView) related(ids []string, typ any) []any {
	out := []any{}
	for _, id := range ids {
		if typ != nil {
			a := v.s.g.actorIdx[id]
			if a == nil || a.Type != show(typ) {
				continue
			}
		}
		out = append(out, &ActorView{v.s, id})
	}
	return out
}

func (v *ActorView) method(name string, args []any, kw *OMap) (any, error) {
	a, err := v.actor()
	if err != nil {
		return nil, err
	}
	switch name {
	case "acc":
		acc := v.s.account(v.id, show(argOr(args, kw, 0, "name", nil)))
		if acc == nil {
			return new(big.Rat), nil
		}
		return acc.Value, nil
	case "sum_accounts":
		vt := argOr(args, kw, 0, "value_type", nil)
		sum := new(big.Rat)
		for _, acc := range v.s.g.accounts {
			if acc.ActorID == v.id && (vt == nil || acc.ValueType == show(vt)) {
				sum.Add(sum, acc.Value)
			}
		}
		return sum, nil
	case "has":
		return a.Data.Has(show(argOr(args, kw, 0, "key", nil))), nil
	case "get":
		if val, ok := a.Data.Get(show(argOr(args, kw, 0, "key", nil))); ok {
			return val, nil
		}
		return argOr(args, kw, 1, "default", nil), nil
	case "children", "parents":
		et := ""
		if e := argOr(args, kw, 1, "edge_type", nil); e != nil {
			et = show(e)
		}
		ids := v.s.g.children(v.id, et)
		if name == "parents" {
			ids = v.s.g.parents(v.id, et)
		}
		return v.related(ids, argOr(args, kw, 0, "type", nil)), nil
	}
	return nil, exprErr("no method %q on actor %s", name, v.label())
}

type Namespace struct {
	data *OMap
	name string
}

func (n *Namespace) attr(k string) (any, error) {
	if v, ok := n.data.Get(k); ok {
		return v, nil
	}
	return nil, exprErr("%s has no field %q", n.name, k)
}

func actorIDOf(v any) (string, error) {
	switch x := v.(type) {
	case *ActorView:
		return x.id, nil
	case string:
		return x, nil
	}
	return "", ruleErr("expected an actor, got %s", repr(v))
}

// ---- step context ------------------------------------------------------------

type Event struct {
	Time     int64
	Priority int64
	Seq      int64
	Kind     string
	Target   string
	Payload  *OMap
	Key      string
	// a recurring initial event keeps only its next occurrence queued
	chain *chain
	n     int64
}

// chain is one recurring initial event on one target.
type chain struct {
	every, last int64 // period; index of the last occurrence within the horizon
	keyPrefix   string
}

type scheduled struct {
	kind, target string
	time, prio   int64
	payload      *OMap
}

type Note struct {
	Op     string         `json:"op"`
	Var    string         `json:"var,omitempty"`
	Choice string         `json:"choice,omitempty"`
	Source string         `json:"source,omitempty"`
	ID     string         `json:"id,omitempty"`
	Msg    string         `json:"message,omitempty"`
	Extra  map[string]any `json:"extra,omitempty"`
}

// Decider picks an option of a decide action.
type Decider func(ctx *StepContext, spec *OMap, options []string, ruleChoice *string) (string, map[string]any, error)

type StepContext struct {
	s        *store
	model    *Model
	params   *OMap
	refs     *OMap // name -> actor id
	now      int64
	event    *Event
	self     string
	seed     string
	schedule func(scheduled)
	decider  Decider
	vars     map[string]any
	notes    []Note
	draws    int
}

func (c *StepContext) actorType(id string) string {
	a := c.s.g.actorIdx[id]
	if a == nil {
		return ""
	}
	if c.model.TypeField != "" {
		if v, ok := a.Data.Get(c.model.TypeField); ok {
			return show(v)
		}
	}
	return a.Type
}

func (c *StepContext) logicalSelf() string {
	a := c.s.g.actorIdx[c.self]
	if a == nil {
		return c.self
	}
	if v, ok := a.Data.Get("_logical_id"); ok && truthy(v) {
		return show(v)
	}
	if a.OriginID != "" {
		return a.OriginID
	}
	return a.ID
}

func (c *StepContext) names() map[string]any {
	refs := NewOMap()
	for _, k := range c.refs.Keys() {
		id, _ := c.refs.Get(k)
		refs.Set(k, &ActorView{c.s, show(id)})
	}
	payload := c.event.Payload
	if payload == nil {
		payload = NewOMap()
	}
	n := map[string]any{
		"self":   &ActorView{c.s, c.self},
		"event":  &Namespace{payload, "event"},
		"params": &Namespace{c.params, "params"},
		"refs":   &Namespace{refs, "refs"},
		"now":    ratInt(c.now),
	}
	for k, v := range c.vars {
		n[k] = v
	}
	return n
}

func (c *StepContext) rand() *big.Rat {
	c.draws++
	return stableUniform(c.seed, "rand", c.logicalSelf(), c.event.Key, fmt.Sprint(c.draws))
}

func (c *StepContext) filterActors(args []any, kw *OMap, typeArgIndex int) []*ActorView {
	typ := argOr(args, nil, typeArgIndex, "", nil)
	if t, ok := kw.Get("type"); ok {
		typ = t
	}
	var out []*ActorView
	for _, a := range c.s.g.actors {
		if typ != nil && c.actorType(a.ID) != show(typ) {
			continue
		}
		ok := true
		for _, k := range kw.Keys() {
			if k == "type" {
				continue
			}
			want, _ := kw.Get(k)
			got, _ := a.Data.Get(k)
			if !equal(got, want) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, &ActorView{c.s, a.ID})
		}
	}
	return out
}

func numArg(v any, fn string) (*big.Rat, error) {
	n, err := toNum(v)
	if err != nil {
		return nil, exprErr("%s: %v", fn, err)
	}
	return n, nil
}

func (c *StepContext) funcs() map[string]Func {
	listOf := func(vs []*ActorView) []any {
		out := make([]any, len(vs))
		for i, v := range vs {
			out[i] = v
		}
		return out
	}
	extreme := func(name string, sign int) Func {
		return func(args []any, kw *OMap) (any, error) {
			items := args
			if len(args) == 1 {
				if l, ok := args[0].([]any); ok {
					items = l
				}
			}
			if len(items) == 0 {
				return nil, exprErr("%s() arg is an empty sequence", name)
			}
			best := items[0]
			env := &Env{src: name}
			for _, it := range items[1:] {
				less, err := env.compare("<", it, best)
				if err != nil {
					return nil, err
				}
				if (sign < 0 && less) || (sign > 0 && !less && !equal(it, best)) {
					best = it
				}
			}
			return best, nil
		}
	}
	return map[string]Func{
		"actor": func(args []any, kw *OMap) (any, error) {
			id, err := actorIDOf(argOr(args, kw, 0, "x", nil))
			if err != nil {
				return nil, err
			}
			return &ActorView{c.s, id}, nil
		},
		"actors": func(args []any, kw *OMap) (any, error) { return listOf(c.filterActors(args, kw, 0)), nil },
		"count": func(args []any, kw *OMap) (any, error) {
			return ratInt(int64(len(c.filterActors(args, kw, 0)))), nil
		},
		"total": func(args []any, kw *OMap) (any, error) {
			account := show(argOr(args, kw, 0, "account", nil))
			rest := NewOMap()
			for _, k := range kw.Keys() {
				if k != "account" {
					v, _ := kw.Get(k)
					rest.Set(k, v)
				}
			}
			var typeArgs []any
			if len(args) > 1 {
				typeArgs = args[1:]
			}
			sum := new(big.Rat)
			for _, v := range c.filterActors(typeArgs, rest, 0) {
				if acc := c.s.account(v.id, account); acc != nil {
					sum.Add(sum, acc.Value)
				}
			}
			return sum, nil
		},
		"dur": func(args []any, kw *OMap) (any, error) {
			d, err := parseDuration(argOr(args, kw, 0, "v", nil))
			if err != nil {
				return nil, &ExprError{err.Error()}
			}
			return ratInt(d), nil
		},
		"dec": func(args []any, kw *OMap) (any, error) { return numArg(argOr(args, kw, 0, "x", nil), "dec") },
		"min": extreme("min", -1),
		"max": extreme("max", 1),
		"abs": func(args []any, kw *OMap) (any, error) {
			n, err := numArg(argOr(args, kw, 0, "x", nil), "abs")
			if err != nil {
				return nil, err
			}
			return new(big.Rat).Abs(n), nil
		},
		"len": func(args []any, kw *OMap) (any, error) {
			switch x := argOr(args, kw, 0, "x", nil).(type) {
			case []any:
				return ratInt(int64(len(x))), nil
			case string:
				return ratInt(int64(len([]rune(x)))), nil
			case *OMap:
				return ratInt(int64(x.Len())), nil
			}
			return nil, exprErr("len() of unsized value")
		},
		"round": func(args []any, kw *OMap) (any, error) {
			n, err := numArg(argOr(args, kw, 0, "x", nil), "round")
			if err != nil {
				return nil, err
			}
			places := int64(0)
			if p := argOr(args, kw, 1, "n", nil); p != nil {
				pn, err := numArg(p, "round")
				if err != nil {
					return nil, err
				}
				places = truncInt(pn).Int64()
			}
			return quantize(n, int(places)), nil
		},
		"str":  func(args []any, kw *OMap) (any, error) { return show(argOr(args, kw, 0, "x", nil)), nil },
		"rand": func(args []any, kw *OMap) (any, error) { return c.rand(), nil },
		"pick": func(args []any, kw *OMap) (any, error) {
			items, _ := argOr(args, kw, 0, "items", nil).([]any)
			if len(items) == 0 {
				return nil, nil
			}
			u := c.rand()
			idx := truncInt(new(big.Rat).Mul(u, ratInt(int64(len(items))))).Int64()
			if idx > int64(len(items)-1) {
				idx = int64(len(items) - 1)
			}
			return items[idx], nil
		},
		"where": func(args []any, kw *OMap) (any, error) {
			items, _ := argOr(args, kw, 0, "items", nil).([]any)
			has, hasOK := kw.Get("title_has")
			not, notOK := kw.Get("title_not")
			leaf, leafOK := kw.Get("leaf")
			out := []any{}
			for _, it := range items {
				v, ok := it.(*ActorView)
				if !ok {
					continue
				}
				t := strings.ToLower(v.title())
				if hasOK && has != nil && !strings.Contains(t, strings.ToLower(show(has))) {
					continue
				}
				if notOK && not != nil && strings.Contains(t, strings.ToLower(show(not))) {
					continue
				}
				if leafOK && leaf != nil && (len(c.s.g.children(v.id, "")) == 0) != truthy(leaf) {
					continue
				}
				out = append(out, v)
			}
			return out, nil
		},
		"chance": func(args []any, kw *OMap) (any, error) {
			p, err := numArg(argOr(args, kw, 0, "p", nil), "chance")
			if err != nil {
				return nil, err
			}
			return c.rand().Cmp(p) < 0, nil
		},
	}
}

func (c *StepContext) ev(src any) (any, error) { return evaluate(src, c.names(), c.funcs()) }

func (c *StepContext) val(spec any) (any, error) { return valueOf(spec, c.names(), c.funcs()) }

func (c *StepContext) evActor(src any) (string, error) {
	v, err := c.ev(src)
	if err != nil {
		return "", err
	}
	return actorIDOf(v)
}

func (c *StepContext) evNum(src any) (*big.Rat, error) {
	v, err := c.ev(src)
	if err != nil {
		return nil, err
	}
	n, err := toNum(v)
	if err != nil {
		return nil, ruleErr("%v", err)
	}
	return n, nil
}

// ---- actions (spec §6) -----------------------------------------------------------

func need(spec *OMap, action string, fields ...string) error {
	for _, f := range fields {
		if !spec.Has(f) {
			return ruleErr("action %q is missing field %q: %s", action, f, show(spec))
		}
	}
	return nil
}

func runActions(c *StepContext, actions any) error {
	if actions == nil {
		return nil
	}
	list, ok := actions.([]any)
	if !ok {
		return ruleErr("actions must be a list: %s", show(actions))
	}
	for _, it := range list {
		a, ok := it.(*OMap)
		if !ok || a.Len() == 0 {
			return ruleErr("bad action %s", show(it))
		}
		if a.Has("if") {
			cond, _ := a.Get("if")
			v, err := c.ev(cond)
			if err != nil {
				return err
			}
			branch := "else"
			if truthy(v) {
				branch = "then"
			}
			sub, _ := a.Get(branch)
			if err := runActions(c, sub); err != nil {
				return err
			}
			continue
		}
		name := a.keys[0]
		spec := a.m[name]
		if err := doAction(c, name, spec); err != nil {
			return err
		}
	}
	return nil
}

func accountRef(c *StepContext, spec any) (accKey, error) {
	m, ok := spec.(*OMap)
	if !ok || !m.Has("account") {
		return accKey{}, ruleErr("account reference needs {actor, account}: %s", repr(spec))
	}
	src, has := m.Get("actor")
	if !has {
		src = "self"
	}
	actor, err := c.evActor(src)
	if err != nil {
		return accKey{}, err
	}
	return accKey{actor, getString(m, "account")}, nil
}

func (c *StepContext) timeValue(raw any) (int64, error) {
	switch x := raw.(type) {
	case *big.Rat:
		return parseDuration(x)
	case string:
		if durationRe.MatchString(x) {
			return parseDuration(x)
		}
	}
	v, err := c.ev(raw)
	if err != nil {
		return 0, err
	}
	d, err := parseDuration(v)
	if err != nil {
		return 0, ruleErr("%v", err)
	}
	return d, nil
}

func intField(m *OMap, k string, def int64) int64 {
	v, ok := m.Get(k)
	if !ok || v == nil {
		return def
	}
	n, err := toNum(v)
	if err != nil {
		return def
	}
	return truncInt(n).Int64()
}

func doAction(c *StepContext, name string, raw any) error {
	if name == "log" {
		v, err := c.val(raw)
		if err != nil {
			return err
		}
		c.notes = append(c.notes, Note{Op: "log", Msg: show(v)})
		return nil
	}
	spec, ok := raw.(*OMap)
	if !ok {
		if raw == nil && (name == "release" || name == "set") {
			spec = NewOMap()
		} else {
			return ruleErr("action %q expects a mapping: %s", name, show(raw))
		}
	}
	switch name {
	case "set":
		for _, k := range spec.Keys() {
			v, err := c.val(spec.m[k])
			if err != nil {
				return err
			}
			if err := c.s.setField(c.self, k, v); err != nil {
				return err
			}
		}
	case "set_on":
		if err := need(spec, name, "actor"); err != nil {
			return err
		}
		target, err := c.evActor(spec.m["actor"])
		if err != nil {
			return err
		}
		if fields := getMap(spec, "fields"); fields != nil {
			for _, k := range fields.Keys() {
				v, err := c.val(fields.m[k])
				if err != nil {
					return err
				}
				if err := c.s.setField(target, k, v); err != nil {
					return err
				}
			}
		}
	case "create":
		if err := need(spec, name, "type"); err != nil {
			return err
		}
		typ := show(spec.m["type"])
		as := getString(spec, "as")
		ordinal := 0
		for _, n := range c.notes {
			if n.Op == "create" {
				ordinal++
			}
		}
		label := as
		if label == "" {
			label = typ
		}
		logical := fmt.Sprintf("%s:%s#%d", c.event.Key, label, ordinal)
		titleSpec, has := spec.Get("title")
		if !has {
			titleSpec = typ
		}
		title, err := c.val(titleSpec)
		if err != nil {
			return err
		}
		data := NewOMap()
		if dm := getMap(spec, "data"); dm != nil {
			v, err := c.val(dm)
			if err != nil {
				return err
			}
			data = v.(*OMap)
		}
		id, err := c.s.createActor(typ, show(title), data, c.event.Key, logical)
		if err != nil {
			return err
		}
		c.notes = append(c.notes, Note{Op: "create", ID: id, Extra: map[string]any{"logical_id": logical}})
		if lf, ok := spec.Get("link_from"); ok {
			src, err := c.evActor(lf)
			if err != nil {
				return err
			}
			et := getString(spec, "edge_type")
			if et == "" {
				et = hierarchy
			}
			if _, err := c.s.createLink(src, id, et); err != nil {
				return err
			}
		}
		for _, acc := range getList(spec, "accounts") {
			am, _ := acc.(*OMap)
			if _, err := c.s.ensureAccount(id, getString(am, "name"), getString(am, "value_type")); err != nil {
				return err
			}
		}
		if as != "" {
			c.vars[as] = &ActorView{c.s, id}
		}
	case "link":
		if err := need(spec, name, "from", "to"); err != nil {
			return err
		}
		src, err := c.evActor(spec.m["from"])
		if err != nil {
			return err
		}
		dst, err := c.evActor(spec.m["to"])
		if err != nil {
			return err
		}
		et := getString(spec, "edge_type")
		if et == "" {
			et = hierarchy
		}
		_, err = c.s.createLink(src, dst, et)
		return err
	case "transfer":
		if err := need(spec, name, "amount", "from", "to"); err != nil {
			return err
		}
		amount, err := c.evNum(spec.m["amount"])
		if err != nil {
			return err
		}
		if amount.Sign() == 0 {
			return nil
		}
		src, err := accountRef(c, spec.m["from"])
		if err != nil {
			return err
		}
		dst, err := accountRef(c, spec.m["to"])
		if err != nil {
			return err
		}
		return c.s.transfer(src, dst, amount)
	case "add", "set_account":
		field := "amount"
		if name == "set_account" {
			field = "value"
		}
		ref, err := accountRef(c, spec)
		if err != nil {
			return err
		}
		if err := need(spec, name, field); err != nil {
			return err
		}
		n, err := c.evNum(spec.m[field])
		if err != nil {
			return err
		}
		if name == "add" {
			return c.s.add(ref, n, getString(spec, "value_type"))
		}
		return c.s.setAccount(ref, n, getString(spec, "value_type"))
	case "schedule":
		if err := need(spec, name, "event"); err != nil {
			return err
		}
		tsrc, has := spec.Get("target")
		if !has {
			tsrc = "self"
		}
		target, err := c.evActor(tsrc)
		if err != nil {
			return err
		}
		var after int64
		if a, ok := spec.Get("after"); ok {
			if after, err = c.timeValue(a); err != nil {
				return err
			}
		}
		at := c.now + after
		if a, ok := spec.Get("at"); ok {
			if at, err = c.timeValue(a); err != nil {
				return err
			}
		}
		if at < c.now {
			return ruleErr("cannot schedule %s in the past (%d < %d)", show(spec.m["event"]), at, c.now)
		}
		payload, err := c.payload(spec)
		if err != nil {
			return err
		}
		c.schedule(scheduled{show(spec.m["event"]), target, at, intField(spec, "priority", 30), payload})
	case "enqueue":
		if err := need(spec, name, "resource", "event"); err != nil {
			return err
		}
		resource, err := c.evActor(spec.m["resource"])
		if err != nil {
			return err
		}
		tsrc, has := spec.Get("target")
		if !has {
			tsrc = "self"
		}
		target, err := c.evActor(tsrc)
		if err != nil {
			return err
		}
		res, err := c.s.actor(resource)
		if err != nil {
			return err
		}
		if acc, ok := spec.Get("account"); ok {
			cur, _ := res.Data.Get("_capacity_account")
			if !equal(cur, acc) {
				if err := c.s.setField(resource, "_capacity_account", acc); err != nil {
					return err
				}
			}
		}
		amtSrc, has := spec.Get("amount")
		if !has {
			amtSrc = ratOne
		}
		amt, err := c.ev(amtSrc)
		if err != nil {
			return err
		}
		payload, err := c.payload(spec)
		if err != nil {
			return err
		}
		job := NewOMap()
		job.Set("target", target)
		job.Set("event", show(spec.m["event"]))
		job.Set("amount", show(amt))
		job.Set("priority", ratInt(intField(spec, "priority", 40)))
		job.Set("payload", payload)
		queue := append(queueOf(res), job)
		if err := c.s.setField(resource, "_queue", queue); err != nil {
			return err
		}
		return c.dispatch(resource)
	case "release":
		tsrc, has := spec.Get("target")
		if !has {
			tsrc = "self"
		}
		target, err := c.evActor(tsrc)
		if err != nil {
			return err
		}
		a, err := c.s.actor(target)
		if err != nil {
			return err
		}
		hv, _ := a.Data.Get("_holding")
		hold, _ := hv.(*OMap)
		if hold == nil || !truthy(hold) {
			return nil
		}
		amt, err := toNum(getString(hold, "amount"))
		if err != nil {
			return ruleErr("%v", err)
		}
		account, resource := getString(hold, "account"), getString(hold, "resource")
		if err := c.s.transfer(accKey{target, account}, accKey{resource, account}, amt); err != nil {
			return err
		}
		if err := c.s.setField(target, "_holding", nil); err != nil {
			return err
		}
		return c.dispatch(resource)
	case "dequeue":
		if err := need(spec, name, "resource"); err != nil {
			return err
		}
		resource, err := c.evActor(spec.m["resource"])
		if err != nil {
			return err
		}
		tsrc, has := spec.Get("target")
		if !has {
			tsrc = "self"
		}
		target, err := c.evActor(tsrc)
		if err != nil {
			return err
		}
		res, err := c.s.actor(resource)
		if err != nil {
			return err
		}
		queue := queueOf(res)
		kept := []any{}
		for _, j := range queue {
			if getString(j.(*OMap), "target") != target {
				kept = append(kept, j)
			}
		}
		if len(kept) != len(queue) {
			return c.s.setField(resource, "_queue", kept)
		}
	case "decide":
		if err := need(spec, name, "options"); err != nil {
			return err
		}
		var options []string
		for _, o := range getList(spec, "options") {
			options = append(options, show(o))
		}
		varName := getString(spec, "var")
		if varName == "" {
			varName = "choice"
		}
		var ruleChoice *string
		if r, ok := spec.Get("rule"); ok {
			v, err := c.ev(r)
			if err != nil {
				return err
			}
			s := show(v)
			if !contains(options, s) {
				return ruleErr("rule returned %s, not one of %s", repr(s), show(toAny(options)))
			}
			ruleChoice = &s
		}
		choice, record, err := c.decider(c, spec, options, ruleChoice)
		if err != nil {
			return err
		}
		if !contains(options, choice) {
			return ruleErr("decision %s is not one of %s", repr(choice), show(toAny(options)))
		}
		c.vars[varName] = choice
		src, _ := record["source"].(string)
		c.notes = append(c.notes, Note{Op: "decide", Var: varName, Choice: choice, Source: src, Extra: record})
	default:
		known := []string{"add", "create", "decide", "dequeue", "enqueue", "link", "log", "release", "schedule", "set", "set_account", "set_on", "transfer"}
		sort.Strings(known)
		return ruleErr("unknown action %q", name)
	}
	return nil
}

func (c *StepContext) payload(spec *OMap) (*OMap, error) {
	p := getMap(spec, "payload")
	if p == nil {
		return NewOMap(), nil
	}
	v, err := c.val(p)
	if err != nil {
		return nil, err
	}
	return v.(*OMap), nil
}

func queueOf(a *Actor) []any {
	v, _ := a.Data.Get("_queue")
	l, _ := v.([]any)
	return append([]any{}, l...)
}

// dispatch gives free capacity to waiting jobs in FIFO order (spec §6, resource queue).
func (c *StepContext) dispatch(resource string) error {
	res, err := c.s.actor(resource)
	if err != nil {
		return err
	}
	capAcc := "capacity"
	if v, ok := res.Data.Get("_capacity_account"); ok && v != nil {
		capAcc = show(v)
	}
	queue := queueOf(res)
	changed := false
	for len(queue) > 0 {
		job := queue[0].(*OMap)
		needAmt := ratOne
		if a, ok := job.Get("amount"); ok {
			if needAmt, err = toNum(a); err != nil {
				return ruleErr("%v", err)
			}
		}
		free := c.s.account(resource, capAcc)
		if free == nil || free.Value.Cmp(needAmt) < 0 {
			break
		}
		queue = queue[1:]
		changed = true
		target := getString(job, "target")
		if err := c.s.transfer(accKey{resource, capAcc}, accKey{target, capAcc}, needAmt); err != nil {
			return err
		}
		hold := NewOMap()
		hold.Set("resource", resource)
		hold.Set("account", capAcc)
		hold.Set("amount", numString(needAmt))
		if err := c.s.setField(target, "_holding", hold); err != nil {
			return err
		}
		payload := getMap(job, "payload")
		if payload == nil {
			payload = NewOMap()
		}
		c.schedule(scheduled{getString(job, "event"), target, c.now, intField(job, "priority", 40), deepCopy(payload).(*OMap)})
	}
	if changed {
		return c.s.setField(resource, "_queue", queue)
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func toAny(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// RuleDecider: the rule if present, else a uniform pick with a stable stream (spec §7).
func RuleDecider(c *StepContext, spec *OMap, options []string, ruleChoice *string) (string, map[string]any, error) {
	if ruleChoice != nil {
		return *ruleChoice, map[string]any{"source": "rule"}, nil
	}
	varName := getString(spec, "var")
	if varName == "" {
		varName = "choice"
	}
	u := stableUniform(c.seed, "decide", c.logicalSelf(), c.event.Key, varName)
	idx := truncInt(new(big.Rat).Mul(u, ratInt(int64(len(options))))).Int64()
	if idx > int64(len(options)-1) {
		idx = int64(len(options) - 1)
	}
	return options[idx], map[string]any{"source": "uniform", "u": numString(u)}, nil
}

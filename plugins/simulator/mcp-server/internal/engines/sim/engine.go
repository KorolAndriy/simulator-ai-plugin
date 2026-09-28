package sim

import (
	"container/heap"
	"fmt"
	"math/big"
	"strings"
)

type Scenario struct {
	Name    string
	Params  *OMap
	Set     []any
	Horizon any
	Seed    string
}

func ScenarioFrom(d *OMap) Scenario {
	s := Scenario{Name: getString(d, "name"), Params: getMap(d, "params"), Set: getList(d, "set"), Seed: getString(d, "seed")}
	if s.Name == "" {
		s.Name = "base"
	}
	if s.Params == nil {
		s.Params = NewOMap()
	}
	if s.Seed == "" {
		s.Seed = "morrow"
	}
	if h, ok := d.Get("horizon"); ok {
		s.Horizon = h
	}
	return s
}

// LoadScenarios reads scenarios.yaml: a list of scenarios (spec §8).
func LoadScenarios(src []byte) ([]Scenario, []*OMap, error) {
	raw, err := parseYAML(src)
	if err != nil {
		return nil, nil, fmt.Errorf("scenarios: %w", err)
	}
	list, ok := raw.([]any)
	if raw != nil && !ok {
		return nil, nil, fmt.Errorf("scenarios: a list expected")
	}
	var out []Scenario
	var rawList []*OMap
	for i, it := range list {
		m, ok := it.(*OMap)
		if !ok {
			return nil, nil, fmt.Errorf("scenarios[%d]: mapping expected", i)
		}
		out = append(out, ScenarioFrom(m))
		rawList = append(rawList, m)
	}
	return out, rawList, nil
}

// LogEntry is one processed event.
type LogEntry struct {
	Time      int64  `json:"time"`
	Priority  int64  `json:"priority"`
	Kind      string `json:"kind"`
	Target    string `json:"target"`
	Title     string `json:"target_title,omitempty"`
	Key       string `json:"key"`
	Skipped   string `json:"skipped,omitempty"`
	Error     string `json:"error,omitempty"`
	Changes   int    `json:"changes"`
	Notes     []Note `json:"notes,omitempty"`
	Scheduled int    `json:"scheduled,omitempty"`
}

type RunResult struct {
	Scenario      string         `json:"scenario"`
	Status        string         `json:"status"`
	ModelTime     int64          `json:"model_time"`
	Steps         int            `json:"steps"`
	Metrics       map[string]any `json:"metrics"`
	MetricOrder   []string       `json:"-"`
	Log           []LogEntry     `json:"log"`
	Error         string         `json:"error,omitempty"`
	PendingEvents int            `json:"pending_events"`
	Graph         *Graph         `json:"-"`
}

type eventQueue []*Event

func (q eventQueue) Len() int { return len(q) }
func (q eventQueue) Less(i, j int) bool {
	a, b := q[i], q[j]
	if a.Time != b.Time {
		return a.Time < b.Time
	}
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	return a.Seq < b.Seq
}
func (q eventQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *eventQueue) Push(x any)   { *q = append(*q, x.(*Event)) }
func (q *eventQueue) Pop() any {
	old := *q
	n := len(old)
	e := old[n-1]
	*q = old[:n-1]
	return e
}

type Engine struct {
	s        *store
	model    *Model
	scenario Scenario
	decider  Decider
	MaxSteps int
	params   *OMap
	horizon  int64
	refs     *OMap
	queue    eventQueue
	seq      int64
	log      []LogEntry
}

func NewEngine(g *Graph, model *Model, sc Scenario, decider Decider) (*Engine, error) {
	if decider == nil {
		decider = RuleDecider
	}
	e := &Engine{s: newStore(g), model: model, scenario: sc, decider: decider, MaxSteps: 1_000_000}
	e.params = NewOMap()
	for _, k := range model.Params.Keys() {
		e.params.Set(k, model.Params.m[k])
	}
	for _, k := range sc.Params.Keys() {
		e.params.Set(k, sc.Params.m[k])
	}
	e.horizon = model.Horizon
	if sc.Horizon != nil {
		h, err := parseDuration(sc.Horizon)
		if err != nil {
			return nil, fmt.Errorf("scenario %s horizon: %w", sc.Name, err)
		}
		e.horizon = h
	}
	for _, name := range model.vtOrder {
		g.valueTypes[name] = model.ValueTypes[name]
	}
	if err := addModelActors(g, model, e.paramValue); err != nil {
		return nil, err
	}
	e.refs = NewOMap()
	for _, name := range model.Refs.Keys() {
		id, err := resolveActor(g, model.Refs.m[name])
		if err != nil {
			return nil, err
		}
		e.refs.Set(name, id)
	}
	return e, nil
}

func (e *Engine) paramValue(v any) (any, error) {
	if s, ok := v.(string); ok && strings.HasPrefix(s, "=") {
		return evaluate(s[1:], map[string]any{"params": &Namespace{e.params, "params"}}, map[string]Func{})
	}
	return v, nil
}

// addModelActors appends the helper actors declared by the model (spec §5).
func addModelActors(g *Graph, model *Model, resolve func(any) (any, error)) error {
	for _, spec := range model.Actors {
		title := getString(spec, "title")
		id := getString(spec, "id")
		if id == "" {
			id = "model:" + title
		}
		if g.actorIdx[id] != nil {
			continue
		}
		typ := getString(spec, "type")
		if typ == "" {
			typ = "helper"
		}
		data := NewOMap()
		if d := getMap(spec, "data"); d != nil {
			data = deepCopy(d).(*OMap)
		}
		g.addActor(&Actor{ID: id, Type: typ, Title: title, Data: data})
		for _, it := range getList(spec, "accounts") {
			acc, _ := it.(*OMap)
			vt := getString(acc, "value_type")
			if vt == "" {
				vt = "default"
			}
			raw, has := acc.Get("value")
			if !has || raw == nil {
				raw = ratZero
			}
			v, err := resolve(raw)
			if err != nil {
				return err
			}
			n, err := toNum(v)
			if err != nil {
				return fmt.Errorf("model actor %s: %w", title, err)
			}
			q, err := g.vtype(vt).quantize(n)
			if err != nil {
				return err
			}
			g.addAccount(&Account{ActorID: id, Name: getString(acc, "name"), ValueType: vt, Value: q})
		}
	}
	return nil
}

func (e *Engine) push(kind, target string, t, prio int64, payload *OMap, key string) {
	e.seq++
	heap.Push(&e.queue, &Event{t, prio, e.seq, kind, target, payload, key})
}

func (e *Engine) actorType(id string) string {
	a := e.s.g.actorIdx[id]
	if a == nil {
		return ""
	}
	if e.model.TypeField != "" {
		if v, ok := a.Data.Get(e.model.TypeField); ok {
			return show(v)
		}
	}
	return a.Type
}

func (e *Engine) applyScenario() error {
	if len(e.scenario.Set) == 0 {
		return nil
	}
	if err := e.s.begin(); err != nil {
		return err
	}
	names := map[string]any{"params": &Namespace{e.params, "params"}}
	for _, it := range e.scenario.Set {
		item, ok := it.(*OMap)
		if !ok {
			e.s.rollback()
			return ruleErr("scenario set item must be a mapping")
		}
		ref, _ := item.Get("actor")
		target := ""
		if id, ok := e.refs.Get(show(ref)); ok {
			target = show(id)
		} else {
			var err error
			if target, err = resolveActor(e.s.g, ref); err != nil {
				e.s.rollback()
				return err
			}
		}
		if fields := getMap(item, "fields"); fields != nil {
			for _, k := range fields.Keys() {
				v := fields.m[k]
				if s, ok := v.(string); ok && strings.HasPrefix(s, "=") {
					var err error
					if v, err = evaluate(s[1:], names, map[string]Func{}); err != nil {
						e.s.rollback()
						return err
					}
				}
				if err := e.s.setField(target, k, v); err != nil {
					e.s.rollback()
					return err
				}
			}
		}
	}
	if err := e.s.commit(); err != nil {
		e.s.rollback()
		return err
	}
	return nil
}

func (e *Engine) initialEvents() error {
	for i, spec := range e.model.InitialEvents {
		var targets []string
		if ft, ok := spec.Get("for_type"); ok {
			for _, a := range e.s.g.actors {
				if e.actorType(a.ID) == show(ft) {
					targets = append(targets, a.ID)
				}
			}
		} else {
			t, _ := spec.Get("target")
			if id, ok := e.refs.Get(show(t)); ok {
				targets = []string{show(id)}
			} else {
				id, err := resolveActor(e.s.g, t)
				if err != nil {
					return err
				}
				targets = []string{id}
			}
		}
		atRaw, has := spec.Get("at")
		if !has {
			atRaw = ratZero
		}
		atv, err := e.paramValue(atRaw)
		if err != nil {
			return err
		}
		at, err := parseDuration(atv)
		if err != nil {
			return err
		}
		every := int64(-1)
		if ev, ok := spec.Get("every"); ok {
			v, err := e.paramValue(ev)
			if err != nil {
				return err
			}
			if every, err = parseDuration(v); err != nil {
				return err
			}
		}
		payload := getMap(spec, "payload")
		for _, tid := range targets {
			a := e.s.g.actorIdx[tid]
			logical := a.OriginID
			if logical == "" {
				logical = a.ID
			}
			for t, n := at, 0; t <= e.horizon; n++ {
				p := NewOMap()
				if payload != nil {
					p = deepCopy(payload).(*OMap)
				}
				e.push(getString(spec, "event"), tid, t, intField(spec, "priority", 30), p, fmt.Sprintf("init%d:%s#%d", i, logical, n))
				if every <= 0 {
					break
				}
				t += every
			}
		}
	}
	return nil
}

func (e *Engine) Run() *RunResult {
	var now int64
	steps, status, errMsg := 0, "completed", ""
	fail := func(err error) *RunResult {
		return &RunResult{Scenario: e.scenario.Name, Status: "failed", Error: err.Error(), Log: e.log, Graph: e.s.g, Metrics: map[string]any{}}
	}
	if err := e.applyScenario(); err != nil {
		return fail(err)
	}
	if err := e.initialEvents(); err != nil {
		return fail(err)
	}
	for e.queue.Len() > 0 {
		ev := e.queue[0]
		if ev.Time > e.horizon {
			break
		}
		if steps >= e.MaxSteps {
			status, errMsg = "stopped_by_limit", fmt.Sprintf("max_steps %d reached", e.MaxSteps)
			break
		}
		heap.Pop(&e.queue)
		now = ev.Time
		steps++
		if err := e.step(ev); err != nil {
			status, errMsg = "failed", fmt.Sprintf("t=%d %s -> %s: %s", ev.Time, ev.Kind, ev.Target, err)
			e.log = append(e.log, LogEntry{Time: ev.Time, Priority: ev.Priority, Kind: ev.Kind, Target: ev.Target, Key: ev.Key, Error: err.Error()})
			break
		}
	}
	res := &RunResult{Scenario: e.scenario.Name, Status: status, ModelTime: now, Steps: steps, Log: e.log,
		Error: errMsg, PendingEvents: e.queue.Len(), Graph: e.s.g, Metrics: map[string]any{}}
	if status != "failed" {
		m, order, err := e.metrics()
		if err != nil {
			res.Status, res.Error = "failed", "metrics: "+err.Error()
		} else {
			res.Metrics, res.MetricOrder = m, order
		}
	}
	return res
}

func (e *Engine) step(ev *Event) error {
	a := e.s.g.actorIdx[ev.Target]
	entry := LogEntry{Time: ev.Time, Priority: ev.Priority, Kind: ev.Kind, Target: ev.Target, Key: ev.Key}
	if a != nil {
		entry.Title = a.Title
	}
	var handler any
	if byType := getMap(e.model.Behaviors, e.actorType(ev.Target)); byType != nil {
		handler, _ = byType.Get(ev.Kind) // a missing or null handler skips the event
	}
	if handler == nil {
		entry.Skipped = "no handler"
		e.log = append(e.log, entry)
		return nil
	}
	var sched []scheduled
	ctx := &StepContext{s: e.s, model: e.model, params: e.params, refs: e.refs, now: ev.Time, event: ev,
		self: ev.Target, seed: e.scenario.Seed, schedule: func(x scheduled) { sched = append(sched, x) },
		decider: e.decider, vars: map[string]any{}}
	if err := e.s.begin(); err != nil {
		return err
	}
	if err := runActions(ctx, handler); err != nil {
		e.s.rollback()
		return err
	}
	changes := e.s.changes
	if err := e.s.commit(); err != nil {
		e.s.rollback()
		return err
	}
	for n, x := range sched {
		e.push(x.kind, x.target, x.time, x.prio, x.payload, fmt.Sprintf("%s>%s#%d", ev.Key, x.kind, n))
	}
	entry.Changes, entry.Notes, entry.Scheduled = changes, ctx.notes, len(sched)
	e.log = append(e.log, entry)
	return nil
}

func (e *Engine) metrics() (map[string]any, []string, error) {
	out := map[string]any{}
	var order []string
	if len(e.s.g.actors) == 0 {
		return out, order, nil
	}
	ctx := &StepContext{s: e.s, model: e.model, params: e.params, refs: e.refs, now: e.horizon,
		event: &Event{Time: e.horizon, Kind: "metrics", Payload: NewOMap(), Key: "metrics"},
		self:  e.s.g.actors[0].ID, seed: e.scenario.Seed, schedule: func(scheduled) {}, decider: e.decider, vars: map[string]any{}}
	for _, name := range e.model.Metrics.Keys() {
		v, err := ctx.ev(e.model.Metrics.m[name])
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = v
		order = append(order, name)
	}
	return out, order, nil
}

// RunScenario runs one scenario on a copy of the graph.
func RunScenario(g *Graph, model *Model, sc Scenario, decider Decider) *RunResult {
	e, err := NewEngine(g.clone(), model, sc, decider)
	if err != nil {
		return &RunResult{Scenario: sc.Name, Status: "failed", Error: err.Error(), Metrics: map[string]any{}}
	}
	return e.Run()
}

// MetricText renders a metric for people: numbers in plain notation.
func MetricText(v any) string {
	if r, ok := v.(*big.Rat); ok {
		return trimZeros(numString(r))
	}
	return show(v)
}

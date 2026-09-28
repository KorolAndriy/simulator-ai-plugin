package sim

import (
	"fmt"
	"math/big"
	"sort"
)

// Stats of one numeric metric over many runs (spec §9).
type Stats struct {
	N                             int
	Mean, P10, P50, P90, Min, Max *big.Rat
}

type Summary struct {
	Scenario  string
	Runs      int
	Completed int
	Failed    int
	Stopped   int
	Metrics   map[string]*Stats
	Goals     map[string][2]int // held, evaluated
	Errors    []string
}

func percentile(sorted []*big.Rat, p int) *big.Rat {
	n := len(sorted)
	k := (p*n+99)/100 - 1 // nearest rank: ceil(p*n/100), 1-based
	if k < 0 {
		k = 0
	}
	if k > n-1 {
		k = n - 1
	}
	return sorted[k]
}

func statsOf(vals []*big.Rat) *Stats {
	v := append([]*big.Rat(nil), vals...)
	sort.Slice(v, func(i, j int) bool { return v[i].Cmp(v[j]) < 0 })
	sum := new(big.Rat)
	for _, x := range v {
		sum.Add(sum, x)
	}
	return &Stats{N: len(v), Mean: new(big.Rat).Quo(sum, ratInt(int64(len(v)))),
		P10: percentile(v, 10), P50: percentile(v, 50), P90: percentile(v, 90), Min: v[0], Max: v[len(v)-1]}
}

// RunMany runs a scenario n times with seeds "<seed>#i"; failed runs are counted, not averaged.
func RunMany(g *Graph, model *Model, sc Scenario, n int, decider Decider, extraGoals *OMap) *Summary {
	goals := NewOMap()
	for _, k := range model.Goals.Keys() {
		goals.Set(k, model.Goals.m[k])
	}
	for _, k := range extraGoals.Keys() {
		goals.Set(k, extraGoals.m[k])
	}
	s := &Summary{Scenario: sc.Name, Runs: n, Metrics: map[string]*Stats{}, Goals: map[string][2]int{}}
	values := map[string][]*big.Rat{}
	for i := 0; i < n; i++ {
		run := sc
		run.Seed = fmt.Sprintf("%s#%d", sc.Seed, i)
		r := RunScenario(g, model, run, decider)
		if r.Status == "failed" {
			s.Failed++
			if r.Error != "" && !contains(s.Errors, r.Error) {
				s.Errors = append(s.Errors, r.Error)
			}
			continue
		}
		if r.Status == "completed" {
			s.Completed++
		} else {
			s.Stopped++
		}
		names := map[string]any{}
		for k, v := range r.Metrics {
			if num, err := toNum(v); err == nil {
				values[k] = append(values[k], num)
				names[k] = num
			} else {
				names[k] = v
			}
		}
		for _, gname := range goals.Keys() {
			held := s.Goals[gname]
			ok, err := evaluate(goals.m[gname], names, map[string]Func{})
			if err != nil {
				msg := fmt.Sprintf("goal %s: %v", gname, err)
				if !contains(s.Errors, msg) {
					s.Errors = append(s.Errors, msg)
				}
				continue
			}
			if truthy(ok) {
				held[0]++
			}
			held[1]++
			s.Goals[gname] = held
		}
	}
	for k, v := range values {
		s.Metrics[k] = statsOf(v)
	}
	return s
}

func (st *Stats) Short() string {
	if st.Min.Cmp(st.Max) == 0 {
		return fmtShort(st.P50)
	}
	return fmt.Sprintf("%s (%s–%s)", fmtShort(st.P50), fmtShort(st.P10), fmtShort(st.P90))
}

func fmtShort(r *big.Rat) string { return trimZeros(quantize(r, 2).FloatString(2)) }

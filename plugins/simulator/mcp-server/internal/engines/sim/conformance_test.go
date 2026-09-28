package sim

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// The conformance cases come from the Python reference engine (sim-morrow,
// scripts/export_conformance.py). This engine must give the same answers (spec §10).

type expectedScenario struct {
	Status        string         `json:"status"`
	Steps         int            `json:"steps"`
	PendingEvents int            `json:"pending_events"`
	Metrics       map[string]any `json:"metrics"`
	Events        [][]any        `json:"events"`
	Error         *string        `json:"error"`
}

type expectedRuns struct {
	Runs      int                          `json:"runs"`
	Completed int                          `json:"completed"`
	Failed    int                          `json:"failed"`
	Goals     map[string][2]int            `json:"goals"`
	Metrics   map[string]map[string]string `json:"metrics"`
}

type expectedCase struct {
	Scenarios map[string]expectedScenario `json:"scenarios"`
	Runs      map[string]expectedRuns     `json:"runs"`
}

func canonAny(v any) any {
	switch x := v.(type) {
	case *big.Rat:
		return Canon(x)
	case bool:
		return x
	case nil:
		return nil
	}
	if r, err := toNum(v); err == nil {
		return Canon(r)
	}
	return show(v)
}

func loadCase(t *testing.T, dir string) (*Graph, *Model, []Scenario, []*OMap, expectedCase) {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	g, err := LoadGraph(read("graph.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := LoadModel(read("model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	scs, raw, err := LoadScenarios(read("scenarios.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var exp expectedCase
	if err := json.Unmarshal(read("expected.json"), &exp); err != nil {
		t.Fatal(err)
	}
	return g, m, scs, raw, exp
}

func TestConformance(t *testing.T) {
	files, _ := filepath.Glob("testdata/conformance/*/expected.json")
	if len(files) == 0 {
		t.Fatal("no conformance cases")
	}
	for _, f := range files {
		dir := filepath.Dir(f)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			g, m, scs, raw, exp := loadCase(t, dir)
			for i, sc := range scs {
				want, ok := exp.Scenarios[sc.Name]
				if !ok {
					t.Fatalf("no expected result for %s", sc.Name)
				}
				r := RunScenario(g, m, sc, nil, AllLog)
				if r.Status != want.Status || r.Steps != want.Steps || r.PendingEvents != want.PendingEvents {
					t.Errorf("%s: status/steps/pending = %s/%d/%d, want %s/%d/%d (error %q)", sc.Name,
						r.Status, r.Steps, r.PendingEvents, want.Status, want.Steps, want.PendingEvents, r.Error)
				}
				if want.Status == "failed" && r.Error == "" {
					t.Errorf("%s: failed run without an error message", sc.Name)
				}
				got := map[string]any{}
				for k, v := range r.Metrics {
					got[k] = canonAny(v)
				}
				wantM := map[string]any{}
				for k, v := range want.Metrics {
					wantM[k] = canonAny(v)
				}
				if !reflect.DeepEqual(got, wantM) {
					t.Errorf("%s: metrics\n got  %v\n want %v", sc.Name, got, wantM)
				}
				var events [][]any
				for _, e := range r.Log {
					events = append(events, []any{float64(e.Time), float64(e.Priority), e.Kind, e.Target})
				}
				if len(events) != len(want.Events) {
					t.Errorf("%s: %d events, want %d", sc.Name, len(events), len(want.Events))
				}
				for j := 0; j < len(events) && j < len(want.Events); j++ {
					if !reflect.DeepEqual(events[j], want.Events[j]) {
						t.Errorf("%s: event %d = %v, want %v", sc.Name, j, events[j], want.Events[j])
						break
					}
				}
				wantRuns, hasRuns := exp.Runs[sc.Name]
				n := intField(raw[i], "runs", 0)
				if !hasRuns || n == 0 {
					continue
				}
				s := RunMany(nil, g, m, sc, int(n), nil, NewOMap())
				if s.Runs != wantRuns.Runs || s.Completed != wantRuns.Completed || s.Failed != wantRuns.Failed {
					t.Errorf("%s runs: %d/%d/%d, want %d/%d/%d", sc.Name, s.Runs, s.Completed, s.Failed,
						wantRuns.Runs, wantRuns.Completed, wantRuns.Failed)
				}
				if !reflect.DeepEqual(s.Goals, wantRuns.Goals) {
					t.Errorf("%s runs goals = %v, want %v", sc.Name, s.Goals, wantRuns.Goals)
				}
				names := make([]string, 0, len(wantRuns.Metrics))
				for k := range wantRuns.Metrics {
					names = append(names, k)
				}
				sort.Strings(names)
				for _, k := range names {
					st := s.Metrics[k]
					if st == nil {
						t.Errorf("%s runs: no stats for %s", sc.Name, k)
						continue
					}
					got := map[string]string{"n": Canon(ratInt(int64(st.N))), "mean": Canon(st.Mean), "p10": Canon(st.P10),
						"p50": Canon(st.P50), "p90": Canon(st.P90), "min": Canon(st.Min), "max": Canon(st.Max)}
					if !reflect.DeepEqual(got, wantRuns.Metrics[k]) {
						t.Errorf("%s runs %s = %v, want %v", sc.Name, k, got, wantRuns.Metrics[k])
					}
				}
			}
		})
	}
}

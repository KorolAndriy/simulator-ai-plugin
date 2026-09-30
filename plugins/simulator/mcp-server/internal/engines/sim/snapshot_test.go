package sim

import (
	"strings"
	"testing"
)

func TestReadAccountsFoldsDebitCreditAndSkipsSystem(t *testing.T) {
	g := newGraph()
	a := &Actor{ID: "a", Type: "t", Title: "A", Data: NewOMap()}
	g.addActor(a)
	readAccounts(g, a, []accountRow{
		{AccountName: "cash", CurrencyName: "UAH", CurrencyID: "1", NameID: "n1", Amount: "150.5", IncomeType: "credit", Type: "fact"},
		{AccountName: "cash", CurrencyName: "UAH", CurrencyID: "1", NameID: "n1", Amount: "20.25", IncomeType: "debit", Type: "fact"},
		{AccountName: "cash", CurrencyName: "UAH", CurrencyID: "1", NameID: "n1", Amount: "999", IncomeType: "credit", Type: "plan"},
		{AccountName: "Total edges", CurrencyName: "count", CurrencyID: "2", NameID: "n2", Amount: "7", IncomeType: "credit", Type: "fact", IsSystem: true},
		{AccountName: "cash", CurrencyName: "USD", CurrencyID: "3", NameID: "n1", Amount: "3E-8", IncomeType: "credit", Type: "fact"},
	})
	if len(g.accounts) != 2 {
		t.Fatalf("accounts: %d", len(g.accounts))
	}
	if v := g.accIdx[accKey{"a", "cash [UAH]"}].Value; Canon(v) != "130.25" {
		t.Errorf("UAH cash = %s, want 130.25 (credit - debit, exact)", Canon(v))
	}
	if v := g.accIdx[accKey{"a", "cash [USD]"}].Value; Canon(v) != "0.00000003" {
		t.Errorf("USD cash = %s, want 0.00000003", Canon(v))
	}
}

func TestGraphFileRoundTrip(t *testing.T) {
	m, g, _ := caseFiles(t, "sample01")
	_ = m
	b, err := GraphFile(g, "")
	if err != nil {
		t.Fatal(err)
	}
	g2, err := LoadGraph(b)
	if err != nil {
		t.Fatal(err)
	}
	if g2.summary() != g.summary() {
		t.Fatalf("%s != %s", g2.summary(), g.summary())
	}
	for _, a := range g.accounts {
		b := g2.accIdx[accKey{a.ActorID, a.Name}]
		if b == nil || b.Value.Cmp(a.Value) != 0 || b.ValueType != a.ValueType {
			t.Errorf("account %s.%s lost in round trip", a.ActorID, a.Name)
		}
	}
	for _, a := range g.actors {
		if g2.actorIdx[a.ID].Type != a.Type {
			t.Errorf("type of %s lost", a.ID)
		}
	}
}

// The layer endpoints return elements in different orders; a snapshot must not depend
// on which one was read, because graph order drives the random draws.
func TestLayerGraphOrderIsCanonical(t *testing.T) {
	nodes := []layerNode{
		{ID: "c", Title: "C", FormTitle: "t"}, {ID: "a", Title: "A", FormTitle: "t"},
		{ID: "b", Title: "B", FormTitle: "t"}, {ID: "a", Title: "A", FormTitle: "t"},
		{ID: "l", Title: "L", FormTitle: "Layers"},
	}
	edges := []layerEdge{
		{ID: "e2", Source: "a", Target: "b"}, {ID: "e1", Source: "b", Target: "c"},
		{ID: "e2", Source: "a", Target: "b"}, {ID: "e0", Source: "l", Target: "a"},
	}
	g := layerGraph("layer", nodes, edges)
	var ids []string
	for _, a := range g.actors {
		ids = append(ids, a.ID)
	}
	var links []string
	for _, l := range g.links {
		links = append(links, l.ID)
	}
	if got := strings.Join(ids, ","); got != "a,b,c" {
		t.Errorf("actors = %s, want a,b,c", got)
	}
	if got := strings.Join(links, ","); got != "e1,e2" {
		t.Errorf("links = %s, want e1,e2", got)
	}
}

func TestUnnamedFormTypesFindsBareFormIDs(t *testing.T) {
	g, err := LoadGraph([]byte(`layerId: x
actors:
  - {id: a, title: A, formId: 101}
  - {id: b, title: B, formId: 101, formName: Shops}
  - {id: c, title: C, formId: 102}
edges: []
`))
	if err != nil {
		t.Fatal(err)
	}
	got := unnamedFormTypes(g)
	if len(got) != 2 || len(got["101"]) != 1 || got["101"][0].ID != "a" || len(got["102"]) != 1 {
		t.Errorf("unnamed = %v, want 101:[a] 102:[c]", got)
	}
}

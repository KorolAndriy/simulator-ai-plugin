package sim

import "testing"

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

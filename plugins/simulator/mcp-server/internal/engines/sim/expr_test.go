package sim

import (
	"math/big"
	"strings"
	"testing"
)

func evalStr(t *testing.T, src string, names map[string]any) any {
	t.Helper()
	v, err := evaluate(src, names, map[string]Func{})
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return v
}

func TestExpressionsFollowPythonSemantics(t *testing.T) {
	ns := &Namespace{NewOMap(), "params"}
	ns.data.Set("price", ratInt(80))
	ns.data.Set("mode", "ai")
	names := map[string]any{"params": ns, "xs": []any{ratInt(1), "a"}}
	cases := map[string]string{
		"1 + 2 * 3": "7",
		"0.1 + 0.2": "0.3", // exact decimals, no float error
		"7 // 2":    "3",
		"-7 // 2":   "-3", // Decimal // truncates towards zero
		"-7 % 2":    "-1", // and % keeps the dividend's sign
		"'accept' if params.price <= 90 else 'decline'": "accept",
		"params.mode == 'ai' and params.price":          "80", // and/or return an operand
		"0 or 'x'":                                      "x",
		"1 < 2 < 3":                                     "True",
		"'a' in xs":                                     "True",
		"'z' not in xs":                                 "True",
		"'pro' in 'process'":                            "True",
		"params.get('missing', 5)":                      "5",
		"None is None":                                  "True",
		"[1, 2] + [3]":                                  "[1, 2, 3]",
		"'Order ' + 'A'":                                "Order A",
		"not ''":                                        "True",
	}
	for src, want := range cases {
		if got := show(evalStr(t, src, names)); got != want {
			t.Errorf("%s = %s, want %s", src, got, want)
		}
	}
}

func TestExpressionsAreSandboxed(t *testing.T) {
	for _, src := range []string{"().__class__", "__import__('os')", "lambda: 1", "2 ** 10", "x.y", "1 +", "'a' < 1"} {
		if _, err := evaluate(src, map[string]any{}, map[string]Func{}); err == nil {
			t.Errorf("%s: expected an error", src)
		}
	}
	if _, err := evaluate("1 / 0", map[string]any{}, map[string]Func{}); err == nil || !strings.Contains(err.Error(), "division by zero") {
		t.Errorf("1/0: %v", err)
	}
}

func TestQuantizeHalfEven(t *testing.T) {
	cases := map[string]string{"0.125": "0.12", "0.135": "0.14", "-0.125": "-0.12", "2.675": "2.68", "1.005": "1.00"}
	for in, want := range cases {
		r, _ := new(big.Rat).SetString(in)
		if got := quantize(r, 2).FloatString(2); got != want {
			t.Errorf("quantize(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestStableUniformMatchesReference(t *testing.T) {
	// SHA-256("morrow|decide|client_b|init0:client_b#0|choice")[:8] / 2^64, as computed by the Python engine.
	u := stableUniform("morrow", "decide", "client_b", "init0:client_b#0", "choice")
	if got := u.FloatString(28); got != "0.6735588117454456849532602736" {
		t.Fatalf("U = %s, want 0.6735588117454456849532602736 (Python reference)", got)
	}
	if again := stableUniform("morrow", "decide", "client_b", "init0:client_b#0", "choice"); again.Cmp(u) != 0 {
		t.Fatal("not deterministic")
	}
}

func TestDurations(t *testing.T) {
	for in, want := range map[any]int64{"15m": 900, "2h": 7200, "1d": 86400, "30": 30, ratInt(45): 45, "500ms": 0} {
		if got, err := parseDuration(in); err != nil || got != want {
			t.Errorf("parseDuration(%v) = %d, %v; want %d", in, got, err, want)
		}
	}
}

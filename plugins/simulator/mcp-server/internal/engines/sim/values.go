// Package sim is the Go engine of sim-morrow: behaviour simulation of Simulator graphs.
//
// The model format and its semantics are specified in
// plugins/simulator/docs/simulation/model-format.md. testdata/conformance holds the
// answers of the reference engine that this engine must reproduce (TestConformance).
package sim

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// Numbers are exact rationals; nothing goes through float64.

var (
	ratZero = new(big.Rat)
	ratOne  = big.NewRat(1, 1)
)

func ratInt(n int64) *big.Rat { return big.NewRat(n, 1) }

// parseNum converts a decimal string ("12", "-0.5", "3E-8", "1.2e+2") to a rational.
func parseNum(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

// toNum converts any number-like value to a rational.
func toNum(v any) (*big.Rat, error) {
	switch x := v.(type) {
	case *big.Rat:
		return x, nil
	case int:
		return ratInt(int64(x)), nil
	case int64:
		return ratInt(x), nil
	case float64:
		r, _ := parseNum(strconv.FormatFloat(x, 'g', -1, 64))
		return r, nil
	case bool:
		return nil, fmt.Errorf("bool is not a number")
	case string:
		if r, ok := parseNum(x); ok {
			return r, nil
		}
		return nil, fmt.Errorf("not a number: %q", x)
	}
	return nil, fmt.Errorf("not a number: %v", show(v))
}

// quantize rounds r to `scale` decimal places, half to even.
func quantize(r *big.Rat, scale int) *big.Rat {
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(den))
	q, rem := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	// remainder relative to the denominator decides rounding
	twice := new(big.Int).Mul(new(big.Int).Abs(rem), big.NewInt(2))
	cmp := twice.Cmp(scaled.Denom())
	if cmp > 0 || (cmp == 0 && q.Bit(0) == 1) {
		if scaled.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return new(big.Rat).SetFrac(q, den)
}

// truncInt returns the integer part of r (towards zero).
func truncInt(r *big.Rat) *big.Int {
	return new(big.Int).Quo(r.Num(), r.Denom())
}

// numString prints a rational in plain notation. Exact decimals print all their digits;
// others print with 28 significant digits (like Python's Decimal context).
func numString(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}
	for scale := 1; scale <= 40; scale++ {
		q := quantize(r, scale)
		if q.Cmp(r) == 0 {
			return q.FloatString(scale)
		}
	}
	// non-terminating: 28 significant digits
	intDigits := len(new(big.Int).Abs(truncInt(r)).String())
	if truncInt(r).Sign() == 0 {
		intDigits = 0
	}
	scale := 28 - intDigits
	if scale < 0 {
		scale = 0
	}
	return trimZeros(quantize(r, scale).FloatString(scale))
}

func trimZeros(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" || s == "" {
		return "0"
	}
	return s
}

// Canon is the conformance form of a number: 12 decimals at most, half to even, no trailing zeros.
func Canon(r *big.Rat) string { return trimZeros(quantize(r, 12).FloatString(12)) }

// ValueType describes how an account value is counted.
type ValueType struct {
	Name      string
	Integer   bool
	Scale     int
	Conserved bool
	Min, Max  *big.Rat
}

var defaultType = ValueType{Name: "default", Scale: 2}

func (vt ValueType) quantize(v *big.Rat) (*big.Rat, error) {
	q := quantize(v, vt.Scale)
	if vt.Integer && q.Cmp(v) != 0 {
		return nil, fmt.Errorf("%s: integer value expected, got %s", vt.Name, numString(v))
	}
	return q, nil
}

func (vt ValueType) checkBounds(v *big.Rat, where string) error {
	if vt.Min != nil && v.Cmp(vt.Min) < 0 {
		return fmt.Errorf("%s: %s below min %s (%s)", where, numString(v), numString(vt.Min), vt.Name)
	}
	if vt.Max != nil && v.Cmp(vt.Max) > 0 {
		return fmt.Errorf("%s: %s above max %s (%s)", where, numString(v), numString(vt.Max), vt.Name)
	}
	return nil
}

func valueTypeFrom(name string, spec *OMap) (ValueType, error) {
	vt := ValueType{Name: name, Scale: 2}
	if spec == nil {
		return vt, nil
	}
	kind := "decimal"
	if k, ok := spec.Get("kind"); ok && k != nil {
		kind = fmt.Sprint(k)
	}
	switch kind {
	case "integer":
		vt.Integer, vt.Scale = true, 0
	case "decimal":
		if s, ok := spec.Get("scale"); ok && s != nil {
			n, err := toNum(s)
			if err != nil {
				return vt, fmt.Errorf("value type %s: scale: %w", name, err)
			}
			vt.Scale = int(truncInt(n).Int64())
		}
	default:
		return vt, fmt.Errorf("value type %s: kind must be integer or decimal", name)
	}
	if c, ok := spec.Get("conserved"); ok {
		vt.Conserved = truthy(c)
	}
	for _, bound := range []string{"min", "max"} {
		if b, ok := spec.Get(bound); ok && b != nil {
			n, err := toNum(b)
			if err != nil {
				return vt, fmt.Errorf("value type %s: %s: %w", name, bound, err)
			}
			if bound == "min" {
				vt.Min = n
			} else {
				vt.Max = n
			}
		}
	}
	return vt, nil
}

var durationRe = regexp.MustCompile(`^\s*(-?\d+(?:\.\d+)?)\s*(ms|s|m|min|h|d)?\s*$`)

var unitSeconds = map[string]*big.Rat{
	"": ratOne, "s": ratOne, "ms": big.NewRat(1, 1000), "m": ratInt(60), "min": ratInt(60),
	"h": ratInt(3600), "d": ratInt(86400),
}

// parseDuration returns integer model seconds: "15m" -> 900, 10 -> 10.
func parseDuration(v any) (int64, error) {
	switch x := v.(type) {
	case bool:
		return 0, fmt.Errorf("duration cannot be bool")
	case *big.Rat:
		return truncInt(x).Int64(), nil
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case float64:
		r, _ := toNum(x)
		return truncInt(r).Int64(), nil
	case string:
		m := durationRe.FindStringSubmatch(x)
		if m == nil {
			return 0, fmt.Errorf("bad duration %q", x)
		}
		n, _ := parseNum(m[1])
		return truncInt(new(big.Rat).Mul(n, unitSeconds[m[2]])).Int64(), nil
	}
	return 0, fmt.Errorf("bad duration %v", show(v))
}

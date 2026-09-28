package sim

import (
	"fmt"
	"math/big"
	"strings"
	"sync"
	"unicode"
)

// The rule language is a subset of Python expression syntax (spec §8).

type ExprError struct{ msg string }

func (e *ExprError) Error() string { return e.msg }

func exprErr(format string, a ...any) error { return &ExprError{fmt.Sprintf(format, a...)} }

// ---- tokens ------------------------------------------------------------

type tokKind int

const (
	tEOF tokKind = iota
	tNum
	tStr
	tName
	tOp
)

type token struct {
	kind tokKind
	text string
}

func tokenize(src string) ([]token, error) {
	var out []token
	rs := []rune(src)
	for i := 0; i < len(rs); {
		c := rs[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case unicode.IsDigit(c) || (c == '.' && i+1 < len(rs) && unicode.IsDigit(rs[i+1])):
			j := i
			for j < len(rs) && (unicode.IsDigit(rs[j]) || rs[j] == '.' || rs[j] == '_') {
				j++
			}
			if j < len(rs) && (rs[j] == 'e' || rs[j] == 'E') {
				k := j + 1
				if k < len(rs) && (rs[k] == '+' || rs[k] == '-') {
					k++
				}
				if k < len(rs) && unicode.IsDigit(rs[k]) {
					for k < len(rs) && unicode.IsDigit(rs[k]) {
						k++
					}
					j = k
				}
			}
			out = append(out, token{tNum, strings.ReplaceAll(string(rs[i:j]), "_", "")})
			i = j
		case c == '\'' || c == '"':
			var b strings.Builder
			j := i + 1
			for ; j < len(rs) && rs[j] != c; j++ {
				if rs[j] == '\\' && j+1 < len(rs) {
					j++
					switch rs[j] {
					case 'n':
						b.WriteRune('\n')
					case 't':
						b.WriteRune('\t')
					default:
						b.WriteRune(rs[j])
					}
					continue
				}
				b.WriteRune(rs[j])
			}
			if j >= len(rs) {
				return nil, exprErr("syntax error in %q: unterminated string", src)
			}
			out = append(out, token{tStr, b.String()})
			i = j + 1
		case unicode.IsLetter(c) || c == '_':
			j := i
			for j < len(rs) && (unicode.IsLetter(rs[j]) || unicode.IsDigit(rs[j]) || rs[j] == '_') {
				j++
			}
			out = append(out, token{tName, string(rs[i:j])})
			i = j
		default:
			two := ""
			if i+1 < len(rs) {
				two = string(rs[i : i+2])
			}
			switch two {
			case "==", "!=", "<=", ">=", "//", "**":
				out = append(out, token{tOp, two})
				i += 2
				continue
			}
			if strings.ContainsRune("+-*/%<>()[]{},:.=", c) {
				out = append(out, token{tOp, string(c)})
				i++
				continue
			}
			return nil, exprErr("syntax error in %q: unexpected %q", src, string(c))
		}
	}
	return append(out, token{tEOF, ""}), nil
}

// ---- AST ---------------------------------------------------------------

type node interface{}

type (
	nConst struct{ v any }
	nName  struct{ id string }
	nAttr  struct {
		obj  node
		attr string
	}
	nIndex struct{ obj, key node }
	nCall  struct {
		fn     node
		args   []node
		kwKeys []string
		kwVals []node
	}
	nBin struct {
		op   string
		l, r node
	}
	nUnary struct {
		op string
		x  node
	}
	nBool struct {
		op    string
		items []node
	}
	nCmp struct {
		first node
		ops   []string
		rest  []node
	}
	nIf   struct{ test, body, orelse node }
	nList struct{ items []node }
	nDict struct{ keys, vals []node }
)

type parser struct {
	toks []token
	pos  int
	src  string
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) next() token { t := p.toks[p.pos]; p.pos++; return t }

func (p *parser) isOp(s string) bool   { t := p.peek(); return t.kind == tOp && t.text == s }
func (p *parser) isName(s string) bool { t := p.peek(); return t.kind == tName && t.text == s }

func (p *parser) expect(s string) error {
	if !p.isOp(s) {
		return exprErr("syntax error in %q: expected %q", p.src, s)
	}
	p.pos++
	return nil
}

var keywords = map[string]bool{"and": true, "or": true, "not": true, "in": true, "is": true, "if": true,
	"else": true, "lambda": true, "for": true, "import": true, "True": true, "False": true, "None": true}

func (p *parser) expr() (node, error) {
	body, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.isName("if") {
		p.pos++
		test, err := p.or()
		if err != nil {
			return nil, err
		}
		if !p.isName("else") {
			return nil, exprErr("syntax error in %q: expected else", p.src)
		}
		p.pos++
		orelse, err := p.expr()
		if err != nil {
			return nil, err
		}
		return nIf{test, body, orelse}, nil
	}
	return body, nil
}

func (p *parser) boolChain(op string, sub func() (node, error)) (node, error) {
	first, err := sub()
	if err != nil {
		return nil, err
	}
	items := []node{first}
	for p.isName(op) {
		p.pos++
		n, err := sub()
		if err != nil {
			return nil, err
		}
		items = append(items, n)
	}
	if len(items) == 1 {
		return first, nil
	}
	return nBool{op, items}, nil
}

func (p *parser) or() (node, error)  { return p.boolChain("or", p.and) }
func (p *parser) and() (node, error) { return p.boolChain("and", p.not) }

func (p *parser) not() (node, error) {
	if p.isName("not") {
		p.pos++
		x, err := p.not()
		if err != nil {
			return nil, err
		}
		return nUnary{"not", x}, nil
	}
	return p.comparison()
}

func (p *parser) comparison() (node, error) {
	first, err := p.arith()
	if err != nil {
		return nil, err
	}
	var ops []string
	var rest []node
	for {
		t := p.peek()
		op := ""
		switch {
		case t.kind == tOp && (t.text == "==" || t.text == "!=" || t.text == "<" || t.text == "<=" || t.text == ">" || t.text == ">="):
			op = t.text
			p.pos++
		case p.isName("in"):
			op = "in"
			p.pos++
		case p.isName("not") && p.toks[p.pos+1].kind == tName && p.toks[p.pos+1].text == "in":
			op = "not in"
			p.pos += 2
		case p.isName("is"):
			p.pos++
			op = "is"
			if p.isName("not") {
				p.pos++
				op = "is not"
			}
		}
		if op == "" {
			break
		}
		r, err := p.arith()
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
		rest = append(rest, r)
	}
	if len(ops) == 0 {
		return first, nil
	}
	return nCmp{first, ops, rest}, nil
}

func (p *parser) arith() (node, error) {
	l, err := p.term()
	if err != nil {
		return nil, err
	}
	for p.isOp("+") || p.isOp("-") {
		op := p.next().text
		r, err := p.term()
		if err != nil {
			return nil, err
		}
		l = nBin{op, l, r}
	}
	return l, nil
}

func (p *parser) term() (node, error) {
	l, err := p.factor()
	if err != nil {
		return nil, err
	}
	for p.isOp("*") || p.isOp("/") || p.isOp("//") || p.isOp("%") {
		op := p.next().text
		r, err := p.factor()
		if err != nil {
			return nil, err
		}
		l = nBin{op, l, r}
	}
	return l, nil
}

func (p *parser) factor() (node, error) {
	if p.isOp("-") || p.isOp("+") {
		op := p.next().text
		x, err := p.factor()
		if err != nil {
			return nil, err
		}
		return nUnary{op, x}, nil
	}
	if p.isOp("**") {
		return nil, exprErr("not allowed in %q: Pow", p.src)
	}
	return p.primary()
}

func (p *parser) primary() (node, error) {
	n, err := p.atom()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.isOp("."):
			p.pos++
			t := p.next()
			if t.kind != tName {
				return nil, exprErr("syntax error in %q: attribute name expected", p.src)
			}
			n = nAttr{n, t.text}
		case p.isOp("("):
			p.pos++
			call := nCall{fn: n}
			for !p.isOp(")") {
				if p.peek().kind == tName && p.toks[p.pos+1].kind == tOp && p.toks[p.pos+1].text == "=" {
					call.kwKeys = append(call.kwKeys, p.next().text)
					p.pos++
					v, err := p.expr()
					if err != nil {
						return nil, err
					}
					call.kwVals = append(call.kwVals, v)
				} else {
					a, err := p.expr()
					if err != nil {
						return nil, err
					}
					call.args = append(call.args, a)
				}
				if !p.isOp(",") {
					break
				}
				p.pos++
			}
			if err := p.expect(")"); err != nil {
				return nil, err
			}
			n = call
		case p.isOp("["):
			p.pos++
			k, err := p.expr()
			if err != nil {
				return nil, err
			}
			if err := p.expect("]"); err != nil {
				return nil, err
			}
			n = nIndex{n, k}
		default:
			return n, nil
		}
	}
}

func (p *parser) atom() (node, error) {
	t := p.next()
	switch t.kind {
	case tNum:
		r, ok := parseNum(t.text)
		if !ok {
			return nil, exprErr("syntax error in %q: bad number %q", p.src, t.text)
		}
		return nConst{r}, nil
	case tStr:
		s := t.text
		for p.peek().kind == tStr { // implicit concatenation
			s += p.next().text
		}
		return nConst{s}, nil
	case tName:
		switch t.text {
		case "True":
			return nConst{true}, nil
		case "False":
			return nConst{false}, nil
		case "None":
			return nConst{nil}, nil
		case "lambda", "for", "import":
			return nil, exprErr("not allowed in %q: %s", p.src, t.text)
		}
		if keywords[t.text] {
			return nil, exprErr("syntax error in %q: unexpected %q", p.src, t.text)
		}
		return nName{t.text}, nil
	case tOp:
		switch t.text {
		case "(":
			if p.isOp(")") {
				p.pos++
				return nList{}, nil
			}
			first, err := p.expr()
			if err != nil {
				return nil, err
			}
			if p.isOp(",") {
				items := []node{first}
				for p.isOp(",") {
					p.pos++
					if p.isOp(")") {
						break
					}
					e, err := p.expr()
					if err != nil {
						return nil, err
					}
					items = append(items, e)
				}
				if err := p.expect(")"); err != nil {
					return nil, err
				}
				return nList{items}, nil
			}
			if err := p.expect(")"); err != nil {
				return nil, err
			}
			return first, nil
		case "[":
			var items []node
			for !p.isOp("]") {
				e, err := p.expr()
				if err != nil {
					return nil, err
				}
				items = append(items, e)
				if !p.isOp(",") {
					break
				}
				p.pos++
			}
			if err := p.expect("]"); err != nil {
				return nil, err
			}
			return nList{items}, nil
		case "{":
			d := nDict{}
			for !p.isOp("}") {
				k, err := p.expr()
				if err != nil {
					return nil, err
				}
				if err := p.expect(":"); err != nil {
					return nil, err
				}
				v, err := p.expr()
				if err != nil {
					return nil, err
				}
				d.keys, d.vals = append(d.keys, k), append(d.vals, v)
				if !p.isOp(",") {
					break
				}
				p.pos++
			}
			if err := p.expect("}"); err != nil {
				return nil, err
			}
			return d, nil
		}
	}
	return nil, exprErr("syntax error in %q", p.src)
}

var exprCache sync.Map

func compileExpr(src string) (node, error) {
	src = strings.TrimSpace(src)
	if n, ok := exprCache.Load(src); ok {
		return n, nil
	}
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, src: src}
	n, err := p.expr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tEOF {
		return nil, exprErr("syntax error in %q: unexpected %q", src, p.peek().text)
	}
	exprCache.Store(src, n)
	return n, nil
}

// ---- evaluation --------------------------------------------------------

// Func is a function callable from expressions.
type Func func(args []any, kwargs *OMap) (any, error)

type Env struct {
	names map[string]any
	funcs map[string]Func
	src   string
}

// evaluate evaluates an expression string. Non-strings are returned as-is (numbers stay exact).
func evaluate(src any, names map[string]any, funcs map[string]Func) (any, error) {
	s, ok := src.(string)
	if !ok {
		return src, nil
	}
	n, err := compileExpr(s)
	if err != nil {
		return nil, err
	}
	return (&Env{names, funcs, strings.TrimSpace(s)}).eval(n)
}

// valueOf: strings starting with "=" are expressions; maps and lists are resolved recursively.
func valueOf(spec any, names map[string]any, funcs map[string]Func) (any, error) {
	switch x := spec.(type) {
	case string:
		if strings.HasPrefix(x, "=") {
			return evaluate(x[1:], names, funcs)
		}
		return x, nil
	case *OMap:
		o := NewOMap()
		for _, k := range x.keys {
			v, err := valueOf(x.m[k], names, funcs)
			if err != nil {
				return nil, err
			}
			o.Set(k, v)
		}
		return o, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			v, err := valueOf(e, names, funcs)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	}
	return spec, nil
}

var allowedMethods = map[string]bool{"acc": true, "sum_accounts": true, "has": true, "get": true, "children": true,
	"parents": true, "parent": true, "keys": true, "values": true, "items": true, "lower": true, "upper": true,
	"startswith": true, "endswith": true, "count": true}

func (e *Env) eval(n node) (any, error) {
	switch x := n.(type) {
	case nConst:
		return x.v, nil
	case nName:
		if v, ok := e.names[x.id]; ok {
			return v, nil
		}
		return nil, exprErr("unknown name %q in %q", x.id, e.src)
	case nAttr:
		if strings.HasPrefix(x.attr, "_") {
			return nil, exprErr("private attribute %q not allowed", x.attr)
		}
		obj, err := e.eval(x.obj)
		if err != nil {
			return nil, err
		}
		return e.attr(obj, x.attr)
	case nIndex:
		obj, err := e.eval(x.obj)
		if err != nil {
			return nil, err
		}
		key, err := e.eval(x.key)
		if err != nil {
			return nil, err
		}
		return e.index(obj, key)
	case nCall:
		return e.call(x)
	case nBin:
		l, err := e.eval(x.l)
		if err != nil {
			return nil, err
		}
		r, err := e.eval(x.r)
		if err != nil {
			return nil, err
		}
		return e.binop(x.op, l, r)
	case nUnary:
		v, err := e.eval(x.x)
		if err != nil {
			return nil, err
		}
		switch x.op {
		case "not":
			return !truthy(v), nil
		case "-", "+":
			r, ok := v.(*big.Rat)
			if !ok {
				return nil, exprErr("bad operand type for unary %s: %s in %q", x.op, show(v), e.src)
			}
			if x.op == "-" {
				return new(big.Rat).Neg(r), nil
			}
			return r, nil
		}
	case nBool:
		var v any
		for _, it := range x.items {
			var err error
			v, err = e.eval(it)
			if err != nil {
				return nil, err
			}
			if (x.op == "and") != truthy(v) {
				return v, nil
			}
		}
		return v, nil
	case nCmp:
		left, err := e.eval(x.first)
		if err != nil {
			return nil, err
		}
		for i, op := range x.ops {
			right, err := e.eval(x.rest[i])
			if err != nil {
				return nil, err
			}
			ok, err := e.compare(op, left, right)
			if err != nil {
				return nil, err
			}
			if !ok {
				return false, nil
			}
			left = right
		}
		return true, nil
	case nIf:
		t, err := e.eval(x.test)
		if err != nil {
			return nil, err
		}
		if truthy(t) {
			return e.eval(x.body)
		}
		return e.eval(x.orelse)
	case nList:
		out := make([]any, len(x.items))
		for i, it := range x.items {
			v, err := e.eval(it)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case nDict:
		o := NewOMap()
		for i := range x.keys {
			k, err := e.eval(x.keys[i])
			if err != nil {
				return nil, err
			}
			v, err := e.eval(x.vals[i])
			if err != nil {
				return nil, err
			}
			o.Set(show(k), v)
		}
		return o, nil
	}
	return nil, exprErr("not allowed in %q", e.src)
}

func (e *Env) attr(obj any, name string) (any, error) {
	switch o := obj.(type) {
	case *OMap:
		if v, ok := o.Get(name); ok {
			return v, nil
		}
		return nil, exprErr("no field %q in %q", name, e.src)
	case *ActorView:
		return o.attr(name)
	case *Namespace:
		return o.attr(name)
	}
	return nil, exprErr("no attribute %q in %q", name, e.src)
}

func (e *Env) index(obj, key any) (any, error) {
	switch o := obj.(type) {
	case []any:
		k, ok := key.(*big.Rat)
		if !ok {
			return nil, exprErr("bad index %s in %q", show(key), e.src)
		}
		i := int(truncInt(k).Int64())
		if i < 0 {
			i += len(o)
		}
		if i < 0 || i >= len(o) {
			return nil, exprErr("bad index %s in %q", show(key), e.src)
		}
		return o[i], nil
	case *OMap:
		if v, ok := o.Get(show(key)); ok {
			return v, nil
		}
	case *Namespace:
		if v, ok := o.data.Get(show(key)); ok {
			return v, nil
		}
	case string:
		if k, ok := key.(*big.Rat); ok {
			rs := []rune(o)
			i := int(truncInt(k).Int64())
			if i < 0 {
				i += len(rs)
			}
			if i >= 0 && i < len(rs) {
				return string(rs[i]), nil
			}
		}
	}
	return nil, exprErr("bad index %s in %q", show(key), e.src)
}

func (e *Env) call(c nCall) (any, error) {
	args := make([]any, len(c.args))
	for i, a := range c.args {
		v, err := e.eval(a)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	kw := NewOMap()
	for i, k := range c.kwKeys {
		v, err := e.eval(c.kwVals[i])
		if err != nil {
			return nil, err
		}
		kw.Set(k, v)
	}
	switch f := c.fn.(type) {
	case nName:
		fn, ok := e.funcs[f.id]
		if !ok {
			return nil, exprErr("unknown function %q in %q", f.id, e.src)
		}
		return fn(args, kw)
	case nAttr:
		if !allowedMethods[f.attr] {
			return nil, exprErr("call not allowed in %q", e.src)
		}
		obj, err := e.eval(f.obj)
		if err != nil {
			return nil, err
		}
		return e.method(obj, f.attr, args, kw)
	}
	return nil, exprErr("call not allowed in %q", e.src)
}

func argOr(args []any, kw *OMap, i int, name string, def any) any {
	if i < len(args) {
		return args[i]
	}
	if v, ok := kw.Get(name); ok {
		return v
	}
	return def
}

func (e *Env) method(obj any, name string, args []any, kw *OMap) (any, error) {
	switch o := obj.(type) {
	case *ActorView:
		return o.method(name, args, kw)
	case *Namespace:
		if name == "get" {
			if v, ok := o.data.Get(show(argOr(args, kw, 0, "key", nil))); ok {
				return v, nil
			}
			return argOr(args, kw, 1, "default", nil), nil
		}
	case *OMap:
		switch name {
		case "get":
			if v, ok := o.Get(show(argOr(args, kw, 0, "key", nil))); ok {
				return v, nil
			}
			return argOr(args, kw, 1, "default", nil), nil
		case "keys", "values", "items":
			out := make([]any, 0, o.Len())
			for _, k := range o.keys {
				switch name {
				case "keys":
					out = append(out, k)
				case "values":
					out = append(out, o.m[k])
				default:
					out = append(out, []any{k, o.m[k]})
				}
			}
			return out, nil
		}
	case string:
		switch name {
		case "lower":
			return strings.ToLower(o), nil
		case "upper":
			return strings.ToUpper(o), nil
		case "startswith", "endswith", "count":
			sub, ok := argOr(args, kw, 0, "sub", nil).(string)
			if !ok {
				return nil, exprErr("%s expects a string in %q", name, e.src)
			}
			switch name {
			case "startswith":
				return strings.HasPrefix(o, sub), nil
			case "endswith":
				return strings.HasSuffix(o, sub), nil
			}
			return ratInt(int64(strings.Count(o, sub))), nil
		}
	case []any:
		if name == "count" {
			n := 0
			for _, x := range o {
				if equal(x, argOr(args, kw, 0, "x", nil)) {
					n++
				}
			}
			return ratInt(int64(n)), nil
		}
	}
	return nil, exprErr("no method %q on %s in %q", name, show(obj), e.src)
}

func (e *Env) binop(op string, l, r any) (any, error) {
	ln, lok := l.(*big.Rat)
	rn, rok := r.(*big.Rat)
	if lok && rok {
		switch op {
		case "+":
			return new(big.Rat).Add(ln, rn), nil
		case "-":
			return new(big.Rat).Sub(ln, rn), nil
		case "*":
			return new(big.Rat).Mul(ln, rn), nil
		case "/":
			if rn.Sign() == 0 {
				return nil, &arithError{"division by zero in " + e.src}
			}
			return new(big.Rat).Quo(ln, rn), nil
		case "//", "%":
			if rn.Sign() == 0 {
				return nil, &arithError{"division by zero in " + e.src}
			}
			// Python Decimal: // truncates towards zero, % takes the sign of the dividend.
			q := new(big.Rat).SetInt(truncInt(new(big.Rat).Quo(ln, rn)))
			if op == "//" {
				return q, nil
			}
			return new(big.Rat).Sub(ln, new(big.Rat).Mul(q, rn)), nil
		}
	}
	if op == "+" {
		if ls, ok := l.(string); ok {
			if rs, ok := r.(string); ok {
				return ls + rs, nil
			}
		}
		if ll, ok := l.([]any); ok {
			if rl, ok := r.([]any); ok {
				return append(append([]any{}, ll...), rl...), nil
			}
		}
	}
	return nil, exprErr("unsupported operand types for %s: %s and %s in %q", op, show(l), show(r), e.src)
}

type arithError struct{ msg string }

func (e *arithError) Error() string { return e.msg }

func (e *Env) compare(op string, l, r any) (bool, error) {
	switch op {
	case "==":
		return equal(l, r), nil
	case "!=":
		return !equal(l, r), nil
	case "is":
		return l == nil && r == nil || l == r, nil
	case "is not":
		return !(l == nil && r == nil || l == r), nil
	case "in", "not in":
		in, err := e.contains(r, l)
		if err != nil {
			return false, err
		}
		return in == (op == "in"), nil
	}
	if ln, ok := l.(*big.Rat); ok {
		if rn, ok := r.(*big.Rat); ok {
			c := ln.Cmp(rn)
			return map[string]bool{"<": c < 0, "<=": c <= 0, ">": c > 0, ">=": c >= 0}[op], nil
		}
	}
	if ls, ok := l.(string); ok {
		if rs, ok := r.(string); ok {
			c := strings.Compare(ls, rs)
			return map[string]bool{"<": c < 0, "<=": c <= 0, ">": c > 0, ">=": c >= 0}[op], nil
		}
	}
	return false, exprErr("'%s' not supported between %s and %s in %q", op, show(l), show(r), e.src)
}

func (e *Env) contains(container, item any) (bool, error) {
	switch c := container.(type) {
	case string:
		s, ok := item.(string)
		if !ok {
			return false, exprErr("'in <string>' requires string as left operand in %q", e.src)
		}
		return strings.Contains(c, s), nil
	case []any:
		for _, x := range c {
			if equal(x, item) {
				return true, nil
			}
		}
		return false, nil
	case *OMap:
		return c.Has(show(item)), nil
	case *Namespace:
		return c.data.Has(show(item)), nil
	}
	return false, exprErr("argument of type %s is not iterable in %q", show(container), e.src)
}

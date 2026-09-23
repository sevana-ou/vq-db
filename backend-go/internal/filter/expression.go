// Package filter is the Go port of the Python vq_db/filter package: the
// dashboard filter mini-language (tokenizer, Pratt parser, AST) with two
// emitters — a parameterized SQL WHERE fragment and an in-memory predicate —
// plus the SearchFilter transport and the SQL name-map / sort whitelist.
//
// Behavior matches the Python reference exactly, including the deliberate
// departures from the original C++ Calc engine (parameterized SQL, "=="→"=",
// "!="→"<>", uniform numeric coercion in eval). See FILTER.md.
package filter

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ExpressionError is raised on any lex/parse/emit/eval error in a filter
// expression. All filter errors are of this type, so "any error excludes"
// callers can treat a non-nil error uniformly.
type ExpressionError struct{ Msg string }

func (e *ExpressionError) Error() string { return e.Msg }

func exprErr(format string, args ...any) error {
	return &ExpressionError{Msg: fmt.Sprintf(format, args...)}
}

// --------------------------------------------------------------------------- //
// Tokenizer
// --------------------------------------------------------------------------- //

// Token is one lexed token. Value is: float64/int64 for "number", string for
// "string"/"ident"/"op", "("/")" for parens.
type Token struct {
	Kind  string // "number" | "string" | "ident" | "op" | "lparen" | "rparen"
	Value any
}

var twoCharOps = map[string]bool{"<=": true, ">=": true, "==": true, "!=": true, "&&": true, "||": true}
var oneCharOps = map[byte]bool{'+': true, '-': true, '*': true, '/': true, '<': true, '>': true}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentBody(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '.' || c == '_'
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// Tokenize lexes text into a token list, following the C++/Python lexer rules.
func Tokenize(text string) ([]Token, error) {
	var tokens []Token
	i, n := 0, len(text)
	for i < n {
		c := text[i]

		if isSpaceByte(c) {
			i++
			continue
		}
		if c == '(' {
			tokens = append(tokens, Token{"lparen", "("})
			i++
			continue
		}
		if c == ')' {
			tokens = append(tokens, Token{"rparen", ")"})
			i++
			continue
		}
		if c == '"' {
			j := i + 1
			for j < n && text[j] != '"' {
				j++
			}
			if j >= n {
				return nil, exprErr("unterminated string literal")
			}
			tokens = append(tokens, Token{"string", text[i+1 : j]})
			i = j + 1
			continue
		}
		if isDigit(c) {
			ni, tok, err := scanNumber(text, i)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, tok)
			i = ni
			continue
		}
		if isIdentStart(c) {
			j := i + 1
			for j < n && isIdentBody(text[j]) {
				j++
			}
			tokens = append(tokens, Token{"ident", text[i:j]})
			i = j
			continue
		}
		if i+2 <= n && twoCharOps[text[i:i+2]] {
			tokens = append(tokens, Token{"op", text[i : i+2]})
			i += 2
			continue
		}
		if oneCharOps[c] {
			tokens = append(tokens, Token{"op", string(c)})
			i++
			continue
		}
		return nil, exprErr("unexpected character %q at position %d", string(c), i)
	}
	return tokens, nil
}

func scanNumber(text string, i int) (int, Token, error) {
	n := len(text)

	// Hex: 0x....
	if text[i] == '0' && i+1 < n && (text[i+1] == 'x' || text[i+1] == 'X') {
		j := i + 2
		for j < n && isHexDigit(text[j]) {
			j++
		}
		if j == i+2 {
			return 0, Token{}, exprErr("malformed hex literal")
		}
		v, err := strconv.ParseInt(text[i+2:j], 16, 64)
		if err != nil {
			return 0, Token{}, exprErr("malformed hex literal")
		}
		return j, Token{"number", v}, nil
	}

	j := i
	for j < n && (isDigit(text[j]) || text[j] == '.') {
		j++
	}
	raw := text[i:j]

	if strings.Contains(raw, ".") {
		if f, err := pyFloatParse(raw); err == nil {
			return j, Token{"number", f}, nil
		}
		if isPyIPv4(raw) {
			return j, Token{"string", raw}, nil // IP literal -> string token
		}
		// Neither float nor IP: take the leading integer (C++ atoll).
		lead := raw
		if idx := strings.IndexByte(raw, '.'); idx >= 0 {
			lead = raw[:idx]
		}
		if lead == "" {
			return j, Token{"number", int64(0)}, nil
		}
		return j, Token{"number", parseLeadingInt(lead)}, nil
	}

	return j, Token{"number", parseLeadingInt(raw)}, nil
}

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// parseLeadingInt parses an all-digit string as int64, matching Python int().
// On overflow it saturates (realistic filter inputs never overflow).
func parseLeadingInt(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		if strings.HasPrefix(s, "-") {
			return math.MinInt64
		}
		return math.MaxInt64
	}
	return v
}

// isPyIPv4 reports whether s parses as an IPv4 address under Python's
// ipaddress.IPv4Address rules: exactly 4 dotted octets, each 1-3 digits, no
// leading zeros (except a bare "0"), value 0-255.
func isPyIPv4(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if len(p) == 0 || len(p) > 3 {
			return false
		}
		for k := 0; k < len(p); k++ {
			if !isDigit(p[k]) {
				return false
			}
		}
		if len(p) > 1 && p[0] == '0' {
			return false
		}
		v, _ := strconv.Atoi(p)
		if v > 255 {
			return false
		}
	}
	return true
}

// pyFloatParse mirrors Python float(str): trims surrounding whitespace and
// accepts the same numeric forms strconv.ParseFloat does (inf/nan included).
func pyFloatParse(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

// --------------------------------------------------------------------------- //
// AST
// --------------------------------------------------------------------------- //

// SQLFragment is a parameterized SQL fragment: Text with ordered Params.
type SQLFragment struct {
	Text   string
	Params []any
}

// Node is an AST node exposing the two emitters.
type Node interface {
	ToSQL(nameMap map[string]string, dialect string) (SQLFragment, error)
	Evaluate(values map[string]any) (any, error)
}

// Number is a numeric literal (Value is int64 or float64).
type Number struct{ Value any }

// String is a string literal.
type String struct{ Value string }

// Var is a variable/identifier reference.
type Var struct{ Name string }

// BinOp is a binary operation.
type BinOp struct {
	Op    string
	Left  Node
	Right Node
}

var sqlOp = map[string]string{
	"==": "=", "!=": "<>", "&&": "AND", "||": "OR",
	"<": "<", "<=": "<=", ">": ">", ">=": ">=",
	"+": "+", "-": "-", "*": "*", "/": "/",
}

func (n Number) ToSQL(_ map[string]string, _ string) (SQLFragment, error) {
	// Numeric literals are safe to inline; format like Python repr().
	return SQLFragment{Text: pyReprNumber(n.Value)}, nil
}

func (n Number) Evaluate(_ map[string]any) (any, error) { return n.Value, nil }

func (s String) ToSQL(_ map[string]string, dialect string) (SQLFragment, error) {
	m, err := marker(dialect)
	if err != nil {
		return SQLFragment{}, err
	}
	return SQLFragment{Text: m, Params: []any{s.Value}}, nil
}

func (s String) Evaluate(_ map[string]any) (any, error) { return s.Value, nil }

func (v Var) ToSQL(nameMap map[string]string, _ string) (SQLFragment, error) {
	column, ok := nameMap[v.Name]
	if !ok {
		return SQLFragment{}, exprErr("unknown filter field %q", v.Name)
	}
	return SQLFragment{Text: column}, nil
}

func (v Var) Evaluate(values map[string]any) (any, error) {
	val, ok := values[v.Name]
	if !ok {
		return nil, exprErr("variable %q not provided", v.Name)
	}
	return val, nil
}

func (b BinOp) ToSQL(nameMap map[string]string, dialect string) (SQLFragment, error) {
	left, err := b.Left.ToSQL(nameMap, dialect)
	if err != nil {
		return SQLFragment{}, err
	}
	right, err := b.Right.ToSQL(nameMap, dialect)
	if err != nil {
		return SQLFragment{}, err
	}
	params := make([]any, 0, len(left.Params)+len(right.Params))
	params = append(params, left.Params...)
	params = append(params, right.Params...)
	text := "(" + left.Text + " " + sqlOp[b.Op] + " " + right.Text + ")"
	if dialect == "numeric" {
		text = renumber(text)
	}
	return SQLFragment{Text: text, Params: params}, nil
}

func (b BinOp) Evaluate(values map[string]any) (any, error) {
	switch b.Op {
	case "&&":
		l, err := b.Left.Evaluate(values)
		if err != nil {
			return nil, err
		}
		lb, err := asBool(l)
		if err != nil {
			return nil, err
		}
		if !lb {
			return false, nil
		}
		r, err := b.Right.Evaluate(values)
		if err != nil {
			return nil, err
		}
		return asBool(r)
	case "||":
		l, err := b.Left.Evaluate(values)
		if err != nil {
			return nil, err
		}
		lb, err := asBool(l)
		if err != nil {
			return nil, err
		}
		if lb {
			return true, nil
		}
		r, err := b.Right.Evaluate(values)
		if err != nil {
			return nil, err
		}
		return asBool(r)
	}

	left, err := b.Left.Evaluate(values)
	if err != nil {
		return nil, err
	}
	right, err := b.Right.Evaluate(values)
	if err != nil {
		return nil, err
	}
	switch b.Op {
	case "+", "-", "*", "/":
		return arith(b.Op, left, right)
	default:
		return compare(b.Op, left, right)
	}
}

// --------------------------------------------------------------------------- //
// Parser (precedence-climbing / Pratt)
// --------------------------------------------------------------------------- //

var bindingPower = map[string]int{
	"||": 1, "&&": 2,
	"==": 3, "!=": 3,
	"<": 4, "<=": 4, ">": 4, ">=": 4,
	"+": 5, "-": 5,
	"*": 6, "/": 6,
}

type parser struct {
	tokens []Token
	pos    int
}

func (p *parser) peek() *Token {
	if p.pos < len(p.tokens) {
		return &p.tokens[p.pos]
	}
	return nil
}

func (p *parser) next() Token {
	t := p.tokens[p.pos]
	p.pos++
	return t
}

func (p *parser) parse() (Node, error) {
	node, err := p.parseExpression(0)
	if err != nil {
		return nil, err
	}
	if p.peek() != nil {
		return nil, exprErr("unexpected trailing token")
	}
	return node, nil
}

func (p *parser) parseExpression(minBP int) (Node, error) {
	left, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if tok == nil || tok.Kind != "op" {
			break
		}
		bp, ok := bindingPower[tok.Value.(string)]
		if !ok || bp <= minBP {
			break
		}
		op := tok.Value.(string)
		p.next()
		right, err := p.parseExpression(bp) // left-assoc: recurse with op's bp
		if err != nil {
			return nil, err
		}
		left = BinOp{Op: op, Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseAtom() (Node, error) {
	tok := p.peek()
	if tok == nil {
		return nil, exprErr("unexpected end of expression")
	}
	switch tok.Kind {
	case "number":
		p.next()
		return Number{Value: tok.Value}, nil
	case "string":
		p.next()
		return String{Value: tok.Value.(string)}, nil
	case "ident":
		p.next()
		return Var{Name: tok.Value.(string)}, nil
	case "lparen":
		p.next()
		inner, err := p.parseExpression(0)
		if err != nil {
			return nil, err
		}
		closing := p.peek()
		if closing == nil || closing.Kind != "rparen" {
			return nil, exprErr("expected ')'")
		}
		p.next()
		return inner, nil
	}
	return nil, exprErr("unexpected token")
}

// Parse parses a filter expression string into an AST. An empty (or
// whitespace-only) string is a syntax error.
func Parse(text string) (Node, error) {
	tokens, err := Tokenize(text)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, exprErr("empty expression")
	}
	p := &parser{tokens: tokens}
	return p.parse()
}

// --------------------------------------------------------------------------- //
// Evaluation helpers (in-memory predicate)
// --------------------------------------------------------------------------- //

func isNumeric(v any) bool {
	switch v.(type) {
	case int64, float64, bool:
		return true
	}
	return false
}

func isFloatVal(v any) bool {
	_, ok := v.(float64)
	return ok
}

func asBool(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case int64:
		return t != 0, nil
	case float64:
		return t != 0, nil
	case string:
		return len(t) != 0, nil
	}
	return false, exprErr("cannot interpret %v as boolean", v)
}

// numToInt truncates a numeric value toward zero (Python int()).
func numToInt(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case bool:
		if t {
			return 1
		}
		return 0
	}
	return 0
}

// numToFloat converts a numeric value to float64 (Python float()).
func numToFloat(v any) float64 {
	switch t := v.(type) {
	case int64:
		return float64(t)
	case float64:
		return t
	case bool:
		if t {
			return 1
		}
		return 0
	}
	return 0
}

func isNumericZero(v any) bool {
	switch t := v.(type) {
	case int64:
		return t == 0
	case float64:
		return t == 0
	case bool:
		return !t
	}
	return false
}

func arith(op string, left, right any) (any, error) {
	if !isNumeric(left) || !isNumeric(right) {
		return nil, exprErr("arithmetic on non-numeric operands")
	}
	bothInt := !isFloatVal(left) && !isFloatVal(right)
	switch op {
	case "+":
		if bothInt {
			return numToInt(left) + numToInt(right), nil
		}
		return numToFloat(left) + numToFloat(right), nil
	case "-":
		if bothInt {
			return numToInt(left) - numToInt(right), nil
		}
		return numToFloat(left) - numToFloat(right), nil
	case "*":
		if bothInt {
			return numToInt(left) * numToInt(right), nil
		}
		return numToFloat(left) * numToFloat(right), nil
	case "/":
		if isNumericZero(right) {
			return nil, exprErr("division by zero")
		}
		if bothInt {
			// Go int64 division truncates toward zero, matching Python int(li/ri).
			return numToInt(left) / numToInt(right), nil
		}
		return numToFloat(left) / numToFloat(right), nil
	}
	return nil, exprErr("unknown operator %q", op)
}

func compare(op string, left, right any) (bool, error) {
	lf, rf, asFloat, ls, rs := coercePair(left, right)
	if asFloat {
		switch op {
		case "==":
			return lf == rf, nil
		case "!=":
			return lf != rf, nil
		case "<":
			return lf < rf, nil
		case "<=":
			return lf <= rf, nil
		case ">":
			return lf > rf, nil
		case ">=":
			return lf >= rf, nil
		}
		return false, exprErr("unknown operator %q", op)
	}
	switch op {
	case "==":
		return ls == rs, nil
	case "!=":
		return ls != rs, nil
	case "<":
		return ls < rs, nil
	case "<=":
		return ls <= rs, nil
	case ">":
		return ls > rs, nil
	case ">=":
		return ls >= rs, nil
	}
	return false, exprErr("unknown operator %q", op)
}

// coercePair mirrors Python _coerce_pair: two numbers -> both float; two strings
// -> as-is; mixed -> try both to float, else both to string. Returns the float
// pair when asFloat is true, otherwise the string pair.
func coercePair(left, right any) (lf, rf float64, asFloat bool, ls, rs string) {
	leftNum := isNumeric(left)
	rightNum := isNumeric(right)

	if leftNum && rightNum {
		return numToFloat(left), numToFloat(right), true, "", ""
	}
	ls2, lok := left.(string)
	rs2, rok := right.(string)
	if lok && rok {
		return 0, 0, false, ls2, rs2
	}

	// Mixed string/number: try to bring both to numbers; else compare as strings.
	lfv, lErr := toFloatAny(left)
	rfv, rErr := toFloatAny(right)
	if lErr == nil && rErr == nil {
		return lfv, rfv, true, "", ""
	}
	return 0, 0, false, pyStr(left), pyStr(right)
}

// toFloatAny mirrors Python float(x) for numbers and strings.
func toFloatAny(v any) (float64, error) {
	switch t := v.(type) {
	case int64:
		return float64(t), nil
	case float64:
		return t, nil
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	case string:
		return pyFloatParse(t)
	}
	return 0, exprErr("cannot convert to float")
}

// pyStr mirrors Python str(x) for the value types that flow through eval.
func pyStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return pyReprFloat(t)
	case bool:
		if t {
			return "True"
		}
		return "False"
	}
	return fmt.Sprintf("%v", v)
}

// --------------------------------------------------------------------------- //
// Placeholder utilities
// --------------------------------------------------------------------------- //

func marker(dialect string) (string, error) {
	switch dialect {
	case "qmark":
		return "?", nil
	case "format":
		return "%s", nil
	case "numeric":
		return ":1", nil // renumbered later in BinOp.ToSQL
	}
	return "", exprErr("unknown SQL dialect %q", dialect)
}

// renumber rewrites ":1" placeholders left-to-right as ":1", ":2" ... for the
// numeric dialect. This replicates the Python _renumber exactly, including its
// scan-for-":1"-only behavior.
func renumber(text string) string {
	var out strings.Builder
	idx := 0
	i := 0
	for i < len(text) {
		if strings.HasPrefix(text[i:], ":1") {
			idx++
			out.WriteString(":" + strconv.Itoa(idx))
			i += 2
		} else {
			out.WriteByte(text[i])
			i++
		}
	}
	return out.String()
}

// --------------------------------------------------------------------------- //
// Python-repr float/number formatting (for inlined numeric SQL literals)
// --------------------------------------------------------------------------- //

func pyReprNumber(v any) string {
	switch t := v.(type) {
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return pyReprFloat(t)
	}
	return fmt.Sprintf("%v", v)
}

// pyReprFloat reproduces Python's repr(float): shortest round-trip decimal,
// with ".0" on integral values and scientific notation when the decimal point
// position is <= -4 or > 16.
func pyReprFloat(f float64) string {
	if math.IsInf(f, 1) {
		return "inf"
	}
	if math.IsInf(f, -1) {
		return "-inf"
	}
	if math.IsNaN(f) {
		return "nan"
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // e.g. "-3.6e+00"
	neg := false
	if len(s) > 0 && s[0] == '-' {
		neg = true
		s = s[1:]
	}
	ei := strings.IndexByte(s, 'e')
	mant := s[:ei]
	exp, _ := strconv.Atoi(s[ei+1:])
	digits := strings.Replace(mant, ".", "", 1)
	decpt := exp + 1

	var body string
	switch {
	case decpt <= -4 || decpt > 16:
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		body = m + fmtExp(decpt-1)
	case decpt <= 0:
		body = "0." + strings.Repeat("0", -decpt) + digits
	case decpt >= len(digits):
		body = digits + strings.Repeat("0", decpt-len(digits)) + ".0"
	default:
		body = digits[:decpt] + "." + digits[decpt:]
	}
	if neg {
		return "-" + body
	}
	return body
}

func fmtExp(e int) string {
	sign := "+"
	if e < 0 {
		sign = "-"
		e = -e
	}
	es := strconv.Itoa(e)
	if len(es) < 2 {
		es = "0" + es
	}
	return "e" + sign + es
}

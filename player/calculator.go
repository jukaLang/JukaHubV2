package main

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Calculator: a scientific calculator with history and a graph
// ──────────────────────────────────────────────────────────────────────
//
// The line holds what the user typed, in the glyphs it is shown with
// (× ÷ − π √ ² …), and a real expression parser reads it: precedence and
// right-associative powers, unary minus below powers (−2² is −4), implicit
// multiplication (2π, 3(4), 2sin30), factorials, percentages, functions in
// degrees or radians, and brackets the user never closed. Results are shown
// to twelve significant digits, so 0.1+0.2 is 0.3 and sin 180° is 0, and an
// error keeps the line for fixing instead of wiping it. History survives a
// restart and can be walked with the D-pad; a keyboard types sums directly.

const (
	calcCols = 6
	calcRows = 6
	// calcMaxExpr bounds the line, so a pasted megabyte cannot stall the
	// parser on a handheld.
	calcMaxExpr = 512
	// calcMaxDepth bounds the parser's recursion (nested brackets, chains of
	// signs).
	calcMaxDepth       = 200
	calcMaxHistory     = 30
	calcHistoryFile    = "calculator.json"
	calcResultMaxRunes = 64
)

// The kinds of key, which decide how a key treats a result already on show.
const (
	calcKindDigit    = iota // digits, the point and "(": start a new sum after a result
	calcKindConst           // π, e, Ans: start a new sum after a result
	calcKindOperator        // + − × ÷ ^ ² ! %: continue from a result
	calcKindFunc            // sin, ln, √ …: apply to a result
	calcKindInsert          // ")": just typed
	calcKindNegate
	calcKindBack
	calcKindClear
	calcKindEquals
	calcKindShift
	calcKindAngle
)

// calcKey is one key: its face and what it types, and both again under 2nd.
type calcKey struct {
	label, label2   string
	insert, insert2 string
	kind            int
}

func calcDigit(d string) calcKey { return calcKey{label: d, insert: d, kind: calcKindDigit} }

func calcOp(glyph string) calcKey { return calcKey{label: glyph, insert: glyph, kind: calcKindOperator} }

var calcKeys = [calcRows][calcCols]calcKey{
	{
		{label: "2nd", kind: calcKindShift}, {label: "DEG", kind: calcKindAngle},
		{label: "(", insert: "(", kind: calcKindDigit}, {label: ")", insert: ")", kind: calcKindInsert},
		{label: "⌫", kind: calcKindBack}, {label: "C", kind: calcKindClear},
	},
	{
		{label: "sin", label2: "sin⁻¹", insert: "sin(", insert2: "sin⁻¹(", kind: calcKindFunc},
		{label: "x²", label2: "x³", insert: "²", insert2: "³", kind: calcKindOperator},
		calcDigit("7"), calcDigit("8"), calcDigit("9"), calcOp("÷"),
	},
	{
		{label: "cos", label2: "cos⁻¹", insert: "cos(", insert2: "cos⁻¹(", kind: calcKindFunc},
		{label: "√", label2: "³√", insert: "√(", insert2: "cbrt(", kind: calcKindFunc},
		calcDigit("4"), calcDigit("5"), calcDigit("6"), calcOp("×"),
	},
	{
		{label: "tan", label2: "tan⁻¹", insert: "tan(", insert2: "tan⁻¹(", kind: calcKindFunc},
		{label: "xʸ", label2: "ʸ√x", insert: "^", insert2: "^(1÷", kind: calcKindOperator},
		calcDigit("1"), calcDigit("2"), calcDigit("3"), calcOp("−"),
	},
	{
		{label: "ln", label2: "eˣ", insert: "ln(", insert2: "e^(", kind: calcKindFunc},
		{label: "log", label2: "10ˣ", insert: "log(", insert2: "10^(", kind: calcKindFunc},
		calcDigit("0"), calcDigit("."), {label: "±", kind: calcKindNegate}, calcOp("+"),
	},
	{
		{label: "π", insert: "π", kind: calcKindConst}, {label: "e", insert: "e", kind: calcKindConst},
		{label: "n!", insert: "!", kind: calcKindOperator}, {label: "%", insert: "%", kind: calcKindOperator},
		{label: "Ans", insert: "Ans", kind: calcKindConst}, {label: "=", kind: calcKindEquals},
	},
}

// calcEntry is one finished calculation in the history.
type calcEntry struct {
	Expr   string `json:"expr"`
	Result string `json:"result"`
}

type calcState struct {
	// expression is the line being typed, in display glyphs.
	expression string
	// hasResult is set right after "=": the next digit starts a new sum and
	// the next operator continues from the answer.
	hasResult bool
	// result is the last answer, raw (re-readable by the parser), and
	// lastExpr the line it came from.
	result    string
	resultVal float64
	lastExpr  string
	errMsg    string
	// typed: the keyboard typed since the last arrow move, so Enter means "="
	// rather than "press the focused key".
	typed   bool
	shift   bool
	degrees bool
	cursor  [2]int
	// histFocus is the focused history entry (0 = newest), -1 on the keys.
	histFocus  int
	histScroll int
	history    []calcEntry
	loaded     bool

	graphMode       bool
	graphExpression string
	graphDegrees    bool
	graphSamples    []calcGraphSample
	graphCached     bool
}

var calc calcState

// calcGeom is where the last frame drew everything, for the mouse.
type calcGeom struct {
	display  sdl.Rect
	keys     [calcRows][calcCols]sdl.Rect
	panel    sdl.Rect
	graphBtn sdl.Rect
	xBtn     sdl.Rect
	modeBtn  sdl.Rect
	history  []calcHistHit
}

type calcHistHit struct {
	rect  sdl.Rect
	index int // into calc.history
}

var calcLastGeom calcGeom

func initCalc() {
	calc = calcState{cursor: [2]int{1, 2}, histFocus: -1, degrees: true}
}

// ── History and settings on disk ───────────────────────────────────────

type calcSaved struct {
	History []calcEntry `json:"history"`
	Radians bool        `json:"radians,omitempty"`
}

var (
	calcSaveMu  sync.Mutex
	calcSaveGen atomic.Uint64
)

// calcEnsureLoaded reads the history and the angle mode once. The file is the
// user's to edit (or damage), so everything read is checked.
func calcEnsureLoaded() {
	if calc.loaded {
		return
	}
	calc.loaded = true
	var s calcSaved
	if _, err := readJSONWithBackup(osAppsDataPath(calcHistoryFile), &s); err != nil {
		return
	}
	calc.degrees = !s.Radians
	for _, e := range s.History {
		if e.Expr == "" || len(e.Expr) > calcMaxExpr || e.Result == "" || len(e.Result) > calcResultMaxRunes {
			continue
		}
		if _, ok := calcParseResult(e.Result); !ok {
			continue
		}
		calc.history = append(calc.history, e)
	}
	if n := len(calc.history); n > calcMaxHistory {
		calc.history = calc.history[n-calcMaxHistory:]
	}
	if n := len(calc.history); n > 0 {
		calc.result = calc.history[n-1].Result
		calc.resultVal, _ = calcParseResult(calc.result)
		calc.lastExpr = calc.history[n-1].Expr
	}
}

// calcSave writes the history and the angle mode off the render thread; a
// newer state queued behind it makes an older write unnecessary.
func calcSave() {
	if !calc.loaded {
		return
	}
	data, err := json.Marshal(calcSaved{History: append([]calcEntry(nil), calc.history...), Radians: !calc.degrees})
	if err != nil {
		return
	}
	gen := calcSaveGen.Add(1)
	go func() {
		calcSaveMu.Lock()
		defer calcSaveMu.Unlock()
		if calcSaveGen.Load() != gen {
			return
		}
		if err := AtomicWrite(osAppsDataPath(calcHistoryFile), data, 0o644); err != nil {
			logWarn("calculator", "could not save the history: %v", err)
		}
	}()
}

// ── Expression engine ───────────────────────────────────────────────────

// calcError is what the result line says when a sum has no answer.
type calcError string

func (e calcError) Error() string { return string(e) }

const (
	calcErrSyntax   calcError = "Syntax error"
	calcErrDivZero  calcError = "Can't divide by 0"
	calcErrDomain   calcError = "Not defined"
	calcErrOverflow calcError = "Too big"
	calcErrGraphX   calcError = "x is the graph's variable · Y draws it"
)

type calcTokKind uint8

const (
	calcTokNum calcTokKind = iota + 1
	calcTokName
	calcTokOp
	calcTokPostfix
	calcTokRoot
	calcTokLParen
	calcTokRParen
)

type calcTok struct {
	kind calcTokKind
	num  float64
	text string
}

// calcNames are the words the parser knows, longest first so "asin" is not
// read as "a" then "sin" and "exp" not as e·x·p.
var calcNames = []string{
	"asinh", "acosh", "atanh", "floor", "round",
	"asin", "acos", "atan", "sinh", "cosh", "tanh", "sqrt", "cbrt", "ceil",
	"sin", "cos", "tan", "log", "exp", "abs", "ans",
	"ln", "pi",
	"e", "x",
}

// calcSplitNames reads a run of letters as known names ("sinx" is sin x,
// "2pie" is 2·π·e). ok is false for a word with anything unknown in it.
func calcSplitNames(word string) ([]string, bool) {
	var out []string
	for word != "" {
		matched := ""
		for _, name := range calcNames {
			if strings.HasPrefix(word, name) {
				matched = name
				break
			}
		}
		if matched == "" {
			return nil, false
		}
		out = append(out, matched)
		word = word[len(matched):]
	}
	return out, true
}

func calcIsDigit(r rune) bool { return r >= '0' && r <= '9' }

// calcTokenize reads the line. It takes the display glyphs and their ASCII
// spellings alike (× or *, ÷ or /, − or -, π or pi, √ or sqrt), and reads an
// E (or an e between a number and a signed exponent) as scientific notation.
func calcTokenize(expr string) ([]calcTok, error) {
	expr = strings.NewReplacer("sin⁻¹", "asin", "cos⁻¹", "acos", "tan⁻¹", "atan").Replace(expr)
	rs := []rune(expr)
	var toks []calcTok
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case r == ' ' || r == '\t' || r == '\u00a0':
			i++
		case calcIsDigit(r) || r == '.':
			start := i
			for i < len(rs) && (calcIsDigit(rs[i]) || rs[i] == '.') {
				i++
			}
			if i < len(rs) && (rs[i] == 'E' || rs[i] == 'e') {
				j := i + 1
				if j < len(rs) && (rs[j] == '+' || rs[j] == '-' || rs[j] == '−') {
					j++
				}
				if j < len(rs) && calcIsDigit(rs[j]) {
					for j < len(rs) && calcIsDigit(rs[j]) {
						j++
					}
					i = j
				}
			}
			text := strings.ReplaceAll(string(rs[start:i]), "−", "-")
			v, err := strconv.ParseFloat(text, 64)
			if err != nil && !strings.Contains(err.Error(), "range") {
				return nil, calcErrSyntax
			}
			toks = append(toks, calcTok{kind: calcTokNum, num: v})
			continue
		case r == '+':
			toks = append(toks, calcTok{kind: calcTokOp, text: "+"})
		case r == '-' || r == '−' || r == '–':
			toks = append(toks, calcTok{kind: calcTokOp, text: "-"})
		case r == '*' || r == '×' || r == '·' || r == '∙':
			toks = append(toks, calcTok{kind: calcTokOp, text: "*"})
		case r == '/' || r == '÷' || r == '∕':
			toks = append(toks, calcTok{kind: calcTokOp, text: "/"})
		case r == '^':
			toks = append(toks, calcTok{kind: calcTokOp, text: "^"})
		case r == '!' || r == '%' || r == '²' || r == '³':
			toks = append(toks, calcTok{kind: calcTokPostfix, text: string(r)})
		case r == '√' || r == '∛':
			toks = append(toks, calcTok{kind: calcTokRoot, text: string(r)})
		case r == '(' || r == '[':
			toks = append(toks, calcTok{kind: calcTokLParen})
		case r == ')' || r == ']':
			toks = append(toks, calcTok{kind: calcTokRParen})
		case r == 'π':
			toks = append(toks, calcTok{kind: calcTokName, text: "pi"})
		case unicode.IsLetter(r):
			start := i
			for i < len(rs) && unicode.IsLetter(rs[i]) && rs[i] != 'π' {
				i++
			}
			names, ok := calcSplitNames(strings.ToLower(string(rs[start:i])))
			if !ok {
				return nil, calcErrSyntax
			}
			for _, name := range names {
				toks = append(toks, calcTok{kind: calcTokName, text: name})
			}
			continue
		default:
			return nil, calcErrSyntax
		}
		i++
	}
	return toks, nil
}

// calcParser is a recursive-descent reader over the tokens:
//
//	sum     = term {("+" | "−") term}           a ± b% is a ± a·b/100
//	term    = signed {("×" | "÷") signed | signed}   (juxtaposition multiplies)
//	signed  = ("−" | "+") signed | power
//	power   = postfix ["^" signed]                  right-associative
//	postfix = primary {"!" | "%" | "²" | "³"}
//	primary = number | constant | "x" | "Ans" | "(" sum [")"]
//	        | function ("(" sum [")"] | power) | ("√" | "∛") postfix
type calcParser struct {
	toks    []calcTok
	pos     int
	depth   int
	degrees bool
	ans     float64
	x       float64
	hasX    bool
}

func (p *calcParser) peek() *calcTok {
	if p.pos < len(p.toks) {
		return &p.toks[p.pos]
	}
	return nil
}

func (p *calcParser) sum() (float64, error) {
	v, _, err := p.term()
	if err != nil {
		return 0, err
	}
	for {
		t := p.peek()
		if t == nil || t.kind != calcTokOp || (t.text != "+" && t.text != "-") {
			return v, nil
		}
		p.pos++
		rhs, pct, err := p.term()
		if err != nil {
			return 0, err
		}
		if pct {
			// 200 + 10% is 220, the way a phone or desktop calculator adds
			// a percentage: of the left-hand side, not of one.
			rhs *= v
		}
		if t.text == "+" {
			v += rhs
		} else {
			v -= rhs
		}
	}
}

// startsOperand reports whether t can begin a factor, which makes two
// factors side by side a product (2π, 3(4), (1)(2), 2sin30).
func (p *calcParser) startsOperand(t *calcTok) bool {
	switch t.kind {
	case calcTokNum, calcTokName, calcTokLParen, calcTokRoot:
		return true
	}
	return false
}

func (p *calcParser) term() (float64, bool, error) {
	v, pct, err := p.signed()
	if err != nil {
		return 0, false, err
	}
	for {
		t := p.peek()
		if t == nil {
			return v, pct, nil
		}
		switch {
		case t.kind == calcTokOp && (t.text == "*" || t.text == "/"):
			p.pos++
			rhs, _, err := p.signed()
			if err != nil {
				return 0, false, err
			}
			if t.text == "*" {
				v *= rhs
			} else {
				if rhs == 0 {
					return 0, false, calcErrDivZero
				}
				v /= rhs
			}
		case p.startsOperand(t):
			rhs, _, err := p.signed()
			if err != nil {
				return 0, false, err
			}
			v *= rhs
		default:
			return v, pct, nil
		}
		pct = false
	}
}

func (p *calcParser) signed() (float64, bool, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > calcMaxDepth {
		return 0, false, calcErrSyntax
	}
	if t := p.peek(); t != nil && t.kind == calcTokOp && (t.text == "-" || t.text == "+") {
		p.pos++
		v, pct, err := p.signed()
		if t.text == "-" {
			v = -v
		}
		return v, pct, err
	}
	return p.power()
}

func (p *calcParser) power() (float64, bool, error) {
	base, pct, err := p.postfix()
	if err != nil {
		return 0, false, err
	}
	t := p.peek()
	if t == nil || t.kind != calcTokOp || t.text != "^" {
		return base, pct, nil
	}
	p.pos++
	exp, _, err := p.signed()
	if err != nil {
		return 0, false, err
	}
	if base == 0 && exp < 0 {
		return 0, false, calcErrDivZero
	}
	return math.Pow(base, exp), false, nil
}

func (p *calcParser) postfix() (float64, bool, error) {
	v, err := p.primary()
	if err != nil {
		return 0, false, err
	}
	pct := false
	for t := p.peek(); t != nil && t.kind == calcTokPostfix; t = p.peek() {
		p.pos++
		pct = false
		switch t.text {
		case "!":
			if v, err = calcFactorial(v); err != nil {
				return 0, false, err
			}
		case "%":
			v /= 100
			pct = true
		case "²":
			v *= v
		case "³":
			v = v * v * v
		}
	}
	return v, pct, nil
}

// group reads a bracketed sum after its "(" was taken. A bracket the user
// never closed is closed at the end of the line.
func (p *calcParser) group() (float64, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > calcMaxDepth {
		return 0, calcErrSyntax
	}
	v, err := p.sum()
	if err != nil {
		return 0, err
	}
	if t := p.peek(); t != nil {
		if t.kind != calcTokRParen {
			return 0, calcErrSyntax
		}
		p.pos++
	}
	return v, nil
}

func (p *calcParser) primary() (float64, error) {
	t := p.peek()
	if t == nil {
		return 0, calcErrSyntax
	}
	p.pos++
	switch t.kind {
	case calcTokNum:
		return t.num, nil
	case calcTokLParen:
		return p.group()
	case calcTokRoot:
		arg, _, err := p.postfix()
		if err != nil {
			return 0, err
		}
		if t.text == "∛" {
			return math.Cbrt(arg), nil
		}
		if arg < 0 {
			return 0, calcErrDomain
		}
		return math.Sqrt(arg), nil
	case calcTokName:
		switch t.text {
		case "pi":
			return math.Pi, nil
		case "e":
			return math.E, nil
		case "ans":
			return p.ans, nil
		case "x":
			if !p.hasX {
				return 0, calcErrGraphX
			}
			return p.x, nil
		}
		var arg float64
		var err error
		if next := p.peek(); next != nil && next.kind == calcTokLParen {
			p.pos++
			arg, err = p.group()
		} else {
			arg, _, err = p.power()
		}
		if err != nil {
			return 0, err
		}
		return calcApply(t.text, arg, p.degrees)
	}
	return 0, calcErrSyntax
}

// calcSnap removes the floating-point dust trigonometry leaves on exact
// values: cos 90° is 6e-17 in binary, and 0 on any calculator.
func calcSnap(v float64) float64 {
	if math.Abs(v) < 1e-12 {
		return 0
	}
	if r := math.Round(v * 2); math.Abs(v*2-r) < 1e-12 {
		return r / 2
	}
	return v
}

// calcApply evaluates one named function.
func calcApply(name string, v float64, degrees bool) (float64, error) {
	toRad, fromRad := 1.0, 1.0
	if degrees {
		toRad, fromRad = math.Pi/180, 180/math.Pi
	}
	inUnit := func(lo, hi float64) bool { return v >= lo && v <= hi }
	switch name {
	case "sin":
		return calcSnap(math.Sin(v * toRad)), nil
	case "cos":
		return calcSnap(math.Cos(v * toRad)), nil
	case "tan":
		if c := calcSnap(math.Cos(v * toRad)); c == 0 {
			return 0, calcErrDomain
		}
		return calcSnap(math.Tan(v * toRad)), nil
	case "asin":
		if !inUnit(-1, 1) {
			return 0, calcErrDomain
		}
		return math.Asin(v) * fromRad, nil
	case "acos":
		if !inUnit(-1, 1) {
			return 0, calcErrDomain
		}
		return math.Acos(v) * fromRad, nil
	case "atan":
		return math.Atan(v) * fromRad, nil
	case "sinh":
		return math.Sinh(v), nil
	case "cosh":
		return math.Cosh(v), nil
	case "tanh":
		return math.Tanh(v), nil
	case "asinh":
		return math.Asinh(v), nil
	case "acosh":
		if v < 1 {
			return 0, calcErrDomain
		}
		return math.Acosh(v), nil
	case "atanh":
		if v <= -1 || v >= 1 {
			return 0, calcErrDomain
		}
		return math.Atanh(v), nil
	case "sqrt":
		if v < 0 {
			return 0, calcErrDomain
		}
		return math.Sqrt(v), nil
	case "cbrt":
		return math.Cbrt(v), nil
	case "ln":
		if v <= 0 {
			return 0, calcErrDomain
		}
		return math.Log(v), nil
	case "log":
		if v <= 0 {
			return 0, calcErrDomain
		}
		return math.Log10(v), nil
	case "exp":
		return math.Exp(v), nil
	case "abs":
		return math.Abs(v), nil
	case "floor":
		return math.Floor(v), nil
	case "ceil":
		return math.Ceil(v), nil
	case "round":
		return math.Round(v), nil
	}
	return 0, calcErrSyntax
}

// calcFactorial is n! for whole numbers and Γ(n+1) between them.
func calcFactorial(v float64) (float64, error) {
	if v < 0 && v == math.Trunc(v) {
		return 0, calcErrDomain
	}
	if v > 170.62 {
		return 0, calcErrOverflow
	}
	if v == math.Trunc(v) {
		r := 1.0
		for i := 2; i <= int(v); i++ {
			r *= float64(i)
		}
		return r, nil
	}
	return math.Gamma(v + 1), nil
}

// calcEval evaluates a line. ans is what Ans stands for; hasX binds the
// graph's x.
func calcEval(expr string, ans float64, degrees bool, x float64, hasX bool) (float64, error) {
	if len(expr) > calcMaxExpr {
		return 0, calcErrSyntax
	}
	toks, err := calcTokenize(expr)
	if err != nil {
		return 0, err
	}
	if len(toks) == 0 {
		return 0, calcErrSyntax
	}
	p := &calcParser{toks: toks, degrees: degrees, ans: ans, x: x, hasX: hasX}
	v, err := p.sum()
	if err != nil {
		return 0, err
	}
	if p.pos < len(p.toks) {
		return 0, calcErrSyntax
	}
	if math.IsNaN(v) {
		return 0, calcErrDomain
	}
	if math.IsInf(v, 0) {
		return 0, calcErrOverflow
	}
	return v, nil
}

// calcEvalExpr is the plain evaluator the Terminal's calc command uses:
// radians, no Ans.
func calcEvalExpr(expr string) (float64, error) {
	return calcEval(expr, 0, false, 0, false)
}

// calcFormat writes a value to twelve significant digits, the way it is
// shown and read back: plain between 10⁻⁹ and 10¹², scientific (1.5E+15)
// outside, never "-0".
func calcFormat(v float64) string {
	if v == 0 || math.IsNaN(v) {
		return "0"
	}
	if a := math.Abs(v); a < 1e-9 || a >= 1e12 {
		s := strconv.FormatFloat(v, 'E', 11, 64)
		mant, exp, _ := strings.Cut(s, "E")
		if strings.Contains(mant, ".") {
			mant = strings.TrimRight(strings.TrimRight(mant, "0"), ".")
		}
		return mant + "E" + exp
	}
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 12, 64), 64)
	if err != nil {
		rounded = v
	}
	if rounded == 0 {
		return "0"
	}
	return strconv.FormatFloat(rounded, 'f', -1, 64)
}

// calcParseResult reads a stored result back.
func calcParseResult(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, "−", "-"), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// calcShown is a raw value as the line shows it: the typographic minus.
func calcShown(raw string) string {
	return strings.ReplaceAll(raw, "-", "−")
}

// calcGrouped is a result for the big line: thousands separated, so a
// seven-digit answer can be read at a glance.
func calcGrouped(raw string) string {
	if strings.ContainsAny(raw, "Ee") {
		return calcShown(raw)
	}
	sign := ""
	if strings.HasPrefix(raw, "-") {
		sign, raw = "−", raw[1:]
	}
	intPart, frac, hasFrac := strings.Cut(raw, ".")
	if len(intPart) > 3 {
		var b strings.Builder
		lead := len(intPart) % 3
		if lead > 0 {
			b.WriteString(intPart[:lead])
		}
		for i := lead; i < len(intPart); i += 3 {
			if b.Len() > 0 {
				b.WriteByte(',')
			}
			b.WriteString(intPart[i : i+3])
		}
		intPart = b.String()
	}
	if hasFrac {
		return sign + intPart + "." + frac
	}
	return sign + intPart
}

// calcOpenParens is how many brackets the line leaves open, which the display
// shows as faint closing brackets and "=" closes.
func calcOpenParens(expr string) int {
	open := 0
	for _, r := range expr {
		switch r {
		case '(':
			open++
		case ')':
			if open > 0 {
				open--
			}
		}
	}
	return open
}

// ── Editing the line ───────────────────────────────────────────────────

// calcEndsValue reports whether the line ends in something a following
// value would multiply: a number, a constant, a closing bracket or a postfix.
func calcEndsValue(expr string) bool {
	r, _ := utf8.DecodeLastRuneInString(expr)
	if r == utf8.RuneError {
		return false
	}
	return calcIsDigit(r) || r == '.' || r == ')' || r == 'π' || r == '!' || r == '%' || r == '²' || r == '³' ||
		r == 'e' || r == 'x' || strings.HasSuffix(expr, "Ans")
}

// calcInput types one key's text into the line under the rules of its kind.
func calcInput(kind int, text string) {
	calcEnsureLoaded()
	calc.errMsg = ""
	if text == "" {
		return
	}
	if calc.hasResult {
		calc.hasResult = false
		switch kind {
		case calcKindOperator:
			// "+" right after "=" continues from the answer.
			calc.expression = calcShown(calc.result) + text
			return
		case calcKindFunc:
			// sin right after "=" is the sine of the answer.
			calc.expression = text + calcShown(calc.result) + ")"
			return
		case calcKindDigit, calcKindConst:
			calc.expression = ""
		}
	}
	// The e key after a number writes 2×e: "2e" followed by "+3" would read as
	// the exponent 2·10³.
	if text == "e" && calcEndsValue(calc.expression) {
		r, _ := utf8.DecodeLastRuneInString(calc.expression)
		if calcIsDigit(r) || r == '.' {
			text = "×e"
		}
	}
	if len(calc.expression)+len(text) > calcMaxExpr {
		PlayErrorSound()
		return
	}
	calc.expression += text
	calc.graphCached = false
}

// calcAppendValue puts a number (a history answer) into the line, as a new
// sum after "=" or on an empty line, else multiplied in or after an operator.
func calcAppendValue(raw string) {
	calcEnsureLoaded()
	calc.errMsg = ""
	shown := calcShown(raw)
	if calc.hasResult || strings.TrimSpace(calc.expression) == "" {
		calc.expression = shown
		calc.hasResult = false
		calc.graphCached = false
		return
	}
	if calcEndsValue(calc.expression) {
		shown = "×" + shown
	}
	if len(calc.expression)+len(shown) <= calcMaxExpr {
		calc.expression += shown
		calc.graphCached = false
	}
}

// calcBackspace removes the last thing typed: a whole function name with its
// bracket ("sin⁻¹(", "√("), "Ans", or one character.
func calcBackspace() {
	calcEnsureLoaded()
	calc.errMsg = ""
	if calc.hasResult {
		// Editing the answer's sum: bring it back to the line.
		calc.expression, calc.hasResult = calc.lastExpr, false
		return
	}
	for _, word := range []string{"sin⁻¹(", "cos⁻¹(", "tan⁻¹(", "asinh(", "acosh(", "atanh(", "floor(", "round(",
		"asin(", "acos(", "atan(", "sinh(", "cosh(", "tanh(", "sqrt(", "cbrt(", "ceil(",
		"sin(", "cos(", "tan(", "log(", "exp(", "abs(", "ln(", "√(", "e^(", "10^(", "^(1÷", "Ans", "×e"} {
		if strings.HasSuffix(calc.expression, word) {
			calc.expression = strings.TrimSuffix(calc.expression, word)
			calc.graphCached = false
			return
		}
	}
	calc.expression = trimLastRune(calc.expression)
	calc.graphCached = false
}

func calcClear() {
	calcEnsureLoaded()
	calc.expression, calc.errMsg, calc.hasResult = "", "", false
	calc.graphCached = false
}

// calcNegate flips the sign of the last number or bracket: "5+3" becomes
// "5+−3", and pressing again takes the minus away; right after "=" it is
// the answer that changes sign.
func calcNegate() {
	calcEnsureLoaded()
	calc.errMsg = ""
	if calc.hasResult {
		calc.hasResult = false
		if strings.HasPrefix(calc.result, "-") {
			calc.expression = calcShown(calc.result[1:])
		} else {
			calc.expression = "−" + calcShown(calc.result)
		}
		return
	}
	rs := []rune(calc.expression)
	start := len(rs)
	if start > 0 && rs[start-1] == ')' {
		depth := 0
		for i := start - 1; i >= 0; i-- {
			if rs[i] == ')' {
				depth++
			} else if rs[i] == '(' {
				depth--
				if depth == 0 {
					start = i
					// The bracket belongs to a function name before it.
					for start > 0 && (unicode.IsLetter(rs[start-1]) || rs[start-1] == '√' || rs[start-1] == '⁻' || rs[start-1] == '¹') {
						start--
					}
					break
				}
			}
		}
	} else {
		for start > 0 && (calcIsDigit(rs[start-1]) || rs[start-1] == '.' || unicode.IsLetter(rs[start-1]) || rs[start-1] == 'π' ||
			rs[start-1] == '²' || rs[start-1] == '³' || rs[start-1] == '!' || rs[start-1] == '%') {
			start--
		}
	}
	if start > 0 && rs[start-1] == '−' && (start == 1 || strings.ContainsRune("+−×÷^(", rs[start-2])) {
		calc.expression = string(rs[:start-1]) + string(rs[start:])
	} else {
		calc.expression = string(rs[:start]) + "−" + string(rs[start:])
	}
	calc.graphCached = false
}

// calcAnswer is what Ans means now.
func calcAnswer() float64 {
	if calc.result != "" {
		return calc.resultVal
	}
	return 0
}

// calcEvaluate is "=": the answer replaces the line and joins the history; a
// sum with no answer keeps the line and says why.
func calcEvaluate() {
	calcEnsureLoaded()
	expr := strings.TrimSpace(calc.expression)
	if expr == "" || calc.hasResult {
		return
	}
	v, err := calcEval(expr, calcAnswer(), calc.degrees, 0, false)
	if err != nil {
		calc.errMsg = err.Error()
		PlayErrorSound()
		return
	}
	closed := expr + strings.Repeat(")", calcOpenParens(expr))
	result := calcFormat(v)
	entry := calcEntry{Expr: closed, Result: result}
	// "=" pressed again on the same sum is not another calculation for the
	// history or the trophies.
	if n := len(calc.history); n == 0 || calc.history[n-1] != entry {
		calc.history = append(calc.history, entry)
		if n := len(calc.history); n > calcMaxHistory {
			calc.history = calc.history[n-calcMaxHistory:]
		}
		trophyEvent(tkCalc)
		calcSave()
	}
	calc.result, calc.resultVal, calc.lastExpr = result, v, closed
	calc.expression, calc.hasResult, calc.errMsg = closed, true, ""
	calc.histFocus, calc.histScroll = -1, 0
	PlaySuccessSound()
}

// calcPreview is the answer the line would give now, for the faint "= …"
// shown while typing; "" when it has none or is just a number.
func calcPreview() string {
	expr := strings.TrimSpace(calc.expression)
	if expr == "" || calc.hasResult {
		return ""
	}
	if _, err := strconv.ParseFloat(strings.ReplaceAll(expr, "−", "-"), 64); err == nil {
		return ""
	}
	v, err := calcEval(expr, calcAnswer(), calc.degrees, 0, false)
	if err != nil {
		return ""
	}
	return calcGrouped(calcFormat(v))
}

// calcPress presses the focused key.
func calcPress() {
	if calc.histFocus >= 0 {
		calcUseHistory(calc.histFocus, false)
		return
	}
	key := calcKeys[calc.cursor[0]][calc.cursor[1]]
	calcPressKey(key)
}

func calcPressKey(key calcKey) {
	calcEnsureLoaded()
	insert := key.insert
	if calc.shift && key.insert2 != "" {
		insert = key.insert2
	}
	switch key.kind {
	case calcKindShift:
		calc.shift = !calc.shift
		PlayToggleSound()
		return
	case calcKindAngle:
		calc.degrees = !calc.degrees
		calc.graphCached = false
		calcSave()
		PlayToggleSound()
		return
	case calcKindNegate:
		calcNegate()
	case calcKindBack:
		calcBackspace()
	case calcKindClear:
		calcClear()
	case calcKindEquals:
		calcEvaluate()
		calc.shift = false
		return
	default:
		calcInput(key.kind, insert)
	}
	// 2nd is one-shot, as on a scientific calculator.
	if key.insert2 != "" {
		calc.shift = false
	}
	PlayActivateSound()
}

// calcInsertX types the graph variable.
func calcInsertX() {
	calcInput(calcKindConst, "x")
}

func calcToggleGraph() {
	calc.graphMode = !calc.graphMode
	if calc.graphMode {
		calc.histFocus = -1
	}
}

// calcUseHistory puts history entry i (0 = newest) to use: its answer into the
// line, or (edit) its sum back on the line for changing.
func calcUseHistory(i int, edit bool) {
	n := len(calc.history)
	if i < 0 || i >= n {
		return
	}
	entry := calc.history[n-1-i]
	if edit {
		calc.expression, calc.hasResult, calc.errMsg = entry.Expr, false, ""
		calc.graphCached = false
	} else {
		calcAppendValue(entry.Result)
	}
	calc.histFocus = -1
	calc.cursor[1] = calcCols - 1
	PlayActivateSound()
}

// calcMove walks the keys, and from the right-hand column into the history.
func calcMove(dr, dc int) {
	calc.typed = false
	if calc.histFocus >= 0 {
		switch {
		case dc < 0:
			calc.histFocus = -1
		case dr < 0 && calc.histFocus > 0:
			calc.histFocus--
		case dr > 0 && calc.histFocus < len(calc.history)-1:
			calc.histFocus++
		}
		PlayNavSound()
		return
	}
	if dc > 0 && calc.cursor[1] == calcCols-1 && len(calc.history) > 0 && !calc.graphMode && calcLastGeom.panel.W > 0 {
		calc.histFocus = 0
		PlayNavSound()
		return
	}
	calc.cursor[0] = (calc.cursor[0] + dr + calcRows) % calcRows
	calc.cursor[1] = (calc.cursor[1] + dc + calcCols) % calcCols
	PlayNavSound()
}

// ── Keyboard ────────────────────────────────────────────────────────────

// calcKeyboardTyping reports whether keys come from a real keyboard: a PC,
// or a keyboard plugged into the handheld. A TrimUI firmware can deliver its
// own buttons as key events, so without a keyboard nothing is typed.
func calcKeyboardTyping() bool {
	return !hintOnHandheld() || hintExternalKeyboard.Load()
}

// calcOwnsLetterKeys tells main.go's scene keys (Q and E switch screens) to
// stand aside while letters type function names here.
func calcOwnsLetterKeys(config *Config) bool {
	return config != nil && currentSceneIndex >= 0 && currentSceneIndex < len(config.Scenes) &&
		config.Scenes[currentSceneIndex].Name == "Calculator" && calcKeyboardTyping()
}

// calcTypeText types keyboard (or pasted) text: digits, operators, brackets
// and the letters of function names. "=" evaluates; Y is the graph key.
func calcTypeText(text string) {
	for _, r := range text {
		switch r {
		case '=':
			calcEvaluate()
			continue
		case 'y', 'Y':
			continue
		case ',':
			r = '.'
		case '*':
			r = '×'
		case '/':
			r = '÷'
		case '-', '–':
			r = '−'
		case '∛':
			calcInput(calcKindFunc, "cbrt(")
			continue
		}
		switch {
		case calcIsDigit(r) || r == '.' || r == '(':
			calcInput(calcKindDigit, string(r))
		case r == ')':
			calcInput(calcKindInsert, ")")
		case strings.ContainsRune("+−×÷^!%²³", r):
			calcInput(calcKindOperator, string(r))
		case r == '√':
			calcInput(calcKindFunc, "√(")
		case r == 'π' || r == 'E':
			calcInput(calcKindConst, string(r))
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			calcInput(calcKindConst, string(unicode.ToLower(r)))
		}
	}
}

func handleCalcTextInput(e *sdl.TextInputEvent) {
	if e == nil || !calcKeyboardTyping() {
		return
	}
	calcTypeText(e.GetText())
	calc.typed = true
}

// handleCalcInput processes key presses in the Calculator scene. Printable
// characters arrive as text (handleCalcTextInput) when a keyboard types;
// this takes the arrows, Enter, Backspace, Delete, Escape and the chords.
func handleCalcInput(e *sdl.KeyboardEvent) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	calcEnsureLoaded()
	typing := calcKeyboardTyping()
	ctrl := e.Keysym.Mod&sdl.KMOD_CTRL != 0
	switch sym := e.Keysym.Sym; {
	case ctrl && sym == sdl.K_c:
		text := calc.expression
		if calc.hasResult {
			text = calc.result
		}
		if strings.TrimSpace(text) != "" {
			copyWithToast(text, "Calculator", "Copied")
		}
	case ctrl && sym == sdl.K_v:
		if text, err := pasteTextFromClipboard(); err == nil {
			if len(text) > calcMaxExpr {
				text = text[:calcMaxExpr]
			}
			calcTypeText(text)
			calc.typed = true
		}
	case sym == sdl.K_UP:
		calcMove(-1, 0)
	case sym == sdl.K_DOWN:
		calcMove(1, 0)
	case sym == sdl.K_LEFT:
		calcMove(0, -1)
	case sym == sdl.K_RIGHT:
		calcMove(0, 1)
	case sym == sdl.K_KP_ENTER:
		calcEvaluate()
	case sym == sdl.K_RETURN:
		// Enter after typing is "="; after walking the keys it presses the
		// focused one (some firmware sends A as Enter).
		if typing && calc.typed {
			calcEvaluate()
		} else {
			calcPress()
		}
	case sym == sdl.K_SPACE:
		calcPress()
	case sym == sdl.K_BACKSPACE:
		calcBackspace()
	case sym == sdl.K_DELETE:
		calcClear()
	case sym == sdl.K_ESCAPE:
		// The footer's Back (B on the pad, Esc on a keyboard): the scene's
		// handleKey consumes every key, so nothing else would leave.
		if calc.histFocus >= 0 {
			calc.histFocus = -1
			PlayBackSound()
			return
		}
		goBackScene(appConfig)
	case typing && !ctrl && sym == sdl.K_y:
		calcToggleGraph()
	case !typing && !ctrl:
		// Firmware keys without a keyboard: the old letter shortcuts.
		switch sym {
		case sdl.K_c:
			calcClear()
		case sdl.K_g, sdl.K_y:
			calcToggleGraph()
		case sdl.K_x:
			calcInsertX()
		}
	}
}

// ctrlCalculator is the pad: the D-pad walks the keys and the history, A
// presses, X types x (or edits a history sum), Y toggles the graph, L1
// clears, R1 deletes, B leaves (or returns from the history).
func ctrlCalculator(e *sdl.ControllerButtonEvent) bool {
	calcEnsureLoaded()
	calc.typed = false
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		calcMove(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		calcMove(1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		calcMove(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		calcMove(0, 1)
	case sdl.CONTROLLER_BUTTON_A:
		calcPress()
	case sdl.CONTROLLER_BUTTON_X:
		if calc.histFocus >= 0 {
			calcUseHistory(calc.histFocus, true)
			return true
		}
		calcInsertX()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_Y:
		calcToggleGraph()
		PlayToggleSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		calcClear()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		calcBackspace()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		if calc.histFocus >= 0 {
			calc.histFocus = -1
			PlayBackSound()
			return true
		}
		goBackScene(appConfig)
	default:
		return false
	}
	return true
}

// calcFooterHints names the keys of whatever holds the pad: the history list
// or the key grid. Keyboard twins follow the hints rule (inputhints.go).
func calcFooterHints(hints []FooterHint) []FooterHint {
	if calc.histFocus >= 0 {
		return []FooterHint{
			{Button: "D-Pad", Label: "Navigate"},
			{Button: "A", Label: "Use result"},
			{Button: "X", Label: "Edit"},
			{Button: "B", Label: "Keys"},
		}
	}
	if len(hints) > 1 {
		hints[1] = FooterHint{Button: "A", Label: "Select"}
	}
	return append(hints,
		FooterHint{Button: "X", Label: "Insert x"},
		FooterHint{Button: "Y", Label: "Graph"},
		FooterHint{Button: "L1 / Del", Label: "Clear"},
		FooterHint{Button: "R1 / Backspace", Label: "Delete"},
	)
}

// ── Graph ─────────────────────────────────────────────────────────────

// calcGraphSample is one evaluated point; invalid or discontinuous samples
// break the stroke instead of connecting across a pole or a domain error.
type calcGraphSample struct {
	x, y  float64
	valid bool
}

// calcEvalExprAtX evaluates the line with x bound, in the given angle mode.
func calcEvalExprAtX(expression string, x float64, degrees bool) (float64, error) {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0, calcErrSyntax
	}
	return calcEval(expression, calcAnswer(), degrees, x, true)
}

// calcGraphSamples evaluates a bounded number of points across the x range.
// A fixed cap keeps graph redraws cheap on the Smart Pro.
func calcGraphSamples(expression string, minX, maxX float64, count int, degrees bool) []calcGraphSample {
	if count < 2 || count > 512 || math.IsNaN(minX) || math.IsNaN(maxX) ||
		math.IsInf(minX, 0) || math.IsInf(maxX, 0) || minX >= maxX {
		return nil
	}
	samples := make([]calcGraphSample, count)
	for i := range samples {
		x := minX + (maxX-minX)*float64(i)/float64(count-1)
		y, err := calcEvalExprAtX(expression, x, degrees)
		valid := err == nil && !math.IsNaN(y) && !math.IsInf(y, 0) && math.Abs(y) <= 1e6
		samples[i] = calcGraphSample{x: x, y: y, valid: valid}
	}
	return samples
}

// calcGraphRange is the x range: a full turn either side in degrees, where
// sin x over ±10 would be a flat line, and ±10 in radians.
func calcGraphRange(degrees bool) (float64, float64) {
	if degrees {
		return -360, 360
	}
	return -10, 10
}

func currentCalcGraphSamples() []calcGraphSample {
	if calc.graphCached && calc.graphExpression == calc.expression && calc.graphDegrees == calc.degrees {
		return calc.graphSamples
	}
	calc.graphExpression, calc.graphDegrees = calc.expression, calc.degrees
	minX, maxX := calcGraphRange(calc.degrees)
	calc.graphSamples = calcGraphSamples(calc.expression, minX, maxX, 200, calc.degrees)
	calc.graphCached = true
	return calc.graphSamples
}

func calcGraphYBounds(samples []calcGraphSample) (float64, float64, bool) {
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, sample := range samples {
		if !sample.valid {
			continue
		}
		minY, maxY = math.Min(minY, sample.y), math.Max(maxY, sample.y)
	}
	if math.IsInf(minY, 0) || math.IsInf(maxY, 0) {
		return 0, 0, false
	}
	if math.Abs(maxY-minY) < 1e-9 {
		padding := math.Max(1, math.Abs(minY)*0.1)
		return minY - padding, maxY + padding, true
	}
	padding := (maxY - minY) * 0.08
	return minY - padding, maxY + padding, true
}

func calcGraphMapPoint(sample calcGraphSample, minX, maxX, minY, maxY float64, plot sdl.Rect) (int32, int32, bool) {
	if !sample.valid || minX >= maxX || minY >= maxY || plot.W <= 1 || plot.H <= 1 || sample.y < minY || sample.y > maxY {
		return 0, 0, false
	}
	x := float64(plot.X) + (sample.x-minX)/(maxX-minX)*float64(plot.W-1)
	y := float64(plot.Y+plot.H-1) - (sample.y-minY)/(maxY-minY)*float64(plot.H-1)
	return int32(math.Round(x)), int32(math.Round(y)), true
}

// calcHasX reports whether the line uses the graph variable.
func calcHasX(expr string) bool {
	toks, err := calcTokenize(expr)
	if err != nil {
		return false
	}
	for _, t := range toks {
		if t.kind == calcTokName && t.text == "x" {
			return true
		}
	}
	return false
}

func drawCalcGraph(renderer *sdl.Renderer, config *Config, font *ttf.Font, panel sdl.Rect) {
	if panel.W < 100 || panel.H < 120 {
		return
	}
	title := "Graph · y = f(x)"
	if calc.degrees {
		title += " · degrees"
	}
	if font != nil {
		renderText(renderer, config, font, title, ColorTextPrimary(), panel.X+12, panel.Y+10)
	}
	plot := sdl.Rect{X: panel.X + 52, Y: panel.Y + 44, W: panel.W - 64, H: panel.H - 84}
	if plot.W < 20 || plot.H < 20 {
		return
	}
	fillRoundedRect(renderer, plot.X, plot.Y, plot.W, plot.H, 8, ColorSurfaceRow)
	strokeRoundedRect(renderer, plot.X, plot.Y, plot.W, plot.H, 8, 1, ColorBorder)

	minX, maxX := calcGraphRange(calc.degrees)
	message := ""
	var samples []calcGraphSample
	minY, maxY, ok := 0.0, 0.0, false
	if !calcHasX(calc.expression) {
		message = "Type a sum with x (X key)"
	} else {
		samples = currentCalcGraphSamples()
		if minY, maxY, ok = calcGraphYBounds(samples); !ok {
			message = "No points to draw"
		}
	}
	if message != "" {
		if font != nil {
			mw, _, _ := font.SizeUTF8(message)
			renderText(renderer, config, font, message, ColorTextSecondary(), plot.X+(plot.W-int32(mw))/2, plot.Y+plot.H/2-8)
		}
		return
	}

	// Quiet grid lines keep the small plot readable without crowding it.
	grid := WithAlpha(ColorBorderSubtle, 100)
	for i := int32(1); i < 4; i++ {
		x := plot.X + plot.W*i/4
		y := plot.Y + plot.H*i/4
		renderer.SetDrawColor(grid.R, grid.G, grid.B, grid.A)
		_ = renderer.DrawLine(x, plot.Y+1, x, plot.Y+plot.H-2)
		_ = renderer.DrawLine(plot.X+1, y, plot.X+plot.W-2, y)
	}
	renderer.SetDrawColor(130, 145, 165, 210)
	if minY <= 0 && maxY >= 0 {
		y := plot.Y + plot.H - 1 - int32((-minY)/(maxY-minY)*float64(plot.H-1))
		_ = renderer.DrawLine(plot.X+1, y, plot.X+plot.W-2, y)
	}
	if minX <= 0 && maxX >= 0 {
		x := plot.X + int32((-minX)/(maxX-minX)*float64(plot.W-1))
		_ = renderer.DrawLine(x, plot.Y+1, x, plot.Y+plot.H-2)
	}
	// A two-pixel line reads on the handheld's dense screen.
	renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 255)
	for i := 1; i < len(samples); i++ {
		x1, y1, ok1 := calcGraphMapPoint(samples[i-1], minX, maxX, minY, maxY, plot)
		x2, y2, ok2 := calcGraphMapPoint(samples[i], minX, maxX, minY, maxY, plot)
		if !ok1 || !ok2 || abs(int(y2-y1)) > int(plot.H/3) || math.Abs(samples[i].y-samples[i-1].y) > (maxY-minY)*0.5 {
			continue
		}
		_ = renderer.DrawLine(x1, y1, x2, y2)
		_ = renderer.DrawLine(x1, y1+1, x2, y2+1)
	}
	if font != nil {
		ink := ColorTextTertiary()
		top, bottom := calcGrouped(calcFormat(maxY)), calcGrouped(calcFormat(minY))
		renderText(renderer, config, font, ellipsize(font, top, plot.X-panel.X-8), ink, panel.X+8, plot.Y)
		renderText(renderer, config, font, ellipsize(font, bottom, plot.X-panel.X-8), ink, panel.X+8, plot.Y+plot.H-int32(font.Height()))
		bounds := "x: " + calcShown(calcFormat(minX)) + " to " + calcFormat(maxX)
		renderText(renderer, config, font, bounds, ink, plot.X, plot.Y+plot.H+8)
	}
}

// ── Drawing ─────────────────────────────────────────────────────────────

// calcLayout places the display, the key grid and the side panel in the
// space between the shared header and footer, for any canvas: the 1280×720
// Smart Pro, the Brick's compact canvas or a PC window. The panel goes when
// the keys would be too narrow beside it.
func calcLayout() calcGeom {
	var g calcGeom
	top := headerBarHeight() + 10
	bottom := screenHeight - footerBarHeight() - 10
	availH := bottom - top
	const margin, gap, panelGap = int32(16), int32(8), int32(20)
	panelW := screenWidth * 24 / 100
	if panelW < 200 {
		panelW = 200
	}
	if panelW > 320 {
		panelW = 320
	}
	gridW := screenWidth - 2*margin - panelGap - panelW
	if (gridW-gap*(calcCols-1))/calcCols < 64 {
		panelW = 0
		gridW = screenWidth - 2*margin
	}
	displayH := availH / 5
	if displayH < 84 {
		displayH = 84
	}
	if displayH > 124 {
		displayH = 124
	}
	gridH := availH - displayH - 12
	keyW := (gridW - gap*(calcCols-1)) / calcCols
	keyH := (gridH - gap*(calcRows-1)) / calcRows
	keyW = max(40, min(keyW, 120))
	keyH = max(30, min(keyH, 74))
	gridW = keyW*calcCols + gap*(calcCols-1)
	gridH = keyH*calcRows + gap*(calcRows-1)
	blockW := gridW
	if panelW > 0 {
		blockW += panelGap + panelW
	}
	blockH := displayH + 12 + gridH
	x0 := (screenWidth - blockW) / 2
	if x0 < margin {
		x0 = margin
	}
	y0 := top + (availH-blockH)/2
	if y0 < top {
		y0 = top
	}
	g.display = sdl.Rect{X: x0, Y: y0, W: gridW, H: displayH}
	for r := 0; r < calcRows; r++ {
		for c := 0; c < calcCols; c++ {
			g.keys[r][c] = sdl.Rect{
				X: x0 + int32(c)*(keyW+gap), Y: y0 + displayH + 12 + int32(r)*(keyH+gap),
				W: keyW, H: keyH,
			}
		}
	}
	if panelW > 0 {
		g.panel = sdl.Rect{X: x0 + gridW + panelGap, Y: y0, W: panelW, H: blockH}
	}
	g.graphBtn = sdl.Rect{X: g.display.X + g.display.W - 96, Y: g.display.Y + 8, W: 86, H: 28}
	g.xBtn = sdl.Rect{X: g.graphBtn.X - 44, Y: g.display.Y + 8, W: 36, H: 28}
	g.modeBtn = sdl.Rect{X: g.display.X + 12, Y: g.display.Y + 8, W: 64, H: 28}
	return g
}

// calcFitFont is the largest face up to maxPx in which text fits width.
func calcFitFont(config *Config, text string, width int32, maxPx, minPx int) *ttf.Font {
	var font *ttf.Font
	for px := maxPx; px >= minPx; px -= 2 {
		font = getFontSized(config, "medium", px)
		if font == nil {
			return nil
		}
		if w, _, err := font.SizeUTF8(text); err == nil && int32(w) <= width {
			return font
		}
	}
	return font
}

// calcTail is the end of text that fits width, behind an ellipsis: the end of
// a sum is what is being typed.
func calcTail(font *ttf.Font, text string, width int32) string {
	if font == nil {
		return text
	}
	if w, _, err := font.SizeUTF8(text); err == nil && int32(w) <= width {
		return text
	}
	rs := []rune(text)
	for i := 1; i < len(rs); i++ {
		candidate := "…" + string(rs[i:])
		if w, _, err := font.SizeUTF8(candidate); err == nil && int32(w) <= width {
			return candidate
		}
	}
	return "…"
}

func drawCalcDisplay(renderer *sdl.Renderer, config *Config, g calcGeom, small *ttf.Font) {
	d := g.display
	drawCard(renderer, d.X, d.Y, d.W, d.H, 16)
	if small != nil {
		mode := "RAD"
		if calc.degrees {
			mode = "DEG"
		}
		renderText(renderer, config, small, mode, ColorTextAccent(), g.modeBtn.X, g.modeBtn.Y+4)
		if calc.shift {
			mw, _, _ := small.SizeUTF8(mode + "  ")
			renderText(renderer, config, small, "2nd", ColorTextAccent(), g.modeBtn.X+int32(mw), g.modeBtn.Y+4)
		}
	}
	UIButton(renderer, config, g.xBtn, "x", UIButtonStyle{
		Variant: UIButtonSecondary, LabelColor: ColorTextPrimary(), Font: small, Radius: 8, NoShadow: true,
	}, UIButtonState{})
	graphLabel := "Graph"
	if calc.graphMode {
		graphLabel = "History"
	}
	UIButton(renderer, config, g.graphBtn, graphLabel, UIButtonStyle{
		Variant: UIButtonSecondary, LabelColor: ColorTextPrimary(), Font: small, Radius: 8, NoShadow: true,
	}, UIButtonState{Focused: calc.graphMode})

	// The context line: why the sum has no answer, the sum an answer came
	// from, or the answer the line would give now.
	inner := d.W - 32
	if small != nil {
		context, ink := "", ColorTextTertiary()
		switch {
		case calc.errMsg != "":
			context, ink = calc.errMsg, DangerInk()
		case calc.hasResult:
			context = calc.lastExpr + " ="
		default:
			if preview := calcPreview(); preview != "" {
				context = "= " + preview
			}
		}
		if context != "" {
			context = calcTail(small, context, g.xBtn.X-d.X-24)
			cw, _, _ := small.SizeUTF8(context)
			renderText(renderer, config, small, context, ink, g.xBtn.X-12-int32(cw), d.Y+40)
		}
	}
	// The big line: the answer, or the sum being typed with its unclosed
	// brackets shown faintly.
	text, ghost := "0", ""
	switch {
	case calc.hasResult:
		text = calcGrouped(calc.result)
	case calc.expression != "":
		text = calc.expression
		ghost = strings.Repeat(")", calcOpenParens(calc.expression))
	}
	maxPx := int(d.H * 2 / 5)
	if maxPx > 44 {
		maxPx = 44
	}
	if maxPx < 22 {
		maxPx = 22
	}
	font := calcFitFont(config, text+ghost, inner, maxPx, 20)
	if font == nil {
		return
	}
	shown := calcTail(font, text, inner)
	tw, _, _ := font.SizeUTF8(shown)
	gw := 0
	if ghost != "" {
		gw, _, _ = font.SizeUTF8(ghost)
	}
	x := d.X + d.W - 16 - int32(tw) - int32(gw)
	y := d.Y + d.H - int32(font.Height()) - 8
	ink := ColorTextPrimary()
	if calc.errMsg != "" {
		ink = WithAlpha(ink, 170)
	}
	renderText(renderer, config, font, shown, ink, x, y)
	if ghost != "" {
		renderText(renderer, config, font, ghost, WithAlpha(ColorTextTertiary(), 140), x+int32(tw), y)
	}
}

func drawCalcKeys(renderer *sdl.Renderer, config *Config, g calcGeom, font, small *ttf.Font) {
	for row := 0; row < calcRows; row++ {
		for col := 0; col < calcCols; col++ {
			key := calcKeys[row][col]
			rect := g.keys[row][col]
			label := key.label
			if calc.shift && key.label2 != "" {
				label = key.label2
			}
			if key.kind == calcKindAngle && !calc.degrees {
				label = "RAD"
			}
			// Key style: equals is the accent key, clear and delete are
			// danger-tinted, operators are accent ink, the scientific keys
			// are quieter glass than the digits.
			style := UIButtonStyle{Variant: UIButtonSecondary, LabelColor: ColorTextPrimary(), Radius: 10}
			switch {
			case key.kind == calcKindEquals:
				style = UIButtonStyle{Variant: UIButtonPrimary, Fill: glassAccent(), Radius: 10}
			case key.kind == calcKindClear || key.kind == calcKindBack:
				style = UIButtonStyle{
					Variant:    UIButtonCustom,
					Fill:       WithAlpha(ColorDanger, 70),
					HoverFill:  WithAlpha(ColorDanger, 90),
					FocusFill:  WithAlpha(ColorDanger, 100),
					LabelColor: DangerInk(),
					Radius:     10,
				}
			case key.kind == calcKindShift && calc.shift:
				style = UIButtonStyle{Variant: UIButtonPrimary, Fill: glassAccent(), Radius: 10}
			case key.kind == calcKindOperator && col == calcCols-1:
				style.LabelColor = ColorTextAccent()
			case key.kind == calcKindDigit || key.kind == calcKindNegate:
			default:
				style.Variant = UIButtonGhost
			}
			style.Font = font
			if small != nil && utf8.RuneCountInString(label) > 2 {
				style.Font = small
			}
			if w, _, err := style.Font.SizeUTF8(label); err == nil && int32(w) > rect.W-8 && small != nil {
				style.Font = small
			}
			UIButton(renderer, config, rect, label, style, UIButtonState{
				Focused: calc.histFocus < 0 && calc.cursor[0] == row && calc.cursor[1] == col,
			})
		}
	}
}

func drawCalcHistory(renderer *sdl.Renderer, config *Config, g *calcGeom, small *ttf.Font) {
	p := g.panel
	drawCard(renderer, p.X, p.Y, p.W, p.H, 16)
	g.history = g.history[:0]
	if small == nil {
		return
	}
	renderText(renderer, config, small, "History", WithAlpha(ColorTextTertiary(), 200), p.X+12, p.Y+10)
	n := len(calc.history)
	if n == 0 {
		empty := "No calculations yet"
		ew, _, _ := small.SizeUTF8(empty)
		renderText(renderer, config, small, empty, ColorTextTertiary(), p.X+(p.W-int32(ew))/2, p.Y+p.H/2-int32(small.Height()))
		return
	}
	lineH := int32(small.Height())
	entryH := lineH*2 + 12
	listY := p.Y + 40
	visible := int((p.Y + p.H - 8 - listY) / entryH)
	if visible < 1 {
		visible = 1
	}
	// Keep the focused entry on screen.
	if calc.histFocus >= 0 {
		if calc.histFocus < calc.histScroll {
			calc.histScroll = calc.histFocus
		}
		if calc.histFocus >= calc.histScroll+visible {
			calc.histScroll = calc.histFocus - visible + 1
		}
	}
	if calc.histScroll > n-1 {
		calc.histScroll = max(0, n-1)
	}
	inner := p.W - 24
	for k := 0; k < visible; k++ {
		i := calc.histScroll + k
		if i >= n {
			break
		}
		entry := calc.history[n-1-i]
		rect := sdl.Rect{X: p.X + 6, Y: listY + int32(k)*entryH, W: p.W - 12, H: entryH - 4}
		if i == calc.histFocus {
			fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, WithAlpha(accentColor, 55))
			strokeRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 10, 2, accentColor)
		}
		expr := calcTail(small, entry.Expr, inner)
		ew, _, _ := small.SizeUTF8(expr)
		renderText(renderer, config, small, expr, ColorTextTertiary(), p.X+p.W-12-int32(ew), rect.Y+4)
		result := "= " + calcGrouped(entry.Result)
		result = calcTail(small, result, inner)
		rw, _, _ := small.SizeUTF8(result)
		renderText(renderer, config, small, result, ColorTextPrimary(), p.X+p.W-12-int32(rw), rect.Y+4+lineH)
		g.history = append(g.history, calcHistHit{rect: rect, index: i})
	}
}

// renderCalculator draws the full-screen calculator scene.
func renderCalculator(renderer *sdl.Renderer, config *Config) {
	calcEnsureLoaded()
	// Home's backdrop, the one every screen stands on (ui_glass.go).
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	font, _ := getCachedFont(config, "medium")
	small, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	g := calcLayout()
	g.history = calcLastGeom.history[:0]
	drawCalcDisplay(renderer, config, g, small)
	switch {
	case calc.graphMode && g.panel.W > 0:
		drawCalcKeys(renderer, config, g, font, small)
		drawCard(renderer, g.panel.X, g.panel.Y, g.panel.W, g.panel.H, 16)
		drawCalcGraph(renderer, config, small, g.panel)
	case calc.graphMode:
		// No room for a side panel: the graph takes the keys' place.
		area := sdl.Rect{X: g.keys[0][0].X, Y: g.keys[0][0].Y, W: g.display.W,
			H: g.keys[calcRows-1][0].Y + g.keys[calcRows-1][0].H - g.keys[0][0].Y}
		drawCard(renderer, area.X, area.Y, area.W, area.H, 16)
		drawCalcGraph(renderer, config, small, area)
	default:
		drawCalcKeys(renderer, config, g, font, small)
		if g.panel.W > 0 {
			drawCalcHistory(renderer, config, &g, small)
		}
	}
	calcLastGeom = g
	// The shared bars: the calculator used to be the only full-screen scene
	// with no header at all, so it read as a separate app.
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
}

// Scene registration. The renderer, keyboard handler and controller mapping all
// live in this file; scene.go routes to them by name.
type calculatorScene struct{ sceneBase }

func (calculatorScene) render(r *sdl.Renderer, c *Config) bool { renderCalculator(r, c); return true }

func (calculatorScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	handleCalcInput(e)
	return true
}

func (calculatorScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return ctrlCalculator(e)
}

func handleCalculatorMouse(mx, my int32, config *Config) bool {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) || config.Scenes[currentSceneIndex].Name != "Calculator" {
		return false
	}
	g := calcLastGeom
	switch {
	case pointInRect(mx, my, g.graphBtn):
		calcToggleGraph()
		PlayActivateSound()
		return true
	case pointInRect(mx, my, g.xBtn):
		calcInsertX()
		PlayActivateSound()
		return true
	case pointInRect(mx, my, g.modeBtn):
		calc.degrees = !calc.degrees
		calc.graphCached = false
		calcSave()
		PlayToggleSound()
		return true
	}
	if calc.graphMode && g.panel.W == 0 {
		// The graph stands where the keys were.
		return false
	}
	for row := range g.keys {
		for col, rect := range g.keys[row] {
			if !pointInRect(mx, my, rect) {
				continue
			}
			calc.cursor, calc.histFocus, calc.typed = [2]int{row, col}, -1, false
			calcPress()
			return true
		}
	}
	for _, hit := range g.history {
		if pointInRect(mx, my, hit.rect) {
			calcUseHistory(hit.index, false)
			return true
		}
	}
	return false
}

func init() { registerScene(calculatorScene{sceneBase{name: "Calculator"}}) }

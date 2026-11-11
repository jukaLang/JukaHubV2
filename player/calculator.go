package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Calculator with expression history
// ──────────────────────────────────────────────────────────────────────

const (
	calcCols = 5
	calcRows = 6
	calcBtnW = 108
	calcBtnH = 60
	calcGap  = 8
)

type calcBtn struct {
	label  string
	action string // "num", "op", "func", "clear", "eq", "back"
}

var calcButtons = [calcRows][calcCols]calcBtn{
	{{"C", "clear"}, {"+/-", "func"}, {"%", "func"}, {"<", "back"}, {"/", "op"}},
	{{"7", "num"}, {"8", "num"}, {"9", "num"}, {"(", "num"}, {")", "num"}},
	{{"4", "num"}, {"5", "num"}, {"6", "num"}, {"*", "op"}, {"sqrt", "func"}},
	{{"1", "num"}, {"2", "num"}, {"3", "num"}, {"-", "op"}, {"^2", "func"}},
	{{"0", "num"}, {".", "num"}, {"pi", "func"}, {"+", "op"}, {"^", "op"}},
	{{"e", "func"}, {"sin", "func"}, {"cos", "func"}, {"tan", "func"}, {"=", "eq"}},
}

type calcState struct {
	expression      string
	display         string
	cursor          [2]int // [row, col]
	history         []string
	histIdx         int
	result          string
	hasResult       bool
	graphMode       bool
	graphExpression string
	graphSamples    []calcGraphSample
	graphCached     bool
}

var calcGraphButtonRect sdl.Rect
var calcXButtonRect sdl.Rect
var calcButtonRects [calcRows][calcCols]sdl.Rect

var calc calcState

func initCalc() {
	calc = calcState{
		display:   "0",
		cursor:    [2]int{0, 0},
		hasResult: true,
	}
}

// calcNavigate moves the cursor on the button grid.
func calcNavigate(dr, dc int) {
	calc.cursor[0] = (calc.cursor[0] + dr + calcRows) % calcRows
	calc.cursor[1] = (calc.cursor[1] + dc + calcCols) % calcCols
	PlayNavSound()
}

// calcSelect presses the currently focused button.
func calcSelect() {
	btn := calcButtons[calc.cursor[0]][calc.cursor[1]]
	switch btn.action {
	case "num":
		if calc.hasResult {
			if btn.label == "." {
				calc.expression = "0."
			} else {
				calc.expression = btn.label
			}
			calc.hasResult = false
		} else {
			calc.expression += btn.label
		}
	case "op":
		if calc.hasResult && calc.result != "" {
			calc.expression = calc.result
			calc.hasResult = false
		}
		calc.expression += btn.label
	case "func":
		calcHandleFunc(btn.label)
	case "clear":
		calc.expression = ""
		calc.display = "0"
		calc.result = ""
		calc.hasResult = true
		calc.graphCached = false
	case "back":
		calcBackspace()
	case "eq":
		calcEvaluate()
	}
	if calc.expression != "" && !calc.hasResult {
		calc.display = calc.expression
	}
}

// calcTakeResult makes the result on screen the operand of a function key
// pressed straight after "=". +/- after "2+2=" used to negate the old
// expression ("-2+2", which is 0) instead of the 4 the user was looking at,
// and sqrt wrapped "2+2" rather than 4 — the same value only by luck.
func calcTakeResult() {
	if calc.hasResult && calc.result != "" {
		calc.expression = calc.result
		calc.hasResult = false
	}
}

// calcWrap applies a function key: around what is there, or — on an empty
// line — as an open "fn(" for the operand to be typed into. Wrapping nothing
// made "sqrt()", which could only ever be an error.
func calcWrap(fn string) {
	calcTakeResult()
	if strings.TrimSpace(calc.expression) == "" || calc.hasResult {
		calc.expression = fn + "("
		calc.hasResult = false
		return
	}
	calc.expression = fn + "(" + calc.expression + ")"
}

// calcToggleSign negates the whole expression: a plain number flips its sign,
// "-(…)" unwraps, anything else is wrapped. Flipping only a leading "-" turned
// "-2+3" into "2+3" and "2+3" into "-2+3".
func calcToggleSign() {
	calcTakeResult()
	expr := strings.TrimSpace(calc.expression)
	if expr == "" {
		return
	}
	if _, err := strconv.ParseFloat(expr, 64); err == nil {
		if strings.HasPrefix(expr, "-") {
			calc.expression = expr[1:]
		} else {
			calc.expression = "-" + expr
		}
		return
	}
	if strings.HasPrefix(expr, "-(") && calcMatchingParen(expr, 1) == len(expr)-1 {
		calc.expression = expr[2 : len(expr)-1]
		return
	}
	calc.expression = "-(" + expr + ")"
}

// calcMatchingParen returns the index of the ')' closing the '(' at open, or -1.
func calcMatchingParen(expr string, open int) int {
	if open < 0 || open >= len(expr) || expr[open] != '(' {
		return -1
	}
	depth := 0
	for i := open; i < len(expr); i++ {
		switch expr[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// calcExpandConstants replaces pi and e with their full values, bracketed,
// with the multiplication the user implied ("2pi" is 2×π, "pi2" π×2). Two
// bugs lived here: "%f" cut π to six decimals, and every "e" was replaced —
// including the exponent of a result such as 1e+20, which "=" prints itself,
// so reusing it computed 12.718282+20. A digit, "e", a sign and a digit is a
// number's exponent and is left alone.
func calcExpandConstants(expr string) string {
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }
	var b strings.Builder
	b.Grow(len(expr) + 24)
	lastValue := false  // the text so far ends in a value
	afterConst := false // ...and that value is a constant just written
	constant := func(v float64) {
		if lastValue {
			b.WriteByte('*')
		}
		b.WriteByte('(')
		b.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
		b.WriteByte(')')
		lastValue, afterConst = true, true
	}
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		if c == 'p' && i+1 < len(expr) && expr[i+1] == 'i' {
			constant(math.Pi)
			i++
			continue
		}
		if c == 'e' {
			exponent := i > 0 && (isDigit(expr[i-1]) || expr[i-1] == '.') &&
				i+2 < len(expr) && (expr[i+1] == '+' || expr[i+1] == '-') && isDigit(expr[i+2])
			if !exponent {
				constant(math.E)
				continue
			}
		}
		if afterConst && (isDigit(c) || c == '.' || c == '(') {
			b.WriteByte('*')
		}
		b.WriteByte(c)
		lastValue = isDigit(c) || c == '.' || c == ')'
		afterConst = false
	}
	return b.String()
}

func calcHandleFunc(label string) {
	switch label {
	case "C":
		calcClear()
	case "<":
		calcBackspace()
	case "+/-":
		calcToggleSign()
	case "%":
		calcTakeResult()
		if strings.TrimSpace(calc.expression) != "" && !calc.hasResult {
			calc.expression += "/100"
		}
	case "sqrt":
		calcWrap("sqrt")
	case "^2":
		calcTakeResult()
		if strings.TrimSpace(calc.expression) != "" && !calc.hasResult {
			calc.expression = "(" + calc.expression + ")^2"
		}
	case "pi":
		if calc.hasResult {
			calc.expression = "pi"
			calc.hasResult = false
		} else {
			calc.expression += "pi"
		}
	case "e":
		if calc.hasResult {
			calc.expression = "e"
			calc.hasResult = false
		} else if n := len(calc.expression); n > 0 && (calc.expression[n-1] >= '0' && calc.expression[n-1] <= '9' ||
			calc.expression[n-1] == '.' || calc.expression[n-1] == ')') {
			// After a number the key writes "*e": "2e-1" typed as 2, e, -, 1
			// would otherwise read as the exponent 0.2 instead of 2e−1.
			calc.expression += "*e"
		} else {
			calc.expression += "e"
		}
	case "sin", "cos", "tan":
		calcWrap(label)
	}
}

func calcInsertX() {
	if calc.hasResult {
		calc.expression = ""
		calc.hasResult = false
	}
	calc.expression += "x"
	calc.display = calc.expression
}

func calcBackspace() {
	calc.expression = trimLastRune(calc.expression)
	if calc.expression == "" {
		calc.display = "0"
		calc.hasResult = true
	}
}

func calcClear() {
	calc.expression = ""
	calc.display = "0"
	calc.result = ""
	calc.hasResult = true
	calc.graphCached = false
}

func calcToggleGraph() {
	calc.graphMode = !calc.graphMode
}

func calcEvaluate() {
	expr := calc.expression
	if expr == "" {
		return
	}
	// Expand the named constants only. sqrt, ^ and the trig functions are
	// left untouched: calcEvalExpr parses them natively, and rewriting them
	// here (sqrt → math.Sqrt, ^2 → *1) turned valid input into errors or
	// wrong answers — sqrt(9) showed "Error" and (5)^2 showed 5.
	expr = calcExpandConstants(expr)

	val, err := calcEvalExpr(expr)
	if err != nil {
		calc.display = "Error"
		calc.result = ""
		calc.hasResult = true
		return
	}

	resultStr := fmt.Sprintf("%g", val)
	if val == float64(int64(val)) && math.Abs(val) < 1e15 {
		resultStr = fmt.Sprintf("%d", int64(val))
	}

	entry := calc.expression + " = " + resultStr
	// "=" pressed again on the same sum is not another calculation for the
	// trophies.
	repeat := len(calc.history) > 0 && calc.history[len(calc.history)-1] == entry
	calc.history = append(calc.history, entry)
	if len(calc.history) > 20 {
		calc.history = calc.history[len(calc.history)-20:]
	}
	calc.histIdx = len(calc.history)

	calc.result = resultStr
	calc.display = resultStr
	calc.hasResult = true
	if !repeat {
		trophyEvent(tkCalc)
	}
	PlaySuccessSound()
}

// calcEvalExpr is a simple recursive-descent parser for basic math.
func calcEvalExpr(expr string) (float64, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, fmt.Errorf("empty expression")
	}

	// Handle functions: sin, cos, tan, sqrt — only when the call's own bracket
	// closes the expression. "sqrt(4)+sqrt(9)" starts and ends like one call,
	// and taking it for one asked the parser for "4)+sqrt(9".
	for _, fn := range []string{"sin", "cos", "tan", "sqrt"} {
		if strings.HasPrefix(expr, fn+"(") && calcMatchingParen(expr, len(fn)) == len(expr)-1 {
			inner := expr[len(fn)+1 : len(expr)-1]
			val, err := calcEvalExpr(inner)
			if err != nil {
				return 0, err
			}
			switch fn {
			case "sin":
				return math.Sin(val), nil
			case "cos":
				return math.Cos(val), nil
			case "tan":
				return math.Tan(val), nil
			case "sqrt":
				return math.Sqrt(val), nil
			}
		}
	}

	// Handle + and - (left-to-right, lowest precedence).
	depth := 0
	for i := len(expr) - 1; i > 0; i-- {
		switch expr[i] {
		case ')':
			depth++
		case '(':
			depth--
		case '+', '-':
			// Not the sign of an exponent (1e+20): that belongs to its number.
			exponentSign := (expr[i-1] == 'e' || expr[i-1] == 'E') && i >= 2 &&
				((expr[i-2] >= '0' && expr[i-2] <= '9') || expr[i-2] == '.')
			if depth == 0 && !exponentSign && (expr[i-1] != '*' && expr[i-1] != '/' && expr[i-1] != '^') {
				left, err := calcEvalExpr(expr[:i])
				if err != nil {
					return 0, err
				}
				right, err := calcEvalExpr(expr[i+1:])
				if err != nil {
					return 0, err
				}
				if expr[i] == '+' {
					return left + right, nil
				}
				return left - right, nil
			}
		}
	}

	// Handle * and /.
	depth = 0
	for i := len(expr) - 1; i > 0; i-- {
		switch expr[i] {
		case ')':
			depth++
		case '(':
			depth--
		case '*', '/':
			if depth == 0 {
				left, err := calcEvalExpr(expr[:i])
				if err != nil {
					return 0, err
				}
				right, err := calcEvalExpr(expr[i+1:])
				if err != nil {
					return 0, err
				}
				if expr[i] == '*' {
					return left * right, nil
				}
				if right == 0 {
					return 0, fmt.Errorf("division by zero")
				}
				return left / right, nil
			}
		}
	}

	// Handle ^ (power). Checked after +,-,*,/ so the lowest-precedence
	// operator splits first, and depth-aware so a ^ inside parentheses is left
	// to the parentheses handler instead of splitting the group apart
	// (which used to make (3^4) a parse error).
	depth = 0
	for i := len(expr) - 1; i > 0; i-- {
		switch expr[i] {
		case ')':
			depth++
		case '(':
			depth--
		case '^':
			if depth == 0 {
				left, err := calcEvalExpr(expr[:i])
				if err != nil {
					return 0, err
				}
				right, err := calcEvalExpr(expr[i+1:])
				if err != nil {
					return 0, err
				}
				return math.Pow(left, right), nil
			}
		}
	}

	// Handle parentheses.
	if strings.HasPrefix(expr, "(") && calcMatchingParen(expr, 0) == len(expr)-1 {
		return calcEvalExpr(expr[1 : len(expr)-1])
	}

	// Handle unary minus.
	if strings.HasPrefix(expr, "-") {
		val, err := calcEvalExpr(expr[1:])
		if err != nil {
			return 0, err
		}
		return -val, nil
	}

	// Plain number.
	val, err := strconv.ParseFloat(expr, 64)
	if err != nil {
		return 0, fmt.Errorf("parse error: %s", expr)
	}
	return val, nil
}

// calcGraphSample is one evaluated point; invalid or discontinuous samples
// break the stroke instead of connecting across a pole or a domain error.
type calcGraphSample struct {
	x, y  float64
	valid bool
}

// calcEvalExprAtX evaluates the calculator's existing expression language with
// x bound to the requested value. Adjacent numeric factors such as 2x and x2
// are treated as multiplication; malformed or unsupported expressions still
// return the parser's ordinary error.
func calcEvalExprAtX(expression string, x float64) (float64, error) {
	if len(expression) > 512 {
		return 0, fmt.Errorf("expression is too long to graph")
	}
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0, fmt.Errorf("x must be finite")
	}
	value := strconv.FormatFloat(x, 'f', 12, 64)
	var expanded strings.Builder
	expanded.Grow(len(expression) + 16)
	for i := 0; i < len(expression); i++ {
		if expression[i] != 'x' {
			expanded.WriteByte(expression[i])
			continue
		}
		prevLetter := i > 0 && (expression[i-1] >= 'a' && expression[i-1] <= 'z' || expression[i-1] >= 'A' && expression[i-1] <= 'Z')
		nextLetter := i+1 < len(expression) && (expression[i+1] >= 'a' && expression[i+1] <= 'z' || expression[i+1] >= 'A' && expression[i+1] <= 'Z')
		if prevLetter || nextLetter {
			expanded.WriteByte('x')
			continue
		}
		if i > 0 {
			prev := expression[i-1]
			if (prev >= '0' && prev <= '9') || prev == '.' || prev == ')' {
				expanded.WriteByte('*')
			}
		}
		expanded.WriteByte('(')
		expanded.WriteString(value)
		expanded.WriteByte(')')
		if i+1 < len(expression) {
			next := expression[i+1]
			if (next >= '0' && next <= '9') || next == '.' || next == '(' {
				expanded.WriteByte('*')
			}
		}
	}
	// Expand only the two named constants, matching calcEvaluate.
	expr := calcExpandConstants(expanded.String())
	result, err := calcEvalExpr(expr)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0, fmt.Errorf("expression is not finite")
	}
	return result, nil
}

// calcGraphSamples evaluates a bounded number of points across the requested
// x-range. A fixed cap keeps graph redraws inexpensive on the Smart Pro.
func calcGraphSamples(expression string, minX, maxX float64, count int) []calcGraphSample {
	if count < 2 || count > 512 || math.IsNaN(minX) || math.IsNaN(maxX) ||
		math.IsInf(minX, 0) || math.IsInf(maxX, 0) || minX >= maxX {
		return nil
	}
	samples := make([]calcGraphSample, count)
	for i := range samples {
		x := minX + (maxX-minX)*float64(i)/float64(count-1)
		y, err := calcEvalExprAtX(expression, x)
		valid := err == nil && !math.IsNaN(y) && !math.IsInf(y, 0) && math.Abs(y) <= 1e6
		samples[i] = calcGraphSample{x: x, y: y, valid: valid}
	}
	return samples
}

func currentCalcGraphSamples() []calcGraphSample {
	if calc.graphCached && calc.graphExpression == calc.expression {
		return calc.graphSamples
	}
	calc.graphExpression = calc.expression
	calc.graphSamples = calcGraphSamples(calc.expression, -10, 10, 160)
	calc.graphCached = true
	return calc.graphSamples
}

func calcGraphYBounds(samples []calcGraphSample) (float64, float64, bool) {
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, sample := range samples {
		if !sample.valid {
			continue
		}
		if sample.y < minY {
			minY = sample.y
		}
		if sample.y > maxY {
			maxY = sample.y
		}
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

func drawCalcGraph(renderer *sdl.Renderer, config *Config, font *ttf.Font, panel sdl.Rect) {
	if panel.W < 100 || panel.H < 120 {
		return
	}
	if font != nil {
		renderText(renderer, config, font, "Graph: y = f(x)", ColorTextPrimary(), panel.X+12, panel.Y+10)
	}
	plot := sdl.Rect{X: panel.X + 42, Y: panel.Y + 48, W: panel.W - 56, H: panel.H - 92}
	if plot.W < 20 || plot.H < 20 {
		return
	}
	fillRoundedRect(renderer, plot.X, plot.Y, plot.W, plot.H, 8, ColorSurfaceRow)
	strokeRoundedRect(renderer, plot.X, plot.Y, plot.W, plot.H, 8, 1, ColorBorder)

	const minX, maxX = -10.0, 10.0
	samples := currentCalcGraphSamples()
	minY, maxY, ok := calcGraphYBounds(samples)
	if !ok {
		if font != nil {
			message := "Enter an expression with x"
			mw, _, _ := font.SizeUTF8(message)
			renderText(renderer, config, font, message, ColorTextSecondary(), plot.X+(plot.W-int32(mw))/2, plot.Y+plot.H/2-8)
		}
		return
	}

	// Five quiet grid lines keep the small handheld plot readable without
	// crowding it with labels.
	grid := WithAlpha(ColorBorderSubtle, 100)
	for i := int32(1); i < 4; i++ {
		x := plot.X + plot.W*i/4
		y := plot.Y + plot.H*i/4
		renderer.SetDrawColor(grid.R, grid.G, grid.B, grid.A)
		_ = renderer.DrawLine(x, plot.Y+1, x, plot.Y+plot.H-2)
		_ = renderer.DrawLine(plot.X+1, y, plot.X+plot.W-2, y)
	}
	if minY <= 0 && maxY >= 0 {
		y := plot.Y + plot.H - 1 - int32((-minY)/(maxY-minY)*float64(plot.H-1))
		renderer.SetDrawColor(130, 145, 165, 210)
		_ = renderer.DrawLine(plot.X+1, y, plot.X+plot.W-2, y)
	}
	if minX <= 0 && maxX >= 0 {
		x := plot.X + int32((-minX)/(maxX-minX)*float64(plot.W-1))
		renderer.SetDrawColor(130, 145, 165, 210)
		_ = renderer.DrawLine(x, plot.Y+1, x, plot.Y+plot.H-2)
	}

	for i := 1; i < len(samples); i++ {
		x1, y1, ok1 := calcGraphMapPoint(samples[i-1], minX, maxX, minY, maxY, plot)
		x2, y2, ok2 := calcGraphMapPoint(samples[i], minX, maxX, minY, maxY, plot)
		if !ok1 || !ok2 || abs(int(y2-y1)) > int(plot.H/3) || math.Abs(samples[i].y-samples[i-1].y) > (maxY-minY)*0.5 {
			continue
		}
		renderer.SetDrawColor(accentColor.R, accentColor.G, accentColor.B, 255)
		_ = renderer.DrawLine(x1, y1, x2, y2)
	}
	if font != nil {
		bounds := fmt.Sprintf("x: %.0f to %.0f", minX, maxX)
		renderText(renderer, config, font, bounds, ColorTextTertiary(), panel.X+12, plot.Y+plot.H+8)
	}
}

// renderCalculator draws the full-screen calculator scene.
func renderCalculator(renderer *sdl.Renderer, config *Config) {
	// Home's backdrop, the one every screen stands on (ui_glass.go).
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)

	font, _ := getCachedFont(config, "medium")
	smallFont, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	// The display and the key grid form one block, centred in the space
	// between the shared header and the shared footer. The old fixed origin
	// (calcX0/calcY0) put the display under the 56px bar and pushed the history
	// column past the right edge of the Brick's 960px canvas.
	const histW = int32(300)
	const histGap = int32(40)
	displayW := int32(calcBtnW*calcCols + calcGap*(calcCols-1))
	displayH := int32(80)
	gridH := int32(calcBtnH*calcRows + calcGap*(calcRows-1))
	blockW := displayW + histGap + histW
	blockH := displayH + 20 + gridH

	blockX := (screenWidth - blockW) / 2
	if blockX < 16 {
		blockX = 16
	}
	blockY := headerBarHeight() + (screenHeight-footerBarHeight()-headerBarHeight()-blockH)/2
	if blockY < headerBarHeight()+8 {
		blockY = headerBarHeight() + 8
	}
	displayX := blockX
	displayY := blockY
	gridX := blockX
	gridY := blockY + displayH + 20

	// Display background.
	drawCard(renderer, displayX, displayY, displayW, displayH, 16)

	// Expression.
	exprText := calc.expression
	if exprText == "" {
		exprText = " "
	}
	calcGraphButtonRect = sdl.Rect{X: displayX + displayW - 98, Y: displayY + 8, W: 86, H: 30}
	calcXButtonRect = sdl.Rect{X: displayX + displayW - 142, Y: displayY + 8, W: 36, H: 30}
	if smallFont != nil {
		exprWidth := calcXButtonRect.X - displayX - 24
		renderText(renderer, config, smallFont, ellipsize(smallFont, exprText, exprWidth),
			sdl.Color{R: 120, G: 135, B: 160, A: 200}, displayX+16, displayY+12)
	}
	UIButton(renderer, config, calcXButtonRect, "x", UIButtonStyle{
		Variant: UIButtonSecondary, LabelColor: ColorTextPrimary(), Font: font,
		Radius: 8, NoShadow: true,
	}, UIButtonState{})
	graphLabel := "Graph"
	if calc.graphMode {
		graphLabel = "Calc"
	}
	UIButton(renderer, config, calcGraphButtonRect, graphLabel, UIButtonStyle{
		Variant: UIButtonSecondary, LabelColor: ColorTextPrimary(), Font: smallFont,
		Radius: 8, NoShadow: true,
	}, UIButtonState{Focused: calc.graphMode})

	// Result / display value.
	dispText := calc.display
	dispText = truncateRunesSuffix(dispText, 30, "...")
	dw, _, _ := font.SizeUTF8(dispText)
	dx := displayX + displayW - int32(dw) - 16
	dy := displayY + 40
	renderText(renderer, config, font, dispText, ColorTextPrimary(), dx, dy)

	// Button grid.
	for row := 0; row < calcRows; row++ {
		for col := 0; col < calcCols; col++ {
			btn := calcButtons[row][col]
			bx := gridX + int32(col*(calcBtnW+calcGap))
			by := gridY + int32(row*(calcBtnH+calcGap))
			focused := calc.cursor[0] == row && calc.cursor[1] == col
			buttonRect := sdl.Rect{X: bx, Y: by, W: int32(calcBtnW), H: int32(calcBtnH)}
			calcButtonRects[row][col] = buttonRect

			// Key style. The calculator keeps its own semantics — equals is the
			// primary (accent) key, clear/back are danger-tinted, operators are
			// accent ink on glass — while sharing Home's glass, ring and lift
			// with every other button.
			style := UIButtonStyle{Variant: UIButtonSecondary, LabelColor: ColorTextPrimary(), Radius: 10}
			switch btn.action {
			case "op":
				style.LabelColor = ColorTextAccent()
			case "eq":
				style = UIButtonStyle{Variant: UIButtonPrimary, Fill: glassAccent(), Radius: 10}
			case "clear", "back":
				style = UIButtonStyle{
					Variant:    UIButtonCustom,
					Fill:       WithAlpha(ColorDanger, 70),
					HoverFill:  WithAlpha(ColorDanger, 90),
					FocusFill:  WithAlpha(ColorDanger, 100),
					LabelColor: DangerInk(),
					Radius:     10,
				}
			}

			// Label.
			btnFont := font
			if len(btn.label) > 2 {
				btnFont, _ = getCachedFont(config, "small")
				if btnFont == nil {
					btnFont = font
				}
			}
			style.Font = btnFont
			UIButton(renderer, config, buttonRect, btn.label, style, UIButtonState{Focused: focused})
		}
	}

	// History panel on the right, the same height as the display plus grid.
	histX := gridX + displayW + histGap
	histY := blockY
	histH := blockH

	drawCard(renderer, histX, histY, histW, histH, 16)

	if calc.graphMode {
		drawCalcGraph(renderer, config, smallFont, sdl.Rect{X: histX, Y: histY, W: histW, H: histH})
	} else if smallFont != nil && len(calc.history) > 0 {
		// Show last 8 history entries.
		start := len(calc.history) - 8
		if start < 0 {
			start = 0
		}
		hTitle := "History"
		renderText(renderer, config, smallFont, hTitle,
			WithAlpha(ColorTextTertiary(), 200), histX+12, histY+8)

		for i, h := range calc.history[start:] {
			line := h
			line = truncateRunes(line, 28)
			ly := histY + 30 + int32(i)*24
			if ly+24 > histY+histH {
				break
			}
			renderText(renderer, config, smallFont, line,
				sdl.Color{R: 120, G: 135, B: 160, A: 180}, histX+12, ly)
		}
	} else if smallFont != nil {
		empty := "No calculations yet"
		ew, _, _ := smallFont.SizeUTF8(empty)
		renderText(renderer, config, smallFont, empty,
			sdl.Color{R: 80, G: 95, B: 120, A: 150}, histX+(histW-int32(ew))/2, histY+histH/2)
	}

	// The shared bars: the calculator used to be the only full-screen scene
	// with no header at all, so it read as a separate app.
	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
}

// handleCalcInput processes controller/keyboard input in the Calculator scene.
func handleCalcInput(e *sdl.KeyboardEvent) {
	if e == nil || e.Type != sdl.KEYDOWN {
		return
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		calcNavigate(-1, 0)
	case sdl.K_DOWN:
		calcNavigate(1, 0)
	case sdl.K_LEFT:
		calcNavigate(0, -1)
	case sdl.K_RIGHT:
		calcNavigate(0, 1)
	case sdl.K_RETURN, sdl.K_SPACE:
		calcSelect()
	case sdl.K_g, sdl.K_y:
		// Y as well as G: the footer's "Y Graph" names the pad's key.
		calcToggleGraph()
	case sdl.K_x:
		calcInsertX()
	case sdl.K_c:
		calcClear()
	case sdl.K_BACKSPACE, sdl.K_DELETE:
		calcBackspace()
	case sdl.K_ESCAPE:
		// The footer's Back (B on the pad, Esc on a keyboard): the scene's
		// handleKey consumes every key, so nothing else would leave.
		goBackScene(appConfig)
	}
}

// Scene controller mapping, moved here from controller.go so the scene owns
// its own input.

func ctrlCalculator(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		calcNavigate(-1, 0)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		calcNavigate(1, 0)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		calcNavigate(0, -1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		calcNavigate(0, 1)
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_A:
		calcSelect()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_X:
		calcInsertX()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_Y:
		calcToggleGraph()
		PlayActivateSound()
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		calcClear()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		calcBackspace()
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(appConfig)
	default:
		return false
	}
	return true
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
	if pointInRect(mx, my, calcGraphButtonRect) {
		calcToggleGraph()
		PlayActivateSound()
		return true
	}
	if pointInRect(mx, my, calcXButtonRect) {
		calcInsertX()
		PlayActivateSound()
		return true
	}
	if !calc.graphMode {
		for row := range calcButtonRects {
			for col, rect := range calcButtonRects[row] {
				if !pointInRect(mx, my, rect) {
					continue
				}
				calc.cursor = [2]int{row, col}
				calcSelect()
				PlayActivateSound()
				return true
			}
		}
	}
	return false
}

func init() { registerScene(calculatorScene{sceneBase{name: "Calculator"}}) }

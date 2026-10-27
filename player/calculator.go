package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
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
	expression string
	display    string
	cursor     [2]int // [row, col]
	history    []string
	histIdx    int
	result     string
	hasResult  bool
}

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
	case "back":
		calc.expression = trimLastRune(calc.expression)
		if calc.expression == "" {
			calc.display = "0"
			calc.hasResult = true
		}
	case "eq":
		calcEvaluate()
	}
	if calc.expression != "" && !calc.hasResult {
		calc.display = calc.expression
	}
}

func calcHandleFunc(label string) {
	switch label {
	case "+/-":
		if calc.expression != "" {
			if strings.HasPrefix(calc.expression, "-") {
				calc.expression = calc.expression[1:]
			} else {
				calc.expression = "-" + calc.expression
			}
		}
	case "%":
		calc.expression += "/100"
	case "sqrt":
		calc.expression = "sqrt(" + calc.expression + ")"
	case "^2":
		calc.expression = "(" + calc.expression + ")^2"
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
		} else {
			calc.expression += "e"
		}
	case "sin":
		calc.expression = "sin(" + calc.expression + ")"
	case "cos":
		calc.expression = "cos(" + calc.expression + ")"
	case "tan":
		calc.expression = "tan(" + calc.expression + ")"
	}
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
	expr = strings.ReplaceAll(expr, "pi", fmt.Sprintf("%f", math.Pi))
	expr = strings.ReplaceAll(expr, "e", fmt.Sprintf("%f", math.E))

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

	calc.history = append(calc.history, calc.expression+" = "+resultStr)
	if len(calc.history) > 20 {
		calc.history = calc.history[len(calc.history)-20:]
	}
	calc.histIdx = len(calc.history)

	calc.result = resultStr
	calc.display = resultStr
	calc.hasResult = true
	PlaySuccessSound()
}

// calcEvalExpr is a simple recursive-descent parser for basic math.
func calcEvalExpr(expr string) (float64, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, fmt.Errorf("empty expression")
	}

	// Handle functions: sin, cos, tan, sqrt.
	for _, fn := range []string{"sin", "cos", "tan", "sqrt"} {
		if strings.HasPrefix(expr, fn+"(") && strings.HasSuffix(expr, ")") {
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
			if depth == 0 && (expr[i-1] != '*' && expr[i-1] != '/' && expr[i-1] != '^') {
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
	if strings.HasPrefix(expr, "(") && strings.HasSuffix(expr, ")") {
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

// renderCalculator draws the full-screen calculator scene.
func renderCalculator(renderer *sdl.Renderer, config *Config) {
	renderer.SetDrawColor(8, 10, 18, 255)
	renderer.Clear()

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
	if smallFont != nil {
		renderText(renderer, config, smallFont, exprText,
			sdl.Color{R: 120, G: 135, B: 160, A: 200}, displayX+16, displayY+12)
	}

	// Result / display value.
	dispText := calc.display
	dispText = truncateRunesSuffix(dispText, 30, "...")
	dw, _, _ := font.SizeUTF8(dispText)
	dx := displayX + displayW - int32(dw) - 16
	dy := displayY + 40
	renderText(renderer, config, font, dispText,
		sdl.Color{R: 240, G: 245, B: 255, A: 255}, dx, dy)

	// Button grid.
	for row := 0; row < calcRows; row++ {
		for col := 0; col < calcCols; col++ {
			btn := calcButtons[row][col]
			bx := gridX + int32(col*(calcBtnW+calcGap))
			by := gridY + int32(row*(calcBtnH+calcGap))
			focused := calc.cursor[0] == row && calc.cursor[1] == col

			// Button fill. The calculator keeps its own semantic colours
			// (equals is blue, clear/back are red) while sharing the shape,
			// sheen, border and focus ring with every other button.
			fill := ColorCard
			if btn.action == "eq" {
				fill = sdl.Color{R: 80, G: 160, B: 255, A: 255}
			}
			if btn.action == "clear" || btn.action == "back" {
				fill = sdl.Color{R: 60, G: 30, B: 30, A: 255}
			}

			// Label.
			btnFont := font
			if len(btn.label) > 2 {
				btnFont, _ = getCachedFont(config, "small")
				if btnFont == nil {
					btnFont = font
				}
			}
			textCol := ColorTextPrimary()
			if btn.action == "op" {
				textCol = sdl.Color{R: 140, G: 200, B: 255, A: 255}
			}
			if btn.action == "eq" {
				textCol = sdl.Color{R: 255, G: 255, B: 255, A: 255}
			}
			UIButton(renderer, config, sdl.Rect{X: bx, Y: by, W: int32(calcBtnW), H: int32(calcBtnH)}, btn.label, UIButtonStyle{
				Variant:    UIButtonCustom,
				Fill:       fill,
				HoverFill:  fill,
				FocusFill:  fill,
				Border:     ColorBorder,
				LabelColor: textCol,
				Font:       btnFont,
				Radius:     10,
				NoShadow:   true,
			}, UIButtonState{Focused: focused})
		}
	}

	// History panel on the right, the same height as the display plus grid.
	histX := gridX + displayW + histGap
	histY := blockY
	histH := blockH

	drawCard(renderer, histX, histY, histW, histH, 16)

	if smallFont != nil && len(calc.history) > 0 {
		// Show last 8 history entries.
		start := len(calc.history) - 8
		if start < 0 {
			start = 0
		}
		hTitle := "History"
		renderText(renderer, config, smallFont, hTitle,
			sdl.Color{R: 160, G: 175, B: 200, A: 200}, histX+12, histY+8)

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
	case sdl.K_c:
		calc.expression = ""
		calc.display = "0"
		calc.result = ""
		calc.hasResult = true
	case sdl.K_BACKSPACE, sdl.K_x:
		calc.expression = trimLastRune(calc.expression)
		if calc.expression == "" {
			calc.display = "0"
			calc.hasResult = true
		}
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
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		calcHandleFunc("C")
		PlayNavSound()
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		calcHandleFunc("<")
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

func init() { registerScene(calculatorScene{sceneBase{name: "Calculator"}}) }

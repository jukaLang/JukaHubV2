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
	calcX0   = 340
	calcY0   = 140
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
		if len(calc.expression) > 0 {
			calc.expression = calc.expression[:len(calc.expression)-1]
		}
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
	// Expand special symbols.
	expr = strings.ReplaceAll(expr, "pi", fmt.Sprintf("%f", math.Pi))
	expr = strings.ReplaceAll(expr, "e", fmt.Sprintf("%f", math.E))
	expr = strings.ReplaceAll(expr, "sqrt", "math.Sqrt")
	expr = strings.ReplaceAll(expr, "^2", "*1") // handled below

	// Handle ^N exponents.
	if idx := strings.Index(expr, "^"); idx >= 0 {
		parts := strings.SplitN(expr[idx+1:], " ", 2)
		if len(parts) > 0 {
			base := expr[:idx]
			exp := 0
			fmt.Sscanf(parts[0], "%d", &exp)
			val := 1.0
			fmt.Sscanf(base, "%f", &val)
			result := 1.0
			for i := 0; i < exp; i++ {
				result *= val
			}
			expr = fmt.Sprintf("%f", result)
		}
	}

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

	// Handle ^ (power).
	if idx := strings.LastIndex(expr, "^"); idx > 0 {
		left, err := calcEvalExpr(expr[:idx])
		if err != nil {
			return 0, err
		}
		right, err := calcEvalExpr(expr[idx+1:])
		if err != nil {
			return 0, err
		}
		return math.Pow(left, right), nil
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

	// Display area.
	displayW := int32(calcBtnW*calcCols + calcGap*(calcCols-1))
	displayH := int32(80)
	displayX := int32(calcX0)
	displayY := int32(calcY0) - displayH - 20

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
	if len(dispText) > 30 {
		dispText = dispText[:30] + "..."
	}
	dw, _, _ := font.SizeUTF8(dispText)
	dx := displayX + displayW - int32(dw) - 16
	dy := displayY + 40
	renderText(renderer, config, font, dispText,
		sdl.Color{R: 240, G: 245, B: 255, A: 255}, dx, dy)

	// Button grid.
	for row := 0; row < calcRows; row++ {
		for col := 0; col < calcCols; col++ {
			btn := calcButtons[row][col]
			bx := int32(calcX0 + col*(calcBtnW+calcGap))
			by := int32(calcY0 + row*(calcBtnH+calcGap))
			focused := calc.cursor[0] == row && calc.cursor[1] == col

			// Button fill.
			fill := ColorCard
			if focused {
				fill = ColorCardFocus
			}
			if btn.action == "eq" {
				fill = sdl.Color{R: 80, G: 160, B: 255, A: 255}
				if focused {
					fill = sdl.Color{R: 100, G: 180, B: 255, A: 255}
				}
			}
			if btn.action == "clear" || btn.action == "back" {
				fill = sdl.Color{R: 60, G: 30, B: 30, A: 255}
				if focused {
					fill = sdl.Color{R: 80, G: 40, B: 40, A: 255}
				}
			}

			fillRoundedRect(renderer, bx, by, int32(calcBtnW), int32(calcBtnH), 10, fill)
			if focused {
				strokeRoundedRect(renderer, bx, by, int32(calcBtnW), int32(calcBtnH), 10, 3, ColorAccent)
			} else {
				strokeRoundedRect(renderer, bx, by, int32(calcBtnW), int32(calcBtnH), 10, 1, ColorBorder)
			}

			// Label.
			btnFont := font
			if len(btn.label) > 2 {
				btnFont, _ = getCachedFont(config, "small")
				if btnFont == nil {
					btnFont = font
				}
			}
			bw, bh, _ := btnFont.SizeUTF8(btn.label)
			tx := bx + (int32(calcBtnW)-int32(bw))/2
			ty := by + (int32(calcBtnH)-int32(bh))/2
			textCol := ColorTextPrimary()
			if btn.action == "op" {
				textCol = sdl.Color{R: 140, G: 200, B: 255, A: 255}
			}
			if btn.action == "eq" {
				textCol = sdl.Color{R: 255, G: 255, B: 255, A: 255}
			}
			renderText(renderer, config, btnFont, btn.label, textCol, tx, ty)
		}
	}

	// History panel on the right.
	histX := int32(calcX0 + calcBtnW*calcCols + calcGap*calcCols + 40)
	histY := int32(calcY0) - 100
	histW := int32(300)
	histH := int32(calcBtnH*calcRows + calcGap*(calcRows-1))

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
			if len(line) > 28 {
				line = line[:28] + "…"
			}
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

	// Controls hint.
	if smallFont != nil {
		controls := "D-Pad: Navigate  |  A: Select  |  C: Clear"
		chw, _, _ := smallFont.SizeUTF8(controls)
		chx := (screenWidth - int32(chw)) / 2
		chy := screenHeight - 40
		renderText(renderer, config, smallFont, controls,
			sdl.Color{R: 90, G: 105, B: 130, A: 140}, chx, chy)
	}
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
		if len(calc.expression) > 0 {
			calc.expression = calc.expression[:len(calc.expression)-1]
		}
		if calc.expression == "" {
			calc.display = "0"
			calc.hasResult = true
		}
	}
}

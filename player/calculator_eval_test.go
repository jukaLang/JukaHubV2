package main

import (
	"math"
	"testing"
)

// TestCalcEvalExprPrecedence pins the parser's operator precedence and
// grouping. Every case here was wrong at some point: ^ was checked before
// +,-,*,/ so 2*3^2 evaluated as (2*3)^2 = 36, and ^ was not depth-aware so
// (3^4) split inside the parens into "(3" | "4)" and failed to parse.
func TestCalcEvalExprPrecedence(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want float64
	}{
		{"sqrt function", "sqrt(9)", 3},
		{"sin function", "sin(0)", 0},
		{"cos function", "cos(0)", 1},
		{"tan function", "tan(0)", 0},
		{"square of value", "(5)^2", 25},
		{"square of sum", "(3+4)^2", 49},
		{"plain power", "2^3", 8},
		{"power binds over multiply", "2*3^2", 18},
		{"power binds over add", "2+3^2", 11},
		{"multiply after power", "2^3*4", 32},
		{"power inside parens", "(3^4)", 81},
		{"power inside parens then multiply", "2*(3^4)", 162},
		{"negative exponent", "2^-3", 0.125},
		{"left to right add", "3+4", 7},
		{"divide", "10/4", 2.5},
		{"subtract", "9-4", 5},
		{"parens over precedence", "(2+3)*4", 20},
		{"nested parens", "((2+3))*4", 20},
		{"unary minus", "-5", -5},
		{"sum inside function", "sqrt(16+9)", 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := calcEvalExpr(c.expr)
			if err != nil {
				t.Fatalf("calcEvalExpr(%q) returned error: %v", c.expr, err)
			}
			if got != c.want {
				t.Errorf("calcEvalExpr(%q) = %v, want %v", c.expr, got, c.want)
			}
		})
	}
}

// TestCalcEvaluateButtonPath drives the real button path (calcEvaluate), which
// used to pre-rewrite the expression before handing it to calcEvalExpr —
// sqrt -> "math.Sqrt" made sqrt(9) show "Error", and ^2 -> "*1" made (5)^2
// show 5. The rewriting is gone; assert the buttons now report the right
// result through the state machine the UI reads.
func TestCalcEvaluateButtonPath(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want string
	}{
		{"sqrt button", "sqrt(9)", "3"},
		{"square button", "(5)^2", "25"},
		{"square of sum button", "(3+4)^2", "49"},
		{"multiply and power", "2*3^2", "18"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			initCalc()
			calc.expression = c.expr
			calc.hasResult = false
			calcEvaluate()
			if calc.display == "Error" {
				t.Fatalf("calcEvaluate(%q) reported Error", c.expr)
			}
			if calc.display != c.want {
				t.Errorf("calcEvaluate(%q) display = %q, want %q", c.expr, calc.display, c.want)
			}
			if calc.result != c.want {
				t.Errorf("calcEvaluate(%q) result = %q, want %q", c.expr, calc.result, c.want)
			}
		})
	}
}

// The constants keep full precision and the multiplication a user implies, a
// number's exponent is not the constant e, and a call is only a call when its
// own bracket closes the expression.
func TestCalcConstantsExponentsAndCalls(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want float64
	}{
		{"pi in full", "pi", math.Pi},
		{"implied 2pi", "2pi", 2 * math.Pi},
		{"pi times number", "pi2", math.Pi * 2},
		{"e alone", "e", math.E},
		{"result exponent reused", "1e+20*2", 2e20},
		{"small exponent", "5e-7+1", 1.0000005},
		{"two calls", "sqrt(4)+sqrt(9)", 5},
		{"two groups", "(1+2)*(3+4)", 21},
		{"trig sum", "sin(0)+cos(0)", 1},
	}
	for _, c := range cases {
		got, err := calcEvalExpr(calcExpandConstants(c.expr))
		if err != nil {
			t.Errorf("%s: %q failed: %v", c.name, c.expr, err)
			continue
		}
		if math.Abs(got-c.want) > 1e-9*math.Max(1, math.Abs(c.want)) {
			t.Errorf("%s: %q = %v, want %v", c.name, c.expr, got, c.want)
		}
	}
}

// After "=", a function key works on the result on screen: +/- after 2+2 is
// -4 (it used to negate the old expression, "-2+2" = 0).
func TestCalcFunctionKeysUseTheResultAfterEquals(t *testing.T) {
	prev := calc
	t.Cleanup(func() { calc = prev })

	initCalc()
	calc.expression, calc.hasResult = "2+2", false
	calcEvaluate()
	calcHandleFunc("+/-")
	calcEvaluate()
	if calc.result != "-4" {
		t.Fatalf("+/- after 2+2= gave %q, want -4", calc.result)
	}

	initCalc()
	calc.expression, calc.hasResult = "-2+3", false
	calcHandleFunc("+/-")
	calcEvaluate()
	if calc.result != "-1" {
		t.Fatalf("+/- on -2+3 gave %q, want -1", calc.result)
	}

	// On an empty line a function key opens the call for its operand.
	initCalc()
	calcHandleFunc("sqrt")
	if calc.expression != "sqrt(" {
		t.Fatalf("sqrt on an empty line = %q, want an open call", calc.expression)
	}
}

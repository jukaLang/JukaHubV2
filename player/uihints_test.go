package main

// UI hint consistency.
//
// Every overlay panel used to write its own hint line, so the same button was
// advertised as "A: open", "A open", "A: Open" and "A  play" on different
// screens. hintLine is now the only formatter; these tests pin its output and
// fail when a hand-punctuated hint literal reappears in a non-test source.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestHintLineFormat(t *testing.T) {
	cases := []struct {
		name  string
		hints []FooterHint
		want  string
	}{
		{
			name:  "button then label, three spaces between groups",
			hints: []FooterHint{{Button: "A", Label: "Open"}, {Button: "B", Label: "Close"}},
			want:  "A Open   B Close",
		},
		{
			name:  "multi-key buttons keep their spelling",
			hints: []FooterHint{{Button: "A / Enter", Label: "Play"}, {Button: "↑ ↓", Label: "Choose"}},
			want:  "A / Enter Play   ↑ ↓ Choose",
		},
		{
			name:  "label-only groups are kept verbatim",
			hints: []FooterHint{{Label: "Vol 55%"}, {Button: "A", Label: "Next"}},
			want:  "Vol 55%   A Next",
		},
		{
			name:  "button-only groups render the key alone",
			hints: []FooterHint{{Button: "B"}},
			want:  "B",
		},
		{
			name:  "blank entries never leave double separators",
			hints: []FooterHint{{}, {Button: " ", Label: "  "}, {Button: "B", Label: "Back"}},
			want:  "B Back",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hintLine(tc.hints...); got != tc.want {
				t.Fatalf("hintLine() = %q, want %q", got, tc.want)
			}
		})
	}
}

// legacyHint matches the styles this pass removed: "A: open", "B: Close",
// "A: play · X save", and short button phrases padded with two or more spaces
// ("A play    B close", "B back"). The button token must be followed by the
// legacy separator, so prose such as "Boost: ..." is not flagged.
var legacyHint = regexp.MustCompile(
	`^(A|B|X|Y|D-Pad|SELECT|START|Enter|Esc|Del|Space|Up/Down|Left/Right|L/R|L / R)` +
		`( {2,}|: | [A-Za-z/ ]{1,14}( {2,}|: | · ))`)

// TestPanelHintsUseHintLine walks the non-test sources and rejects any string
// literal that still spells a controller hint by hand. Comments are ignored
// because they document the change rather than draw it.
func TestPanelHintsUseHintLine(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		checked++
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if legacyHint.MatchString(value) {
				pos := fset.Position(lit.Pos())
				t.Errorf("%s:%d: hand-written hint %q; build it with hintLine", pos.Filename, pos.Line, value)
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("no sources were checked")
	}
}

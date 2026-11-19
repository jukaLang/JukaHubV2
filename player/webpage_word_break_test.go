package main

import (
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestWebCSSBreaksLongWordsWithoutLosingText(t *testing.T) {
	const word = "supercalifragilisticexpialidociousuncharacteristically"
	for _, tc := range []struct{ name, css string }{
		{"overflow-wrap", "overflow-wrap:anywhere"},
		{"overflow-wrap-break-word", "overflow-wrap:break-word"},
		{"word-wrap-alias", "word-wrap:break-word"},
		{"word-break", "word-break:break-all"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHeadlessBrowser(t)
			_, layout := h.load("break_"+tc.name, `<style>.card{width:130px;`+tc.css+`}</style>`+
				`<div class="card"><a href="https://example.com/article">`+word+`</a></div>`)
			var got strings.Builder
			lines := 0
			for _, row := range layout.Rows {
				if len(row.Runs) == 0 {
					continue
				}
				lines++
				for _, run := range row.Runs {
					got.WriteString(run.Text)
					if run.Href != "https://example.com/article" {
						t.Fatalf("broken word lost its link: %q -> %q", run.Text, run.Href)
					}
					if run.Font != nil {
						width, _, err := run.Font.SizeUTF8(run.Text)
						if err != nil || int32(width) > run.BoxWidth {
							t.Fatalf("word part exceeds its card: %q width=%d box=%d err=%v", run.Text, width, run.BoxWidth, err)
						}
					}
				}
			}
			if lines < 2 || got.String() != word {
				t.Fatalf("word break used %d lines and kept %q, want all of %q", lines, got.String(), word)
			}
		})
	}
}

func TestWebBreakAllUsesAvailableSpaceOnCurrentLine(t *testing.T) {
	h := newHeadlessBrowser(t)
	_, layout := h.load("break_all_current_line", `<style>.card{width:180px;word-break:break-all}</style>`+
		`<div class="card">Start extraordinarycompatibility</div>`)
	for _, row := range layout.Rows {
		if len(row.Runs) == 0 {
			continue
		}
		if len(row.Runs) < 2 || row.Runs[0].Text != "Start" || !strings.HasPrefix("extraordinarycompatibility", row.Runs[1].Text) {
			t.Fatalf("break-all left unused space on the first line: %+v", row.Runs)
		}
		return
	}
	t.Fatal("no text rows")
}

func TestWebOverflowWrapAnywhereShrinksFlexMinContent(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	t.Cleanup(func() { screenWidth, screenHeight = oldW, oldH })
	legibleTestInitFonts(t)
	base := `<style>.row{display:flex;width:300px;gap:20px}` +
		`.long{flex:1 1 200px;background:#cc0000;OVERFLOW}` +
		`.short{flex:1 1 200px;background:#0000cc}` +
		`</style><div class="row"><div class="long">Supercalifragilisticexpialidocious</div>` +
		`<div class="short">Short</div></div>`
	widths := func(overflow string) (int32, int32) {
		source := strings.Replace(base, "OVERFLOW", overflow, 1)
		layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
		long := webTestBackgroundsOf(t, layout, sdl.Color{R: 0xcc, A: 255})
		short := webTestBackgroundsOf(t, layout, sdl.Color{B: 0xcc, A: 255})
		if len(long) != 1 || len(short) != 1 || long[0].Y != short[0].Y {
			t.Fatalf("flex cards were not laid out side by side: long=%v short=%v", long, short)
		}
		return long[0].W, short[0].W
	}
	normalLong, _ := widths("")
	anywhereLong, anywhereShort := widths("overflow-wrap:anywhere")
	if anywhereLong >= normalLong-40 || abs32(anywhereLong-anywhereShort) > 12 {
		t.Fatalf("anywhere did not lower flex min-content: normal=%d, anywhere=%d/%d", normalLong, anywhereLong, anywhereShort)
	}
	breakWordLong, _ := widths("overflow-wrap:break-word")
	if breakWordLong < normalLong-12 {
		t.Fatalf("break-word incorrectly lowered intrinsic min-content: normal=%d, break-word=%d", normalLong, breakWordLong)
	}
}

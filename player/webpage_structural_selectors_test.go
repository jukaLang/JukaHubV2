package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestWebFirstLastAndOnlyTypeSelectorsPaintTheRightSibling(t *testing.T) {
	h := newHeadlessBrowser(t)
	_, layout := h.load("structural_selectors", `<style>
		p:first-of-type{color:#ad0000}
		p:last-of-type{color:#0000ad}
		p:only-of-type{color:#00ad00}
		p:only-child{color:#ad00ad}
	</style>
	<div><p>FirstCard</p><span>Middle</span><p>LastCard</p></div>
	<div><p>SoloType</p><span>AnotherType</span></div>
	<div><p>SoloChild</p></div>`)
	want := map[string]sdl.Color{
		"FirstCard": {R: 0xad, A: 255},
		"LastCard":  {B: 0xad, A: 255},
		"SoloType":  {G: 0xad, A: 255},
		"SoloChild": {R: 0xad, B: 0xad, A: 255},
	}
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if color, ok := want[run.Text]; ok {
				if run.Color != color {
					t.Fatalf("%s painted %+v, want %+v", run.Text, run.Color, color)
				}
				delete(want, run.Text)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("selector targets were not painted: %v", want)
	}
}

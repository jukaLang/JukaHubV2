package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func webTestIsWhereLayout(t *testing.T, css, body string) *webLayout {
	t.Helper()
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)
	return parseWebPage(`<style>`+css+`</style>`+body, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
}

func TestWebWhereSelectorMatchesWithoutAddingSpecificity(t *testing.T) {
	css := `.card:where(#target){width:120px;height:100px;background:#cc0000}` +
		`.card{width:300px;background:#eeeeee}`
	layout := webTestIsWhereLayout(t, css, `<div class="card" id="target">Where card</div>`)
	where := webTestBackgroundsOf(t, layout, sdl.Color{R: 0xcc, G: 0, B: 0, A: 255})
	plain := webTestBackgroundsOf(t, layout, sdl.Color{R: 0xee, G: 0xee, B: 0xee, A: 255})
	if len(where) != 0 || len(plain) != 1 {
		t.Fatalf(":where() background did not lose the specificity tie: where=%d plain=%d", len(where), len(plain))
	}
	box := plain[0]
	t.Logf("where box=%+v", box)
	if abs32(box.W-458) > 6 {
		t.Fatalf(":where() rule added specificity: width=%d, want the .card 458", box.W)
	}
	if abs32(box.H-174) > 8 {
		t.Fatalf(":where() rule did not match: height=%d, want its 100px height (174 painted)", box.H)
	}
}

func TestWebIsSelectorUsesMostSpecificArgumentGeometry(t *testing.T) {
	css := `.card:is(#target,.other){width:120px;height:100px;background:#00cc00}` +
		`.card{width:300px;background:#eeeeee}`
	layout := webTestIsWhereLayout(t, css, `<div class="card" id="target">Is card</div>`)
	green := webTestBackgroundsOf(t, layout, sdl.Color{R: 0, G: 0xcc, B: 0, A: 255})
	if len(green) != 1 {
		t.Fatalf(":is() rule did not paint its box: got %d green boxes", len(green))
	}
	box := green[0]
	t.Logf("is box=%+v", box)
	if abs32(box.W-188) > 6 {
		t.Fatalf(":is() ignored its id argument's specificity: width=%d, want 188", box.W)
	}
	if abs32(box.H-174) > 8 {
		t.Fatalf(":is() rule did not match: height=%d, want 174", box.H)
	}
}

func TestWebIsSelectorSpecificityUsesNonMatchingArgument(t *testing.T) {
	css := `.card:is(#missing,.active){width:120px;background:#00cc00}` +
		`.card{width:300px;background:#eeeeee}`
	layout := webTestIsWhereLayout(t, css, `<div class="card active">Active card</div>`)
	green := webTestBackgroundsOf(t, layout, sdl.Color{R: 0, G: 0xcc, B: 0, A: 255})
	if len(green) != 1 {
		t.Fatalf(":is(.active) did not match the active card: %d green boxes", len(green))
	}
	if abs32(green[0].W-188) > 6 {
		t.Fatalf(":is() specificity = %d, want the id from #missing (188 painted)", green[0].W)
	}
}

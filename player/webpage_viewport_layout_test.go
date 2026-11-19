package main

// webpage_viewport_layout_test.go pins viewport-relative lengths to the
// 1024x768 handheld viewport. These units decide full-panel sections and fluid
// card widths; a parser that accepts the declaration but resolves it to zero
// collapses the geometry, so every assertion is on the painted box.

import (
	"math"
	"testing"
)

func TestWebCSSViewportUnitsResolveLayout(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	config := &Config{}
	scale := webBaseFontSize(config, "body") * browserZoomScale() / 16
	vw, vh := webMediaViewport()
	cases := []struct {
		name         string
		css          string
		wantW, wantH float64
		checkW       bool
		checkH       bool
		tolerance    int32
	}{
		{"vw width", `.box{width:25vw;height:20px;background:#eef}`, vw * 0.25, 0, true, false, 4},
		{"vh height", `.box{width:100px;height:50vh;background:#eef}`, 0, vh * 0.5, false, true, 30},
		{"svh height", `.box{width:100px;height:50svh;background:#eef}`, 0, vh * 0.5, false, true, 30},
		{"dvh calc", `.box{width:100px;height:calc(100dvh - 100px);background:#eef}`, 0, vh - 100, false, true, 24},
		{"clamp width", `.box{width:clamp(10vw,60vw,50vw);height:20px;background:#eef}`, vw * 0.5, 0, true, false, 4},
		{"min width", `.box{width:min(40vw,300px);height:20px;background:#eef}`, math.Min(vw*0.4, 300), 0, true, false, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := parseWebPage(`<style>`+tc.css+`</style><div class="box">Body</div>`, "", "https://example.com/")
			layout := doc.computeLayout(config, Element{Font: "body"}, 800)
			if len(layout.Backgrounds) == 0 {
				t.Fatal("viewport-sized block painted no background")
			}
			box := layout.Backgrounds[0].Rect
			// A background rect adds the reader's 8px edge allowance; a
			// viewport length is in CSS pixels, which the layout scales with
			// the body font exactly as every other authored length.
			wantW := int32(math.Round(tc.wantW*scale)) + 8
			wantH := int32(math.Round(tc.wantH * scale))
			if tc.checkW && abs32(box.W-wantW) > tc.tolerance {
				t.Fatalf("width = %d, want %d (±%d); box=%+v scale=%.3f", box.W, wantW, tc.tolerance, box, scale)
			}
			if tc.checkH && abs32(box.H-wantH) > tc.tolerance {
				t.Fatalf("height = %d, want %d (±%d); box=%+v scale=%.3f", box.H, wantH, tc.tolerance, box, scale)
			}
		})
	}
}

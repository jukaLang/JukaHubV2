package main

import (
	"math"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestWebPercentageHeightsUseDefiniteContainingBlock(t *testing.T) {
	oldW, oldH := screenWidth, screenHeight
	screenWidth, screenHeight = 1024, 768
	defer func() { screenWidth, screenHeight = oldW, oldH }()
	legibleTestInitFonts(t)

	for _, tc := range []struct {
		name, source, marker         string
		height, minHeight, maxHeight float64
	}{
		{"fixed parent", `<div style="height:240px"><div style="height:50%;background:#cc0000">Half</div></div>`, "Half", 120, 0, 0},
		{"border box parent", `<div style="height:240px;box-sizing:border-box;padding:20px 0"><div style="height:50%">Inset</div></div>`, "Inset", 100, 0, 0},
		{"nested percentages", `<div style="height:300px"><div style="height:50%"><div style="height:50%;background:#cc0000">Quarter</div></div></div>`, "Quarter", 75, 0, 0},
		{"height math", `<div style="height:240px"><div style="height:calc(50% - 10px);background:#cc0000">Math</div></div>`, "Math", 110, 0, 0},
		{"container replay", `<div style="height:240px"><div style="height:50%;width:10cqw;background:#cc0000">Replay</div></div>`, "Replay", 120, 0, 0},
		{"min and max math", `<div style="height:240px"><div style="min-height:min(50%, 100px);max-height:clamp(40px, 75%, 160px)">Bounds</div></div>`, "Bounds", 0, 100, 160},
		{"auto parent", `<div><div style="height:50%;min-height:50%;max-height:50%">Auto</div></div>`, "Auto", 0, 0, 0},
		{"root viewport", `<html style="height:100%"><body style="height:50%"><div>Root</div></body></html>`, "Root", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := parseWebPage(tc.source, "", "https://example.com/")
			style, ok := webTestStyleForText(doc, tc.marker)
			if !ok {
				t.Fatal("styled text was not parsed")
			}
			if math.Abs(style.Height-tc.height) > 0.01 || math.Abs(style.MinHeight-tc.minHeight) > 0.01 || math.Abs(style.MaxHeight-tc.maxHeight) > 0.01 {
				t.Fatalf("resolved heights = %.1f/%.1f/%.1f; want %.1f/%.1f/%.1f", style.Height, style.MinHeight, style.MaxHeight, tc.height, tc.minHeight, tc.maxHeight)
			}
			if tc.marker == "Half" || tc.marker == "Math" || tc.marker == "Replay" {
				layout := doc.computeLayout(&Config{}, Element{Font: "body"}, 800)
				boxes := webTestBackgroundsOf(t, layout, sdl.Color{R: 0xcc, A: 255})
				if len(boxes) != 1 {
					t.Fatalf("expected one percentage-height box, got %d", len(boxes))
				}
				want := int32(math.Round(tc.height * webViewportPixelScale()))
				if abs32(boxes[0].H-want) > 30 {
					t.Fatalf("painted height = %d, want %d (±30)", boxes[0].H, want)
				}
			}
			if tc.marker == "Root" {
				_, viewportH := webMediaViewport()
				var htmlHeight, bodyHeight float64
				for _, box := range doc.Boxes {
					if box.style.Height > htmlHeight {
						htmlHeight = box.style.Height
					}
					if box.style.Height > 0 && (bodyHeight == 0 || box.style.Height < bodyHeight) {
						bodyHeight = box.style.Height
					}
				}
				if math.Abs(htmlHeight-viewportH) > 0.01 || math.Abs(bodyHeight-viewportH/2) > 0.01 {
					t.Fatalf("html/body heights = %.1f/%.1f, viewport = %.1f", htmlHeight, bodyHeight, viewportH)
				}
			}
		})
	}
}

func TestWebBorderBoxHeightIncludesVerticalPadding(t *testing.T) {
	legibleTestInitFonts(t)
	paintedHeight := func(sizing string) int32 {
		doc := parseWebPage(`<p style="height:120px;padding:20px 0;box-sizing:`+sizing+`;background:#cc0000">Box</p>`, "", "https://example.com/")
		layout := doc.computeLayout(&Config{}, Element{Font: "body"}, 800)
		boxes := webTestBackgroundsOf(t, layout, sdl.Color{R: 0xcc, A: 255})
		if len(boxes) != 1 {
			t.Fatalf("%s: expected one painted box, got %d", sizing, len(boxes))
		}
		return boxes[0].H
	}
	border := paintedHeight("border-box")
	content := paintedHeight("content-box")
	scale := webViewportPixelScale()
	if want := int32(math.Round(120 * scale)); abs32(border-want) > 35 {
		t.Fatalf("border-box height = %d, want %d (±35)", border, want)
	}
	if want := int32(math.Round(160 * scale)); abs32(content-want) > 35 {
		t.Fatalf("content-box height = %d, want %d (±35)", content, want)
	}
	if content-border < 35 {
		t.Fatalf("vertical padding did not distinguish the two sizing modes: border-box=%d content-box=%d", border, content)
	}
}

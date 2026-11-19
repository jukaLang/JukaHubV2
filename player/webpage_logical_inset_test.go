package main

// webpage_logical_inset_test.go covers the two-axis logical inset shorthands
// used by overlay panels and badges. It asserts the painted float rectangle,
// not only the parsed style, so a shorthand that is accepted but ignored
// cannot pass.

import "testing"

func TestWebLogicalInsetShorthandsPositionFloat(t *testing.T) {
	style := webBaseStyle()
	applyWebDecls(&style, map[string]string{
		"position":     "absolute",
		"inset-block":  "20px 40px",
		"inset-inline": "30px 50px",
	})
	if !style.HasTop || !style.HasBottom || style.Top != 20 || style.Bottom != 40 {
		t.Fatalf("inset-block = top %.0f (%v), bottom %.0f (%v); want 20/40", style.Top, style.HasTop, style.Bottom, style.HasBottom)
	}
	if !style.HasLeft || !style.HasRight || style.Left != 30 || style.Right != 50 {
		t.Fatalf("inset-inline = left %.0f (%v), right %.0f (%v); want 30/50", style.Left, style.HasLeft, style.Right, style.HasRight)
	}

	legibleTestInitFonts(t)
	source := `<style>.stage{position:relative;height:300px}` +
		`.overlay{position:absolute;inset-block:20px 40px;inset-inline:30px 50px;` +
		`background:#eef}</style><div class="stage"><div class="overlay">Overlay</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	if len(layout.Floats) != 1 {
		t.Fatalf("logical inset produced %d floats, want 1", len(layout.Floats))
	}
	float := layout.Floats[0]
	if len(float.Backgrounds) == 0 {
		t.Fatal("logical inset float painted no background")
	}
	box := float.Backgrounds[0].Rect
	// The float origin is the page gutter plus inset-inline-start; the local
	// background extends 4px left of the content edge, so the painted left is
	// 51-4 = 47. The stretched width is the 788px measure minus the 30px and
	// 50px inline insets (scaled to 45/75), plus the 8px background edge.
	if abs32(float.X-51) > 8 {
		t.Errorf("inset-inline start = %d, want 51", float.X)
	}
	if abs32(float.X+box.X-47) > 8 {
		t.Errorf("painted inset-inline start = %d, want 47", float.X+box.X)
	}
	if abs32(box.W-676) > 12 {
		t.Errorf("width between inset-inline edges = %d, want 676", box.W)
	}
	if abs32(float.Y-30) > 8 {
		t.Errorf("inset-block start = %d, want 30", float.Y)
	}
}

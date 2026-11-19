package main

import "testing"

func webTestRunPosition(t *testing.T, layout *webLayout, target string) (int32, int32) {
	t.Helper()
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text == target {
				return run.X, row.Y
			}
		}
	}
	t.Fatalf("text %q was not laid out", target)
	return 0, 0
}

func TestWebFlexFlowShorthandControlsWrapping(t *testing.T) {
	h := newHeadlessBrowser(t)
	_, layout := h.load("flex_flow", `<style>
		.cards{display:flex;flex-flow:row wrap;width:190px}
		.card{width:140px;height:35px}
	</style><div class="cards"><div class="card">One</div><div class="card">Two</div></div>`)
	_, firstY := webTestRunPosition(t, layout, "One")
	_, secondY := webTestRunPosition(t, layout, "Two")
	if secondY <= firstY {
		t.Fatalf("flex-flow:row wrap did not wrap the second card: %d, %d", firstY, secondY)
	}
}

func TestWebPlaceContentShorthandPositionsGrid(t *testing.T) {
	legibleTestInitFonts(t)
	position := func(place string) (int32, int32) {
		source := `<style>.grid{display:grid;grid-template-columns:100px;grid-template-rows:50px;` +
			`width:400px;height:250px;place-content:` + place + `}</style>` +
			`<div class="grid"><div>Probe</div></div>`
		layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
		return webTestRunPosition(t, layout, "Probe")
	}
	startX, startY := position("start start")
	endX, centerY := position("center end")
	if endX-startX < 150 || centerY-startY < 80 {
		t.Fatalf("place-content did not align grid tracks: start=(%d,%d), center/end=(%d,%d)", startX, startY, endX, centerY)
	}
}

func TestWebSupportsNewLayoutAndTextProperties(t *testing.T) {
	for _, condition := range []string{
		"(flex-flow:row wrap)", "(place-content:center end)",
		"(overflow-wrap:anywhere)", "(word-break:break-all)",
		"(text-overflow:clip)",
	} {
		if !webSupportsApplies(condition) {
			t.Fatalf("supported condition %q did not match", condition)
		}
	}
	if webSupportsApplies("(overflow-wrap:scatter)") || webSupportsApplies("(flex-flow:diagonal)") {
		t.Fatal("an unsupported value matched @supports")
	}
}

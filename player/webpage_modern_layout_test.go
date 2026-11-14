package main

import (
	"math"
	"strings"
	"testing"
)

func TestWebGridAutoFitUsesTheMeasuredWidth(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:20px}</style>` +
		`<div class="cards"><div class="card">A</div><div class="card">B</div>` +
		`<div class="card">C</div><div class="card">D</div></div>`
	doc := parseWebPage(source, "", "https://example.com/")
	for _, block := range doc.Blocks {
		if block.FlexGroup != 0 && block.GridMin != 160 {
			t.Fatalf("grid track minimum = %g, want 160", block.GridMin)
		}
	}

	wide := doc.computeLayout(&Config{}, Element{Font: "body"}, 1600)
	aRow, aX := webTestTextPosition(t, wide, "A")
	bRow, bX := webTestTextPosition(t, wide, "B")
	cRow, cX := webTestTextPosition(t, wide, "C")
	dRow, dX := webTestTextPosition(t, wide, "D")
	if aRow != bRow || bRow != cRow || dRow <= aRow || dX != aX || !(aX < bX && bX < cX) {
		t.Fatalf("auto-fit grid at 1600: A=(%d,%d) B=(%d,%d) C=(%d,%d) D=(%d,%d)",
			aRow, aX, bRow, bX, cRow, cX, dRow, dX)
	}

	narrow := doc.computeLayout(&Config{}, Element{Font: "body"}, 420)
	aRow, _ = webTestTextPosition(t, narrow, "A")
	bRow, _ = webTestTextPosition(t, narrow, "B")
	cRow, _ = webTestTextPosition(t, narrow, "C")
	dRow, _ = webTestTextPosition(t, narrow, "D")
	if !(aRow < bRow && bRow < cRow && cRow < dRow) {
		t.Fatalf("auto-fit grid at 420 should collapse to one column: A=%d B=%d C=%d D=%d", aRow, bRow, cRow, dRow)
	}
}

func TestWebLengthSupportsModernMathFunctions(t *testing.T) {
	cases := []struct {
		value string
		rel   float64
		want  float64
	}{
		{"min(100%, 600px)", 800, 600},
		{"max(1rem, 24px)", 16, 24},
		{"clamp(12px, 1rem, 20px)", 16, 16},
		{"clamp(24px, 1rem, 20px)", 16, 24},
	}
	for _, tc := range cases {
		if got := webLength(tc.value, tc.rel); got != tc.want {
			t.Errorf("webLength(%q, %g) = %g, want %g", tc.value, tc.rel, got, tc.want)
		}
	}
}

func TestWebOrderReordersFlexItems(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>.row{display:flex;gap:10px}.first{order:2}.second{order:1}</style>` +
		`<div class="row"><div class="first">A</div><div class="second">B</div></div>`
	layout := parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	aRow, aX := webTestTextPosition(t, layout, "A")
	bRow, bX := webTestTextPosition(t, layout, "B")
	if aRow != bRow || bX >= aX {
		t.Fatalf("order should put B before A on one row: A=(%d,%d) B=(%d,%d)", aRow, aX, bRow, bX)
	}
}

func TestWebPlaceItemsSetsCrossAxisAlignment(t *testing.T) {
	style := webBaseStyle()
	applyWebDecls(&style, map[string]string{
		"display":     "grid",
		"place-items": "center",
	})
	if style.AlignItems != webItemCenter {
		t.Fatalf("place-items: center set AlignItems = %d, want %d", style.AlignItems, webItemCenter)
	}
}

func TestWebSrcsetPicksWidthNearestTarget(t *testing.T) {
	got := webSrcsetPick("small.png 320w, medium.png 800w, large.png 1600w", 960)
	if got != "large.png" {
		t.Fatalf("srcset picked %q, want the 1600w candidate for a 960px layout", got)
	}
	got = webSrcsetPick("small.png 1x, medium.png 2x, large.png 3x", 960)
	if got != "medium.png" {
		t.Fatalf("density srcset picked %q, want the 2x candidate", got)
	}
}

func TestWebImageUsesSrcsetCandidate(t *testing.T) {
	doc := parseWebPage(`<img src="fallback.png" srcset="small.png 320w, large.png 1280w" alt="photo">`, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/large.png"; got != want {
		t.Fatalf("image src %q, want srcset candidate %q", got, want)
	}
}

func TestWebSrcsetKeepsDataURIIntact(t *testing.T) {
	data := "data:image/png;base64,AAAA 1x"
	if got := webSrcsetPick(data, 960); got != "data:image/png;base64,AAAA" {
		t.Fatalf("srcset data URI = %q, want the full data URL", got)
	}
}

func TestWebPictureUsesFirstMatchingSource(t *testing.T) {
	source := `<picture>` +
		`<source media="all" srcset="wide.png 1200w">` +
		`<source media="print" srcset="narrow.png 400w">` +
		`<img src="fallback.png" alt="photo">` +
		`</picture>`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/wide.png"; got != want {
		t.Fatalf("picture image src %q, want first matching source %q", got, want)
	}
}

func TestWebPictureUsesFirstApplicableArtDirectionSource(t *testing.T) {
	source := `<picture>` +
		`<source media="(max-width: 600px)" srcset="small.png 320w">` +
		`<source media="(min-width: 700px)" srcset="medium.png 640w">` +
		`<img src="fallback.png" alt="photo">` +
		`</picture>`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/medium.png"; got != want {
		t.Fatalf("picture image src %q, want first applicable art-direction source %q", got, want)
	}
}

func TestWebPictureSkipsUnsupportedSourceType(t *testing.T) {
	source := `<picture>` +
		`<source type="image/avif" srcset="avif.png 640w">` +
		`<source type="image/png" srcset="png.png 640w">` +
		`<img src="fallback.png" alt="photo">` +
		`</picture>`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/png.png"; got != want {
		t.Fatalf("picture image src %q, want supported source %q", got, want)
	}
}

func TestWebPictureSourceSizesUseContainerUnits(t *testing.T) {
	// The first source must be skipped by media. If source filtering is
	// bypassed, 100cqw would select large.png and this test would catch it.
	source := `<div style="container-type:inline-size;width:500px">` +
		`<picture>` +
		`<source media="(max-width: 600px)" sizes="100cqw" ` +
		`srcset="wrong.png 100w, wide.png 800w">` +
		`<source media="(min-width: 1px)" sizes="50cqw" ` +
		`srcset="small.png 160w, medium.png 320w, large.png 800w">` +
		`<img src="fallback.png" alt="photo">` +
		`</picture></div>`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/medium.png"; got != want {
		t.Fatalf("picture source sizes selected %q, want container-resolved candidate %q", got, want)
	}
}

func TestWebPictureFallsBackToImg(t *testing.T) {
	source := `<picture>` +
		`<source media="print" srcset="print.png 400w">` +
		`<source type="image/avif" srcset="avif.png 400w">` +
		`<img src="fallback.png" srcset="fallback-large.png 1280w" alt="photo">` +
		`</picture>`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/fallback-large.png"; got != want {
		t.Fatalf("picture fallback src %q, want img srcset candidate %q", got, want)
	}
}

func TestWebSizesTargetPicksMatchingConditionAndFallback(t *testing.T) {
	cases := []struct {
		sizes string
		want  float64
	}{
		{"(min-width: 1200px) 400px, (min-width: 700px) 700px, 100vw", 400},
		{"(max-width: 600px) 480px, 800px", 800},
		{"(max-width: 600px) 480px, 100vw", 1280},
		{"calc(100vw - 20px)", 1260},
		{"calc((100vw - 40px) / 2)", 620},
		{"(min-width: 1200px) calc(50vw + 10px), 100vw", 650},
	}
	for _, tc := range cases {
		if got := webSizesTarget(tc.sizes, 1280, 720); got != tc.want {
			t.Errorf("webSizesTarget(%q) = %g, want %g", tc.sizes, got, tc.want)
		}
	}
}

func TestWebImageSizesSelectsCandidate(t *testing.T) {
	source := `<img src="fallback.png" srcset="small.png 320w, medium.png 640w, large.png 1280w" ` +
		`sizes="(max-width: 1px) 320px, (min-width: 1px) 500px, 1000px" alt="photo">`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/medium.png"; got != want {
		t.Fatalf("sizes selected %q, want resolved candidate %q", got, want)
	}
}

func TestWebSizesTargetUsesContainerUnits(t *testing.T) {
	cases := []struct {
		sizes string
		want  float64
	}{
		{"50cqw", 200},
		{"100cqi", 400},
		{"100cqh", 300},
		{"100cqb", 300},
		{"100cqmin", 300},
		{"100cqmax", 400},
		{"calc(50cqw - 20px)", 180},
		{"calc(50cqmax + 10px)", 210},
	}
	for _, tc := range cases {
		if got := webSizesTargetIn(tc.sizes, 1280, 720, 400, 300); got != tc.want {
			t.Errorf("webSizesTargetIn(%q) = %g, want %g", tc.sizes, got, tc.want)
		}
	}
	if got := webSizesTarget("50cqw", 1280, 720); got != 640 {
		t.Errorf("container units without a container = %g, want viewport fallback 640", got)
	}
}

func TestWebSizesTargetUsesCalcArithmetic(t *testing.T) {
	cases := []struct {
		sizes string
		vw    float64
		vh    float64
		cw    float64
		ch    float64
		want  float64
	}{
		{"calc(100vw + 20px)", 1280, 720, 1280, 720, 1300},
		{"calc(100vw - 20px)", 1280, 720, 1280, 720, 1260},
		{"calc(100vw / 2)", 1280, 720, 1280, 720, 640},
		{"calc(100vw * 0.5)", 1280, 720, 1280, 720, 640},
		{"calc((100vw - 100px) / 2 + 10px)", 1280, 720, 1280, 720, 600},
		{"calc(2 * (10em + 5px))", 1280, 720, 1280, 720, 330},
		{"calc(100vw - 10px - 20px)", 1280, 720, 1280, 720, 1250},
		{"calc(50cqw + 25cqh)", 1280, 720, 400, 300, 275},
		{"calc(2 * 50cqw - 10px)", 1280, 720, 400, 300, 390},
	}
	for _, tc := range cases {
		if got := webSizesTargetIn(tc.sizes, tc.vw, tc.vh, tc.cw, tc.ch); got != tc.want {
			t.Errorf("webSizesTargetIn(%q) = %g, want %g", tc.sizes, got, tc.want)
		}
	}
}

func TestWebSizesTargetUsesMinMaxClamp(t *testing.T) {
	cases := []struct {
		sizes string
		vw    float64
		vh    float64
		cw    float64
		ch    float64
		want  float64
	}{
		{"min(50vw, 600px)", 1280, 720, 1280, 720, 600},
		{"max(50vw, 600px)", 1280, 720, 1280, 720, 640},
		{"MAX(50VW, 600PX)", 1280, 720, 1280, 720, 640},
		{"clamp(300px, 50vw, 500px)", 1280, 720, 1280, 720, 500},
		{"clamp(200px, calc(50vw + 100px), 500px)", 1280, 720, 1280, 720, 500},
		{"calc(min(50vw, 600px) - 20px)", 1280, 720, 1280, 720, 580},
		{"min(calc(50vw - 20px), 600px)", 1280, 720, 1280, 720, 600},
		{"max(calc(50vw - 20px), 600px)", 1280, 720, 1280, 720, 620},
		{"min(50cqw, 30cqh)", 1280, 720, 400, 300, 90},
		{"clamp(100px, 50cqw, 300px)", 1280, 720, 400, 300, 200},
		{"clamp(10px, 50cqmax, 60px)", 1280, 720, 400, 300, 60},
		{"min(100vw, 100cqw)", 1280, 720, 400, 300, 400},
	}
	for _, tc := range cases {
		if got := webSizesTargetIn(tc.sizes, tc.vw, tc.vh, tc.cw, tc.ch); got != tc.want {
			t.Errorf("webSizesTargetIn(%q) = %g, want %g", tc.sizes, got, tc.want)
		}
	}
}

func TestWebCSSContainerUnitsResolveOrdinaryLengths(t *testing.T) {
	source := `<style>` +
		`.panel{container-type:inline-size;width:500px;height:300px}` +
		`.child{margin-left:10cqw;padding-left:5cqw;margin-top:4cqw;padding-top:1cqw;` +
		`margin-bottom:10cqh;padding-bottom:1em;margin-right:calc(50cqw - 20px);padding-right:var(--space);` +
		`--space:10cqw;text-indent:6cqw;font-size:2cqw;border-radius:4cqw;gap:3cqw;` +
		`flex-basis:50cqw;width:40cqw}` +
		`</style><div class="panel"><p class="child">Unit child</p></div>`
	doc := parseWebPage(source, "", "https://example.com/")
	var got *webBlock
	for i := range doc.Blocks {
		for _, run := range doc.Blocks[i].Runs {
			if strings.Contains(run.Text, "Unit child") {
				got = &doc.Blocks[i]
				break
			}
		}
	}
	if got == nil {
		t.Fatal("the container-unit paragraph was not drawn")
	}
	st := got.Style
	if st.Indent != 75 || st.IndentRight != 280 {
		t.Fatalf("cqw margins/padding = left %g right %g, want 75/280", st.Indent, st.IndentRight)
	}
	if st.MarginTop != 25 || st.MarginBot != 40 {
		t.Fatalf("cqw/cqh margins/padding = top %g bottom %g, want 25/40", st.MarginTop, st.MarginBot)
	}
	if st.TextIndent != 30 || st.FontPx != 10 || st.Radius != 20 {
		t.Fatalf("cqw text/font/radius = %g/%g/%g, want 30/10/20", st.TextIndent, st.FontPx, st.Radius)
	}
	if st.GapX != 15 || st.GapY != 15 || st.FlexBasis != 250 || st.Width != 200 {
		t.Fatalf("cqw gap/flex/width = %g/%g/%g/%g, want 15/15/250/200", st.GapX, st.GapY, st.FlexBasis, st.Width)
	}
}

func TestWebCSSContainerUnitsUseNearestContainerOrViewport(t *testing.T) {
	source := `<div style="container-type:inline-size;width:500px">` +
		`<div style="container-type:inline-size;width:300px">` +
		`<p style="margin-left:10cqw">Nearest</p></div></div>` +
		`<p style="margin-left:10cqw">Viewport</p>`
	doc := parseWebPage(source, "", "https://example.com/")
	indents := map[string]float64{}
	for _, block := range doc.Blocks {
		for _, run := range block.Runs {
			switch run.Text {
			case "Nearest", "Viewport":
				indents[run.Text] = block.Style.Indent
			}
		}
	}
	if indents["Nearest"] != 30 {
		t.Fatalf("nearest container cqw = %g, want 30", indents["Nearest"])
	}
	if indents["Viewport"] != 128 {
		t.Fatalf("cqw without a container = %g, want viewport fallback 128", indents["Viewport"])
	}
}

func webTestBlockText(b webBlock) string {
	var text strings.Builder
	for _, run := range b.Runs {
		text.WriteString(run.Text)
	}
	return text.String()
}

func TestWebCSSContainerUnitsUsePercentageContainerUsedWidth(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>` +
		`.wrap{width:50%}` +
		`.panel{container-type:inline-size;width:50%}` +
		`.child{margin-left:10cqw;padding-left:5cqi}` +
		`</style><div class="wrap"><div class="panel"><p class="child">Percentage child</p></div></div>`
	doc := parseWebPage(source, "", "https://example.com/")
	layout := doc.computeLayout(&Config{}, Element{Font: "body"}, 900)
	var got *webBlock
	for i := range doc.Blocks {
		if strings.Contains(webTestBlockText(doc.Blocks[i]), "Percentage child") {
			got = &doc.Blocks[i]
			break
		}
	}
	if got == nil || len(got.ContainerIDs) == 0 {
		t.Fatalf("percentage child did not keep its container ancestry: %#v", got)
	}
	size, ok := layout.ContainerSizes[got.ContainerIDs[len(got.ContainerIDs)-1]]
	if !ok || size.Width <= 0 {
		t.Fatalf("percentage container was not measured: %#v", layout.ContainerSizes)
	}
	// The parser models 50% of 50% of the 1280px viewport (320px); the page's
	// used content column is narrower, so a real browser resolves the child
	// against that measured width instead.
	if size.Width >= 320 {
		t.Fatalf("percentage container still uses the modelled viewport width: %g", size.Width)
	}
	want := size.Width * 15 / 100
	if math.Abs(got.Style.Indent-want) > 0.01 {
		t.Fatalf("10cqw + 5cqi = %g, want %g from used width %g", got.Style.Indent, want, size.Width)
	}
}

func TestWebCSSContainerUnitsUseContentSizedContainerUsedSize(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<style>` +
		`.row{display:flex;gap:10px}` +
		`.panel{container-type:inline-size}` +
		`.child{margin-left:10cqw;margin-top:1cqh;margin-bottom:1cqb}` +
		`</style><div class="row"><div class="panel"><p class="child">Panel content</p></div>` +
		`<div class="side">Side</div></div>`
	doc := parseWebPage(source, "", "https://example.com/")
	layout := doc.computeLayout(&Config{}, Element{Font: "body"}, 900)
	var got *webBlock
	for i := range doc.Blocks {
		if strings.Contains(webTestBlockText(doc.Blocks[i]), "Panel content") {
			got = &doc.Blocks[i]
			break
		}
	}
	if got == nil || len(got.ContainerIDs) == 0 {
		t.Fatalf("content-sized child did not keep its container ancestry: %#v", got)
	}
	size, ok := layout.ContainerSizes[got.ContainerIDs[len(got.ContainerIDs)-1]]
	if !ok || size.Width <= 0 || size.Height <= 0 {
		t.Fatalf("content-sized container was not measured: %#v", layout.ContainerSizes)
	}
	if want := size.Width * 10 / 100; math.Abs(got.Style.Indent-want) > 0.01 {
		t.Fatalf("10cqw = %g, want %g from used width %g", got.Style.Indent, want, size.Width)
	}
	if want := size.Height * 1 / 100; math.Abs(got.Style.MarginTop-want) > 0.01 {
		t.Fatalf("1cqh = %g, want %g from used height %g", got.Style.MarginTop, want, size.Height)
	}
	if want := size.Height * 1 / 100; math.Abs(got.Style.MarginBot-want) > 0.01 {
		t.Fatalf("1cqb = %g, want %g from used height %g", got.Style.MarginBot, want, size.Height)
	}
}

func TestWebImageSizesUsesNearestContainer(t *testing.T) {
	source := `<div style="container-type:inline-size;width:500px">` +
		`<img src="fallback.png" srcset="small.png 160w, medium.png 320w, large.png 800w" sizes="50cqw" alt="photo">` +
		`</div>`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/medium.png"; got != want {
		t.Fatalf("container sizes selected %q, want resolved candidate %q", got, want)
	}
}

func TestWebImageSizesContainerUnitsUseNestedContainer(t *testing.T) {
	source := `<div style="container-type:inline-size;width:600px">` +
		`<div style="container-type:inline-size;width:50%">` +
		`<img src="fallback.png" srcset="small.png 200w, medium.png 400w, large.png 800w" ` +
		`sizes="100cqw" alt="photo"></div></div>`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/medium.png"; got != want {
		t.Fatalf("nested container sizes selected %q, want resolved candidate %q", got, want)
	}
}

func TestWebImageSizesContainerUnitsWorkInsideMathFunctions(t *testing.T) {
	source := `<div style="container-type:inline-size;width:500px">` +
		`<img src="fallback.png" srcset="small.png 100w, medium.png 320w, large.png 640w" ` +
		`sizes="min(50cqw, 300px)" alt="photo"></div>`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/medium.png"; got != want {
		t.Fatalf("container-unit min() sizes selected %q, want resolved candidate %q", got, want)
	}
}

func TestWebImageSizesContainerUnitsFallBackToViewport(t *testing.T) {
	source := `<img src="fallback.png" srcset="small.png 320w, medium.png 640w, large.png 1280w" ` +
		`sizes="50cqw" alt="photo">`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/medium.png"; got != want {
		t.Fatalf("container units without a container selected %q, want viewport candidate %q", got, want)
	}
}

func TestWebImageSizesUsesContainerShorthand(t *testing.T) {
	source := `<div style="container:card / inline-size;width:600px">` +
		`<img src="fallback.png" srcset="small.png 200w, large.png 640w" sizes="50cqw" alt="photo">` +
		`</div>`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/large.png"; got != want {
		t.Fatalf("container shorthand selected %q, want resolved candidate %q", got, want)
	}
}

func TestWebImageSizesCalcSelectsCandidate(t *testing.T) {
	source := `<img src="fallback.png" srcset="small.png 320w, medium.png 640w, large.png 1280w" ` +
		`		sizes="(min-width: 1px) calc(1000px - 500px), 100vw" alt="photo">`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/medium.png"; got != want {
		t.Fatalf("calc sizes selected %q, want resolved candidate %q", got, want)
	}
}

func TestWebImageSizesClampSelectsCandidate(t *testing.T) {
	source := `<img src="fallback.png" srcset="small.png 320w, medium.png 640w, large.png 1280w" ` +
		`sizes="clamp(300px, 50vw, 500px)" alt="photo">`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/medium.png"; got != want {
		t.Fatalf("sizes selected %q, want resolved candidate %q", got, want)
	}
}

func TestWebImageSizesFallsBackToFinalLength(t *testing.T) {
	source := `<img src="fallback.png" srcset="small.png 320w, medium.png 640w, large.png 1280w" ` +
		`sizes="(max-width: 1px) 320px, 1000px" alt="photo">`
	doc := parseWebPage(source, "", "https://example.com/page")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got, want := doc.Blocks[0].ImageSrc, "https://example.com/large.png"; got != want {
		t.Fatalf("default sizes length selected %q, want resolved candidate %q", got, want)
	}
}

func TestWebImageFetchesSelectedSrcsetDataURI(t *testing.T) {
	const pixel = "data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw=="
	doc := parseWebPage(`<img src="fallback.png" srcset="`+pixel+` 1x" alt="pixel">`, "", "https://example.com/")
	if len(doc.Blocks) != 1 || !doc.Blocks[0].Image {
		t.Fatalf("expected one image block, got %#v", doc.Blocks)
	}
	if got := doc.Blocks[0].ImageSrc; got != pixel {
		t.Fatalf("selected image %q, want the srcset data URI", got)
	}
	fetchWebImages(doc)
	if len(doc.imageBytes(pixel)) == 0 {
		t.Fatal("fetchWebImages did not fetch the selected srcset data URI")
	}
}

func TestWebImageTypeSupport(t *testing.T) {
	if !webImageTypeSupported("image/png") {
		t.Fatal("image/png should be supported")
	}
	if webImageTypeSupported("image/avif") {
		t.Fatal("image/avif should not be treated as supported")
	}
}

func TestWebImageAspectRatioUsesCSSWidth(t *testing.T) {
	style := webBaseStyle()
	style.Width = 300
	style.AspectRatio = 16.0 / 9.0
	w, h := webImageDimsFromBlock(webBlock{Image: true, Style: style}, nil, 1, 900)
	if w != 300 {
		t.Fatalf("CSS width produced %d px, want 300", w)
	}
	if h != 168 {
		t.Fatalf("16:9 image with 300px width produced height %d, want 168", h)
	}
}

func TestWebInsetShorthandSetsEveryEdge(t *testing.T) {
	style := webBaseStyle()
	applyWebDecls(&style, map[string]string{
		"position": "absolute",
		"inset":    "10px 20px 30px 40px",
	})
	if style.Position != webPositionAbsolute {
		t.Fatalf("position = %d, want absolute", style.Position)
	}
	if !style.HasTop || !style.HasRight || !style.HasBottom || !style.HasLeft {
		t.Fatalf("inset did not set every edge: %+v", style)
	}
	if style.Top != 10 || style.Right != 20 || style.Bottom != 30 || style.Left != 40 {
		t.Fatalf("inset = %g %g %g %g, want 10 20 30 40", style.Top, style.Right, style.Bottom, style.Left)
	}

	zero := webBaseStyle()
	applyWebDecls(&zero, map[string]string{"inset": "0"})
	if !zero.HasTop || !zero.HasRight || !zero.HasBottom || !zero.HasLeft {
		t.Fatal("inset: 0 must pin every edge, even though each value is zero")
	}
}

func TestWebAutoMarginsCentreConstrainedBlocks(t *testing.T) {
	legibleTestInitFonts(t)
	parse := func(css string) *webLayout {
		source := `<style>` + css + `</style><p class="column">Centred text</p>`
		return parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 1000)
	}
	_, baseX := webTestTextPosition(t, parse(`.column{max-width:400px}`), "Centred")
	_, centredX := webTestTextPosition(t, parse(`.column{max-width:400px;margin:0 auto}`), "Centred")
	_, rightX := webTestTextPosition(t, parse(`.column{max-width:400px;margin-left:auto}`), "Centred")
	_, inlineX := webTestTextPosition(t, parse(`.column{max-width:400px;margin-inline:auto}`), "Centred")
	free := rightX - baseX
	center := centredX - baseX
	inline := inlineX - baseX
	if free < 100 {
		t.Fatalf("the 400px block should have free space beside it, got %dpx", free)
	}
	if math.Abs(float64(center)-float64(free)/2) > 20 {
		t.Fatalf("margin:0 auto moved the block %dpx, want half of the %dpx free space", center, free)
	}
	if math.Abs(float64(inline)-float64(center)) > 20 {
		t.Fatalf("margin-inline:auto moved the block %dpx, margin:0 auto moved it %dpx", inline, center)
	}
}

func TestWebLogicalFloatClearAndInsets(t *testing.T) {
	legibleTestInitFonts(t)
	style := webBaseStyle()
	applyWebDecls(&style, map[string]string{
		"position":           "absolute",
		"float":              "inline-end",
		"clear":              "inline-start",
		"inset-inline-start": "12px",
		"inset-block-end":    "0px",
	})
	if style.FloatSide != 2 || style.Clear != 1 {
		t.Fatalf("logical float/clear = %d/%d, want right/left", style.FloatSide, style.Clear)
	}
	if !style.HasLeft || style.Left != 12 {
		t.Fatalf("inset-inline-start = %t/%g, want a set 12px edge", style.HasLeft, style.Left)
	}
	if !style.HasBottom || style.Bottom != 0 {
		t.Fatalf("inset-block-end: 0 must pin the bottom edge, got set=%t value=%g", style.HasBottom, style.Bottom)
	}

	base := parseWebPage(`<div class="f">Float</div><p>Following</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	logical := parseWebPage(`<style>.f{float:inline-start;width:120px}</style><div class="f">Float</div><p>Following</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	_, baseX := webTestTextPosition(t, base, "Following")
	_, logicalX := webTestTextPosition(t, logical, "Following")
	if logicalX <= baseX+60 {
		t.Fatalf("logical float did not displace the following sibling: base x=%d, logical x=%d", baseX, logicalX)
	}

	zero := parseWebPage(`<style>.x{position:absolute;inset-inline-start:0}</style><div class="x">Box</div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	shifted := parseWebPage(`<style>.x{position:absolute;inset-inline-start:80px}</style><div class="x">Box</div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	if len(zero.Floats) != 1 || len(shifted.Floats) != 1 || shifted.Floats[0].X <= zero.Floats[0].X {
		t.Fatalf("logical inset did not move the absolute box: zero=%+v shifted=%+v", zero.Floats, shifted.Floats)
	}
}

func TestWebFlexAutoMarginsPositionItems(t *testing.T) {
	legibleTestInitFonts(t)
	layout := func(source string) *webLayout {
		return parseWebPage(source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 800)
	}

	left := layout(`<style>.row{display:flex}.last{width:120px;margin-left:auto;background:#eee}</style>` +
		`<div class="row"><div class="last">Last</div></div>`)
	_, lastX := webTestTextPosition(t, left, "Last")
	if lastX < 500 {
		t.Fatalf("margin-left:auto did not push the flex item to the end: Last x=%d", lastX)
	}
	if paintedX, _ := webTestPaintedAt(t, left, lastX); !webTestNearX(paintedX, lastX) {
		t.Fatalf("painted flex item did not follow its auto-margin position: run x=%d painted x=%d", lastX, paintedX)
	}

	right := layout(`<style>.row{display:flex;justify-content:flex-end}.item{width:120px;margin-right:auto;background:#eee}</style>` +
		`<div class="row"><div class="item">Right</div></div>`)
	_, rightX := webTestTextPosition(t, right, "Right")
	if rightX > 60 {
		t.Fatalf("margin-right:auto did not override justify-content and move to the start: x=%d", rightX)
	}
	if paintedX, _ := webTestPaintedAt(t, right, rightX); !webTestNearX(paintedX, rightX) {
		t.Fatalf("painted flex item did not follow margin-right:auto: run x=%d painted x=%d", rightX, paintedX)
	}

	rowReverse := layout(`<style>.row{display:flex;flex-direction:row-reverse}.last{width:120px;margin-left:auto;background:#eee}</style>` +
		`<div class="row"><div class="last">Last</div></div>`)
	_, reverseLastX := webTestTextPosition(t, rowReverse, "Last")
	if reverseLastX < 500 {
		t.Fatalf("row-reverse margin-left:auto did not mirror the row: Last x=%d", reverseLastX)
	}
	if paintedX, _ := webTestPaintedAt(t, rowReverse, reverseLastX); !webTestNearX(paintedX, reverseLastX) {
		t.Fatalf("painted row-reverse item did not follow its run: run x=%d painted x=%d", reverseLastX, paintedX)
	}

	center := layout(`<style>.row{display:flex}.center{width:120px;margin:0 auto;background:#eee}</style>` +
		`<div class="row"><div class="center">Center</div></div>`)
	_, centerX := webTestTextPosition(t, center, "Center")
	if centerX < 100 || centerX > 500 {
		t.Fatalf("margin:0 auto did not centre the flex item: x=%d", centerX)
	}
	if paintedX, _ := webTestPaintedAt(t, center, centerX); !webTestNearX(paintedX, centerX) {
		t.Fatalf("painted centred flex item did not follow its run: run x=%d painted x=%d", centerX, paintedX)
	}

	logical := layout(`<style>.row{display:flex}.last{width:120px;margin-inline-start:auto;background:#eee}</style>` +
		`<div class="row"><div class="last">End</div></div>`)
	_, logicalEndX := webTestTextPosition(t, logical, "End")
	if logicalEndX < 500 {
		t.Fatalf("margin-inline-start:auto did not push the flex item: end x=%d", logicalEndX)
	}
	if paintedX, _ := webTestPaintedAt(t, logical, logicalEndX); !webTestNearX(paintedX, logicalEndX) {
		t.Fatalf("painted logical auto-margin item did not follow its run: run x=%d painted x=%d", logicalEndX, paintedX)
	}

	cross := layout(`<style>.row{display:flex;gap:10px}.tall{background:#ddd}.short{margin-top:auto;background:#eee}</style>` +
		`<div class="row"><div class="tall">Tall<br>line<br>line</div><div class="short">Short</div></div>`)
	tallRow, tallX := webTestTextPosition(t, cross, "Tall")
	shortRow, shortX := webTestTextPosition(t, cross, "Short")
	if shortRow <= tallRow+1 {
		t.Fatalf("margin-top:auto did not move the cross-axis item: tall row=%d short row=%d", tallRow, shortRow)
	}
	_, tallPaintedY := webTestPaintedAt(t, cross, tallX)
	_, shortPaintedY := webTestPaintedAt(t, cross, shortX)
	if shortPaintedY <= tallPaintedY {
		t.Fatalf("cross-axis auto margin did not move the painted box: tall y=%d short y=%d", tallPaintedY, shortPaintedY)
	}

	grid := layout(`<style>.grid{display:grid;grid-template-columns:1fr 1fr;gap:0}.cell{width:120px;margin:auto;background:#eee}</style>` +
		`<div class="grid"><div class="cell">One</div><div class="cell">Two</div></div>`)
	_, oneX := webTestTextPosition(t, grid, "One")
	_, twoX := webTestTextPosition(t, grid, "Two")
	if oneX < 50 || twoX-oneX < 150 || twoX < 400 {
		t.Fatalf("grid auto margins did not centre items in their tracks: One=%d Two=%d", oneX, twoX)
	}
	if paintedX, _ := webTestPaintedAt(t, grid, oneX); !webTestNearX(paintedX, oneX) {
		t.Fatalf("painted grid item One did not follow its track position: run x=%d painted x=%d", oneX, paintedX)
	}
	if paintedX, _ := webTestPaintedAt(t, grid, twoX); !webTestNearX(paintedX, twoX) {
		t.Fatalf("painted grid item Two did not follow its track position: run x=%d painted x=%d", twoX, paintedX)
	}

	column := layout(`<style>.col{display:flex;flex-direction:column}.cell{width:120px;margin-inline:auto;background:#eee}</style>` +
		`<div class="col"><div class="cell">Column</div></div>`)
	_, columnX := webTestTextPosition(t, column, "Column")
	if columnX < 100 || columnX > 500 {
		t.Fatalf("column auto margins did not centre the cross-axis item: x=%d", columnX)
	}
	if paintedX, _ := webTestPaintedAt(t, column, columnX); !webTestNearX(paintedX, columnX) {
		t.Fatalf("painted column item did not follow its cross-axis position: run x=%d painted x=%d", columnX, paintedX)
	}

	columnBottom := layout(`<style>.col{display:flex;flex-direction:column;height:240px}.item{margin-top:auto;background:#eee}</style>` +
		`<div class="col"><div class="item">Bottom</div></div>`)
	bottomRow, bottomX := webTestTextPosition(t, columnBottom, "Bottom")
	_, bottomY := webTestPaintedAt(t, columnBottom, bottomX)
	if bottomRow < 6 || bottomY < 100 {
		t.Fatalf("column margin-top:auto did not reach the end: row=%d painted y=%d", bottomRow, bottomY)
	}

	logicalColumn := layout(`<style>.col{display:flex;flex-direction:column;height:240px}.item{margin-block-start:auto;border:1px solid #000}</style>` +
		`<div class="col"><div class="item">Logical bottom</div></div>`)
	logicalRow, logicalX := webTestTextPosition(t, logicalColumn, "Logical")
	_, logicalY := webTestPaintedAt(t, logicalColumn, logicalX)
	if logicalRow < 6 || logicalY < 100 {
		t.Fatalf("column margin-block-start:auto did not reach the end: row=%d painted y=%d", logicalRow, logicalY)
	}

	columnTop := layout(`<style>.col{display:flex;flex-direction:column;height:240px;justify-content:flex-end}.item{margin-bottom:auto;background:#eee}</style>` +
		`<div class="col"><div class="item">Top</div></div>`)
	topRow, topX := webTestTextPosition(t, columnTop, "Top")
	_, topY := webTestPaintedAt(t, columnTop, topX)
	if topRow > 2 || topY > 50 {
		t.Fatalf("column margin-bottom:auto did not reach the start: row=%d painted y=%d", topRow, topY)
	}

	columnCenter := layout(`<style>.col{display:flex;flex-direction:column;height:240px}.item{margin:auto;background:#eee}</style>` +
		`<div class="col"><div class="item">Middle</div></div>`)
	middleRow, middleX := webTestTextPosition(t, columnCenter, "Middle")
	_, middleY := webTestPaintedAt(t, columnCenter, middleX)
	if middleRow < 3 || middleRow > 7 || middleY < 50 || middleY > 200 {
		t.Fatalf("column margin:auto did not centre: row=%d painted y=%d", middleRow, middleY)
	}

	columnReverse := layout(`<style>.col{display:flex;flex-direction:column-reverse;height:240px}.item{margin-top:auto;background:#eee}</style>` +
		`<div class="col"><div class="item">Reverse</div></div>`)
	reverseRow, reverseX := webTestTextPosition(t, columnReverse, "Reverse")
	_, reverseY := webTestPaintedAt(t, columnReverse, reverseX)
	if reverseRow < 6 || reverseY < 100 {
		t.Fatalf("column-reverse margin-top:auto did not mirror to the end: row=%d painted y=%d", reverseRow, reverseY)
	}
}

func webTestPaintedAt(t *testing.T, layout *webLayout, x int32) (int32, int32) {
	t.Helper()
	for _, rect := range layout.Rects {
		dx := rect.Rect.X - x
		if dx < 0 {
			dx = -dx
		}
		if rect.Rect.W > 0 && rect.Rect.H > 0 && dx <= 8 {
			return rect.Rect.X, rect.Rect.Y
		}
	}
	t.Fatalf("no painted box at run x=%d", x)
	return 0, 0
}

func webTestNearX(a, b int32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= 8
}

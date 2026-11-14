package main

// A table of the properties the cascade accepts. Each case applies one
// declaration and compares the rendered layout fingerprint with a baseline, so
// a declaration that is parsed but never painted or measured fails this test.

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

func webTestLayoutFingerprint(layout *webLayout) string {
	if layout == nil {
		return "nil"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "height=%d rowH=%d\n", layout.Height, layout.RowH)
	for i, row := range layout.Rows {
		fmt.Fprintf(&b, "row%d y=%d:", i, row.Y)
		for _, run := range row.Runs {
			w, h := 0, 0
			if run.Font != nil {
				if fw, fh, err := run.Font.SizeUTF8(run.Text); err == nil {
					w, h = int(fw), int(fh)
				}
			}
			fmt.Fprintf(&b, "[%q x=%d w=%d h=%d c=%v bold=%t u=%t s=%t dc=%v/%t dt=%d ds=%d sh=%t/%d/%d/%v/%t bg=%t/%v box=%d/%d inv=%t vo=%d clip=%v/%t]",
				run.Text, run.X, w, h, run.Color, run.Bold, run.Underline, run.Strike,
				run.DecorationColor, run.HasDecorationColor, run.DecorationThickness, run.DecorationStyle,
				run.HasShadow, run.ShadowX, run.ShadowY,
				run.ShadowColor, run.HasShadowColor, run.HasBg, run.Bg, run.BoxW, run.BoxH, run.Invisible,
				run.VerticalOffset, run.Clip, run.HasClip)
		}
		b.WriteByte('\n')
	}
	for _, rect := range layout.Rects {
		fmt.Fprintf(&b, "rect=%v c=%v stroke=%t th=%d bs=%d grad=%t radius=%d z=%d clip=%v/%t\n",
			rect.Rect, rect.Color, rect.Stroke, rect.Thickness, rect.BorderStyle, rect.HasGradient, rect.Radius, rect.Z, rect.Clip, rect.HasClip)
	}
	for _, bg := range layout.Backgrounds {
		fmt.Fprintf(&b, "background=%v c=%v grad=%t radius=%d z=%d image=%s fit=%d pos=%s opacity=%.3f\n",
			bg.Rect, bg.Color, bg.HasGradient, bg.Radius, bg.Z, bg.Image, bg.ObjectFit, bg.ObjectPosition, bg.Opacity)
	}
	for _, im := range layout.Images {
		fmt.Fprintf(&b, "image=%s %v fit=%d pos=%s inv=%t opacity=%.3f clip=%v/%t\n", im.Key, im.Rect, im.ObjectFit, im.ObjectPosition, im.Invisible, im.Opacity, im.Clip, im.HasClip)
	}
	for _, v := range layout.Videos {
		fmt.Fprintf(&b, "video=%s %v fit=%d pos=%s clip=%v/%t opacity=%.3f\n", v.Src, v.Rect, v.ObjectFit, v.ObjectPosition, v.Clip, v.HasClip, webVideoPaintOpacity(v))
	}
	for _, f := range layout.Floats {
		fmt.Fprintf(&b, "float z=%d x=%d y=%d rows=%d rects=%d images=%d\n", f.Z, f.X, f.Y, len(f.Rows), len(f.Rects), len(f.Images))
	}
	return b.String()
}

func TestWebCSSEveryAcceptedPropertyChangesRenderedState(t *testing.T) {
	legibleTestInitFonts(t)
	type propertyCase struct {
		name    string
		html    string
		baseCSS string
		css     string
	}
	text := `<p class="x">Alpha beta gamma delta</p>`
	flex := `<div class="x"><div>One</div><div>Two</div><div>Three</div></div>`
	grid := `<div class="x"><div>One</div><div>Two</div><div>Three</div></div>`
	cases := []propertyCase{
		{"align-items", `<div class="x"><div>Short</div><div>Tall<br>line<br>line<br>line</div></div>`, `.x{display:flex}`, `.x{display:flex;align-items:center}`},
		{"align-self", `<div class="x"><div>Short</div><div>Tall<br>line<br>line<br>line</div></div>`, `.x{display:flex;align-items:flex-start}`, `.x{display:flex;align-items:flex-start}.x div:first-child{align-self:center}`},
		{"aspect-ratio", `<img class="x" src="missing.png" width="100">`, ``, `.x{aspect-ratio:2}`},
		{"background", text, ``, `.x{background:#eef}`},
		{"background-color", text, ``, `.x{background-color:#eef}`},
		{"background-image", text, ``, `.x{background-image:linear-gradient(#fff,#000)}`},
		{"background-image-layers", text, `.x{background-image:url(first.png);background-size:50px 10px;background-repeat:no-repeat}`, `.x{background-image:url(first.png),url(second.png);background-size:50px 10px,20px 5px;background-repeat:no-repeat}`},
		{"background-position", text, `.x{background-image:url(missing.png);background-size:50% 50%;background-repeat:no-repeat}`, `.x{background-image:url(missing.png);background-size:50% 50%;background-repeat:no-repeat;background-position:right bottom}`},
		{"background-repeat", text, `.x{background-image:url(missing.png);background-size:20% 20%;background-repeat:no-repeat}`, `.x{background-image:url(missing.png);background-size:20% 20%;background-repeat:repeat}`},
		{"background-repeat-round", text, `.x{height:100px;background-image:url(missing.png);background-size:70px 40px;background-repeat:repeat}`, `.x{height:100px;background-image:url(missing.png);background-size:70px 40px;background-repeat:round}`},
		{"background-repeat-space", text, `.x{height:100px;background-image:url(missing.png);background-size:70px 40px;background-repeat:repeat}`, `.x{height:100px;background-image:url(missing.png);background-size:70px 40px;background-repeat:space}`},
		{"background-size", text, `.x{background-image:url(missing.png);background-repeat:no-repeat}`, `.x{background-image:url(missing.png);background-repeat:no-repeat;background-size:50% 50%}`},
		{"border", text, ``, `.x{border:2px solid #c00}`},
		{"border-bottom", text, ``, `.x{border-bottom:2px solid #c00}`},
		{"border-bottom-left-radius", text, `.x{background:#eef}`, `.x{background:#eef;border-bottom-left-radius:12px}`},
		{"border-bottom-width", text, `.x{border:1px solid #000}`, `.x{border:1px solid #000;border-bottom-width:5px}`},
		{"border-color", text, `.x{border:1px solid #000}`, `.x{border:1px solid #c00}`},
		{"border-left", text, ``, `.x{border-left:2px solid #c00}`},
		{"border-left-width", text, `.x{border:1px solid #000}`, `.x{border:1px solid #000;border-left-width:5px}`},
		{"border-radius", text, `.x{background:#eef}`, `.x{background:#eef;border-radius:12px}`},
		{"border-right", text, ``, `.x{border-right:2px solid #c00}`},
		{"border-right-width", text, `.x{border:1px solid #000}`, `.x{border:1px solid #000;border-right-width:5px}`},
		{"border-style", text, `.x{border:2px solid #c00}`, `.x{border:2px solid #c00;border-style:dashed}`},
		{"border-top", text, ``, `.x{border-top:2px solid #c00}`},
		{"border-top-left-radius", text, `.x{background:#eef}`, `.x{background:#eef;border-top-left-radius:12px}`},
		{"border-top-right-radius", text, `.x{background:#eef}`, `.x{background:#eef;border-top-right-radius:12px}`},
		{"border-top-width", text, `.x{border:1px solid #000}`, `.x{border:1px solid #000;border-top-width:5px}`},
		{"border-width", text, ``, `.x{border-width:3px}`},
		{"bottom", text, `.x{position:relative}`, `.x{position:relative;bottom:12px}`},
		{"box-shadow", text, ``, `.x{box-shadow:4px 4px #c00}`},
		{"box-sizing", text, `.x{width:200px;padding:20px;background:#eef}`, `.x{width:200px;padding:20px;background:#eef;box-sizing:border-box}`},
		{"clear", `<div class="f">Float</div><p class="x">After</p>`, `.f{float:left;width:100px;min-height:100px}`, `.f{float:left;width:100px;min-height:100px}.x{clear:both}`},
		{"color", text, ``, `.x{color:#c00}`},
		{"column-gap", flex, `.x{display:flex}`, `.x{display:flex;column-gap:24px}`},
		{"container", `<div class="x"><p>Card text</p></div>`, ``, `.x{container:panel / inline-size;width:300px}@container panel (min-width:100px){p{color:#c00}}`},
		{"container-name", `<div class="x"><p>Card text</p></div>`, ``, `.x{container-name:panel;container-type:inline-size;width:300px}@container panel (min-width:100px){p{color:#c00}}`},
		{"container-type", `<div class="x"><p>Card text</p></div>`, ``, `.x{container-type:inline-size;width:300px}@container (min-width:100px){p{color:#c00}}`},
		{"content", `<p class="x">Body</p>`, ``, `.x::before{content:"Generated "}`},
		{"display", flex, ``, `.x{display:flex}`},
		{"display-grid", grid, ``, `.x{display:grid;grid-template-columns:1fr 1fr 1fr}`},
		{"display-table", `<div class="x"><div class="r"><div class="c">A</div><div class="c">B</div></div></div>`, `.x{display:block;width:300px}`, `.x{display:table;width:300px}.r{display:table-row}.c{display:table-cell}`},
		{"border-collapse", `<table class="x"><tr><td>A</td><td>B</td></tr><tr><td>C</td><td>D</td></tr></table>`, `.x{width:300px;border-collapse:separate}td{border:2px solid #000}`, `.x{width:300px;border-collapse:collapse}td{border:2px solid #000}`},
		{"border-spacing", `<table class="x"><tr><td>A</td><td>B</td></tr></table>`, `.x{width:300px;border-spacing:0}`, `.x{width:300px;border-spacing:12px}`},
		{"table-layout", `<table class="x"><tr><td>A</td><td>B</td></tr></table>`, `.x{width:400px;table-layout:auto}`, `.x{width:400px;table-layout:fixed}.x td:first-child{width:80px}`},
		{"col-width", `<table class="x"><colgroup><col class="c"><col></colgroup><tr><td>A</td><td>B</td></tr></table>`, `.x{width:300px;table-layout:fixed}`, `.x{width:300px;table-layout:fixed}.c{width:120px}`},
		{"colgroup-width", `<table class="x"><colgroup class="g" span="2"></colgroup><tr><td>A</td><td>B</td><td>C</td><td>D</td></tr></table>`, `.x{width:300px;table-layout:fixed}`, `.x{width:300px;table-layout:fixed}.g{width:80px}`},
		{"flex", `<div class="x"><div>A</div><div>B</div></div>`, `.x{display:flex}`, `.x{display:flex}.x div:first-child{flex:0 0 60px}`},
		{"flex-basis", `<div class="x"><div>A</div><div>B</div></div>`, `.x{display:flex}`, `.x{display:flex}.x div{flex-basis:140px}`},
		{"flex-direction", flex, `.x{display:flex}`, `.x{display:flex;flex-direction:column}`},
		{"flex-grow", `<div class="x"><div>A</div><div>B</div></div>`, `.x{display:flex}`, `.x{display:flex}.x div:first-child{flex-grow:1}`},
		{"flex-shrink", `<div class="x"><div>A</div><div>B</div></div>`, `.x{display:flex}.x div{flex:0 1 700px}`, `.x{display:flex}.x div{flex:0 1 700px}.x div:first-child{flex-shrink:3}`},
		{"flex-wrap", `<div class="x"><div>A</div><div>B</div><div>C</div><div>D</div></div>`, `.x{display:flex}.x div{flex:0 0 200px}`, `.x{display:flex;flex-wrap:wrap}.x div{flex:0 0 200px}`},
		{"float", `<div class="x">Float</div><p>After</p>`, ``, `.x{float:left;width:100px}`},
		{"font", text, ``, `.x{font:24px serif}`},
		{"font-size", text, ``, `.x{font-size:24px}`},
		{"font-style", text, ``, `.x{font-style:italic}`},
		{"font-weight", text, ``, `.x{font-weight:bold}`},
		{"gap", flex, `.x{display:flex}`, `.x{display:flex;gap:24px}`},
		{"grid-template-columns", grid, `.x{display:grid;grid-template-columns:1fr}`, `.x{display:grid;grid-template-columns:1fr 1fr 1fr}`},
		{"height", text, ``, `.x{height:120px}`},
		{"inset", text, `.x{position:absolute}`, `.x{position:absolute;inset:12px}`},
		{"justify-content", flex, `.x{display:flex}.x div{width:80px}`, `.x{display:flex;justify-content:center}.x div{width:80px}`},
		{"left", text, `.x{position:relative}`, `.x{position:relative;left:24px}`},
		{"letter-spacing", text, ``, `.x{letter-spacing:8px}`},
		{"line-height", text, ``, `.x{line-height:2.4}`},
		{"margin", text, ``, `.x{margin:24px}`},
		{"margin-block", `<p class="x">Alpha</p><p>After</p>`, ``, `.x{margin-block:24px}`},
		{"margin-block-end", `<p class="x">Alpha</p><p>After</p>`, ``, `.x{margin-block-end:80px}`},
		{"margin-block-start", `<p>Before</p><p class="x">Alpha</p>`, ``, `.x{margin-block-start:80px}`},
		{"margin-bottom", `<p class="x">Alpha</p><p>After</p>`, ``, `.x{margin-bottom:80px}`},
		{"margin-inline", text, ``, `.x{margin-inline:24px}`},
		{"margin-inline-end", `<p class="x">` + strings.Repeat("alpha beta gamma delta ", 12) + `</p>`, ``, `.x{margin-inline-end:60px}`},
		{"margin-inline-start", text, ``, `.x{margin-inline-start:28px}`},
		{"margin-left", text, ``, `.x{margin-left:24px}`},
		{"margin-right", `<p class="x">` + strings.Repeat("alpha beta gamma delta ", 12) + `</p>`, ``, `.x{margin-right:60px}`},
		{"margin-top", `<p>Before</p><p class="x">Alpha</p>`, ``, `.x{margin-top:80px}`},
		{"max-height", `<p class="x">` + strings.Repeat("alpha beta gamma delta ", 12) + `</p>`, ``, `.x{max-height:20px}`},
		{"max-width", text, ``, `.x{max-width:120px}`},
		{"min-height", text, ``, `.x{min-height:120px}`},
		{"min-width", text, `.x{width:80px}`, `.x{width:80px;min-width:240px}`},
		{"opacity", text, ``, `.x{opacity:0.35}`},
		{"opacity-image", `<img class="x" src="missing.png" width="100">`, ``, `.x{opacity:0.35}`},
		{"order", flex, `.x{display:flex}`, `.x{display:flex}.x div:last-child{order:-1}`},
		{"overflow", text, ``, `.x{overflow:hidden}`},
		{"object-fit", `<img class="x" src="missing.png" width="100">`, ``, `.x{object-fit:contain}`},
		{"object-position", `<img class="x" src="missing.png" width="100">`, `.x{object-fit:contain}`, `.x{object-fit:contain;object-position:left top}`},
		{"outline", text, ``, `.x{outline:3px solid #c00}`},
		{"outline-color", text, `.x{outline:2px solid #000}`, `.x{outline:2px solid #c00}`},
		{"outline-offset", text, `.x{outline:2px solid #c00}`, `.x{outline:2px solid #c00;outline-offset:6px}`},
		{"outline-width", text, `.x{outline:2px solid #000}`, `.x{outline:5px solid #000}`},
		{"padding", text, ``, `.x{padding:24px}`},
		{"padding-block", `<p class="x">Alpha</p><p>After</p>`, ``, `.x{padding-block:24px}`},
		{"padding-block-end", `<p class="x">Alpha</p><p>After</p>`, ``, `.x{padding-block-end:80px}`},
		{"padding-block-start", `<p>Before</p><p class="x">Alpha</p>`, ``, `.x{padding-block-start:80px}`},
		{"padding-bottom", `<p class="x">Alpha</p><p>After</p>`, ``, `.x{padding-bottom:24px}`},
		{"padding-inline", text, ``, `.x{padding-inline:24px}`},
		{"padding-inline-end", `<p class="x">` + strings.Repeat("alpha beta gamma delta ", 12) + `</p>`, ``, `.x{padding-inline-end:60px}`},
		{"padding-inline-start", text, ``, `.x{padding-inline-start:28px}`},
		{"padding-left", text, ``, `.x{padding-left:24px}`},
		{"padding-right", `<p class="x">` + strings.Repeat("alpha beta gamma delta ", 12) + `</p>`, ``, `.x{padding-right:60px}`},
		{"padding-top", `<p>Before</p><p class="x">Alpha</p>`, ``, `.x{padding-top:80px}`},
		{"place-items", `<div class="x"><div>Short</div><div>Tall<br>line<br>line<br>line</div></div>`, `.x{display:grid;grid-template-columns:1fr 1fr}`, `.x{display:grid;grid-template-columns:1fr 1fr;place-items:center}`},
		{"position", text, ``, `.x{position:absolute}`},
		{"relative-position", text, ``, `.x{position:relative;left:20px}`},
		{"right", text, `.x{position:relative}`, `.x{position:relative;right:12px}`},
		{"row-gap", grid, `.x{display:grid;grid-template-columns:1fr 1fr}`, `.x{display:grid;grid-template-columns:1fr 1fr;row-gap:24px}`},
		{"text-align", text, ``, `.x{text-align:center}`},
		{"text-decoration", text, ``, `.x{text-decoration:underline}`},
		{"text-decoration-color", text, `.x{text-decoration:underline}`, `.x{text-decoration:underline;text-decoration-color:#c00}`},
		{"text-decoration-line", text, ``, `.x{text-decoration-line:line-through}`},
		{"text-decoration-thickness", text, `.x{text-decoration:underline}`, `.x{text-decoration:underline;text-decoration-thickness:4px}`},
		{"text-decoration-style", text, `.x{text-decoration:underline}`, `.x{text-decoration:underline;text-decoration-style:wavy}`},
		{"text-indent", text, ``, `.x{text-indent:24px}`},
		{"text-shadow", text, ``, `.x{text-shadow:2px 3px #c00}`},
		{"text-transform", text, ``, `.x{text-transform:uppercase}`},
		{"vertical-align", text, ``, `.x{vertical-align:top}`},
		{"video-opacity", `<video src="https://example.com/movie.mp4"></video>`, ``, `video{opacity:0.5}`},
		{"top", text, `.x{position:relative}`, `.x{position:relative;top:12px}`},
		{"visibility", text, ``, `.x{visibility:hidden}`},
		{"white-space", `<p class="x">Alpha  beta</p>`, ``, `.x{white-space:pre}`},
		{"width", text, ``, `.x{width:140px}`},
		{"word-spacing", text, ``, `.x{word-spacing:20px}`},
		{"z-index", text, `.x{position:relative;background:#eef}`, `.x{position:relative;background:#eef;z-index:3}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			style := func(css string) string {
				if strings.TrimSpace(css) == "" {
					return ""
				}
				return "<style>" + css + "</style>"
			}
			base := parseWebPage(style(tc.baseCSS)+tc.html, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
			got := parseWebPage(style(tc.css)+tc.html, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
			if webTestLayoutFingerprint(base) == webTestLayoutFingerprint(got) {
				t.Fatalf("accepted CSS property %q changed no rendered state", tc.name)
			}
		})
	}
}

func TestWebCSSFontStyleUsesAnItalicFace(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<style>.x{font-style:italic}</style><p class="x">Italic sample</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if run.Text != "Italic" {
				continue
			}
			if run.Font == nil || run.Font.GetStyle()&ttf.STYLE_ITALIC == 0 {
				t.Fatalf("font-style:italic did not select an italic face: %+v", run.Font)
			}
			return
		}
	}
	t.Fatal("the italic run was not laid out")
}

func TestWebCSSVisibilityPreservesFlowAndLetsADescendantShow(t *testing.T) {
	legibleTestInitFonts(t)
	source := `<p class="x">Hidden but measured</p>`
	base := parseWebPage(`<style>.x{background:#eef}</style>`+source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	hidden := parseWebPage(`<style>.x{visibility:hidden;background:#eef}</style>`+source, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	if len(base.Rows) != len(hidden.Rows) {
		t.Fatalf("visibility:hidden changed the flow row count: base %d, hidden %d", len(base.Rows), len(hidden.Rows))
	}
	baseRow, baseX := webTestTextPosition(t, base, "Hidden")
	hiddenRow, hiddenX := webTestTextPosition(t, hidden, "Hidden")
	if baseRow != hiddenRow || baseX != hiddenX {
		t.Fatalf("visibility:hidden moved the run: base (%d,%d), hidden (%d,%d)", baseRow, baseX, hiddenRow, hiddenX)
	}
	if !hidden.Rows[hiddenRow].Runs[0].Invisible {
		t.Fatal("visibility:hidden did not mark the run as invisible")
	}
	if len(hidden.Rects) != 0 {
		t.Fatalf("visibility:hidden painted its background: %+v", hidden.Rects)
	}

	descendant := parseWebPage(`<style>.x{visibility:hidden}.shown{visibility:visible}</style><div class="x"><span class="shown">Shown child</span></div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	for _, row := range descendant.Rows {
		for _, run := range row.Runs {
			if run.Text == "Shown" {
				if run.Invisible {
					t.Fatal("visibility:visible did not clear the inherited hidden state")
				}
				return
			}
		}
	}
	t.Fatal("the visibility:visible descendant was not laid out")
}

func TestWebCSSObjectFitReachesTheImagePaintBox(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<style>.x{object-fit:contain}</style><img class="x" src="missing.png" width="100">`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	if len(layout.Images) != 1 {
		t.Fatalf("image layout = %+v", layout.Images)
	}
	if layout.Images[0].ObjectFit != webObjectFitContain {
		t.Fatalf("object-fit did not reach the paint box: %+v", layout.Images[0])
	}
	box := webObjectFitBox(sdl.Rect{X: 10, Y: 20, W: 200, H: 100}, 400, 100, false)
	if box != (sdl.Rect{X: 10, Y: 45, W: 200, H: 50}) {
		t.Fatalf("contain box = %+v, want a centred 200x50 box", box)
	}
}

func TestWebCSSPercentageHeightIsIgnoredWithoutCorruptingRows(t *testing.T) {
	legibleTestInitFonts(t)
	base := parseWebPage(`<p>Tiny</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	for _, css := range []string{`height:50%`, `min-height:50%`, `max-height:50%`} {
		layout := parseWebPage(`<style>p{`+css+`}</style><p>Tiny</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
		if len(layout.Rows) != len(base.Rows) {
			t.Fatalf("%s changed the row count: base %d, got %d", css, len(base.Rows), len(layout.Rows))
		}
		_, bx := webTestTextPosition(t, base, "Tiny")
		_, gx := webTestTextPosition(t, layout, "Tiny")
		if bx != gx {
			t.Fatalf("%s shifted the run: base x=%d, got %d", css, bx, gx)
		}
	}
}

func TestWebCSSBorderStyleChangesPaintGeometry(t *testing.T) {
	legibleTestInitFonts(t)
	layout := func(css string) *webLayout {
		return parseWebPage(`<style>.x{`+css+`}</style><div class="x">Boxed</div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	}
	firstStroke := func(l *webLayout) (webRect, bool) {
		for _, r := range l.Rects {
			if r.Stroke {
				return r, true
			}
		}
		return webRect{}, false
	}
	solid := layout(`border:4px solid #c00`)
	solidRect, ok := firstStroke(solid)
	if !ok || solidRect.BorderStyle != webBorderSolid {
		t.Fatalf("solid border did not reach paint as solid: %+v", solidRect)
	}
	dashed := layout(`border:4px solid #c00;border-style:dashed`)
	dashedRect, ok := firstStroke(dashed)
	if !ok || dashedRect.BorderStyle != webBorderDashed {
		t.Fatalf("dashed border did not reach paint as dashed: %+v", dashedRect)
	}
	if len(webBorderPaintRects(dashedRect.Rect, 4, dashedRect.BorderStyle, true)) == 0 {
		t.Fatal("dashed border produced no dash geometry")
	}
	dotted := layout(`border:4px solid #c00;border-style:dotted`)
	dottedRect, ok := firstStroke(dotted)
	if !ok || dottedRect.BorderStyle != webBorderDotted {
		t.Fatalf("dotted border did not reach paint as dotted: %+v", dottedRect)
	}
	double := layout(`border:4px solid #c00;border-style:double`)
	doubleRect, ok := firstStroke(double)
	if !ok || len(webBorderPaintRects(doubleRect.Rect, 4, doubleRect.BorderStyle, true)) < 4 {
		t.Fatalf("double border did not produce two-line geometry: %+v", doubleRect)
	}
	none := layout(`border:4px solid #c00;border-style:none`)
	if _, ok := firstStroke(none); ok {
		t.Fatal("border-style:none still painted a border")
	}
}

func TestWebCSSTextDecorationStyleChangesGeometry(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<style>p{text-decoration:underline;text-decoration-style:wavy}</style><p>Decorated</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	var run webRunPos
	found := false
	for _, row := range layout.Rows {
		for _, r := range row.Runs {
			if r.Text == "Decorated" {
				run, found = r, true
			}
		}
	}
	if !found || run.DecorationStyle != webDecorationWavy {
		t.Fatalf("text-decoration-style did not reach the run: %+v", run)
	}
	solid := webDecorationPaintRects(10, 20, 120, 3, webDecorationSolid)
	dashed := webDecorationPaintRects(10, 20, 120, 3, webDecorationDashed)
	dotted := webDecorationPaintRects(10, 20, 120, 3, webDecorationDotted)
	double := webDecorationPaintRects(10, 20, 120, 3, webDecorationDouble)
	wavy := webDecorationPaintRects(10, 20, 120, 3, webDecorationWavy)
	if len(solid) != 1 || len(dashed) < 2 || len(dotted) < 2 || len(double) != 2 || len(wavy) < 2 {
		t.Fatalf("decoration styles produced indistinguishable geometry: solid=%d dashed=%d dotted=%d double=%d wavy=%d", len(solid), len(dashed), len(dotted), len(double), len(wavy))
	}
}

func TestWebCSSOutlineOffsetMovesTheOutlineBox(t *testing.T) {
	legibleTestInitFonts(t)
	layout := func(css string) *webLayout {
		return parseWebPage(`<style>.x{`+css+`}</style><div class="x">Boxed</div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	}
	stroke := func(l *webLayout) (webRect, bool) {
		for _, r := range l.Rects {
			if r.Stroke {
				return r, true
			}
		}
		return webRect{}, false
	}
	base, ok := stroke(layout(`outline:2px solid #c00`))
	if !ok {
		t.Fatal("outline did not paint a stroke")
	}
	shifted, ok := stroke(layout(`outline:2px solid #c00;outline-offset:8px`))
	if !ok {
		t.Fatal("offset outline did not paint a stroke")
	}
	if shifted.Rect.X >= base.Rect.X || shifted.Rect.Y >= base.Rect.Y || shifted.Rect.W <= base.Rect.W || shifted.Rect.H <= base.Rect.H {
		t.Fatalf("outline-offset did not move the outline outward: base=%+v shifted=%+v", base.Rect, shifted.Rect)
	}
}

func TestWebCSSObjectPositionAnchorsTheFittedBox(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<style>.x{object-fit:contain;object-position:left top}</style><img class="x" src="missing.png" width="100">`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	if len(layout.Images) != 1 || layout.Images[0].ObjectPosition != "left top" {
		t.Fatalf("object-position did not reach the image paint state: %+v", layout.Images)
	}
	box := sdl.Rect{X: 10, Y: 20, W: 200, H: 100}
	if got := webObjectFitBoxAt(box, 400, 100, false, 0, 0); got != (sdl.Rect{X: 10, Y: 20, W: 200, H: 50}) {
		t.Fatalf("left/top positioned contain box = %+v", got)
	}
	x, y := webObjectPositionFraction("100% 100%", box.W, box.H)
	if got := webObjectFitBoxAt(box, 400, 100, false, x, y); got != (sdl.Rect{X: 10, Y: 70, W: 200, H: 50}) {
		t.Fatalf("100%%/100%% positioned contain box = %+v", got)
	}
}

func TestWebCSSFlexShrinkUsesEachItemFactor(t *testing.T) {
	legibleTestInitFonts(t)
	layout := func(css string) *webLayout {
		return parseWebPage(`<style>.row{display:flex}.row div{flex:0 1 700px}`+css+`</style><div class="row"><div>A</div><div>B</div></div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	}
	base := layout(``)
	target := layout(`.row div:first-child{flex-shrink:3}`)
	_, baseA := webTestTextPosition(t, base, "A")
	_, baseB := webTestTextPosition(t, base, "B")
	_, targetA := webTestTextPosition(t, target, "A")
	_, targetB := webTestTextPosition(t, target, "B")
	baseGap, targetGap := baseB-baseA, targetB-targetA
	if targetGap >= baseGap {
		t.Fatalf("higher flex-shrink did not shrink the first item: base gap %d, target gap %d", baseGap, targetGap)
	}
	if targetGap < 172 || targetGap > 182 {
		t.Fatalf("per-item shrink widths = %d, want the 3:1 split near 177", targetGap)
	}
}

func TestWebCSSBackgroundSizeAndPositionSizeTheLayer(t *testing.T) {
	legibleTestInitFonts(t)
	layout := func(css string) *webLayout {
		return parseWebPage(`<style>.x{`+css+`}</style><div class="x">Background</div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	}
	background := func(l *webLayout) (webBackgroundPaint, bool) {
		for _, im := range l.Backgrounds {
			if strings.Contains(im.Image, "missing.png") {
				return im, true
			}
		}
		return webBackgroundPaint{}, false
	}
	base, ok := background(layout(`background-image:url(missing.png);background-size:50% 50%;background-repeat:no-repeat`))
	if !ok {
		t.Fatal("background image did not reach layout.Images")
	}
	anchored, ok := background(layout(`background-image:url(missing.png);background-size:50% 50%;background-repeat:no-repeat;background-position:right bottom`))
	if !ok {
		t.Fatal("anchored background image did not reach layout.Images")
	}
	if anchored.Rect.X <= base.Rect.X || anchored.Rect.Y <= base.Rect.Y {
		t.Fatalf("background-position did not move the layer: base=%+v anchored=%+v", base.Rect, anchored.Rect)
	}
	box := sdl.Rect{X: 0, Y: 0, W: 200, H: 100}
	doc := &webDocument{ImageMeta: map[string]webImageMeta{"bg": {HasSize: true, W: 400, H: 100}}}
	cover, coverFit := webBackgroundLayerRect(box, webStyle{BackgroundImage: "bg", BackgroundSize: "cover"}, doc, 1)
	contain, containFit := webBackgroundLayerRect(box, webStyle{BackgroundImage: "bg", BackgroundSize: "contain"}, doc, 1)
	if cover != box || coverFit != webObjectFitCover {
		t.Fatalf("cover layer = %+v fit=%d, want the whole box and cover crop", cover, coverFit)
	}
	if contain.W != 200 || contain.H != 50 || containFit != webObjectFitFill {
		t.Fatalf("contain layer = %+v fit=%d, want 200x50 inside the box", contain, containFit)
	}
}

func TestWebCSSVerticalAlignMovesInlineRuns(t *testing.T) {
	legibleTestInitFonts(t)
	layout := func(css string) *webLayout {
		return parseWebPage(`<style>.x{`+css+`}</style><p class="x">Aligned text</p>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	}
	offset := func(l *webLayout) int32 {
		for _, row := range l.Rows {
			for _, run := range row.Runs {
				if strings.Contains(run.Text, "Aligned") {
					return run.VerticalOffset
				}
			}
		}
		return 0
	}
	top, middle, bottom := offset(layout(`vertical-align:top`)), offset(layout(`vertical-align:middle`)), offset(layout(`vertical-align:bottom`))
	if top >= middle || middle >= bottom {
		t.Fatalf("vertical-align offsets are not distinct: top=%d middle=%d bottom=%d", top, middle, bottom)
	}
}

func TestWebCSSOverflowClipsDescendantPaint(t *testing.T) {
	legibleTestInitFonts(t)
	layout := func(css string) *webLayout {
		return parseWebPage(`<style>.x{`+css+`}</style><div class="x">Clipped text that overflows its box</div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 320)
	}
	visible := layout(``)
	hidden := layout(`overflow:hidden`)
	if len(hidden.Rows) == 0 || len(hidden.Rows[0].Runs) == 0 {
		t.Fatal("overflow layout produced no runs")
	}
	if visible.Rows[0].Runs[0].HasClip {
		t.Fatal("visible overflow unexpectedly clipped its run")
	}
	clipped := hidden.Rows[0].Runs[0]
	if !clipped.HasClip || clipped.Clip.W <= 0 || clipped.Clip.H <= 0 {
		t.Fatalf("overflow:hidden did not install a paint clip: %+v", clipped.Clip)
	}
	if len(visible.Rows[0].Runs) == 0 || visible.Rows[0].Runs[0].VerticalOffset != clipped.VerticalOffset {
		t.Fatal("overflow clipping changed unrelated run geometry")
	}
}

func TestWebCSSBoxOpacityReachesImagePaint(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<style>.x{opacity:0.4}</style><img class="x" src="missing.png" width="100">`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	if len(layout.Images) != 1 || layout.Images[0].Opacity < 0.39 || layout.Images[0].Opacity > 0.41 {
		t.Fatalf("box opacity did not reach the image paint state: %+v", layout.Images)
	}
}

func TestWebCSSBackgroundRepeatTilesTheLayer(t *testing.T) {
	legibleTestInitFonts(t)
	layout := func(repeat string) *webLayout {
		return parseWebPage(`<style>.x{background-image:url(missing.png);background-size:50px 10px;background-repeat:`+repeat+`}</style><div class="x">Tiled background</div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	}
	images := func(l *webLayout) []webBackgroundPaint {
		out := []webBackgroundPaint{}
		for _, im := range l.Backgrounds {
			if strings.Contains(im.Image, "missing.png") {
				out = append(out, im)
			}
		}
		return out
	}
	one := images(layout(`no-repeat`))
	many := images(layout(`repeat`))
	if len(one) != 1 || len(many) <= len(one) {
		t.Fatalf("background-repeat did not tile: no-repeat=%d repeat=%d", len(one), len(many))
	}
	repeatX := images(layout(`repeat-x`))
	repeatY := images(layout(`repeat-y`))
	unique := func(items []webBackgroundPaint, horizontal bool) int {
		seen := map[int32]bool{}
		for _, im := range items {
			if horizontal {
				seen[im.Rect.X] = true
			} else {
				seen[im.Rect.Y] = true
			}
		}
		return len(seen)
	}
	if len(repeatX) <= 1 || unique(repeatX, false) != 1 || unique(repeatX, true) <= 1 {
		t.Fatalf("repeat-x did not repeat horizontally only: %d tiles x=%d y=%d", len(repeatX), unique(repeatX, true), unique(repeatX, false))
	}
	if len(repeatY) <= 1 || unique(repeatY, true) != 1 || unique(repeatY, false) <= 1 {
		t.Fatalf("repeat-y did not repeat vertically only: %d tiles x=%d y=%d", len(repeatY), unique(repeatY, true), unique(repeatY, false))
	}
	twoValue := images(layout(`repeat no-repeat`))
	if len(twoValue) <= 1 || unique(twoValue, false) != 1 || unique(twoValue, true) <= 1 {
		t.Fatalf("two-value repeat did not mirror repeat-x: %d tiles x=%d y=%d", len(twoValue), unique(twoValue, true), unique(twoValue, false))
	}
	gradientCount := func(repeat string) int {
		l := parseWebPage(`<style>.x{background-image:linear-gradient(#fff,#000);background-size:50px 10px;background-repeat:`+repeat+`}</style><div class="x">Tiled gradient</div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
		n := 0
		for _, bg := range l.Backgrounds {
			if bg.HasGradient {
				n++
			}
		}
		return n
	}
	if oneGradient, manyGradient := gradientCount(`no-repeat`), gradientCount(`repeat`); manyGradient <= oneGradient {
		t.Fatalf("gradient repeat did not tile: no-repeat=%d repeat=%d", oneGradient, manyGradient)
	}
	box := sdl.Rect{X: 10, Y: 20, W: 200, H: 100}
	layer := sdl.Rect{X: 10, Y: 20, W: 50, H: 25}
	if got := webBackgroundTiles(box, layer, "no-repeat"); len(got) != 1 {
		t.Fatalf("no-repeat produced %d clipped tiles, want 1", len(got))
	}
	if got := webBackgroundTiles(box, layer, "repeat"); len(got) != 16 {
		t.Fatalf("repeat produced %d tiles, want 16", len(got))
	}
	for _, tile := range webBackgroundTiles(box, layer, "repeat") {
		if tile.X < box.X || tile.Y < box.Y || tile.X+tile.W > box.X+box.W || tile.Y+tile.H > box.Y+box.H {
			t.Fatalf("tile escapes the padding box: %+v in %+v", tile, box)
		}
	}
}

func TestWebCSSBackgroundLayerListPaintsEveryLayer(t *testing.T) {
	legibleTestInitFonts(t)
	layout := func(css string) *webLayout {
		return parseWebPage(`<style>.x{`+css+`}</style><div class="x">Layered</div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	}
	images := func(l *webLayout) map[string]sdl.Rect {
		out := map[string]sdl.Rect{}
		for _, im := range l.Backgrounds {
			if strings.Contains(im.Image, "first.png") || strings.Contains(im.Image, "second.png") {
				out[im.Image] = im.Rect
			}
		}
		return out
	}
	one := images(layout(`background-image:url(first.png);background-size:50px 10px;background-repeat:no-repeat`))
	twoLayout := layout(`background-image:url(first.png),url(second.png);background-size:50px 10px,20px 5px;background-repeat:no-repeat`)
	two := images(twoLayout)
	if len(one) != 1 || len(two) != 2 {
		t.Fatalf("layer list did not paint both layers: one=%d two=%d", len(one), len(two))
	}
	if two[firstKey(two, "first.png")].W == 0 || two[firstKey(two, "second.png")].W == 0 {
		t.Fatalf("layer list lost a layer geometry: %+v", two)
	}
	if two[firstKey(two, "first.png")].W != 75 || two[firstKey(two, "second.png")].W != 30 {
		t.Fatalf("per-layer background-size was not applied: %+v", two)
	}
	order := []string{}
	for _, bg := range twoLayout.Backgrounds {
		if bg.Image != "" {
			order = append(order, bg.Image)
		}
	}
	if len(order) != 2 || !strings.Contains(order[0], "second.png") || !strings.Contains(order[1], "first.png") {
		t.Fatalf("layers are not painted in CSS order (first layer last): %v", order)
	}
	shorthand := images(layout(`background:url(first.png) left top / 50px 10px no-repeat,url(second.png) right bottom / 20px 5px no-repeat`))
	if len(shorthand) != 2 || shorthand[firstKey(shorthand, "first.png")].W != 75 || shorthand[firstKey(shorthand, "second.png")].W != 30 {
		t.Fatalf("background shorthand layer list was not parsed: %+v", shorthand)
	}
	gradient := layout(`background-image:linear-gradient(#fff,#000),url(second.png);background-size:50px 10px,20px 5px;background-repeat:no-repeat`)
	gradientRects := 0
	for _, bg := range gradient.Backgrounds {
		if bg.HasGradient {
			gradientRects++
		}
	}
	if gradientRects != 1 || len(images(gradient)) != 1 {
		t.Fatalf("gradient+image layers did not both paint: rects=%d images=%d", gradientRects, len(images(gradient)))
	}
}

func firstKey(images map[string]sdl.Rect, suffix string) string {
	for key := range images {
		if strings.Contains(key, suffix) {
			return key
		}
	}
	return suffix
}

func TestWebCSSBackgroundSpaceAndRoundGeometry(t *testing.T) {
	box := sdl.Rect{X: 0, Y: 0, W: 200, H: 100}
	layer := sdl.Rect{X: 0, Y: 0, W: 60, H: 30}
	space := webBackgroundTiles(box, layer, "space")
	if len(space) != 9 {
		t.Fatalf("space produced %d tiles, want 9", len(space))
	}
	xs := map[int32]int32{}
	for _, tile := range space {
		if tile.X+tile.W > box.X+box.W || tile.Y+tile.H > box.Y+box.H {
			t.Fatalf("space tile clipped at the far edge: %+v", tile)
		}
		xs[tile.X] = tile.W
	}
	positions := make([]int32, 0, len(xs))
	for x := range xs {
		positions = append(positions, x)
	}
	slices.Sort(positions)
	if len(positions) != 3 {
		t.Fatalf("space x positions = %v, want 3", positions)
	}
	if gap := positions[1] - (positions[0] + xs[positions[0]]); gap != positions[2]-(positions[1]+xs[positions[1]]) {
		t.Fatalf("space gaps differ: %v", positions)
	}
	round := webBackgroundTiles(box, layer, "round")
	if len(round) != 9 {
		t.Fatalf("round produced %d tiles, want 9", len(round))
	}
	lastRight, lastBottom := int32(0), int32(0)
	for _, tile := range round {
		if tile.X+tile.W > lastRight {
			lastRight = tile.X + tile.W
		}
		if tile.Y+tile.H > lastBottom {
			lastBottom = tile.Y + tile.H
		}
	}
	if lastRight != box.W || lastBottom != box.H {
		t.Fatalf("round did not exactly fill the box: right=%d bottom=%d", lastRight, lastBottom)
	}
	if got := webBackgroundTiles(box, layer, "space no-repeat"); len(got) != 3 {
		t.Fatalf("space no-repeat produced %d tiles, want one space row", len(got))
	}
}

func TestWebCSSVideoOpacityScalesCardPaint(t *testing.T) {
	legibleTestInitFonts(t)
	layout := func(css string) *webLayout {
		return parseWebPage(`<style>video{`+css+`}</style><video src="https://example.com/movie.mp4"></video>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	}
	full := layout(`opacity:1`)
	half := layout(`opacity:0.5`)
	if len(full.Videos) != 1 || len(half.Videos) != 1 {
		t.Fatalf("video card did not reach the paint layout: full=%+v half=%+v", full.Videos, half.Videos)
	}
	if full.Videos[0].Opacity != 1 || half.Videos[0].Opacity != 0.5 {
		t.Fatalf("box opacity did not reach video paint: full=%.2f half=%.2f", full.Videos[0].Opacity, half.Videos[0].Opacity)
	}
	if got := webVideoPaintOpacity(half.Videos[0]); got != 0.5 {
		t.Fatalf("video paint opacity = %.2f, want 0.5", got)
	}
	if got := webScaleColorAlpha(sdl.Color{R: 250, A: 200}, 0.5); got.A != 100 {
		t.Fatalf("video alpha multiply = %d, want 100", got.A)
	}
}

func TestWebHTMLTableRendersCellsInColumns(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<table><caption>Totals</caption><tr><td>Alpha</td><td>Beta</td></tr><tr><td>Gamma</td><td>Delta</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	alphaRow, alphaX := webTestTextPosition(t, layout, "Alpha")
	betaRow, betaX := webTestTextPosition(t, layout, "Beta")
	gammaRow, _ := webTestTextPosition(t, layout, "Gamma")
	if alphaRow != betaRow {
		t.Fatalf("table cells landed on different rows: Alpha row %d, Beta row %d", alphaRow, betaRow)
	}
	if betaX <= alphaX {
		t.Fatalf("table cells did not occupy columns: Alpha x=%d, Beta x=%d", alphaX, betaX)
	}
	if gammaRow <= alphaRow {
		t.Fatalf("table rows did not advance: first row %d, second row %d", alphaRow, gammaRow)
	}
	webTestTextPosition(t, layout, "Totals")
}

func TestWebHTMLTableColspanAndRowspanOccupyGrid(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<style>table{width:400px;table-layout:fixed;border-spacing:0}td{border:1px solid #000}</style>`+
		`<table><tr><td colspan="2">AB</td><td>C</td></tr>`+
		`<tr><td rowspan="2">D</td><td>E</td><td>F</td></tr>`+
		`<tr><td>G</td><td>H</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	abRow, abX := webTestTextPosition(t, layout, "AB")
	cRow, cX := webTestTextPosition(t, layout, "C")
	dRow, dX := webTestTextPosition(t, layout, "D")
	eRow, eX := webTestTextPosition(t, layout, "E")
	fRow, fX := webTestTextPosition(t, layout, "F")
	gRow, gX := webTestTextPosition(t, layout, "G")
	hRow, hX := webTestTextPosition(t, layout, "H")
	if abRow != cRow {
		t.Fatalf("colspan cells left the row: AB row %d, C row %d", abRow, cRow)
	}
	if cX <= abX {
		t.Fatalf("colspan cell overlapped the following cell: AB x=%d, C x=%d", abX, cX)
	}
	if dRow != eRow || eRow != fRow {
		t.Fatalf("rowspan row split: D=%d E=%d F=%d", dRow, eRow, fRow)
	}
	if gRow != hRow || gRow <= dRow {
		t.Fatalf("rowspan did not advance the next row: D=%d G=%d H=%d", dRow, gRow, hRow)
	}
	if abs32(gX-eX) > 8 || abs32(hX-fX) > 8 {
		t.Fatalf("rowspan did not keep its column slots: E=%d G=%d F=%d H=%d", eX, gX, fX, hX)
	}
	if gap := cX - abX; gap < (eX-dX)*2-2 || gap > (eX-dX)*2+2 {
		t.Fatalf("colspan width is not two columns: AB-C=%d, column=%d", gap, eX-dX)
	}
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func TestWebHTMLTableFixedAndAutoTracksDiffer(t *testing.T) {
	legibleTestInitFonts(t)
	makeLayout := func(css string) *webLayout {
		return parseWebPage(`<style>`+css+`</style><table><tr><td class="x">A</td><td>B</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	}
	fixed := makeLayout(`table{width:400px;table-layout:fixed;border-spacing:0}.x{width:80px}`)
	auto := makeLayout(`table{width:400px;table-layout:auto;border-spacing:0}`)
	autoWidth := makeLayout(`table{width:400px;table-layout:auto;border-spacing:0}.x{width:120px}`)
	fixedARow, fixedAX := webTestTextPosition(t, fixed, "A")
	_, fixedBX := webTestTextPosition(t, fixed, "B")
	autoARow, autoAX := webTestTextPosition(t, auto, "A")
	_, autoBX := webTestTextPosition(t, auto, "B")
	if fixedARow != autoARow {
		t.Fatalf("fixed and auto rows differ: %d vs %d", fixedARow, autoARow)
	}
	if fixedBX-fixedAX >= autoBX-autoAX {
		t.Fatalf("fixed did not honor the first-row width: fixed column=%d auto column=%d", fixedBX-fixedAX, autoBX-autoAX)
	}
	if autoBX <= autoAX {
		t.Fatalf("auto table lost its column: A x=%d B x=%d", autoAX, autoBX)
	}
	_, autoWidthAX := webTestTextPosition(t, autoWidth, "A")
	_, autoWidthBX := webTestTextPosition(t, autoWidth, "B")
	if autoWidthBX-autoWidthAX == autoBX-autoAX {
		t.Fatalf("auto table ignored the direct cell width: sized=%d unsized=%d", autoWidthBX-autoWidthAX, autoBX-autoAX)
	}
}

func TestWebHTMLTableCollapseSharesInteriorBorders(t *testing.T) {
	legibleTestInitFonts(t)
	makeLayout := func(collapse string) *webLayout {
		return parseWebPage(`<style>table{width:300px;border-collapse:`+collapse+`;border-spacing:8px}td{border:2px solid #000}</style>`+
			`<table><tr><td>A</td><td>B</td></tr><tr><td>C</td><td>D</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	}
	collapsed := makeLayout("collapse")
	separate := makeLayout("separate")
	strikes := func(layout *webLayout) int {
		n := 0
		for _, rect := range layout.Rects {
			if rect.Stroke && !rect.BackgroundFill {
				n++
			}
		}
		return n
	}
	if strikes(collapsed) != 0 {
		t.Fatalf("collapsed table still paints %d doubled cell outlines", strikes(collapsed))
	}
	if strikes(separate) == 0 {
		t.Fatal("separate table lost its per-cell borders")
	}
	// Interior verticals are one line per boundary, not one per adjacent cell.
	xs := map[int32]bool{}
	for _, rect := range collapsed.Rects {
		if rect.Rect.H > rect.Rect.W*2 {
			xs[rect.Rect.X] = true
		}
	}
	if len(xs) > 3 {
		t.Fatalf("collapsed table painted %d vertical boundary positions, want at most 3: %v", len(xs), xs)
	}
	_, ax := webTestTextPosition(t, collapsed, "A")
	_, bx := webTestTextPosition(t, collapsed, "B")
	_, sax := webTestTextPosition(t, separate, "A")
	_, sbx := webTestTextPosition(t, separate, "B")
	if bx-ax <= 0 || sbx-sax <= 0 {
		t.Fatalf("cell columns collapsed: collapsed A/B=%d/%d separate A/B=%d/%d", ax, bx, sax, sbx)
	}
	if sbx-sax <= bx-ax {
		t.Fatalf("border-spacing did not separate cells: collapsed column=%d separate column=%d", bx-ax, sbx-sax)
	}
}

func TestWebHTMLTableClampsInvalidSpans(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<table><tr><td colspan="0">A</td><td colspan="999">B</td></tr>`+
		`<tr><td rowspan="-3">C</td><td rowspan="999">D</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	aRow, aX := webTestTextPosition(t, layout, "A")
	bRow, bX := webTestTextPosition(t, layout, "B")
	cRow, _ := webTestTextPosition(t, layout, "C")
	dRow, _ := webTestTextPosition(t, layout, "D")
	if aRow != bRow || cRow != dRow || bX <= aX {
		t.Fatalf("invalid spans broke the grid: A=%d B=%d C=%d D=%d", aRow, bRow, cRow, dRow)
	}
}

func TestWebHTMLDisplayTableModesOutsideTable(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<div style="display:table;width:400px;border-spacing:0">`+
		`<div style="display:table-caption">Cap</div>`+
		`<div style="display:table-header-group"><div style="display:table-row"><div style="display:table-cell">A</div><div style="display:table-cell">B</div></div></div>`+
		`<div style="display:table-row-group"><div style="display:table-row"><div style="display:table-cell">C</div><div style="display:table-cell">D</div></div></div>`+
		`<div style="display:table-footer-group"><div style="display:table-row"><div style="display:table-cell">E</div><div style="display:table-cell">F</div></div></div>`+
		`</div>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	aRow, aX := webTestTextPosition(t, layout, "A")
	bRow, bX := webTestTextPosition(t, layout, "B")
	cRow, _ := webTestTextPosition(t, layout, "C")
	dRow, _ := webTestTextPosition(t, layout, "D")
	if aRow != bRow || cRow != dRow || cRow <= aRow {
		t.Fatalf("display:table flow did not form rows: A=%d B=%d C=%d D=%d", aRow, bRow, cRow, dRow)
	}
	if bX <= aX {
		t.Fatalf("display:table cells did not form columns: A x=%d B x=%d", aX, bX)
	}
	webTestTextPosition(t, layout, "Cap")
	if eRow, _ := webTestTextPosition(t, layout, "E"); eRow <= cRow {
		t.Fatalf("display:table footer group did not add a row: C=%d E=%d", cRow, eRow)
	}
}

func TestWebReplacedElementsKeepPlaceholderBoxes(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<style>svg,canvas,object,embed,audio,video{background:#eee;border:2px solid #000}</style>`+
		`<svg width="120" height="80"></svg><canvas width="200" height="100"></canvas>`+
		`<object width="90" height="60"></object><embed width="70" height="50">`+
		`<audio width="100" height="30"></audio><video width="160" height="90"></video>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	widths := make([]int32, 0, 6)
	for _, rect := range layout.Rects {
		if !rect.Stroke && rect.Rect.H > 12 && rect.Rect.W > 12 {
			widths = append(widths, rect.Rect.W)
		}
	}
	if len(widths) != 6 {
		t.Fatalf("replaced placeholders painted %d background boxes, want 6: %+v", len(widths), widths)
	}
	if len(layout.Rows) < 6 {
		t.Fatalf("placeholder boxes did not occupy rows: %d", len(layout.Rows))
	}
	if widths[0] >= widths[1] || widths[1] <= widths[3] {
		t.Fatalf("placeholder widths did not follow their attributes: %+v", widths)
	}
}

func TestWebHTMLTableColumnWidthsFromColAndColgroup(t *testing.T) {
	legibleTestInitFonts(t)
	makeLayout := func(layout, cols string) *webLayout {
		return parseWebPage(`<style>table{width:600px;table-layout:`+layout+`;border-spacing:0}td{padding:0;border:0}`+cols+`</style>`+
			`<table><colgroup><col width="80"><col width="120"></colgroup>`+
			`<tr><td>A</td><td>B</td><td>C</td></tr><tr><td>D</td><td>E</td><td>F</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	}
	for _, mode := range []string{"fixed", "auto"} {
		layout := makeLayout(mode, "")
		_, ax := webTestTextPosition(t, layout, "A")
		_, bx := webTestTextPosition(t, layout, "B")
		_, cx := webTestTextPosition(t, layout, "C")
		if bx <= ax || cx <= bx {
			t.Fatalf("%s col widths collapsed: A=%d B=%d C=%d", mode, ax, bx, cx)
		}
		if bx-ax >= cx-bx {
			t.Fatalf("%s did not apply <col> widths left-to-right: AB=%d BC=%d", mode, bx-ax, cx-bx)
		}
	}
	for _, mode := range []string{"fixed", "auto"} {
		group := parseWebPage(`<style>table{width:600px;table-layout:`+mode+`;border-spacing:0}td{padding:0;border:0}</style>`+
			`<table><colgroup span="2" width="100"></colgroup><col width="50">`+
			`<tr><td>A</td><td>B</td><td>C</td><td>D</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
		_, gax := webTestTextPosition(t, group, "A")
		_, gbx := webTestTextPosition(t, group, "B")
		_, gcx := webTestTextPosition(t, group, "C")
		_, gdx := webTestTextPosition(t, group, "D")
		if gbx-gax != gcx-gbx {
			t.Fatalf("%s <colgroup span=2> did not cover two equal columns: AB=%d BC=%d", mode, gbx-gax, gcx-gbx)
		}
		if gdx-gcx >= gcx-gbx {
			t.Fatalf("%s <col width> did not size its column: BC=%d CD=%d", mode, gcx-gbx, gdx-gcx)
		}
		precedence := parseWebPage(`<style>table{width:600px;table-layout:`+mode+`;border-spacing:0}td{padding:0;border:0}</style>`+
			`<table><colgroup><col width="50"><col width="50"></colgroup>`+
			`<tr><td style="width:200px">A</td><td>B</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
		_, pax := webTestTextPosition(t, precedence, "A")
		_, pbx := webTestTextPosition(t, precedence, "B")
		if pbx-pax <= 50*3 { // 50 CSS px scaled to this reader's base size
			t.Fatalf("%s cell width did not take precedence over <col>: AB=%d", mode, pbx-pax)
		}
	}
	invalid := parseWebPage(`<table><colgroup><col span="0" width="-5"><col span="999" width="999999"></colgroup>`+
		`<tr><td>A</td><td>B</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	_, iax := webTestTextPosition(t, invalid, "A")
	_, ibx := webTestTextPosition(t, invalid, "B")
	if ibx <= iax {
		t.Fatalf("invalid <col> spans broke the table: A=%d B=%d", iax, ibx)
	}
}

func TestWebHTMLTableEmptyRowspanRowKeepsHeight(t *testing.T) {
	legibleTestInitFonts(t)
	layout := parseWebPage(`<style>table{width:400px;border-spacing:0}td{padding:0;border:0}</style>`+
		`<table><tr><td rowspan="3">Tall</td></tr><tr></tr><tr><td>Next</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	tallRow, tallX := webTestTextPosition(t, layout, "Tall")
	nextRow, nextX := webTestTextPosition(t, layout, "Next")
	if nextRow < tallRow+2 {
		t.Fatalf("empty rowspan row did not reserve a row: Tall=%d Next=%d", tallRow, nextRow)
	}
	if nextX <= tallX {
		t.Fatalf("empty rowspan row shifted later cells into its column: Tall x=%d Next x=%d", tallX, nextX)
	}
}

func TestWebHTMLTableCollapseBorderConflictWinners(t *testing.T) {
	legibleTestInitFonts(t)
	widthWin := parseWebPage(`<style>table{width:400px;border-collapse:collapse}td{border:1px solid #000}`+
		`.wide{border-right:8px solid #c00}.thin{border-left:4px dashed #00c}</style>`+
		`<table><tr><td class="wide">A</td><td class="thin">B</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	styleWin := parseWebPage(`<style>table{width:400px;border-collapse:collapse}td{border:1px solid #000}`+
		`.dash{border-right:4px dashed #0a0}.solid{border-left:4px solid #c00}</style>`+
		`<table><tr><td class="dash">A</td><td class="solid">B</td></tr></table>`, "", "https://example.com/").computeLayout(&Config{}, Element{Font: "body"}, 720)
	vertical := func(layout *webLayout, width int32) []webRect {
		var out []webRect
		for _, rect := range layout.Rects {
			if rect.Rect.W == width && rect.Rect.H > rect.Rect.W*2 {
				out = append(out, rect)
			}
		}
		return out
	}
	edge := vertical(widthWin, 8)
	if len(edge) != 1 || edge[0].Color.R != 0xcc || edge[0].BorderStyle != webBorderSolid {
		t.Fatalf("wider cell border did not win exactly once: %+v", edge)
	}
	styleEdge := vertical(styleWin, 4)
	if len(styleEdge) != 1 || styleEdge[0].Color.R != 0xcc || styleEdge[0].BorderStyle != webBorderSolid {
		t.Fatalf("solid border did not beat dashed on an equal-width edge: %+v", styleEdge)
	}
}

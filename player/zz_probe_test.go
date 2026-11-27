package main

// Temporary diagnostic probe: loads a saved live page through the real page
// path and dumps the boxes/rows around a text needle. Deleted before finishing.

import (
	"context"
	"fmt"
	neturl "net/url"
	"os"
	"strings"
	"testing"
)

func TestProbeLivePage(t *testing.T) {
	file := os.Getenv("LIVE_HTML")
	if file == "" {
		t.Skip("no LIVE_HTML")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	url := os.Getenv("LIVE_URL")
	if url == "" {
		url = "https://fixture.jukahub.test/"
	}
	needle := os.Getenv("LIVE_FIND")
	h := newHeadlessBrowser(t)
	parsed, err := neturl.Parse(url)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	source := webExpandInlineCSSImportsContext(ctx, string(raw), parsed)
	sheets := fetchWebStylesheetBlocksContext(ctx, source, parsed)
	doc := parseWebPageWithLinkedSheets(source, webReaderCleanCSS(url), url, sheets)
	if doc == nil {
		t.Fatal("parse failed")
	}
	if !headlessSkipImages {
		fetchWebImages(doc)
	}
	variable := "probe_live"
	h.elem.Variable = variable
	setBrowserDocument(variable, doc)
	t.Cleanup(func() { setBrowserDocument(variable, nil) })
	layout := browserPageLayout(h.config, h.elem)
	t.Logf("blocks=%d rows=%d floats=%d height=%d", len(doc.Blocks), len(layout.Rows), len(layout.Floats), layout.Height)
	if name := os.Getenv("LIVE_NAME"); name != "" {
		h.paintPage(doc, layout, 0)
		h.rasterize(name)
	}

	groupOf := map[int]bool{}
	for i, b := range doc.Blocks {
		txt := ""
		for _, r := range b.Runs {
			txt += r.Text
		}
		if needle == "" || !strings.Contains(txt, needle) {
			continue
		}
		t.Logf("BLOCK %d text=%q group=%d item=%d disp=%d pos=%d w=%v h=%v rows=%d cols=%d weights=%v len=%d trail=%v", i, trunc(txt, 40),
			b.FlexGroup, b.FlexItem, b.Style.Display, b.Style.Position, b.Style.Width, b.Style.Height,
			b.GridRows, b.GridCols, b.GridWeights, len(b.Runs), flexTrailString(b.FlexTrail))
		groupOf[b.FlexGroup] = true
	}
	if os.Getenv("LIVE_STYLES") != "" {
		disp := map[int]int{}
		grids := 0
		for i, b := range doc.Blocks {
			disp[b.Style.Display]++
			if b.Style.Display == webDisplayGrid {
				grids++
				if grids <= 12 {
					txt := ""
					for _, r := range b.Runs {
						txt += r.Text
					}
					t.Logf("GRID block=%d text=%q cols=%d weights=%v min=%v autoRepeat=%d gapx=%v items=%v", i, trunc(txt, 30), b.GridCols, b.GridWeights, b.GridMin, b.GridAutoRepeat, b.FlexGapX, b.Style.JustifyItems)
				}
			}
		}
		t.Logf("DISPLAYS %v grids=%d", disp, grids)
	}
	type gi struct{ g, i int }
	counts := map[gi]int{}
	for _, b := range doc.Blocks {
		counts[gi{b.FlexGroup, b.FlexItem}]++
	}
	for k, v := range counts {
		t.Logf("PAIR group=%d item=%d blocks=%d", k.g, k.i, v)
	}
	for g, d := range doc.FlexBoxDepth {
		t.Logf("FLEXCONTAINER group=%d boxDepth=%d scrollX=%v adCut=%v", g, d, doc.FlexScrollX[g], doc.FlexAdCut[g])
	}
	for g := range groupOf {
		if g == 0 {
			continue
		}
		for i, b := range doc.Blocks {
			if b.FlexGroup != g {
				continue
			}
			txt := ""
			for _, r := range b.Runs {
				txt += r.Text
			}
			t.Logf("  GROUP %d BLOCK %d item=%d text=%q dir=%d disp=%d pos=%d w=%v h=%v", g, i, b.FlexItem, trunc(txt, 30), b.FlexDir, b.Style.Display, b.Style.Position, b.Style.Width, b.Style.Height)
		}
	}
	if os.Getenv("LIVE_SCAN") != "" {
		bad := 0
		for ri, row := range layout.Rows {
			for a := 0; a < len(row.Runs); a++ {
				ra := row.Runs[a]
				if ra.Font == nil || strings.TrimSpace(ra.Text) == "" {
					continue
				}
				wa, _, err := ra.Font.SizeUTF8(ra.Text)
				if err != nil {
					continue
				}
				for b := a + 1; b < len(row.Runs); b++ {
					rb := row.Runs[b]
					if rb.Font == nil || strings.TrimSpace(rb.Text) == "" {
						continue
					}
					wb, _, err := rb.Font.SizeUTF8(rb.Text)
					if err != nil {
						continue
					}
					if ra.X < rb.X+int32(wb)-2 && rb.X < ra.X+int32(wa)-2 {
						bad++
						if bad <= 12 {
							t.Logf("OVERLAP2 row=%d y=%d %q@%d+%d vs %q@%d+%d", ri, row.Y, ra.Text, ra.X, wa, rb.Text, rb.X, wb)
						}
					}
				}
			}
		}
		t.Logf("TEXTOVERLAPS %d", bad)
	}
	if os.Getenv("LIVE_PINS") != "" {
		for i, b := range doc.Blocks {
			if b.Style.GridRowStart == 0 && b.Style.GridColumnStart == 0 && b.Style.GridArea == "" {
				continue
			}
			txt := ""
			for _, r := range b.Runs {
				txt += r.Text
			}
			t.Logf("PIN block=%d row=%d col=%d area=%q disp=%d w=%v text=%q", i, b.Style.GridRowStart, b.Style.GridColumnStart, b.Style.GridArea, b.Style.Display, b.Style.Width, trunc(txt, 30))
		}
	}
	if os.Getenv("LIVE_DUP") != "" {
		for ri, row := range layout.Rows {
			same := 0
			for a := 0; a < len(row.Runs); a++ {
				for b := a + 1; b < len(row.Runs); b++ {
					if row.Runs[a].X == row.Runs[b].X {
						same++
					}
				}
			}
			if same > 0 {
				txt := ""
				for _, r := range row.Runs {
					txt += r.Text
				}
				t.Logf("OVERLAP row=%d y=%d shared=%d text=%q", ri, row.Y, same, trunc(txt, 60))
			}
		}
	}
	if spec := os.Getenv("LIVE_ROWS"); spec != "" {
		var lo, hi int
		fmt.Sscanf(spec, "%d:%d", &lo, &hi)
		for ri, row := range layout.Rows {
			if int(row.Y) < lo || int(row.Y) > hi {
				continue
			}
			var xs []string
			for _, r := range row.Runs {
				xs = append(xs, fmt.Sprintf("%d:%q", r.X, trunc(r.Text, 22)))
			}
			t.Logf("ROWRANGE %d y=%d %s", ri, row.Y, strings.Join(xs, " | "))
		}
	}
	for ri, row := range layout.Rows {
		line := ""
		for _, r := range row.Runs {
			line += r.Text
		}
		if needle != "" && !strings.Contains(line, needle) {
			continue
		}
		var xs []string
		for _, r := range row.Runs {
			xs = append(xs, fmt.Sprintf("%d:%q", r.X, trunc(r.Text, 18)))
		}
		t.Logf("ROW %d y=%d %s", ri, row.Y, strings.Join(xs, " "))
	}
	for fi, f := range layout.Floats {
		txt := ""
		for _, r := range f.Rows {
			for _, rr := range r.Runs {
				txt += rr.Text
			}
		}
		if needle != "" && !strings.Contains(txt, needle) {
			continue
		}
		t.Logf("FLOAT %d x=%d y=%d w=%d h=%d z=%d phase=%d text=%q", fi, f.X, f.Y, f.W, f.H, f.Z, f.Phase, trunc(txt, 40))
	}
}

var headlessSkipImages = os.Getenv("LIVE_NOIMG") != ""

func flexTrailString(trail []webFlexMembership) string {
	var parts []string
	for _, m := range trail {
		parts = append(parts, fmt.Sprintf("%d/%d:gap%.0f cols%d", m.group, m.item, m.gapX, m.gridCols))
	}
	return strings.Join(parts, " ")
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func TestProbeNthOfType(t *testing.T) {
	legibleTestInitFonts(t)
	html := `<style>ul{display:grid;grid-template-columns:1fr 1fr}
	li:first-of-type .link:nth-of-type(2){color:#cc0000}
	li:first-of-type .link:first-of-type{color:#0000cc}</style>
	<ul><li><a class="link">A1</a><a class="link">A2</a></li><li><a class="link">B1</a><a class="link">B2</a></li><li><a class="link">C1</a><a class="link">C2</a></li></ul>`
	doc := parseWebPage(html, "", "https://x.test/")
	for _, label := range []string{"A1", "A2", "B1", "B2", "C1", "C2"} {
		st, ok := webTestStyleForText(doc, label)
		if !ok {
			t.Logf("%s: not found", label)
			continue
		}
		t.Logf("%s: has=%v r=%d g=%d b=%d", label, st.HasColor, st.Color.R, st.Color.G, st.Color.B)
	}
}

func TestProbeNthOfTypeHas(t *testing.T) {
	legibleTestInitFonts(t)
	html := `<style>ul{display:grid;grid-template-columns:1fr 1fr}
	ul:has(>li){margin:0}
	li:first-of-type .link:nth-of-type(2){color:#cc0000}
	li:first-of-type .link:first-of-type{color:#0000cc}</style>
	<ul><li><a class="link">A1</a><a class="link">A2</a></li><li><a class="link">B1</a><a class="link">B2</a></li><li><a class="link">C1</a><a class="link">C2</a></li></ul>`
	doc := parseWebPage(html, "", "https://x.test/")
	for _, label := range []string{"A1", "A2", "B1", "B2", "C1", "C2"} {
		st, ok := webTestStyleForText(doc, label)
		if !ok {
			t.Logf("%s: not found", label)
			continue
		}
		t.Logf("%s: has=%v r=%d g=%d b=%d", label, st.HasColor, st.Color.R, st.Color.G, st.Color.B)
	}
}

func TestProbeGridAreaInherit(t *testing.T) {
	legibleTestInitFonts(t)
	html := `<style>ul{display:grid;grid-area:1/1/auto/-1;grid-template-columns:1fr 1fr}li{display:contents}
	li:first-of-type .link:nth-of-type(2){grid-area:1/1}</style>
	<ul><li><a class="link">A1</a><a class="link">A2</a></li><li><a class="link">B1</a><a class="link">B2</a></li></ul>`
	doc := parseWebPage(html, "", "https://x.test/")
	for i, b := range doc.Blocks {
		txt := ""
		for _, r := range b.Runs {
			txt += r.Text
		}
		t.Logf("block %d text=%q row=%d col=%d area=%q disp=%d", i, txt, b.Style.GridRowStart, b.Style.GridColumnStart, b.Style.GridArea, b.Style.Display)
	}
}

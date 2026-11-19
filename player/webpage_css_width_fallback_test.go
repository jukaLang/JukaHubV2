package main

import (
	"strings"
	"testing"
)

func TestWebUnsupportedCardWidthKeepsEarlierUsableRule(t *testing.T) {
	const css = `<style>
		.cards{display:flex;gap:16px;overflow-x:auto;--card-width:calc((100% - 48px)/3.2)}
		.card{width:400px;flex-shrink:0;white-space:nowrap}
		.cards .card{width:round(down,var(--card-width),1px);flex:none}
	</style>`
	var html strings.Builder
	html.WriteString(css)
	html.WriteString(`<div class="cards">`)
	for i := 0; i < 6; i++ {
		html.WriteString(`<div class="card">Readable card headline</div>`)
	}
	html.WriteString(`</div>`)
	doc := parseWebPage(html.String(), "", "https://example.com/")
	if len(doc.Blocks) != 6 {
		t.Fatalf("got %d cards, want 6", len(doc.Blocks))
	}
	for i, block := range doc.Blocks {
		if block.Style.Width != 400 || block.FlexShrink != 0 {
			t.Fatalf("card %d width=%g shrink=%g, want 400 and 0", i, block.Style.Width, block.FlexShrink)
		}
	}
	h := newHeadlessBrowser(t)
	_, layout := h.load("card_width_fallback", html.String())
	readable := 0
	var positions [][3]int32
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if strings.Contains(run.Text, "Readable") {
				readable++
				positions = append(positions, [3]int32{run.X, row.Y, run.BoxWidth})
				if run.BoxWidth < 300 || strings.Contains(run.Text, "…") || strings.Contains(run.Text, "...") {
					t.Fatalf("card text clipped: text=%q width=%d", run.Text, run.BoxWidth)
				}
			}
		}
	}
	if readable != 6 {
		t.Fatalf("laid out %d readable headlines, want 6", readable)
	}
	if positions[0][1] != positions[1][1] || positions[0][0] >= positions[1][0] || positions[2][1] <= positions[1][1] {
		t.Fatalf("fixed-width cards did not flow into two readable columns: %v groups=%d/%d,%d/%d", positions, doc.Blocks[0].FlexGroup, doc.Blocks[0].FlexItem, doc.Blocks[1].FlexGroup, doc.Blocks[1].FlexItem)
	}
}

func TestWebClippedImageUsesWrapperAspectRatio(t *testing.T) {
	doc := parseWebPage(`<div style="width:400px;aspect-ratio:16/9;overflow:hidden"><img style="width:100%;object-fit:cover" src="https://example.com/photo.jpg" width="1000" height="1000"></div>`, "", "https://example.com/")
	for _, block := range doc.Blocks {
		if !block.Image {
			continue
		}
		w, h := webImageDimsFromBlock(block, doc, 1, 400)
		if w != 400 || h < 224 || h > 226 {
			t.Fatalf("clipped image reserved %dx%d, want about 400x225", w, h)
		}
		return
	}
	t.Fatal("image block was not parsed")
}

func TestWebFlexClipMovesWithCard(t *testing.T) {
	h := newHeadlessBrowser(t)
	_, layout := h.load("flex_clip_offset", `<div style="height:80px">Before</div><div style="display:flex"><div style="width:300px"><div style="overflow:hidden;height:48px">Left card text</div></div><div style="width:300px"><div style="overflow:hidden;height:48px">Right card text</div></div></div>`)
	found := 0
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if !strings.Contains(run.Text, "card") {
				continue
			}
			found++
			if !run.HasClip || run.X < run.Clip.X || run.X >= run.Clip.X+run.Clip.W ||
				row.Y < run.Clip.Y || row.Y >= run.Clip.Y+run.Clip.H {
				t.Fatalf("card text %q at (%d,%d) lies outside its clip %+v", run.Text, run.X, row.Y, run.Clip)
			}
		}
	}
	if found != 2 {
		t.Fatalf("found %d clipped card texts, want 2", found)
	}
}

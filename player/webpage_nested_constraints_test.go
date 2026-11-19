package main

import (
	"strings"
	"testing"
)

func TestWebNestedFlexCardKeepsItsResolvedWidth(t *testing.T) {
	h := newHeadlessBrowser(t)
	_, layout := h.load("nested_flex_card_width", `<style>
		.outer { display:flex; width:900px; }
		.card { width:50%; padding:20px; }
		.inner { display:flex; width:100%; }
		.headline { width:100%; }
	</style><div class="outer"><div class="card"><div class="inner"><div class="headline">Readable nested headline</div></div></div><div class="card">Second card</div></div>`)
	for _, row := range layout.Rows {
		for _, run := range row.Runs {
			if strings.Contains(run.Text, "Readable") {
				if run.BoxWidth < 250 {
					t.Fatalf("nested card text was squeezed to %dpx", run.BoxWidth)
				}
				return
			}
		}
	}
	t.Fatal("nested card headline was not laid out")
}

func TestWebNestedCardTextUsesInnerPaddingWithoutChangingTrack(t *testing.T) {
	doc := &webDocument{
		Boxes: map[int]webBoxInfo{
			3: {style: webStyle{Width: 1000, Indent: 20, IndentRight: 20}},
		},
		FlexBoxDepth: map[int]int{1: 1},
	}
	l := &webLayouter{doc: doc, baseSize: 16, constraintSkip: 2}
	block := webBlock{
		Boxes:     []int{1, 2, 3, 4},
		BoxOwner:  4,
		FlexGroup: 1,
		Runs:      []webRun{{Text: "Headline"}},
	}
	x, width := l.applyBoxConstraints(block, 0, 350)
	if x != 20 || width != 310 {
		t.Fatalf("nested card text at x=%d width=%d, want 20 and 310", x, width)
	}
	block.Runs = nil
	block.Image = true
	x, width = l.applyBoxConstraints(block, 0, 350)
	if x != 0 || width != 350 {
		t.Fatalf("image track changed to x=%d width=%d, want 0 and 350", x, width)
	}
}

func TestWebBoxOwnerAppliesPaddingMissingFromTextBlock(t *testing.T) {
	doc := &webDocument{Boxes: map[int]webBoxInfo{
		1: {style: webStyle{Indent: 8, IndentRight: 8}},
	}}
	l := &webLayouter{doc: doc, baseSize: 24}
	block := webBlock{Boxes: []int{1}, BoxOwner: 1, Runs: []webRun{{Text: "Article text"}}}
	x, width := l.applyBoxConstraints(block, 0, 300)
	if x != 12 || width != 276 {
		t.Fatalf("text missed its box padding: x=%d width=%d, want 12/276", x, width)
	}
	block.Indent, block.Style.IndentRight = 8, 8
	x, width = l.applyBoxConstraints(block, 0, 300)
	if x != 0 || width != 300 {
		t.Fatalf("box padding was counted twice: x=%d width=%d", x, width)
	}
}

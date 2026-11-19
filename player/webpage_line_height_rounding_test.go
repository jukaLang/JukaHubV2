package main

import "testing"

func TestWebArticleLineHeightDoesNotDoubleRowSpacing(t *testing.T) {
	h := newHeadlessBrowser(t)
	_, layout := h.load("article_line_height", `<style>
		.article{width:220px;line-height:26.4px}
	</style><div class="article">Alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu.</div>`)
	var lines []int32
	for _, row := range layout.Rows {
		if len(row.Runs) > 0 {
			lines = append(lines, row.Y)
		}
	}
	if len(lines) < 2 {
		t.Fatalf("article did not wrap into multiple lines: %v", lines)
	}
	for i := 1; i < len(lines); i++ {
		if lines[i]-lines[i-1] > layout.RowH {
			t.Fatalf("article line spacing = %dpx, want at most one %dpx row", lines[i]-lines[i-1], layout.RowH)
		}
	}
}

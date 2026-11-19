package main

import (
	"strings"
	"testing"
)

func TestWebCSSTextOverflowClipAndEllipsis(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		ellipsis bool
	}{
		{"clip", false},
		{"ellipsis", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			h := newHeadlessBrowser(t)
			_, layout := h.load("text_overflow_"+tc.mode, `<style>.card{width:140px;white-space:nowrap;overflow:hidden;text-overflow:`+tc.mode+`}</style>`+
				`<div class="card">A very long heading with extra words</div>`)
			var text strings.Builder
			lines := 0
			for _, row := range layout.Rows {
				if len(row.Runs) == 0 {
					continue
				}
				lines++
				for _, run := range row.Runs {
					text.WriteString(run.Text)
				}
			}
			if lines != 1 || !strings.HasPrefix(text.String(), "Avery") {
				t.Fatalf("nowrap text = %q over %d lines", text.String(), lines)
			}
			if strings.Contains(text.String(), "…") != tc.ellipsis {
				t.Fatalf("text-overflow:%s rendered %q", tc.mode, text.String())
			}
		})
	}
}

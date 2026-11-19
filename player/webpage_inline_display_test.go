package main

import (
	"strings"
	"testing"
)

func TestWebCSSCanMakeHeadingInline(t *testing.T) {
	doc := parseWebPage(`<style>h1{display:inline}</style><div><h1>Welcome to Wikipedia</h1>, the free encyclopedia</div>`, "", "https://example.com/")
	if len(doc.Blocks) != 1 {
		t.Fatalf("inline heading split the sentence into %d blocks", len(doc.Blocks))
	}
	var text strings.Builder
	for _, run := range doc.Blocks[0].Runs {
		text.WriteString(run.Text)
	}
	if got := text.String(); got != "Welcome to Wikipedia, the free encyclopedia" {
		t.Fatalf("inline heading lost following punctuation: %q", got)
	}
}

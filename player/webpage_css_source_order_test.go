package main

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestWebLinkedAndInlineStylesFollowSourceOrder(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/card.css": `.card{color:#0000cc}`,
	})
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		html string
		red  bool
	}{
		{
			name: "linked sheet after inline block",
			html: `<style>.card{color:#cc0000}</style><link rel="stylesheet" href="/card.css"><p class="card">Card headline</p>`,
		},
		{
			name: "inline block after linked sheet",
			html: `<link rel="stylesheet" href="/card.css"><style>.card{color:#cc0000}</style><p class="card">Card headline</p>`,
			red:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sheets := fetchWebStylesheetBlocks(tc.html, base)
			if len(sheets) != 1 {
				t.Fatalf("fetched %d sheets, want one", len(sheets))
			}
			doc := parseWebPageWithLinkedSheets(tc.html, "", base.String(), sheets)
			style, ok := webTestStyleForText(doc, "Card headline")
			if !ok || !style.HasColor {
				t.Fatal("card text lost its stylesheet color")
			}
			if tc.red && (style.Color.R != 0xcc || style.Color.B != 0) {
				t.Fatalf("later inline rule did not win: %+v", style.Color)
			}
			if !tc.red && (style.Color.R != 0 || style.Color.B != 0xcc) {
				t.Fatalf("later linked rule did not win: %+v", style.Color)
			}
		})
	}
}

func TestWebPageKeepsStylesAfterTheOldSheetLimit(t *testing.T) {
	const count = 40
	files := make(map[string]string, count)
	var html strings.Builder
	for i := 0; i < count; i++ {
		path := fmt.Sprintf("/sheet-%d.css", i)
		files[path] = `.card{color:#cc0000}`
		if i == count-1 {
			files[path] = `.card{color:#0000cc}`
		}
		fmt.Fprintf(&html, `<link rel="stylesheet" href="%s">`, path)
	}
	html.WriteString(`<p class="card">Later sheet</p>`)
	srv := webImportTestServer(t, files)
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatal(err)
	}
	source := html.String()
	sheets := fetchWebStylesheetBlocks(source, base)
	if len(sheets) != count {
		t.Fatalf("fetched %d of %d linked sheets", len(sheets), count)
	}
	doc := parseWebPageWithLinkedSheets(source, "", base.String(), sheets)
	style, ok := webTestStyleForText(doc, "Later sheet")
	if !ok || !style.HasColor || style.Color.R != 0 || style.Color.B != 0xcc {
		t.Fatalf("late linked sheet did not style the card: %+v", style.Color)
	}
}

func TestWebPageKeepsLateRulesInLargeLinkedSheet(t *testing.T) {
	srv := webImportTestServer(t, map[string]string{
		"/large.css": `.early{color:#cc0000}` + strings.Repeat(" ", 2<<20) + `.card{color:#0000cc}`,
	})
	base, err := url.Parse(srv.URL + "/page")
	if err != nil {
		t.Fatal(err)
	}
	source := `<link rel="stylesheet" href="/large.css"><p class="card">Late rule</p>`
	sheets := fetchWebStylesheetBlocks(source, base)
	if len(sheets) != 1 {
		t.Fatalf("fetched %d sheets, want one", len(sheets))
	}
	doc := parseWebPageWithLinkedSheets(source, "", base.String(), sheets)
	style, ok := webTestStyleForText(doc, "Late rule")
	if !ok || !style.HasColor || style.Color.R != 0 || style.Color.B != 0xcc {
		t.Fatalf("late rule was lost from large sheet: %+v", style.Color)
	}
}

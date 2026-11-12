package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestStartpageVerificationOffersUsableSearch(t *testing.T) {
	requested := "https://www.startpage.com/sp/search?query=cnn"
	landed, err := url.Parse(requested)
	if err != nil {
		t.Fatal(err)
	}
	// Startpage currently sends this challenge as HTTP 200, so recognizing the
	// body is what keeps the reader from presenting it as search results.
	challenge := []byte(`<html><head><script id="anubis_challenge" type="application/json">{}</script></head><body>Verifying your request...</body></html>`)
	f, ok := brwSearchWall(requested, landed, challenge)
	if !ok || f.Kind != brwFailWall || f.Title != "Startpage needs verification" {
		t.Fatalf("challenge = %+v, recognized %v", f, ok)
	}
	if f.URL != requested || brwSearchQueryOf(f.URL) != "cnn" {
		t.Fatalf("search words lost from challenge: %+v", f)
	}
	alt := brwWallAltEngine()
	if alt.ID != "brave" || brwEngineAddress(alt, brwSearchQueryOf(f.URL)) != "https://search.brave.com/search?q=cnn" {
		t.Fatalf("verification alternate = %+v", alt)
	}
	if strings.Contains(f.Detail, "Google's results") {
		t.Fatalf("alternate claims to serve Google results: %q", f.Detail)
	}
}

func TestStartpageHomeChallengeAndOrdinaryPage(t *testing.T) {
	home := "https://www.startpage.com/"
	landed, _ := url.Parse(home)
	f, ok := brwSearchWall(home, landed, []byte(`<html><body>Verifying your request...</body></html>`))
	if !ok || brwSearchQueryOf(f.URL) != "" {
		t.Fatalf("home challenge = %+v, recognized %v", f, ok)
	}
	if got := brwEngineAddress(brwWallAltEngine(), brwSearchQueryOf(f.URL)); got != "https://search.brave.com/" {
		t.Fatalf("alternate home = %q", got)
	}
	if _, ok := brwSearchWall(home, landed, []byte(`<html><body>Startpage home</body></html>`)); ok {
		t.Fatal("ordinary Startpage page treated as a verification challenge")
	}
	other, _ := url.Parse("https://example.com/")
	if _, ok := brwSearchWall(other.String(), other, []byte(`<html>Verifying your request...</html>`)); ok {
		t.Fatal("an unrelated site treated as Startpage")
	}
}

func TestVerificationCardNamesHomeAndSearchActions(t *testing.T) {
	label := func(buttons []brwCardButton) string {
		for _, button := range buttons {
			if button.action == brwActWallSearch {
				return button.label
			}
		}
		return ""
	}
	if got := label(brwWallButtons(false)); got != "Open Brave Search" {
		t.Fatalf("home action = %q", got)
	}
	if got := label(brwWallButtons(true)); got != "Search on Brave Search" {
		t.Fatalf("search action = %q", got)
	}
}

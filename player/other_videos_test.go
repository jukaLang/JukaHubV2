package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOtherVideosChildrenFollowTheSiteList(t *testing.T) {
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{}
	kids := otherVideosChildren(c)
	// Two tools remain (type a domain, play a link). YouTube is omitted because
	// Entertainment already has a tile for it, and there is no search tile.
	want := len(videoSitesAll(c)) + 2
	if len(kids) != want {
		t.Fatalf("%d tiles, want %d", len(kids), want)
	}
	for _, kid := range kids {
		if kid.Title == "Any other link" || kid.Title == "Search this site" || kid.Title == "YouTube" {
			t.Fatalf("redundant tile %q is back", kid.Title)
		}
	}
	for _, kid := range kids {
		if kid.Activate == nil || kid.Title == "" {
			t.Fatalf("unusable tile %+v", kid)
		}
	}
	otherVideosSite = videoSite{Name: "Vimeo", Host: "vimeo.com"}
	if hint := otherVideosPlaceholder(); !strings.Contains(hint, "vimeo.com") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestOtherVideosTypedDomainAndSearch(t *testing.T) {
	old := appConfig
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{}
	appConfig = c
	defer func() { appConfig = old }()

	otherVideosDomainSubmit("example.org")
	if got := videoCustomSites(c); len(got) != 1 || got[0] != "example.org" {
		t.Fatalf("typed domains = %v", got)
	}
	// A URL is not a domain: it must not be saved as a shortcut.
	otherVideosDomainSubmit("https://vimeo.com/clip")
	if len(videoCustomSites(c)) != 1 {
		t.Fatalf("a URL was saved as a domain: %v", videoCustomSites(c))
	}
	otherVideosSite = videoSite{}
	if got := otherVideosSearchHost(); got != "example.org" {
		t.Fatalf("search host after typing = %q", got)
	}
	var titles []string
	for _, kid := range otherVideosChildren(c) {
		titles = append(titles, kid.Title)
	}
	joined := strings.Join(titles, "|")
	if !strings.Contains(joined, "example.org") {
		t.Fatalf("no tile for the typed domain: %s", joined)
	}
	for _, unwanted := range []string{"Search example.org", "Play from example.org"} {
		if strings.Contains(joined, unwanted) {
			t.Fatalf("%s should not be a separate tile", unwanted)
		}
	}
	if got := otherVideosSearchURL(otherVideosSearchHost(), "cats playing piano"); got != "https://www.example.org/search?q=cats+playing+piano" {
		t.Fatalf("search URL = %q", got)
	}
	if otherVideosSearchURL("", "cats") != "" {
		t.Fatal("search without a site returned a URL")
	}
}

func TestOtherVideosQueuesAVideoNotAnAudioTrack(t *testing.T) {
	downloadsTestReset(t)
	// An earlier test may have left the monitor with a stale offline answer.
	netMon.mu.Lock()
	netMon.checked = false
	netMon.mu.Unlock()
	called := 0
	downloadRunner = func(ctx context.Context, job downloadJob, progress func(downloadProgress)) (downloadResult, error) {
		called++
		if job.Kind != downloadKindVideo {
			t.Errorf("job kind = %v, want video", job.Kind)
		}
		// A cancelled job is requeued by the worker; a plain failure ends it.
		return downloadResult{}, errors.New("test download")
	}
	otherVideosSite = videoSite{Name: "Vimeo", Host: "vimeo.com"}
	otherVideosLinkSubmit("https://vimeo.com/12345")
	waitForDownload(t, "the video runner", func() bool { return called == 1 })
	otherVideosLinkSubmit("   ")
	if called != 1 {
		t.Fatal("an empty link queued a download")
	}
}

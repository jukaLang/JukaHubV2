package main

import (
	"strings"
	"testing"
)

func TestVideoSitesCanBeAddedFromTheBuiltinList(t *testing.T) {
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{}
	if len(videoBuiltinSites) < 10 {
		t.Fatalf("only %d built-in sites", len(videoBuiltinSites))
	}
	if !videoSiteAdd(c, " VIMEO.COM ") {
		t.Fatal("a built-in host was refused")
	}
	if videoSiteAdd(c, "vimeo.com") {
		t.Fatal("the same host was added twice")
	}
	if videoSiteAdd(c, "https://example.org/watch") {
		t.Fatal("a URL was accepted where a host was required")
	}
	if got := c.Variables.Custom[videoSitesCustomKey]; got != "vimeo.com" {
		t.Fatalf("stored sites = %v", got)
	}
	for _, o := range videoSiteAddOptions(c) {
		if o.Value == "vimeo.com" {
			t.Fatal("an added site was still offered")
		}
	}
	if got := videoSiteSummary(c); !strings.Contains(got, "Vimeo") {
		t.Fatalf("summary = %q", got)
	}
}

func TestVideoSiteForURLNamesAKnownHost(t *testing.T) {
	old := appConfig
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{}
	appConfig = c
	defer func() { appConfig = old }()
	if got := videoSiteForURL("https://www.vimeo.com/12345"); got != "Vimeo" {
		t.Fatalf("site = %q", got)
	}
	if got := videoSiteForURL("https://example.com/video"); got != "" {
		t.Fatalf("unknown site = %q", got)
	}
}

package main

import (
	"strings"
	"testing"
)

func TestVideoSitesCanBeAddedFromTheBuiltinList(t *testing.T) {
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{}
	if len(videoBuiltinSites) != 2 {
		t.Fatalf("built-in sites = %d, want 2", len(videoBuiltinSites))
	}
	for _, site := range videoBuiltinSites {
		switch site.Host {
		case "bilibili.com", "soundcloud.com":
		default:
			t.Fatalf("built-in %q is not one of the added sites", site.Host)
		}
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
	if got := videoSiteSummary(c); !strings.Contains(got, "vimeo.com") {
		t.Fatalf("summary = %q", got)
	}
	if !videoSiteRemove(c, "vimeo.com") {
		t.Fatal("the typed domain could not be removed")
	}
	if videoSiteRemove(c, "youtube.com") {
		t.Fatal("a built-in site was removed")
	}
	if len(videoCustomSites(c)) != 0 {
		t.Fatalf("custom sites after removal = %v", videoCustomSites(c))
	}
	if len(videoSiteRemoveOptions(c)) != 0 {
		t.Fatalf("remove options after removal = %v", videoSiteRemoveOptions(c))
	}
}

func TestVideoSiteForURLNamesAKnownHost(t *testing.T) {
	old := appConfig
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{}
	appConfig = c
	defer func() { appConfig = old }()
	if !videoSiteAdd(c, "example.org") {
		t.Fatal("the test domain was not saved")
	}
	if got := videoSiteForURL("https://www.example.org/clip/1"); got != "example.org" {
		t.Fatalf("site = %q", got)
	}
	if got := videoSiteForURL("https://example.com/video"); got != "" {
		t.Fatalf("unknown site = %q", got)
	}
}

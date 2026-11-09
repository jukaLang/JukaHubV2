package main

import (
	"testing"
)

const sampleM3U = "\ufeff#EXTM3U\r\n" +
	"#EXTINF:-1 tvg-id=\"bbc1.uk\" tvg-name=\"BBC, One\" group-title=\"News, UK\",BBC One\r\n" +
	"http://example.com/bbc1.m3u8\r\n" +
	"#EXTINF:-1 group-title=\"Sports\",Eurosport\r\n" +
	"#EXTVLCOPT:network-caching=1000\r\n" +
	"http://example.com/eurosport.m3u8\r\n" +
	"#EXTGRP:Kids\r\n" +
	"#EXTINF:-1,Kids TV\r\n" +
	"http://example.com/kids.m3u8\r\n" +
	"#EXTINF:-1,No Group\r\n" +
	"http://example.com/nogroup.m3u8\r\n" +
	"#EXTINF:-1,Broken Entry\r\n" +
	"file:///tmp/local.mp4\r\n" +
	"#EXTINF:-1,Duplicate\r\n" +
	"http://example.com/bbc1.m3u8\r\n"

// TestParseM3UChannelsReadsGroupsAndNames covers the parser upgrades: BOM and
// CRLF handling, group-title, #EXTGRP, quoted commas in attributes, directive
// lines between the metadata and the URL, skipped non-stream URLs, and dedupe.
func TestParseM3UChannelsReadsGroupsAndNames(t *testing.T) {
	chans := parseM3UChannels(sampleM3U)
	if len(chans) != 4 {
		t.Fatalf("parsed %d channels, want 4: %+v", len(chans), chans)
	}
	want := []TVChannel{
		{Name: "BBC One", URL: "http://example.com/bbc1.m3u8", Group: "News, UK"},
		{Name: "Eurosport", URL: "http://example.com/eurosport.m3u8", Group: "Sports"},
		{Name: "Kids TV", URL: "http://example.com/kids.m3u8", Group: "Kids"},
		{Name: "No Group", URL: "http://example.com/nogroup.m3u8", Group: tvOtherGroup},
	}
	for i, w := range want {
		if chans[i] != w {
			t.Errorf("channel %d = %+v, want %+v", i, chans[i], w)
		}
	}
}

// TestParseM3UBareURLList keeps simple one-URL-per-line playlists working.
func TestParseM3UBareURLList(t *testing.T) {
	chans := parseM3UChannels("http://a.example/one.m3u8\n\nhttp://b.example/two.m3u8\n")
	if len(chans) != 2 {
		t.Fatalf("parsed %d channels, want 2", len(chans))
	}
	if chans[0].URL != "http://a.example/one.m3u8" || chans[1].URL != "http://b.example/two.m3u8" {
		t.Fatalf("bare URLs lost: %+v", chans)
	}
	for _, ch := range chans {
		if ch.Group != tvOtherGroup || ch.Name == "" {
			t.Errorf("channel %+v has no fallback name/group", ch)
		}
	}
}

// TestParseM3UInfoLineHandlesQuotedCommas: the display name follows the last
// comma outside quotes, never the comma inside tvg-name.
func TestParseM3UInfoLineHandlesQuotedCommas(t *testing.T) {
	attrs, name := parseM3UInfoLine(`#EXTINF:-1 tvg-id="x" tvg-name="BBC, One" group-title="News, UK",BBC One HD`)
	if name != "BBC One HD" {
		t.Fatalf("name = %q, want %q", name, "BBC One HD")
	}
	if attrs["group-title"] != "News, UK" || attrs["tvg-id"] != "x" {
		t.Fatalf("attrs = %v", attrs)
	}
}

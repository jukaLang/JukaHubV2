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

// TestSaverRadioParseKeepsPlayableStations: the dock radio's station list from
// GitHub keeps only links ffmpeg can open (no playlist files, no other
// schemes), each once, in the list's order, with a readable name.
func TestSaverRadioParseKeepsPlayableStations(t *testing.T) {
	got := saverRadioParse("#EXTM3U\r\n" +
		"#EXTINF:-1 tvg-logo=\"http://x/logo.png\",1.FM - Chillout Lounge\r\n" +
		"http://strm112.1.fm/chilloutlounge_mobile_mp3\r\n" +
		"#EXTINF:-1,Playlist Station\r\n" +
		"http://example.com/listen.pls?id=4\r\n" +
		"#EXTINF:-1,RTMP Station\r\n" +
		"rtmp://example.com/live\r\n" +
		"#EXTINF:-1,HLS Music\r\n" +
		"https://example.com/music/index.m3u8\r\n" +
		"#EXTINF:-1,Again\r\n" +
		"http://strm112.1.fm/chilloutlounge_mobile_mp3\r\n" +
		"https://stream.example.org/live\r\n")
	want := []IPStream{
		{Name: "1.FM - Chillout Lounge", URL: "http://strm112.1.fm/chilloutlounge_mobile_mp3"},
		{Name: "HLS Music", URL: "https://example.com/music/index.m3u8"},
		{Name: "stream.example.org", URL: "https://stream.example.org/live"},
	}
	if len(got) != len(want) {
		t.Fatalf("kept %d stations, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("station %d = %+v, want %+v", i, got[i], w)
		}
	}
}

// Radio Browser's answers are outside data: only http(s) stream links that
// are not playlists, each once; names cut to one short line; broken stations
// and bad country codes dropped.
func TestRadioDirParseStationsKeepsSafeStreams(t *testing.T) {
	data := `[
		{"name":"Good\nFM","url":"http://a.example/x.pls","url_resolved":"https://a.example/live","countrycode":"de","tags":"jazz,smooth","codec":"MP3","bitrate":128,"lastcheckok":1,"stationuuid":"ab-12"},
		{"name":"Dup","url_resolved":"https://a.example/live","lastcheckok":1},
		{"name":"Broken","url_resolved":"https://b.example/live","lastcheckok":0},
		{"name":"Script","url_resolved":"javascript:alert(1)","lastcheckok":1},
		{"name":"Playlist","url_resolved":"https://c.example/list.m3u","lastcheckok":1},
		{"name":"Spaced","url_resolved":"https://d.example/a b","lastcheckok":1},
		{"name":"","url_resolved":"http://e.example/stream","countrycode":"Germany","bitrate":"96"}]`
	got, err := radioDirParseStations([]byte(data))
	if err != nil || len(got) != 2 {
		t.Fatalf("radioDirParseStations = %#v, %v", got, err)
	}
	if s := got[0]; s.Name != "Good FM" || s.URL != "https://a.example/live" || s.Country != "DE" || s.Bitrate != 128 || s.ID != "ab-12" {
		t.Errorf("first station = %#v", s)
	}
	if s := got[1]; s.URL != "http://e.example/stream" || s.Country != "" || s.Name == "" || s.Bitrate != 96 {
		t.Errorf("second station = %#v", s)
	}
	for _, h := range []string{"de1.api.radio-browser.info", "nl1.api.radio-browser.info."} {
		if !radioDirHostValid(h) {
			t.Errorf("%s refused", h)
		}
	}
	for _, h := range []string{"all.api.radio-browser.info", "evil.example", "x.api.radio-browser.info.evil.example", "a.b.api.radio-browser.info"} {
		if radioDirHostValid(h) {
			t.Errorf("%s accepted as an API server", h)
		}
	}
}

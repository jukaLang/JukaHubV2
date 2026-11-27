package main

import (
	"net/url"
	"strings"
	"testing"
)

func plexTestItem(videoCodec, audioCodec string, width, height, bitDepth int) plexItem {
	return plexItem{
		RatingKey: "42",
		Type:      "movie",
		Media: []plexMedia{{
			VideoCodec: videoCodec,
			AudioCodec: audioCodec,
			Width:      width,
			Height:     height,
			Parts: []plexPart{{
				Key: "/library/parts/7/file.mkv",
				Streams: []plexStream{
					{StreamType: 1, Codec: videoCodec, Width: width, Height: height, BitDepth: bitDepth},
					{StreamType: 2, Codec: audioCodec, Channels: 2},
				},
			}},
		}},
	}
}

func TestPlexCanDirectPlay(t *testing.T) {
	cases := []struct {
		name string
		item plexItem
		want bool
	}{
		{"h264 1080p aac", plexTestItem("h264", "aac", 1920, 1080, 8), true},
		{"h264 720p ac3", plexTestItem("h264", "ac3", 1280, 720, 8), true},
		{"hevc", plexTestItem("hevc", "aac", 1920, 1080, 8), false},
		{"10 bit h264", plexTestItem("h264", "aac", 1920, 1080, 10), false},
		{"4k h264", plexTestItem("h264", "aac", 3840, 2160, 8), false},
		{"dts", plexTestItem("h264", "dts", 1920, 1080, 8), false},
		{"truehd", plexTestItem("h264", "truehd", 1920, 1080, 8), false},
		{"mpeg4", plexTestItem("mpeg4", "mp3", 720, 480, 8), true},
	}
	for _, c := range cases {
		if got := plexCanDirectPlay(c.item); got != c.want {
			t.Errorf("%s: plexCanDirectPlay = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPlexCanDirectPlayFallsBackToMediaFields(t *testing.T) {
	it := plexItem{Media: []plexMedia{{VideoCodec: "h264", AudioCodec: "aac", Width: 1280, Height: 720,
		Parts: []plexPart{{Key: "/library/parts/7/file.mkv"}}}}}
	if !plexCanDirectPlay(it) {
		t.Fatal("a file without stream rows should still direct-play from its media fields")
	}
	it.Media[0].VideoCodec = "hevc"
	if plexCanDirectPlay(it) {
		t.Fatal("HEVC without stream rows must transcode")
	}
}

func TestPlexCandidatesPreferHomeOverRelay(t *testing.T) {
	res := []plexResource{
		{Name: "Shared", Provides: "server", Connections: []plexConnection{
			{URI: "https://relay.plex.direct:8443", Relay: true},
		}},
		{Name: "Home", Provides: "server", Owned: true, Connections: []plexConnection{
			{URI: "http://192.168.1.10:32400", Local: true},
			{URI: "https://remote.plex.direct:32400"},
		}},
	}
	cands := plexCandidates(res)
	if len(cands) != 3 {
		t.Fatalf("got %d candidates, want 3", len(cands))
	}
	if cands[0].res.Name != "Home" || !cands[0].conn.Local {
		t.Fatalf("first candidate = %s %+v, want the local Home connection", cands[0].res.Name, cands[0].conn)
	}
	if cands[2].res.Name != "Shared" || !cands[2].conn.Relay {
		t.Fatalf("last candidate = %s %+v, want the relay", cands[2].res.Name, cands[2].conn)
	}
}

func TestPlexConnectionURL(t *testing.T) {
	if got := plexConnectionURL(plexConnection{URI: "http://10.0.0.2:32400"}); got != "http://10.0.0.2:32400" {
		t.Fatalf("URI connection = %q", got)
	}
	got := plexConnectionURL(plexConnection{Protocol: "https", Address: "10.0.0.2", Port: 32400})
	if got != "https://10.0.0.2:32400" {
		t.Fatalf("built connection = %q", got)
	}
}

func TestPlexCleanPath(t *testing.T) {
	if p, ok := plexCleanPath("/library/parts/7/file.mkv", "/library/parts/"); !ok || p != "/library/parts/7/file.mkv" {
		t.Fatalf("clean library path rejected: %q %v", p, ok)
	}
	for _, bad := range []string{"", "http://evil/library/parts/7", "/library/parts/../secret", "/photo/a.jpg"} {
		if _, ok := plexCleanPath(bad, "/library/parts/"); ok {
			t.Errorf("plexCleanPath accepted %q", bad)
		}
	}
}

func TestPlexURLsKeepTheTokenOut(t *testing.T) {
	direct := plexDirectURL("ab12", "42", "/library/parts/7/file.mkv", "mkv", "sess1")
	if strings.Contains(strings.ToLower(direct), "token") {
		t.Fatalf("direct URL carries a token: %s", direct)
	}
	if !strings.Contains(direct, "/plexv/ab12/42.mkv?") || !strings.Contains(direct, "part=%2Flibrary%2Fparts%2F7%2Ffile.mkv") {
		t.Fatalf("direct URL = %s", direct)
	}
	trans := plexTranscodeURL("ab12", "42", "sess2", 90)
	if strings.Contains(strings.ToLower(trans), "token") {
		t.Fatalf("transcode URL carries a token: %s", trans)
	}
	if !strings.Contains(trans, "/plexh/ab12/start/42.m3u8?") || !strings.Contains(trans, "offset=90") {
		t.Fatalf("transcode URL = %s", trans)
	}
}

func TestPlexRewritePlaylist(t *testing.T) {
	base, _ := url.Parse("https://plex.example/video/transcode/universal/start.m3u8?X-Plex-Token=leak")
	body := "#EXTM3U\n" +
		"session/abc/index.m3u8\n" +
		"https://plex.example/video/transcode/universal/session/abc/seg.ts?X-Plex-Token=leak&foo=1\n" +
		"#EXT-X-ENDLIST"
	got := plexRewritePlaylist(body, "ab12", base)
	if strings.Contains(got, "leak") {
		t.Fatalf("playlist kept the server token: %s", got)
	}
	if !strings.Contains(got, "/plexh/ab12/session/abc/index.m3u8") {
		t.Fatalf("relative playlist entry was not rewritten: %s", got)
	}
	if !strings.Contains(got, "/plexh/ab12/session/abc/seg.ts?foo=1") {
		t.Fatalf("absolute playlist entry was not rewritten: %s", got)
	}
	if !strings.Contains(got, "#EXT-X-ENDLIST") {
		t.Fatalf("playlist tags were lost: %s", got)
	}
}

func TestMusicProxyVideoReportRecognisesPlex(t *testing.T) {
	cases := []struct {
		addr string
		want proxyVideoReport
	}{
		{"http://127.0.0.1:47533/plexv/ab12/42.mp4?part=x&session=s1", proxyVideoReport{Src: "plex", ID: "42", Session: "s1"}},
		{"http://127.0.0.1:47533/plexh/ab12/start/43.m3u8?session=s2", proxyVideoReport{Src: "plex", ID: "43", Session: "s2"}},
		{"http://127.0.0.1:47533/jfv/ab12/44/-.mkv?MediaSourceId=x", proxyVideoReport{Src: "jellyfin", ID: "44"}},
		{"http://example.com/plexv/ab12/42.mp4", proxyVideoReport{}},
	}
	for _, c := range cases {
		if got := musicProxyVideoReport(c.addr); got != c.want {
			t.Errorf("musicProxyVideoReport(%q) = %+v, want %+v", c.addr, got, c.want)
		}
	}
}

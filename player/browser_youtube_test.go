package main

import "testing"

// Every form a YouTube video link takes on the web — including the
// click-tracking wrappers search results use — opens the video in Media, at
// the start time the link names, without the playlist it came from.
func TestBrowserYouTubeVideoLinks(t *testing.T) {
	const id = "dQw4w9WgXcQ"
	cases := []struct {
		in    string
		start float64
	}{
		{"https://www.youtube.com/watch?v=" + id, 0},
		{"http://youtube.com/watch?v=" + id + "&feature=share", 0},
		{"https://m.youtube.com/watch?v=" + id + "&list=PL0123456789&index=2", 0},
		{"https://music.youtube.com/watch?v=" + id, 0},
		{"https://youtu.be/" + id + "?t=90", 90},
		{"https://www.youtube.com/shorts/" + id, 0},
		{"https://www.youtube.com/live/" + id + "?si=abc", 0},
		{"https://www.youtube-nocookie.com/embed/" + id + "?start=42", 42},
		{"https://www.youtube.com/watch?v=" + id + "&t=1m30s", 90},
		{"https://www.youtube.com/watch?v=" + id + "#t=2m", 120},
		// DuckDuckGo Lite's results (the browser's search engine) are redirect
		// links, relative or absolute once resolved against the results page.
		{"/l/?uddg=https%3A%2F%2Fwww.youtube.com%2Fwatch%3Fv%3D" + id + "&rut=abc", 0},
		{"https://lite.duckduckgo.com/l/?uddg=https%3A%2F%2Fwww.youtube.com%2Fwatch%3Fv%3D" + id + "&rut=abc", 0},
		{"https://duckduckgo.com/l/?uddg=https%3A%2F%2Fyoutu.be%2F" + id + "%3Ft%3D1h2m3s", 3723},
		{"https://www.google.com/url?q=https://www.youtube.com/watch?v%3D" + id + "&sa=U", 0},
		{"https://www.google.co.uk/url?url=https%3A%2F%2Fyoutu.be%2F" + id, 0},
		// The EU consent wall a redirect to a watch page ends on.
		{"https://consent.youtube.com/m?continue=https%3A%2F%2Fwww.youtube.com%2Fwatch%3Fv%3D" + id + "%26cbrd%3D1&gl=DE&hl=de", 0},
	}
	for _, c := range cases {
		link, ok := brwYouTubeVideo(c.in)
		if !ok {
			t.Errorf("%q: not taken for a YouTube video", c.in)
			continue
		}
		if link.ID != id || link.URL != "https://www.youtube.com/watch?v="+id {
			t.Errorf("%q: got id %q url %q", c.in, link.ID, link.URL)
		}
		if link.Start != c.start {
			t.Errorf("%q: start %v, want %v", c.in, link.Start, c.start)
		}
	}
}

// Pages that are not one video stay pages, and lookalike hosts are not
// YouTube.
func TestBrowserYouTubeNonVideoLinks(t *testing.T) {
	for _, in := range []string{
		"",
		"javascript:alert(1)",
		"https://www.youtube.com/",
		"https://www.youtube.com/@somechannel/videos",
		"https://www.youtube.com/channel/UC0123456789abcdefghijkl",
		"https://www.youtube.com/c/abcdefghijk",
		"https://www.youtube.com/playlist?list=PL0123456789",
		"https://www.youtube.com/results?search_query=cats",
		"https://www.youtube.com/watch?v=short",
		"https://www.youtube.com/watch?v=dQw4w9WgXc!",
		"https://notyoutube.com/watch?v=dQw4w9WgXcQ",
		"https://youtube.com.example.net/watch?v=dQw4w9WgXcQ",
		"https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fwatch%3Fv%3DdQw4w9WgXcQ",
		"https://duckduckgo.com/l/?uddg=javascript%3Aalert(1)",
		"https://example.com/l/?uddg=https%3A%2F%2Fyoutu.be%2FdQw4w9WgXcQ",
	} {
		if link, ok := brwYouTubeVideo(in); ok {
			t.Errorf("%q: taken for video %q", in, link.ID)
		}
	}
}

func TestBrowserYouTubeStartOffsets(t *testing.T) {
	for in, want := range map[string]float64{
		"90": 90, "90s": 90, "1m30s": 90, "1m30": 90, "1h2m3s": 3723, " 15 ": 15,
		"": 0, "0": 0, "-5": 0, "abc": 0, "1x": 0, "nan": 0, "inf": 0, "9999999999": 0,
	} {
		if got := brwYouTubeStart(in); got != want {
			t.Errorf("brwYouTubeStart(%q) = %v, want %v", in, got, want)
		}
	}
}

// The link's text becomes the video's title, without the search result's
// " - YouTube", a play card's mark, or a URL standing in for a title.
func TestBrowserYouTubeTitle(t *testing.T) {
	for in, want := range map[string]string{
		"Never Gonna Give You Up (Official Video) - YouTube": "Never Gonna Give You Up (Official Video)",
		"▶ Live coding,   tonight":                           "Live coding, tonight",
		"▶ Play video":                                       "",
		"https://youtu.be/dQw4w9WgXcQ":                       "",
		"www.youtube.com/watch?v=dQw4w9WgXcQ":                "",
		"/watch":                                             "",
		"YouTube":                                            "",
		"":                                                   "",
	} {
		if got := brwVideoTitle(in); got != want {
			t.Errorf("brwVideoTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// A load that redirects to a video is queued for the render pass; any other
// landing is the browser's to publish.
func TestBrowserYouTubeLandingQueue(t *testing.T) {
	t.Cleanup(func() {
		brwLanding.mu.Lock()
		brwLanding.pending = false
		brwLanding.mu.Unlock()
	})
	if brwQueueLanding("page", "https://t.co/x", "https://example.com/", true, 1, "https://example.com/next") {
		t.Fatal("a landing on an ordinary page was queued")
	}
	if brwQueueLanding("page", "https://youtu.be/dQw4w9WgXcQ", "", true, 2, "https://youtu.be/dQw4w9WgXcQ") {
		t.Fatal("a load that did not move was queued")
	}
	if !brwQueueLanding("page", "https://t.co/x", "https://example.com/", true, 3, "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=5") {
		t.Fatal("a redirect to a video was not queued")
	}
	brwLanding.mu.Lock()
	pending, back, request, link := brwLanding.pending, brwLanding.back, brwLanding.request, brwLanding.link
	brwLanding.mu.Unlock()
	if !pending || back != "https://example.com/" || request != 3 || link.ID != "dQw4w9WgXcQ" || link.Start != 5 {
		t.Fatalf("queued %v back %q request %d link %+v", pending, back, request, link)
	}
}

// The hop that only led to a video leaves the history, so Back from the page
// it was followed from does not step into it.
func TestBrowserYouTubeDropHop(t *testing.T) {
	browserState.mu.Lock()
	savedHistory, savedIndex := browserState.history, browserState.index
	browserState.history = []string{"https://a/", "https://b/", "https://t.co/x"}
	browserState.index = 2
	browserState.mu.Unlock()
	t.Cleanup(func() {
		browserState.mu.Lock()
		browserState.history, browserState.index = savedHistory, savedIndex
		browserState.mu.Unlock()
	})

	brwDropHop("https://elsewhere/")
	brwDropHop("https://t.co/x")
	browserState.mu.Lock()
	history, index := append([]string(nil), browserState.history...), browserState.index
	browserState.mu.Unlock()
	if len(history) != 2 || history[1] != "https://b/" || index != 1 {
		t.Fatalf("history %v index %d after dropping the hop", history, index)
	}
}

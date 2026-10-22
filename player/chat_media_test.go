package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestChatImageURLDetection(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"look https://example.com/cat.png", "https://example.com/cat.png"},
		{"(https://example.com/cat.jpg?w=400)", "https://example.com/cat.jpg?w=400"},
		{"https://example.com/page and https://example.com/dog.webp", "https://example.com/dog.webp"},
		{"https://example.com/page", ""},
		{"no links here", ""},
		{"https://example.com/cat.png.", "https://example.com/cat.png"},
	}
	for _, tc := range cases {
		if got := firstChatImageURL(tc.text); got != tc.want {
			t.Errorf("firstChatImageURL(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

// resetChatAttachmentState isolates the attachment cache and root between
// cases.
func resetChatAttachmentState(t *testing.T, root string) {
	t.Helper()
	prevDownloads, prevFetching := chatImageDownloads, chatImageFetching
	prevRoot := chatAttachmentRoot
	chatImageDownloads = make(map[string][]byte)
	chatImageFetching = make(map[string]bool)
	chatAttachmentRoot = root
	t.Cleanup(func() {
		chatImageDownloads, chatImageFetching = prevDownloads, prevFetching
		chatAttachmentRoot = prevRoot
	})
}

// A chat attachment stores a local path; the preview must read that file
// directly instead of treating it as an HTTP URL.
func TestChatImageLocalAttachmentLoads(t *testing.T) {
	dir := t.TempDir()
	resetChatAttachmentState(t, dir)

	path := filepath.Join(dir, "attached.png")
	payload := []byte("not-a-real-png-but-enough-for-the-fetch")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	fetchChatImage(path)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		chatImageDLMutex.Lock()
		data := chatImageDownloads[path]
		chatImageDLMutex.Unlock()
		if len(data) > 0 {
			if string(data) != string(payload) {
				t.Fatalf("preview data = %q, want the file contents", data)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("local attachment was never read")
}

// The path in a message is not proof of anything: it can be typed by hand, come
// from a restored history file, or arrive with a remote message. Only an image
// below the folder the File Explorer attached from is read from disk.
func TestChatAttachmentRejectsPathsOutsideTheRoot(t *testing.T) {
	root := t.TempDir()
	resetChatAttachmentState(t, root)

	outside := t.TempDir()
	stolen := filepath.Join(outside, "secret.png")
	if err := os.WriteFile(stolen, []byte("top secret"), 0o644); err != nil {
		t.Fatalf("write decoy: %v", err)
	}
	if got, ok := chatLocalAttachment(stolen); ok {
		t.Fatalf("read %q outside the attachment root", got)
	}

	traversal := filepath.Join(root, "..", filepath.Base(outside), "secret.png")
	if got, ok := chatLocalAttachment(traversal); ok {
		t.Fatalf("traversal path %q was accepted as %q", traversal, got)
	}

	// An image inside the root is still loaded.
	inside := filepath.Join(root, "photo.png")
	if err := os.WriteFile(inside, []byte("png"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	if got, ok := chatLocalAttachment(inside); !ok || got != inside {
		t.Fatalf("chatLocalAttachment(%q) = %q, %v; want the file itself", inside, got, ok)
	}

	// A non-image file is not an attachment even inside the root.
	text := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(text, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	if got, ok := chatLocalAttachment(text); ok {
		t.Fatalf("non-image %q was read as an attachment (%q)", text, got)
	}
}

// Every link a message carries must become a preview card, in order and
// without duplicates, so nothing in a chat is unreadable or unclickable.
func TestChatMessageLinksCollectsEveryLink(t *testing.T) {
	got := chatMessageLinks("see https://example.com/a, https://example.com/a and https://youtu.be/abc123.")
	want := []string{"https://example.com/a", "https://youtu.be/abc123"}
	if len(got) != len(want) {
		t.Fatalf("chatMessageLinks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("link %d = %q, want %q", i, got[i], want[i])
		}
	}
	if many := chatMessageLinks("a.com b.com c.com d.com"); len(many) != 0 {
		t.Errorf("bare words produced links: %v", many)
	}
}

// Media links play; anything else has to be browsable, which is what the card's
// action and its click handler key off.
func TestChatLinkActionRouting(t *testing.T) {
	cases := []struct {
		url   string
		plays bool
	}{
		{"https://youtu.be/abc123", true},
		{"https://cdn.example.com/track.mp3", true},
		{"https://example.com/article", false},
		{"https://github.com/jukaLang/JukaHubV2/issues", false},
	}
	for _, tc := range cases {
		if got := chatLinkOpensInPlayer(tc.url); got != tc.plays {
			t.Errorf("chatLinkOpensInPlayer(%q) = %v, want %v", tc.url, got, tc.plays)
		}
	}
}

// A preview tile must be cheap: the poster frame for YouTube, the picture
// itself for an image link, and the site's 64px icon for any other page.
func TestChatLinkPreviewURLCostsLittle(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "https://i.ytimg.com/vi/dQw4w9WgXcQ/default.jpg"},
		{"https://youtu.be/dQw4w9WgXcQ", "https://i.ytimg.com/vi/dQw4w9WgXcQ/default.jpg"},
		{"https://example.com/cat.png", "https://example.com/cat.png"},
		{"https://news.example.com/story", "https://www.google.com/s2/favicons?sz=64&domain=news.example.com"},
	}
	for _, tc := range cases {
		if got := chatLinkPreviewURL(tc.url); got != tc.want {
			t.Errorf("chatLinkPreviewURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// A YouTube id is used to build a thumbnail URL, so only a plain id is allowed
// through; anything else must not reach the CDN path.
func TestChatYouTubeIDRejectsJunk(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ": "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ":                "dQw4w9WgXcQ",
		"https://youtu.be/../../etc":                  "",
		"https://youtu.be/short":                      "",
		"https://example.com/watch?v=dQw4w9WgXcQ":     "dQw4w9WgXcQ",
	}
	for u, want := range cases {
		if got := chatYouTubeID(u); got != want {
			t.Errorf("chatYouTubeID(%q) = %q, want %q", u, got, want)
		}
	}
}

// A message with links renders one preview card per link, in reading order and
// inside the chat panel, and every card is a live click target. This is what
// turns a pasted URL into a page the handheld can actually read.
func TestChatLinksRenderAsClickableCards(t *testing.T) {
	if testing.Short() {
		t.Skip("render probe skipped in -short mode")
	}
	config, err := loadConfig("jukaconfig.json")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if findSceneIndex(config, "Chat") < 0 {
		t.Skip("Chat scene missing")
	}
	newProbeFrame(t)
	renderer, err := sdl.CreateRenderer(renderWin, -1, sdl.RENDERER_SOFTWARE)
	if err != nil {
		t.Skipf("software renderer unavailable: %v", err)
	}
	defer destroyTestRenderer(renderer)
	withScreen(t, probeW, probeH)

	links := []string{"https://example.com/story", "https://github.com/jukaLang/JukaHubV2/issues/12", "https://youtu.be/dQw4w9WgXcQ"}
	prevMsgs := chatMessages
	prevOffset := chatScrollOffset
	chatMutex.Lock()
	chatMessages = []ChatMessage{
		{Sender: "Juka AI", Text: "Story: " + links[0] + " and issue " + links[1], Timestamp: time.Now()},
		{Sender: "Juka AI", Text: "Video " + links[2], Timestamp: time.Now()},
	}
	chatMutex.Unlock()
	chatScrollOffset = 0
	resetChatAttachmentState(t, ".")
	// Layout is what is under test; claim each preview fetch so the test never
	// reaches the network.
	chatImageDLMutex.Lock()
	for _, link := range links {
		if u := chatLinkPreviewURL(link); u != "" {
			chatImageFetching[u] = true
		}
	}
	chatImageDLMutex.Unlock()
	t.Cleanup(func() {
		chatMutex.Lock()
		chatMessages = prevMsgs
		chatMutex.Unlock()
		chatScrollOffset = prevOffset
		chatLinkRects = nil
		chatTextLinkRects = nil
	})

	renderChat(renderer, config, Element{Type: "chat", X: 0, Y: 0, Width: "1240", Height: "640", Font: "small"})

	if len(chatLinkRects) != len(links) {
		t.Fatalf("rendered %d link cards, want %d", len(chatLinkRects), len(links))
	}
	// Messages draw newest-first from the bottom, so the cards arrive out of
	// reading order; what must hold is that no two cards overlap.
	for i := 0; i < len(chatLinkRects); i++ {
		for j := i + 1; j < len(chatLinkRects); j++ {
			a, b := chatLinkRects[i].rect, chatLinkRects[j].rect
			if a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H {
				t.Errorf("cards %d and %d overlap: %+v %+v", i, j, a, b)
			}
		}
	}
	plays, browsable := 0, 0
	for _, hit := range chatLinkRects {
		r := hit.rect
		if r.W <= 0 || r.H <= 0 || r.W > 360 {
			t.Errorf("card %+v has an unusable size", r)
			continue
		}
		if r.Y < 0 || r.Y+r.H > 640 {
			t.Errorf("card %+v is outside the chat panel", r)
		}
		if hit.play {
			plays++
			continue
		}
		browsable++
		if !handleChatLinkClick(r.X+1, r.Y+1, nil) {
			t.Errorf("click at (%d,%d) did not hit its link card", r.X+1, r.Y+1)
		}
	}
	if plays != 1 || browsable != 2 {
		t.Errorf("play cards = %d, browsable cards = %d; want 1 and 2", plays, browsable)
	}

	// The link text itself is a target too, so tapping the words plays the
	// video instead of requiring a hit on the card below.
	if len(chatTextLinkRects) == 0 {
		t.Fatal("no clickable link text was recorded")
	}
	sawPlayText := false
	for _, hit := range chatTextLinkRects {
		r := hit.rect
		if r.W <= 0 || r.H <= 0 || r.Y < 0 || r.Y+r.H > 640 {
			t.Errorf("text link %+v has an unusable rect", r)
		}
		if hit.play {
			sawPlayText = true
		}
	}
	if !sawPlayText {
		t.Error("the YouTube link text was not marked as playable")
	}
}

func TestChatPlayableURLDetection(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"watch https://www.youtube.com/watch?v=abc123 now", "https://www.youtube.com/watch?v=abc123"},
		{"https://youtu.be/abc123", "https://youtu.be/abc123"},
		{"song https://cdn.example.com/track.mp3", "https://cdn.example.com/track.mp3"},
		{"https://example.com/article", ""},
		{"https://example.com/article and https://youtu.be/xyz", "https://youtu.be/xyz"},
	}
	for _, tc := range cases {
		if got := chatPlayableURL(tc.text); got != tc.want {
			t.Errorf("chatPlayableURL(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

// The link text inside a bubble is a click target of its own: the spans must
// point at exactly the URL characters, so the rect drawn over them lands on the
// words the reader taps.
func TestChatLineLinkSpans(t *testing.T) {
	line := "watch https://youtu.be/dQw4w9WgXcQ now"
	spans := chatLineLinkSpans(line)
	if len(spans) != 1 {
		t.Fatalf("spans = %+v, want one YouTube link", spans)
	}
	if got := line[spans[0].start:spans[0].end]; got != "https://youtu.be/dQw4w9WgXcQ" {
		t.Fatalf("span covers %q", got)
	}
	if spans[0].url != "https://youtu.be/dQw4w9WgXcQ" {
		t.Errorf("url = %q", spans[0].url)
	}

	// Trailing punctuation belongs to the sentence, not the link.
	spans = chatLineLinkSpans("see https://example.com/story, then https://youtu.be/abc123.")
	if len(spans) != 2 {
		t.Fatalf("spans = %+v, want two links", spans)
	}
	if spans[0].url != "https://example.com/story" || spans[1].url != "https://youtu.be/abc123" {
		t.Errorf("urls = %q, %q", spans[0].url, spans[1].url)
	}

	if spans := chatLineLinkSpans("no links on this line"); len(spans) != 0 {
		t.Errorf("plain text produced spans: %+v", spans)
	}
}

// A link whose text is clipped by the wrap must not become a target: clicking
// it would play a truncated URL.
func TestChatLineLinkTargetsSkipClippedLinks(t *testing.T) {
	whole := map[string]bool{"https://youtu.be/dQw4w9WgXcQ": true}
	if got := chatLineLinkTargets("watch https://youtu.be/dQw4w9WgXcQ now", whole); len(got) != 1 {
		t.Fatalf("whole link targets = %+v, want 1", got)
	}
	if got := chatLineLinkTargets("watch https://youtu.be/dQw4w9WgX…", whole); len(got) != 0 {
		t.Fatalf("clipped link targets = %+v, want none", got)
	}
}

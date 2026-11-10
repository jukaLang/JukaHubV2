package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

// The upload body is laid out ahead of time with its exact length, and it
// parses as Discord's multipart message: payload_json first, then files[n]
// with their names, each part holding exactly Size bytes of its file.
func TestChatMultipartBodyLengthAndParts(t *testing.T) {
	files := []chatUploadFile{
		{Name: "save.srm", Type: "application/octet-stream", Size: 5, Reader: strings.NewReader("hello")},
		// A reader longer than Size is cut at Size, so the length holds.
		{Name: `we"ird.txt`, Size: 3, Reader: strings.NewReader("abcEXTRA")},
	}
	payload := `{"content":"Juka: hi"}`
	body, ctype, length, err := chatMultipartBody([]byte(payload), files, "jukahubtestboundary")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(data)) != length {
		t.Fatalf("declared %d bytes, the body has %d", length, len(data))
	}
	media, params, err := mime.ParseMediaType(ctype)
	if err != nil || media != "multipart/form-data" || params["boundary"] != "jukahubtestboundary" {
		t.Fatalf("content type %q (%v)", ctype, err)
	}
	r := multipart.NewReader(bytes.NewReader(data), params["boundary"])
	want := []struct{ form, file, ctype, body string }{
		{"payload_json", "", "application/json", payload},
		{"files[0]", "save.srm", "application/octet-stream", "hello"},
		{"files[1]", `we"ird.txt`, "application/octet-stream", "abc"},
	}
	for i, w := range want {
		p, err := r.NextPart()
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		if p.FormName() != w.form || p.FileName() != w.file || p.Header.Get("Content-Type") != w.ctype {
			t.Fatalf("part %d is %q / %q / %q, want %q / %q / %q", i,
				p.FormName(), p.FileName(), p.Header.Get("Content-Type"), w.form, w.file, w.ctype)
		}
		got, err := io.ReadAll(p)
		if err != nil || string(got) != w.body {
			t.Fatalf("part %d holds %q (%v), want %q", i, got, err, w.body)
		}
	}
	if _, err := r.NextPart(); !errors.Is(err, io.EOF) {
		t.Fatalf("the body goes on past the last part: %v", err)
	}
}

// Upload names keep letters (any script), digits, dots and dashes, turn runs
// of anything else into one underscore, and never carry a path or a line
// break into the part header.
func TestChatUploadName(t *testing.T) {
	cases := map[string]string{
		"my save (1).srm":     "my_save_1.srm",
		"../../etc/passwd":    "passwd",
		`C:\Users\x\game.gba`: "game.gba",
		"Покемон.sav":         "Покемон.sav",
		"a\r\nb.txt":          "a_b.txt",
		"":                    "file",
		"  .bashrc":           "file.bashrc",
		"report.final.v2.log": "report.final.v2.log",
	}
	for in, want := range cases {
		if got := chatUploadName(in); got != want {
			t.Errorf("chatUploadName(%q) = %q, want %q", in, got, want)
		}
	}
}

// This device's own message is recognised in the shared bot's echo by its
// text and its files: names as Discord rewrites them, or the size when the
// name became something else.
func TestChatSameContentMatchesEchoedFiles(t *testing.T) {
	mine := ChatMessage{Text: "my save", Files: []ChatFile{{Name: "My Save.srm", Size: 8192}, {Name: "фото.png", Size: 100}}}
	echo := ChatMessage{Text: "my save", Files: []ChatFile{{Name: "My_Save.srm", Size: 8192}, {Name: "unknown.png", Size: 100}}}
	if !chatSameContent(mine, echo) {
		t.Fatal("the echo of a file message was not recognised")
	}
	other := ChatMessage{Text: "my save", Files: []ChatFile{{Name: "other.srm", Size: 1}, {Name: "x.png", Size: 2}}}
	if chatSameContent(mine, other) {
		t.Fatal("different files matched")
	}
	if chatSameContent(mine, ChatMessage{Text: "my save"}) {
		t.Fatal("a message without the files matched")
	}
	if !chatSameContent(ChatMessage{Text: "hi"}, ChatMessage{Text: "hi"}) {
		t.Fatal("plain text echoes stopped matching")
	}
}

// Juka is shown all of a short text file, and the start and the end of a long
// one, cut on character boundaries; a binary file is refused.
func TestChatAIFileExcerpt(t *testing.T) {
	dir := t.TempDir()
	short := filepath.Join(dir, "short.log")
	if err := os.WriteFile(short, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := chatAIFileExcerpt(short, 400)
	if err != nil || got != "one\ntwo\n" {
		t.Fatalf("short file: %q, %v", got, err)
	}

	// Two-byte letters after a five-byte word, so with a 401-byte budget (a
	// 100-byte start, a 301-byte end) both cuts land inside a character.
	long := filepath.Join(dir, "long.log")
	text := "BEGIN" + strings.Repeat("ж", 2000) + " END"
	if err := os.WriteFile(long, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = chatAIFileExcerpt(long, 401)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(got) {
		t.Fatal("the excerpt cut a character in half")
	}
	if !strings.HasPrefix(got, "BEGIN") || !strings.HasSuffix(got, " END") || !strings.Contains(got, "left out") {
		t.Fatalf("the excerpt lost the start, the end or the marker: %q", got)
	}
	if len(got) > 401+80 {
		t.Fatalf("the excerpt is %d bytes for a 401-byte budget", len(got))
	}

	bin := filepath.Join(dir, "save.srm")
	if err := os.WriteFile(bin, []byte{1, 0, 2, 0, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := chatAIFileExcerpt(bin, 400); !errors.Is(err, errChatNotText) {
		t.Fatalf("a binary file was read as text: %v", err)
	}

	// A log in an old code page is text: the odd byte is replaced, not a
	// reason to refuse the file.
	latin := filepath.Join(dir, "latin1.log")
	if err := os.WriteFile(latin, []byte("caf\xe9 ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if binary, err := chatLooksBinary(latin); err != nil || binary {
		t.Fatalf("a Latin-1 log looked binary: %v, %v", binary, err)
	}
	got, err = chatAIFileExcerpt(latin, 400)
	if err != nil || !utf8.ValidString(got) || !strings.Contains(got, " ok") {
		t.Fatalf("Latin-1 log: %q, %v", got, err)
	}
}

// Only a message of this device's that is going out right now, or one whose
// answer from Discord was lost a little while ago, is taken for an echo of the
// shared bot; a queued copy with the same words is not, nor is anybody else's
// message.
func TestChatEchoCandidate(t *testing.T) {
	now := time.Now()
	queued := ChatMessage{ID: "lq", Sender: "Juka", Own: true, Pending: true, Timestamp: now.Add(-time.Minute)}
	if chatEchoCandidate(queued, now, false) {
		t.Fatal("a queued message that has not started was taken for an echo")
	}
	if !chatEchoCandidate(queued, now, true) {
		t.Fatal("a page of history did not recognise the queued copy of what already went out")
	}
	chatUploadBegin("lq", func() {})
	inFlight := chatEchoCandidate(ChatMessage{ID: "lq", Own: true, Pending: true, Timestamp: now.Add(-time.Hour)}, now, false)
	chatUploadEnd()
	if !inFlight {
		t.Fatal("the upload in flight was not recognised after a long upload")
	}
	lost := ChatMessage{ID: "ll", Own: true, Timestamp: now.Add(-2 * time.Minute)}
	if !chatEchoCandidate(lost, now, false) {
		t.Fatal("a message whose answer was lost was not recognised")
	}
	if chatEchoCandidate(ChatMessage{ID: "123456789012345678", Own: true}, now, false) {
		t.Fatal("a message that holds its Discord id was taken for another echo")
	}
	if chatEchoCandidate(ChatMessage{ID: "lx"}, now, false) {
		t.Fatal("somebody else's message was taken for an echo")
	}
	if chatSameContent(ChatMessage{Sender: "Juka", Text: "hi"}, ChatMessage{Sender: "Alex", Text: "hi"}) {
		t.Fatal("another sender's message matched")
	}
	for i := 0; i < 50; i++ {
		if id := newChatMessageID(); isDiscordSnowflake(id) || len(id) > 25 {
			t.Fatalf("local id %q reads as a Discord id or is too long for a nonce", id)
		}
	}
}

// A message deleted here while it went out is recognised when its echo, or
// the answer to its upload, comes back — once.
func TestChatUnsendTakesBackDeletedMessages(t *testing.T) {
	now := time.Now()
	gone := ChatMessage{ID: "lgone", Sender: "Juka", Text: "oops", Timestamp: now.Add(-time.Minute),
		Files: []ChatFile{{Name: "a.zip", Size: 9}}}
	chatRememberUnsend(gone)
	// Somebody else's message with the same words, long after: never it.
	later := ChatMessage{ID: "444444444444444444", Sender: "Juka", Text: "oops", Timestamp: now.Add(10 * time.Minute),
		Files: []ChatFile{{Name: "a.zip", Size: 9}}}
	if chatTakeUnsend("", &later) {
		t.Fatal("a later message with the same words was taken for the deleted one")
	}
	echo := ChatMessage{ID: "333333333333333333", Sender: "Juka", Text: "oops", Timestamp: now,
		Files: []ChatFile{{Name: "a.zip", Size: 9}}}
	if !chatTakeUnsend("", &echo) {
		t.Fatal("the echo of a deleted message was not recognised")
	}
	if chatTakeUnsend("", &echo) {
		t.Fatal("the same deleted message was taken back twice")
	}
	chatRememberUnsend(gone)
	if !chatTakeUnsend("lgone", nil) {
		t.Fatal("the answer to a deleted upload was not recognised")
	}
	chatNoteDropped(echo.ID)
	if !chatWasDropped(echo.ID) || chatWasDropped(later.ID) {
		t.Fatal("taken-back ids are not remembered as such")
	}
}

// Discord's file links carry their expiry (ex=, hex Unix seconds); other
// links never expire.
func TestChatLinkExpiry(t *testing.T) {
	link := "https://cdn.discordapp.com/attachments/1/2/a.png?ex=6720f1a0&is=671fa020&hm=abc"
	if got := chatLinkExpiry(link); got != 0x6720f1a0 {
		t.Fatalf("expiry %x", got)
	}
	if got := chatLinkExpiry("https://example.com/a.png?ex=6720f1a0"); got != 0 {
		t.Fatalf("a link from elsewhere expires at %x", got)
	}
	if got := chatLinkExpiry("https://media.discordapp.net/a.png"); got != 0 {
		t.Fatalf("a link without ex= expires at %x", got)
	}
	if !chatLinkExpired(link, time.Unix(0x6720f1a0, 0)) {
		t.Fatal("a link at its expiry still counts as fresh")
	}
	if chatLinkExpired(link, time.Unix(0x6720f1a0-3600, 0)) {
		t.Fatal("a link an hour from its expiry counts as expired")
	}
}

// A follow-up question carries the latest file of the conversation again; a
// "New conversation" divider ends that.
func TestChatAIFileContextFollowUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crash.log")
	if err := os.WriteFile(path, []byte("panic: nil map\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	asked := ChatMessage{ID: "q1", Sender: "You", Own: true, Text: "why?", Files: []ChatFile{{Name: "crash.log", Size: 15, Path: path}}}
	answer := ChatMessage{ID: "a1", Sender: chatAISender, Text: "A nil map."}
	follow := ChatMessage{ID: "q2", Sender: "You", Own: true, Text: "which line?"}

	extra, err := chatAIFileContext(asked, nil)
	if err != nil || !strings.Contains(extra, "panic: nil map") || !strings.Contains(extra, "crash.log") {
		t.Fatalf("the question's own file: %q, %v", extra, err)
	}
	extra, err = chatAIFileContext(follow, []ChatMessage{asked, answer})
	if err != nil || !strings.Contains(extra, "panic: nil map") {
		t.Fatalf("the follow-up lost the file: %q, %v", extra, err)
	}
	divider := ChatMessage{ID: "d", Sender: chatSystemSender, Text: "New conversation"}
	extra, err = chatAIFileContext(follow, []ChatMessage{asked, answer, divider})
	if err != nil || extra != "" {
		t.Fatalf("a new conversation still carried the old file: %q, %v", extra, err)
	}
	if got := chatAIMessageText(asked); got != "why? [attached: crash.log]" {
		t.Fatalf("history text %q", got)
	}
}

// Every Discord attachment becomes a file; the first picture among them is
// the bubble's picture and the others are cards.
func TestChatFilesFromDiscordAndInlinePicture(t *testing.T) {
	m := &discordgo.Message{ID: "123456789012345678", Attachments: []*discordgo.MessageAttachment{
		{Filename: "notes.txt", URL: "https://cdn.discordapp.com/a/notes.txt", Size: 10, ContentType: "text/plain"},
		{Filename: "shot.png", URL: "https://cdn.discordapp.com/a/shot.png", Size: 2048, ContentType: "image/png"},
		nil,
		{Filename: "gone.bin"},
	}}
	files := chatFilesFromDiscord(m)
	if len(files) != 2 || files[0].Name != "notes.txt" || files[1].Msg != m.ID || files[1].Size != 2048 {
		t.Fatalf("files %+v", files)
	}
	msg := ChatMessage{ID: m.ID, Files: files}
	if i := chatInlineImageIndex(msg); i != 1 {
		t.Fatalf("picture index %d, want 1", i)
	}
	if u := chatMessageImageURL(msg); u != files[1].URL {
		t.Fatalf("picture %q", u)
	}
	lay := chatBuildLayout(nil, &msg, 300, false)
	if lay.imageFile != 1 || lay.image != "" || len(lay.files) != 1 || lay.files[0] != 0 {
		t.Fatalf("layout picture %d %q cards %v", lay.imageFile, lay.image, lay.files)
	}
	if got := chatMessagePreview(msg); got != "Sent 2 files" {
		t.Fatalf("preview %q", got)
	}
}

// What Discord answers to an upload decides between done, try again later and
// a sentence under the bubble.
func TestChatUploadAnswer(t *testing.T) {
	ok := `{"id":"111111111111111111","attachments":[{"id":"1","url":"https://cdn.example/y.zip","filename":"y.zip","size":3}]}`
	m, err := chatUploadAnswer(http.StatusOK, http.Header{}, []byte(ok))
	if err != nil || m == nil || m.ID != "111111111111111111" || len(m.Attachments) != 1 || m.Attachments[0].URL != "https://cdn.example/y.zip" {
		t.Fatalf("200: %+v, %v", m, err)
	}
	cases := []struct {
		status  int
		body    string
		refusal string // "" means it is tried again
		retry   time.Duration
	}{
		{http.StatusTooManyRequests, `{"retry_after": 2.5}`, "", 2500 * time.Millisecond},
		{http.StatusRequestEntityTooLarge, ``, "too big", 0},
		{http.StatusForbidden, `{"code": 50013, "message": "Missing Permissions"}`, "doesn't take files", 0},
		{http.StatusBadGateway, ``, "", 0},
		{http.StatusBadRequest, `{"code": 50035}`, "refused the message", 0},
	}
	for _, c := range cases {
		_, err := chatUploadAnswer(c.status, http.Header{}, []byte(c.body))
		var upErr *chatUploadError
		if !errors.As(err, &upErr) {
			t.Fatalf("HTTP %d: %v", c.status, err)
		}
		if c.refusal == "" && upErr.refusal != "" || c.refusal != "" && !strings.Contains(upErr.refusal, c.refusal) {
			t.Errorf("HTTP %d: refusal %q, want %q", c.status, upErr.refusal, c.refusal)
		}
		if upErr.retryAfter != c.retry {
			t.Errorf("HTTP %d: retry after %v, want %v", c.status, upErr.retryAfter, c.retry)
		}
	}
	if d := chatRetryAfter("3", 0); d != 3*time.Second {
		t.Errorf("Retry-After header: %v", d)
	}
	if d := chatRetryAfter("", 0); d != 5*time.Second {
		t.Errorf("no figure: %v", d)
	}
	if d := chatRetryAfter("", 9999); d != 10*time.Minute {
		t.Errorf("capped: %v", d)
	}
}

// An upload is one POST to the channel's messages with the session's
// credentials: the caption in the relay format, the file under its cleaned
// name, an exact Content-Length, and progress reported up to the last byte.
func TestChatDiscordUploadStreamsToChannel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "my save.srm")
	content := bytes.Repeat([]byte{7}, 40000)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen struct {
		method, path, auth, agent string
		length                    int64
		content                   string
		names                     []string
		sizes                     []int
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mr, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		seen.method, seen.path = r.Method, r.URL.Path
		seen.auth, seen.agent, seen.length = r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.ContentLength
		mu.Unlock()
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(p)
			mu.Lock()
			if p.FormName() == "payload_json" {
				seen.content = string(data)
			} else {
				seen.names = append(seen.names, p.FileName())
				seen.sizes = append(seen.sizes, len(data))
			}
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"222222222222222222","attachments":[{"id":"9","filename":"my_save.srm","url":"https://cdn.example/my_save.srm","size":40000}]}`)
	}))
	defer srv.Close()
	oldBase := discordAPIBase
	discordAPIBase = srv.URL
	defer func() { discordAPIBase = oldBase }()

	msg := ChatMessage{ID: "local1", Sender: "Juka", Text: "my save", Files: []ChatFile{{Name: "my save.srm", Size: int64(len(content)), Path: path}}}
	// Progress is reported from the transport's goroutine that writes the body.
	var lastSent, lastTotal int64
	sent, err := chatDiscordUpload(context.Background(), chatUploadTarget{auth: "Bot secret", agent: "TestAgent", channel: "42"}, msg,
		func(n, total int64) {
			mu.Lock()
			lastSent, lastTotal = n, total
			mu.Unlock()
		})
	if err != nil {
		t.Fatal(err)
	}
	if sent == nil || sent.ID != "222222222222222222" {
		t.Fatalf("answer %+v", sent)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen.method != http.MethodPost || seen.path != "/channels/42/messages" || seen.auth != "Bot secret" || seen.agent != "TestAgent" {
		t.Fatalf("request %s %s auth %q agent %q", seen.method, seen.path, seen.auth, seen.agent)
	}
	if !strings.Contains(seen.content, `"content":"Juka: my save"`) ||
		!strings.Contains(seen.content, `"nonce":"local1"`) || !strings.Contains(seen.content, `"enforce_nonce":true`) {
		t.Fatalf("payload %s", seen.content)
	}
	if len(seen.names) != 1 || seen.names[0] != "my_save.srm" || seen.sizes[0] != len(content) {
		t.Fatalf("files %v %v", seen.names, seen.sizes)
	}
	if seen.length <= int64(len(content)) || lastTotal != seen.length || lastSent != seen.length {
		t.Fatalf("Content-Length %d, progress %d of %d", seen.length, lastSent, lastTotal)
	}

	// A file gone from the device is a refusal, not a retry.
	gone := msg
	gone.Files = []ChatFile{{Name: "lost.srm", Size: 1, Path: filepath.Join(dir, "lost.srm")}}
	_, err = chatDiscordUpload(context.Background(), chatUploadTarget{auth: "Bot secret", channel: "42"}, gone, nil)
	var upErr *chatUploadError
	if !errors.As(err, &upErr) || !strings.Contains(upErr.refusal, "no longer on this device") {
		t.Fatalf("missing file: %v", err)
	}
}

// The tray takes what a tab can send: no folders, empty files or duplicates,
// Discord's size and count limits in the community tab, and text only for
// Juka AI.
func TestChatStageFileRules(t *testing.T) {
	saved := chatViews
	defer func() { chatViews = saved }()
	chatViews = [chatTabCount]chatTabView{}
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	logFile := write("crash.log", []byte("panic: oops\n"))
	binFile := write("save.srm", []byte{0, 1, 2, 3})
	emptyFile := write("empty.txt", nil)

	if err := chatStageFile(chatTabCommunity, logFile); err != nil {
		t.Fatalf("a log for the community: %v", err)
	}
	if err := chatStageFile(chatTabCommunity, logFile); err == nil {
		t.Error("the same file went in twice")
	}
	if err := chatStageFile(chatTabCommunity, dir); err == nil {
		t.Error("a folder went in")
	}
	if err := chatStageFile(chatTabCommunity, emptyFile); err == nil {
		t.Error("an empty file went in")
	}
	big := filepath.Join(dir, "huge.zip")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(chatUploadLimit() + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := chatStageFile(chatTabCommunity, big); err == nil || !strings.Contains(err.Error(), "Discord takes") {
		t.Errorf("a file past Discord's limit: %v", err)
	}
	if err := chatStageFile(chatTabAI, binFile); err == nil {
		t.Error("Juka AI took a binary file")
	}
	if err := chatStageFile(chatTabAI, logFile); err != nil {
		t.Errorf("Juka AI refused a log: %v", err)
	}
	for i := 0; i < chatFilesMax; i++ {
		if err := chatStageFile(chatTabSaved, write(fmt.Sprintf("f%d.bin", i), []byte{1})); err != nil {
			t.Fatalf("file %d for Saved: %v", i, err)
		}
	}
	if err := chatStageFile(chatTabSaved, binFile); err == nil {
		t.Errorf("an eleventh file went in")
	}
	if n := chatStagedCount(chatTabCommunity); n != 1 {
		t.Fatalf("community tray holds %d files", n)
	}
	staged := chatStagedFiles(chatTabCommunity)[0]
	if staged.Name != "crash.log" || staged.Size != 12 || !strings.HasPrefix(staged.Type, "text/plain") || !chatPathTrusted(staged.Path) {
		t.Fatalf("staged %+v", staged)
	}
	if !chatUnstage(chatTabCommunity, 0) || chatStagedCount(chatTabCommunity) != 0 {
		t.Fatal("the file did not come off the tray")
	}
}

// A fresh history page renews the expiring file links of messages already
// held, keeps this device's paths, and upgrades a picture saved before files
// existed.
func TestChatRefreshLinks(t *testing.T) {
	old := ChatMessage{ID: "1", Files: []ChatFile{{Name: "a.zip", URL: "https://cdn/old", Path: "/card/a.zip"}}}
	fresh := ChatMessage{ID: "1", Files: []ChatFile{{Name: "a.zip", URL: "https://cdn/new", Msg: "1"}}}
	next, changed := chatRefreshLinks(old, fresh)
	if !changed || next.Files[0].URL != "https://cdn/new" || next.Files[0].Path != "/card/a.zip" || next.Files[0].Msg != "1" {
		t.Fatalf("relinked %+v (%v)", next.Files, changed)
	}
	if old.Files[0].URL != "https://cdn/old" {
		t.Fatal("the held message was changed in place")
	}
	legacy := ChatMessage{ID: "2", ImageURL: "https://cdn/pic.png"}
	upgraded, changed := chatRefreshLinks(legacy, ChatMessage{ID: "2", Files: []ChatFile{{Name: "pic.png", URL: "https://cdn/pic2.png"}}})
	if !changed || upgraded.ImageURL != "" || len(upgraded.Files) != 1 {
		t.Fatalf("legacy picture %+v", upgraded)
	}
	if _, changed := chatRefreshLinks(old, ChatMessage{ID: "1"}); changed {
		t.Fatal("a page without files changed the message")
	}
}

func TestChatSizeAndProgressLabels(t *testing.T) {
	cases := map[int64]string{0: "0 B", 512: "512 B", 1536: "2 KB", 5 << 20: "5.0 MB", 3 << 30: "3.0 GB"}
	for n, want := range cases {
		if got := chatSizeLabel(n); got != want {
			t.Errorf("chatSizeLabel(%d) = %q, want %q", n, got, want)
		}
	}
	files := []ChatFile{{Size: 100}, {Size: 300}}
	if got := chatFileShare(files, 0, 200, 400); got != 1 {
		t.Errorf("first file at half the request: %v", got)
	}
	if got := chatFileShare(files, 1, 200, 400); got < 0.33 || got > 0.34 {
		t.Errorf("second file at half the request: %v", got)
	}
	if got := chatPercentLabel(0.456); got != "46%" {
		t.Errorf("percent %q", got)
	}
}

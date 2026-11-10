package main

// chatupload.go — files going to the Discord channel, and files coming back
// from it.
//
// discordgo can send files, but it builds the whole request in memory first
// (MultipartBodyWithJSON) and gives every request twenty seconds: a 20 MB save
// would sit in the handheld's 1 GB of RAM twice over, and over its Wi-Fi it
// would time out half way. An upload here is one streaming multipart POST to
// the same endpoint the session posts messages to, with the same credentials:
// the JSON part and the part headers are laid out in memory (a few hundred
// bytes), the files are read from disk as the request goes, the length is
// exact, and the bubble shows how far it got. A watchdog ends a transfer that
// stops moving, instead of a clock that cuts a slow one short.
//
// One upload runs at a time, in the queue's order: the sender loop
// (chatsync.go) sends nothing else meanwhile, so the channel shows messages in
// the order they were written. Network trouble keeps the message queued and
// tries again with a back-off; Discord refusing the file (too big, no
// permission) says so under the bubble and offers Retry.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/bwmarrin/discordgo"
)

// The per-file limit Discord applies to a server, by its boost tier. The
// default rose from 10 to 20 MiB in September 2026; tier 2 and 3 raise it.
const (
	chatUploadDefault = 20 << 20
	chatUploadTier2   = 50 << 20
	chatUploadTier3   = 100 << 20
)

// chatUploadTier is the channel's server boost tier, learned when the chat
// connects (discordLoadHistory); chatUploadTierKnown says it has been.
var (
	chatUploadTier      atomic.Int32
	chatUploadTierKnown atomic.Bool
)

// chatSetUploadTier records the server's boost tier.
func chatSetUploadTier(tier int) {
	chatUploadTier.Store(int32(tier))
	chatUploadTierKnown.Store(true)
}

// chatUploadLimit is the biggest file the channel takes. Until the server's
// tier is known (the chat has not connected yet this session) it is the most
// any server takes: a queued upload can start before the tier arrives, and
// Discord itself refuses a file too big for its server (413).
func chatUploadLimit() int64 {
	if !chatUploadTierKnown.Load() {
		return chatUploadTier3
	}
	switch chatUploadTier.Load() {
	case 2:
		return chatUploadTier2
	case 3:
		return chatUploadTier3
	}
	return chatUploadDefault
}

// chatTransferStall is how long an upload or a download may go without moving
// a byte before it is given up (and, for an upload, tried again later).
const chatTransferStall = 60 * time.Second

// chatTransferClient is the client uploads and downloads use: no overall
// timeout (a big file on slow Wi-Fi takes as long as it takes, and
// chatStallWatch ends one that stopped), keep-alives off, and the fallback
// certificate bundle on a firmware without one (bugrepRootCAs).
func chatTransferClient() *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		DisableKeepAlives:     true,
	}
	if pool := bugrepRootCAs(); pool != nil {
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr}
}

// chatStallWatch cancels a transfer once no byte has moved for limit. touch
// records progress; stop ends the watch and is safe to call more than once.
func chatStallWatch(cancel context.CancelFunc, limit time.Duration) (touch func(), stop func()) {
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	done := make(chan struct{})
	var once sync.Once
	touch = func() { last.Store(time.Now().UnixNano()) }
	stop = func() { once.Do(func() { close(done) }) }
	step := limit / 6
	if step < 100*time.Millisecond {
		step = 100 * time.Millisecond
	}
	go func() {
		tick := time.NewTicker(step)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				if time.Since(time.Unix(0, last.Load())) > limit {
					cancel()
					return
				}
			}
		}
	}()
	return touch, stop
}

// chatProgressReader counts what is read through it. on hears the running
// total; done runs once at the end of the stream.
type chatProgressReader struct {
	r    io.Reader
	n    int64
	on   func(n int64)
	done func()
}

func (p *chatProgressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.n += int64(n)
		if p.on != nil {
			p.on(p.n)
		}
	}
	if err == io.EOF && p.done != nil {
		p.done()
		p.done = nil
	}
	return n, err
}

// ── The request ──────────────────────────────────────────────────────────

// chatUploadFile is one file part of an upload.
type chatUploadFile struct {
	Name   string
	Type   string
	Size   int64
	Reader io.Reader
}

// chatQuoteEscaper escapes a file name inside a quoted header parameter the
// way mime/multipart does, and drops line breaks so a name cannot add a
// header of its own.
var chatQuoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"", "\r", "", "\n", "")

// chatMultipartBody lays out a message upload the way Discord takes it: a
// payload_json part, then one files[n] part per file. The headers are built
// in memory and the files are read as the body is, so the length returned is
// exact before a byte is sent. boundary may be "" for a random one.
func chatMultipartBody(payload []byte, files []chatUploadFile, boundary string) (io.Reader, string, int64, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if boundary != "" {
		if err := mw.SetBoundary(boundary); err != nil {
			return nil, "", 0, err
		}
	}
	head := make(textproto.MIMEHeader)
	head.Set("Content-Disposition", `form-data; name="payload_json"`)
	head.Set("Content-Type", "application/json")
	part, err := mw.CreatePart(head)
	if err != nil {
		return nil, "", 0, err
	}
	if _, err := part.Write(payload); err != nil {
		return nil, "", 0, err
	}
	readers := make([]io.Reader, 0, 2*len(files)+1)
	var length int64
	for i, f := range files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[%d]"; filename="%s"`, i, chatQuoteEscaper.Replace(f.Name)))
		ctype := f.Type
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		h.Set("Content-Type", ctype)
		if _, err := mw.CreatePart(h); err != nil {
			return nil, "", 0, err
		}
		// Everything written so far (the payload, the delimiter and this
		// part's headers) goes ahead of the file's own bytes.
		lead := append([]byte(nil), buf.Bytes()...)
		buf.Reset()
		readers = append(readers, bytes.NewReader(lead), io.LimitReader(f.Reader, f.Size))
		length += int64(len(lead)) + f.Size
	}
	if err := mw.Close(); err != nil {
		return nil, "", 0, err
	}
	tail := append([]byte(nil), buf.Bytes()...)
	readers = append(readers, bytes.NewReader(tail))
	length += int64(len(tail))
	return io.MultiReader(readers...), mw.FormDataContentType(), length, nil
}

// chatUploadName is the name a file goes up under. Discord rewrites names it
// does not like; cleaning them here keeps what the channel and the other
// devices see predictable: letters, digits, dots and dashes, and one
// underscore for every run of anything else.
func chatUploadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	ext := filepath.Ext(name)
	clean := func(s string, limit int) string {
		var b strings.Builder
		n := 0
		under := false
		for _, r := range s {
			if n >= limit {
				break
			}
			if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.') {
				if under {
					continue
				}
				r = '_'
			}
			under = r == '_'
			b.WriteRune(r)
			n++
		}
		return strings.Trim(b.String(), "_.")
	}
	base := clean(strings.TrimSuffix(name, ext), 80)
	suffix := clean(strings.TrimPrefix(ext, "."), 12)
	if base == "" {
		base = "file"
	}
	if suffix == "" {
		return base
	}
	return base + "." + suffix
}

// chatUploadContent is the message text that goes with the files: "name:
// caption", the relay format every JukaHub device reads the sender from.
// Without a caption it is just "name:".
func chatUploadContent(sender, text string) string {
	text = strings.TrimSpace(text)
	switch {
	case sender == "":
		return text
	case text == "":
		return sender + ":"
	}
	return sender + ": " + text
}

// chatUploadError is an upload that did not go through. refusal is the
// sentence under the bubble when Discord said no; without one the trouble
// is worth another try later.
type chatUploadError struct {
	status     int
	code       int
	msg        string
	refusal    string
	retryAfter time.Duration
	stalled    bool
}

func (e *chatUploadError) Error() string {
	switch {
	case e.stalled:
		return "upload stalled"
	case e.status == 0 && e.refusal != "":
		return e.refusal
	}
	return fmt.Sprintf("discord upload: HTTP %d (code %d) %s", e.status, e.code, e.msg)
}

// chatUploadTarget is where an upload goes and as whom. It is copied from
// the session on the sender loop, which owns the session, before the upload's
// own goroutine starts.
type chatUploadTarget struct {
	auth    string
	agent   string
	channel string
}

// chatLiveSession is the open session and its channel as one value. The
// connect (chat.go) publishes it, so a goroutine other than the sender loop —
// a download asking for a fresh link, a delete taking a message back — reads
// the pair without racing the loop's writes to discordSession.
type chatLiveSession struct {
	dg      *discordgo.Session
	channel string
}

var chatLive atomic.Pointer[chatLiveSession]

// chatPublishSession records the session the chat now talks through (nil when
// it closed).
func chatPublishSession(dg *discordgo.Session, channel string) {
	if dg == nil {
		chatLive.Store(nil)
		return
	}
	chatLive.Store(&chatLiveSession{dg: dg, channel: channel})
}

// chatLiveNow is the published session and channel, or nil and "".
func chatLiveNow() (*discordgo.Session, string) {
	if s := chatLive.Load(); s != nil {
		return s.dg, s.channel
	}
	return nil, ""
}

// chatUploadTargetNow reads the live session; a variable so tests can stand in.
var chatUploadTargetNow = func() (chatUploadTarget, bool) {
	dg, ch := chatLiveNow()
	if dg == nil || ch == "" || dg.Token == "" {
		return chatUploadTarget{}, false
	}
	return chatUploadTarget{auth: dg.Token, agent: dg.UserAgent, channel: ch}, true
}

// chatDiscordUploadFn posts one message with its files; tests replace it.
var chatDiscordUploadFn = chatDiscordUpload

// chatDiscordUpload posts msg's text and files to the channel and returns the
// message Discord made of it (nil when the answer could not be read; the
// gateway's echo still brings the id then).
func chatDiscordUpload(ctx context.Context, target chatUploadTarget, msg ChatMessage, progress func(sent, total int64)) (*discordgo.Message, error) {
	payload := map[string]interface{}{"content": chatUploadContent(msg.Sender, msg.Text)}
	// The message's own id as a nonce Discord enforces: an upload sent again
	// because its answer was lost (a dropped connection after the last byte)
	// returns the message Discord already made instead of posting it twice.
	if id := msg.ID; id != "" && len(id) <= 25 {
		payload["nonce"] = id
		payload["enforce_nonce"] = true
	}
	if msg.Quote != nil && isDiscordSnowflake(msg.Quote.ID) {
		payload["message_reference"] = map[string]interface{}{
			"message_id":         msg.Quote.ID,
			"channel_id":         target.channel,
			"fail_if_not_exists": false,
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	parts := make([]chatUploadFile, 0, len(msg.Files))
	var opened []*os.File
	defer func() {
		for _, f := range opened {
			f.Close()
		}
	}()
	for _, cf := range msg.Files {
		gone := &chatUploadError{refusal: "Not sent — " + cf.Name + " is no longer on this device"}
		if cf.Path == "" {
			return nil, gone
		}
		f, err := os.Open(cf.Path)
		if err != nil {
			return nil, gone
		}
		opened = append(opened, f)
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return nil, gone
		}
		if info.Size() > chatUploadLimit() {
			return nil, &chatUploadError{refusal: "Not sent — " + cf.Name + " is too big for Discord"}
		}
		parts = append(parts, chatUploadFile{
			Name:   chatUploadName(cf.Name),
			Type:   chatMIMEType(cf.Name),
			Size:   info.Size(),
			Reader: f,
		})
	}
	reader, ctype, length, err := chatMultipartBody(body, parts, "")
	if err != nil {
		return nil, err
	}
	watch, stopAll := context.WithCancel(ctx)
	defer stopAll()
	touch, stop := chatStallWatch(stopAll, chatTransferStall)
	defer stop()
	// The watch ends when the last byte is out: waiting for Discord's answer
	// is the transport's ResponseHeaderTimeout's job.
	counted := &chatProgressReader{r: reader, done: stop, on: func(n int64) {
		touch()
		if progress != nil {
			progress(n, length)
		}
	}}
	req, err := http.NewRequestWithContext(watch, http.MethodPost, discordAPIBase+"/channels/"+target.channel+"/messages", counted)
	if err != nil {
		return nil, err
	}
	req.ContentLength = length
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", target.auth)
	if target.agent != "" {
		req.Header.Set("User-Agent", target.agent)
	}
	resp, err := chatTransferClient().Do(req)
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case watch.Err() != nil:
			return nil, &chatUploadError{stalled: true}
		}
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return chatUploadAnswer(resp.StatusCode, resp.Header, data)
}

// chatUploadAnswer reads Discord's answer to an upload.
func chatUploadAnswer(status int, header http.Header, data []byte) (*discordgo.Message, error) {
	if status >= 200 && status < 300 {
		var m discordgo.Message
		if err := json.Unmarshal(data, &m); err != nil || m.ID == "" {
			return nil, nil
		}
		return &m, nil
	}
	var apiErr struct {
		Code       int     `json:"code"`
		Message    string  `json:"message"`
		RetryAfter float64 `json:"retry_after"`
	}
	_ = json.Unmarshal(data, &apiErr)
	e := &chatUploadError{status: status, code: apiErr.Code, msg: apiErr.Message}
	switch {
	case status == http.StatusTooManyRequests:
		e.retryAfter = chatRetryAfter(header.Get("Retry-After"), apiErr.RetryAfter)
	case status == http.StatusRequestEntityTooLarge, apiErr.Code == 40005, apiErr.Code == 50045:
		e.refusal = "Not sent — the file is too big for this Discord server"
	case status >= 500, status == http.StatusRequestTimeout:
		// Discord's own trouble, or a request that took too long on its
		// side: worth another try in a while.
	case apiErr.Code == 50013, apiErr.Code == 50001, status == http.StatusForbidden:
		e.refusal = "Not sent — this channel doesn't take files"
	default:
		// A caption past Discord's 2000 characters lands here too.
		e.refusal = "Not sent — Discord refused the message"
	}
	return nil, e
}

// chatRetryAfter is how long a rate-limited upload waits: Discord's own
// figure (the body's retry_after, else the Retry-After header), within reason.
func chatRetryAfter(header string, seconds float64) time.Duration {
	if seconds <= 0 {
		if v, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil {
			seconds = v
		}
	}
	if seconds <= 0 {
		return 5 * time.Second
	}
	d := time.Duration(seconds * float64(time.Second))
	if d < time.Second {
		d = time.Second
	}
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

// ── The upload in flight ─────────────────────────────────────────────────

// chatUploads is the upload in flight and the back-off after a failed one.
// posting is the text message whose post (chatsync.go) is on its way; last is
// the message that went out last (posted or uploaded), and when that ended.
var chatUploads struct {
	mu        sync.Mutex
	id        string
	posting   string
	last      string
	lastAt    time.Time
	sent      int64
	total     int64
	cancel    context.CancelFunc
	cancelled bool
	retryAt   time.Time
	backoff   time.Duration
	note      string
}

// chatUploadBusy is set while an upload runs.
var chatUploadBusy atomic.Bool

// chatInFlight reports whether message id is going out right now: its files
// are uploading, or its text is being posted.
func chatInFlight(id string) bool {
	if id == "" {
		return false
	}
	chatUploads.mu.Lock()
	defer chatUploads.mu.Unlock()
	return chatUploads.id == id || chatUploads.posting == id
}

// chatPostingBegin and chatPostingEnd bracket the post of a text message, so
// its echo is recognised even when it beats Discord's answer.
func chatPostingBegin(id string) {
	chatUploads.mu.Lock()
	chatUploads.posting = id
	chatUploads.mu.Unlock()
}

func chatPostingEnd() {
	chatUploads.mu.Lock()
	chatUploads.last, chatUploads.lastAt = chatUploads.posting, time.Now()
	chatUploads.posting = ""
	chatUploads.mu.Unlock()
}

// chatTriedRecently reports whether message id was the last one to go out,
// within window. After a dropped connection the message is still queued, yet
// Discord may have it: its echo (replayed when the gateway reconnects) must
// find it, or the retry would post it twice.
func chatTriedRecently(id string, window time.Duration) bool {
	if id == "" {
		return false
	}
	chatUploads.mu.Lock()
	defer chatUploads.mu.Unlock()
	return chatUploads.last == id && time.Since(chatUploads.lastAt) < window
}

// chatEchoWindow is how far apart an own message and its echo may be when the
// message is no longer going out (Discord's answer was lost, and the echo is
// what gives the message its id). A message with files took its upload's
// time too.
func chatEchoWindow(msg ChatMessage) time.Duration {
	if len(msg.Files) > 0 {
		return 30 * time.Minute
	}
	return 5 * time.Minute
}

// chatEchoCandidate reports whether own message have may be the one an echo
// from the shared bot brings back. One going out right now is, however long
// its upload took. One that already holds a Discord id is not: its echo
// carries that id. A queued one is only if it was the last to go out (a
// dropped connection left it queued, yet Discord may have it) — the echo of an
// earlier message with the same words must not mark a waiting copy sent —
// except in a page of history (history set), fetched after a restart, which
// shows what already went out: the copy still queued here must not go out a
// second time. A message whose answer from Discord was lost is, within
// chatEchoWindow.
func chatEchoCandidate(have ChatMessage, at time.Time, history bool) bool {
	if !have.Own || isDiscordSnowflake(have.ID) {
		return false
	}
	if chatInFlight(have.ID) {
		return true
	}
	window := chatEchoWindow(have)
	if have.Pending && !history && !chatTriedRecently(have.ID, window) {
		return false
	}
	gap := at.Sub(have.Timestamp)
	if have.Pending && history {
		// The copy in the channel cannot be older than the message (two
		// minutes allow for a device clock that runs fast).
		return gap > -2*time.Minute && gap < window
	}
	return gap > -window && gap < window
}

// chatAdoptEcho gives this device's own message what its echo carries:
// Discord's id and its files' links. It is delivered.
func chatAdoptEcho(m *ChatMessage, echo ChatMessage) {
	if echo.ID != "" {
		m.ID = echo.ID
	}
	m.Pending = false
	m.Error = ""
	chatAdoptFileLinks(m, echo)
}

// chatUnsend remembers this device's messages deleted while they were going
// out. If Discord got one after all — its answer or its echo arrives later —
// it is taken back off the channel instead of coming back as somebody else's
// message. dropped holds the Discord ids already taken back, so an echo that
// comes after Discord's answer is not shown either.
var chatUnsend struct {
	mu      sync.Mutex
	list    []chatUnsendEntry
	dropped map[string]time.Time
}

type chatUnsendEntry struct {
	msg   ChatMessage
	at    time.Time // when it was deleted
	until time.Time
}

// chatUnsendKeep is how long a deleted message (or a taken-back id) is
// remembered.
const chatUnsendKeep = 30 * time.Minute

// chatRememberUnsend notes a message deleted while it was going out.
func chatRememberUnsend(msg ChatMessage) {
	chatUnsend.mu.Lock()
	defer chatUnsend.mu.Unlock()
	now := time.Now()
	kept := make([]chatUnsendEntry, 0, len(chatUnsend.list)+1)
	for _, e := range chatUnsend.list {
		if now.Before(e.until) {
			kept = append(kept, e)
		}
	}
	kept = append(kept, chatUnsendEntry{msg: msg, at: now, until: now.Add(chatUnsendKeep)})
	if len(kept) > 32 {
		kept = kept[len(kept)-32:]
	}
	chatUnsend.list = kept
}

// chatTakeUnsend reports whether the answer to the message with localID, or a
// message from the channel (echo), is one deleted here while it went out,
// and forgets it. An echo must say the same and have been made while the
// message was going out — written before it was deleted — so somebody else's
// message with the same words, later on, is never taken for it.
func chatTakeUnsend(localID string, echo *ChatMessage) bool {
	chatUnsend.mu.Lock()
	defer chatUnsend.mu.Unlock()
	now := time.Now()
	for i, e := range chatUnsend.list {
		if !now.Before(e.until) {
			continue
		}
		match := localID != "" && e.msg.ID == localID
		if !match && echo != nil && chatSameContent(e.msg, *echo) {
			made := echo.Timestamp
			match = !made.Before(e.msg.Timestamp.Add(-2*time.Minute)) && !made.After(e.at.Add(2*time.Minute))
		}
		if match {
			next := make([]chatUnsendEntry, 0, len(chatUnsend.list)-1)
			next = append(next, chatUnsend.list[:i]...)
			chatUnsend.list = append(next, chatUnsend.list[i+1:]...)
			return true
		}
	}
	return false
}

// chatForgetUnsend drops the entry of a message that cannot have reached
// Discord (its upload stopped before the last byte).
func chatForgetUnsend(localID string) {
	chatTakeUnsend(localID, nil)
}

// chatNoteDropped remembers a Discord id that was taken back.
func chatNoteDropped(id string) {
	if id == "" {
		return
	}
	chatUnsend.mu.Lock()
	defer chatUnsend.mu.Unlock()
	now := time.Now()
	if chatUnsend.dropped == nil {
		chatUnsend.dropped = make(map[string]time.Time, 4)
	}
	for k, at := range chatUnsend.dropped {
		if now.Sub(at) > chatUnsendKeep {
			delete(chatUnsend.dropped, k)
		}
	}
	chatUnsend.dropped[id] = now
}

// chatWasDropped reports whether a message from the channel is one taken back.
func chatWasDropped(id string) bool {
	if id == "" {
		return false
	}
	chatUnsend.mu.Lock()
	defer chatUnsend.mu.Unlock()
	at, ok := chatUnsend.dropped[id]
	return ok && time.Since(at) <= chatUnsendKeep
}

// chatDeleteRemote takes a message the shared bot posted for this device back
// off the channel.
func chatDeleteRemote(id string) {
	if !isDiscordSnowflake(id) {
		return
	}
	chatNoteDropped(id)
	dg, ch := chatLiveNow()
	if dg == nil || ch == "" {
		return
	}
	go func() {
		if err := dg.ChannelMessageDelete(ch, id); err != nil {
			logWarn("chat", "taking back a deleted message failed: %v", err)
		}
	}()
}

// chatDropRemote is chatDeleteRemote plus the local copy, if an echo already
// brought one in. chatMutex must not be held.
func chatDropRemote(id string) {
	chatMutex.Lock()
	removed := chatRemoveLocked(chatTabCommunity, id)
	chatMutex.Unlock()
	if removed {
		chatDirty.Store(true)
	}
	chatDeleteRemote(id)
}

func chatUploadBegin(id string, cancel context.CancelFunc) {
	chatUploads.mu.Lock()
	chatUploads.id, chatUploads.sent, chatUploads.total = id, 0, 0
	chatUploads.cancel, chatUploads.cancelled = cancel, false
	chatUploads.mu.Unlock()
}

func chatUploadSetProgress(sent, total int64) {
	chatUploads.mu.Lock()
	chatUploads.sent, chatUploads.total = sent, total
	chatUploads.mu.Unlock()
}

// chatUploadEnd clears the upload in flight and reports whether it was
// cancelled.
func chatUploadEnd() bool {
	chatUploads.mu.Lock()
	defer chatUploads.mu.Unlock()
	cancelled := chatUploads.cancelled
	if chatUploads.id != "" {
		chatUploads.last, chatUploads.lastAt = chatUploads.id, time.Now()
	}
	chatUploads.id, chatUploads.cancel, chatUploads.cancelled = "", nil, false
	chatUploads.sent, chatUploads.total = 0, 0
	return cancelled
}

// chatUploadProgress reports the upload of message id: bytes sent so far and
// the request's length.
func chatUploadProgress(id string) (int64, int64, bool) {
	chatUploads.mu.Lock()
	defer chatUploads.mu.Unlock()
	if id == "" || chatUploads.id != id {
		return 0, 0, false
	}
	return chatUploads.sent, chatUploads.total, true
}

// chatCancelUpload stops message id from going out: the upload in flight is
// cancelled, a queued one is marked as not sent (Retry sends it after all).
func chatCancelUpload(id string) bool {
	chatUploads.mu.Lock()
	if id != "" && chatUploads.id == id && chatUploads.cancel != nil {
		chatUploads.cancelled = true
		cancel := chatUploads.cancel
		chatUploads.mu.Unlock()
		cancel()
		return true
	}
	chatUploads.mu.Unlock()
	stopped := false
	chatUpdateMessage(chatTabCommunity, id, func(m *ChatMessage) {
		if m.Pending {
			m.Pending = false
			m.Error = "Upload cancelled"
			stopped = true
		}
	})
	if stopped {
		// The wait a failed try left belonged to this message; the next one
		// in the queue goes at once.
		chatUploadRetryReset()
	}
	return stopped
}

// chatUploadDue reports whether the back-off after a failed upload is over.
func chatUploadDue(now time.Time) bool {
	chatUploads.mu.Lock()
	defer chatUploads.mu.Unlock()
	return !now.Before(chatUploads.retryAt)
}

// chatUploadRetryLater backs the next upload off. wait is Discord's own figure
// when it gave one; otherwise the wait doubles from 15 seconds to 2 minutes.
func chatUploadRetryLater(now time.Time, wait time.Duration, note string) {
	chatUploads.mu.Lock()
	defer chatUploads.mu.Unlock()
	if wait <= 0 {
		switch {
		case chatUploads.backoff < 15*time.Second:
			chatUploads.backoff = 15 * time.Second
		case chatUploads.backoff < 2*time.Minute:
			chatUploads.backoff *= 2
		}
		if chatUploads.backoff > 2*time.Minute {
			chatUploads.backoff = 2 * time.Minute
		}
		wait = chatUploads.backoff
	}
	chatUploads.retryAt = now.Add(wait)
	chatUploads.note = note
}

// chatUploadRetryReset forgets the back-off (a delivered upload, or the
// banner's Retry).
func chatUploadRetryReset() {
	chatUploads.mu.Lock()
	chatUploads.retryAt, chatUploads.backoff, chatUploads.note = time.Time{}, 0, ""
	chatUploads.mu.Unlock()
}

// chatUploadNote says why the next upload is waiting, "" when it is not.
func chatUploadNote() string {
	chatUploads.mu.Lock()
	defer chatUploads.mu.Unlock()
	if time.Now().Before(chatUploads.retryAt) {
		return chatUploads.note
	}
	return ""
}

// chatStartUpload hands a queued message with files to its own goroutine. It
// runs on the sender loop, which owns the session.
func chatStartUpload(msg ChatMessage) bool {
	target, ok := chatUploadTargetNow()
	if !ok {
		return false
	}
	chatUploadBusy.Store(true)
	go chatRunUpload(target, msg)
	return true
}

// chatRunUpload sends one message with its files and files Discord's answer.
func chatRunUpload(target chatUploadTarget, msg ChatMessage) {
	defer chatWakeSync()
	defer chatUploadBusy.Store(false)
	defer func() {
		if r := recover(); r != nil {
			chatUploadEnd()
			// Backed off like a failed upload, so a message that trips the
			// same fault is not tried again on every pass of the loop.
			chatUploadRetryLater(time.Now(), 0, "Upload interrupted — trying again")
			logDebug("chat", "upload recovered from: %v", r)
		}
	}()
	stillQueued := func() bool {
		cur, ok := chatFindMessage(chatTabCommunity, msg.ID)
		return ok && cur.Pending && cur.Error == ""
	}
	if !stillQueued() {
		return // deleted or cancelled while it waited its turn
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	chatUploadBegin(msg.ID, cancel)
	// A Cancel or a Delete that landed between the look above and the line
	// before found no upload in flight and marked the message itself; from
	// here on it cancels the upload instead.
	if !stillQueued() {
		chatUploadEnd()
		return
	}
	sent, err := chatDiscordUploadFn(ctx, target, msg, chatUploadSetProgress)
	sentBytes, totalBytes, _ := chatUploadProgress(msg.ID)
	cancelled := chatUploadEnd()
	switch {
	case err == nil:
		chatUploadRetryReset()
		chatFileSent(msg.ID, sent)
	case cancelled:
		chatUploadRetryReset()
		if totalBytes == 0 || sentBytes < totalBytes {
			// Stopped before the last byte: Discord cannot have it, so a
			// delete has nothing to take back.
			chatForgetUnsend(msg.ID)
		}
		chatUpdateMessage(chatTabCommunity, msg.ID, func(m *ChatMessage) {
			m.Pending = false
			m.Error = "Upload cancelled"
		})
	default:
		var upErr *chatUploadError
		if errors.As(err, &upErr) && upErr.refusal != "" {
			// The next message in the queue starts at once: the back-off is
			// for trouble with the connection, not for this file.
			chatUploadRetryReset()
			logWarn("chat", "discord refused an upload: %v", err)
			chatUpdateMessage(chatTabCommunity, msg.ID, func(m *ChatMessage) {
				m.Pending = false
				m.Error = upErr.refusal
			})
			return
		}
		wait := time.Duration(0)
		note := "Upload interrupted — trying again"
		if upErr != nil && upErr.retryAfter > 0 {
			wait = upErr.retryAfter
			note = "Discord is busy — sending again in a moment"
		}
		logWarn("chat", "upload will be retried: %v", chatPlainError(err))
		chatUploadRetryLater(time.Now(), wait, note)
	}
}

// chatPlainError drops the request URL from a transport error before it is
// logged: a file link carries a signature in its query string.
func chatPlainError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Err != nil {
		return uerr.Err
	}
	return err
}

// chatFileSent records a delivered upload: the message takes Discord's id and
// its files their links. The gateway's echo can land first and give the
// message its id, so the Discord id is tried as well. A message deleted here
// while it went out is taken back off the channel.
func chatFileSent(localID string, sent *discordgo.Message) {
	update := func(m *ChatMessage) {
		m.Pending = false
		m.Error = ""
		if sent == nil {
			return
		}
		if sent.ID != "" {
			m.ID = sent.ID
		}
		chatAdoptAttachments(m, sent)
	}
	if chatUpdateMessage(chatTabCommunity, localID, update) {
		return
	}
	if sent != nil && sent.ID != "" && chatUpdateMessage(chatTabCommunity, sent.ID, update) {
		return
	}
	if sent != nil && chatTakeUnsend(localID, nil) {
		chatDropRemote(sent.ID)
	}
}

// chatAdoptAttachments copies Discord's links onto the files of a message this
// device sent; Discord keeps them in upload order.
func chatAdoptAttachments(m *ChatMessage, sent *discordgo.Message) {
	if m == nil || sent == nil || len(m.Files) == 0 {
		return
	}
	files := append([]ChatFile(nil), m.Files...)
	for i, att := range sent.Attachments {
		if i >= len(files) {
			break
		}
		if att == nil {
			continue
		}
		if att.URL != "" {
			files[i].URL = att.URL
		}
		if att.Size > 0 {
			files[i].Size = int64(att.Size) // what went up, if the file changed since
		}
		files[i].Msg = sent.ID
	}
	m.Files = files
}

// ── Coming back ──────────────────────────────────────────────────────────

// chatFilesFromDiscord lists a Discord message's attachments as files.
func chatFilesFromDiscord(m *discordgo.Message) []ChatFile {
	if m == nil || len(m.Attachments) == 0 {
		return nil
	}
	out := make([]ChatFile, 0, len(m.Attachments))
	for _, att := range m.Attachments {
		if att == nil || strings.TrimSpace(att.URL) == "" {
			continue
		}
		name := strings.TrimSpace(att.Filename)
		if name == "" {
			name = "file"
		}
		out = append(out, ChatFile{
			Name: name,
			Size: int64(att.Size),
			Type: att.ContentType,
			URL:  att.URL,
			Msg:  m.ID,
		})
		if len(out) == chatFilesMax {
			break
		}
	}
	return out
}

// chatFileKey is a file name as a match key. Discord rewrites names on the
// way in (spaces and some punctuation), so only letters, digits and dots
// count.
func chatFileKey(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// chatSameFiles reports whether two lists are the same files in the same
// order: the same size where both sizes are known, and the same name where
// Discord kept it recognisable (a size alone stands in for a renamed one).
func chatSameFiles(a, b []ChatFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Size > 0 && b[i].Size > 0 && a[i].Size != b[i].Size {
			return false
		}
		if chatFileKey(a[i].Name) == chatFileKey(b[i].Name) {
			continue
		}
		if a[i].Size > 0 && a[i].Size == b[i].Size {
			continue
		}
		return false
	}
	return true
}

// chatSameContent reports whether a message from the channel says what a
// message of this device says: the same sender, text and files. That is how
// this device's own message is recognised when the shared bot echoes it; the
// sender keeps another device's message with the same words (every device
// posts through the same bot) from being taken for it.
func chatSameContent(have, msg ChatMessage) bool {
	return strings.EqualFold(strings.TrimSpace(have.Sender), strings.TrimSpace(msg.Sender)) &&
		have.Text == msg.Text && chatSameFiles(have.Files, msg.Files)
}

// chatAdoptFileLinks takes the links of an echo's files into this device's
// own copy of the message.
func chatAdoptFileLinks(m *ChatMessage, echo ChatMessage) {
	if m == nil || len(m.Files) == 0 || len(m.Files) != len(echo.Files) {
		return
	}
	files := append([]ChatFile(nil), m.Files...)
	for i := range files {
		if echo.Files[i].URL != "" {
			files[i].URL = echo.Files[i].URL
		}
		if echo.Files[i].Msg != "" {
			files[i].Msg = echo.Files[i].Msg
		}
	}
	m.Files = files
}

// chatRefreshLinks takes the current file links of a message the history
// already holds from a freshly fetched copy. Discord's file links expire
// after about a day, so a saved history's links stop working while a fresh
// page's work. A message saved before files existed (its picture in
// ImageURL, its other attachments dropped) takes the fresh files whole.
func chatRefreshLinks(old, fresh ChatMessage) (ChatMessage, bool) {
	if len(fresh.Files) == 0 {
		return old, false
	}
	if len(old.Files) == 0 {
		old.Files = append([]ChatFile(nil), fresh.Files...)
		old.ImageURL = ""
		return old, true
	}
	if len(old.Files) != len(fresh.Files) {
		return old, false
	}
	files := append([]ChatFile(nil), old.Files...)
	changed := false
	for i := range files {
		if u := fresh.Files[i].URL; u != "" && files[i].URL != u {
			files[i].URL = u
			changed = true
		}
		if id := fresh.Files[i].Msg; id != "" && files[i].Msg != id {
			files[i].Msg = id
			changed = true
		}
	}
	if changed {
		old.Files = files
	}
	return old, changed
}

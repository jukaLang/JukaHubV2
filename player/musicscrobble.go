package main

// musicscrobble.go — what you listen to, sent where you keep it: Last.fm
// (signed in by scanning a code), ListenBrainz (a user token), and the
// Navidrome and Jellyfin servers the songs came from (their play counts,
// "now playing" and, for Jellyfin videos, where a film was left).
//
// A watcher reads the player twice a second. A song counts as listened to
// (Last.fm's rule, which ListenBrainz shares) once it has played for half its
// length or four minutes, whichever comes first, and only if it is longer than
// 30 seconds; paused and skipped-over time does not count. Each listen goes
// into scrobbles.json first and is sent from there, so a listen made on a
// plane is sent when the handheld is next online (the offline scrobble cache).
//
// Last.fm needs an API account: a release built with JUKAHUB_LASTFM_API_KEY
// and JUKAHUB_LASTFM_SECRET has one; otherwise the player makes a free one at
// last.fm/api/account/create and pastes it on the phone page.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// lastfmAPIKey and lastfmAPISecret are set at build time
// (-X main.lastfmAPIKey=… -X main.lastfmAPISecret=…); empty in a build
// without them.
var (
	lastfmAPIKey    string
	lastfmAPISecret string
)

const (
	lastfmAPIURL       = "https://ws.audioscrobbler.com/2.0/"
	lastfmAuthURL      = "https://www.last.fm/api/auth/"
	listenBrainzAPI    = "https://api.listenbrainz.org/1/"
	scrobbleMinLength  = 30.0
	scrobbleMaxWait    = 240.0
	scrobbleQueueLimit = 5000
	phoneCardLastFM    = phoneCardKind("lastfm")
)

// lastfmCredentials is the API account in use and where it came from.
func lastfmCredentials() (key, secret, source string) {
	if k, s := strings.TrimSpace(os.Getenv("JUKAHUB_LASTFM_API_KEY")), strings.TrimSpace(os.Getenv("JUKAHUB_LASTFM_SECRET")); k != "" && s != "" {
		return k, s, "environment"
	}
	conf := musicServersGet().LastFM
	if conf.APIKey != "" {
		if s := musicDecryptField(conf.Secret); s != "" {
			return conf.APIKey, s, "yours"
		}
	}
	if lastfmAPIKey != "" && lastfmAPISecret != "" {
		return lastfmAPIKey, lastfmAPISecret, "build"
	}
	return "", "", ""
}

// lastfmSign is Last.fm's api_sig: md5 of the sorted name+value pairs and the
// secret (format and callback left out). Pure.
func lastfmSign(params url.Values, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "format" || k == "callback" || k == "api_sig" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(params.Get(k))
	}
	b.WriteString(secret)
	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

type lastfmError struct {
	Code    int    `json:"error"`
	Message string `json:"message"`
}

func (e *lastfmError) Error() string { return fmt.Sprintf("Last.fm error %d: %s", e.Code, e.Message) }

var scrobbleHTTP = sync.OnceValue(func() *http.Client { return musicHTTPClient(30 * time.Second) })

// lastfmCall runs a signed method (POST when post) and decodes the answer.
func lastfmCall(ctx context.Context, params url.Values, post bool, out interface{}) error {
	key, secret, _ := lastfmCredentials()
	if key == "" {
		return errors.New("no Last.fm API account in this build — add yours on the phone page")
	}
	params.Set("api_key", key)
	params.Set("api_sig", lastfmSign(params, secret))
	params.Set("format", "json")
	var req *http.Request
	var err error
	if post {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, lastfmAPIURL, strings.NewReader(params.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, lastfmAPIURL+"?"+params.Encode(), nil)
	}
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "JukaHub/"+strings.TrimPrefix(versionString(nil), "v"))
	resp, err := scrobbleHTTP().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var le lastfmError
	if json.Unmarshal(data, &le) == nil && le.Code != 0 {
		return &le
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Last.fm answered %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ── Last.fm sign-in by QR code ──────────────────────────────────────────────

var lastfmSignIn struct {
	mu      sync.Mutex
	waiting bool
	url     string
	gen     int
}

// lastfmStartSignIn asks Last.fm for a token and returns the page that
// approves it; a watcher waits for the approval (up to ten minutes).
func lastfmStartSignIn(ctx context.Context) (string, error) {
	var tok struct {
		Token string `json:"token"`
	}
	if err := lastfmCall(ctx, url.Values{"method": {"auth.getToken"}}, false, &tok); err != nil {
		return "", err
	}
	if tok.Token == "" {
		return "", errors.New("Last.fm sent no sign-in token")
	}
	key, _, _ := lastfmCredentials()
	link := lastfmAuthURL + "?" + url.Values{"api_key": {key}, "token": {tok.Token}}.Encode()
	lastfmSignIn.mu.Lock()
	lastfmSignIn.gen++
	gen := lastfmSignIn.gen
	lastfmSignIn.waiting, lastfmSignIn.url = true, link
	lastfmSignIn.mu.Unlock()
	go lastfmWaitSession(tok.Token, gen)
	return link, nil
}

func lastfmWaitSession(token string, gen int) {
	defer func() {
		lastfmSignIn.mu.Lock()
		if lastfmSignIn.gen == gen {
			lastfmSignIn.waiting, lastfmSignIn.url = false, ""
		}
		lastfmSignIn.mu.Unlock()
	}()
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(4 * time.Second)
		lastfmSignIn.mu.Lock()
		stale := lastfmSignIn.gen != gen
		lastfmSignIn.mu.Unlock()
		if stale {
			return
		}
		var r struct {
			Session struct {
				Name string `json:"name"`
				Key  string `json:"key"`
			} `json:"session"`
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := lastfmCall(ctx, url.Values{"method": {"auth.getSession"}, "token": {token}}, false, &r)
		cancel()
		var le *lastfmError
		if errors.As(err, &le) && le.Code == 14 {
			continue // not approved yet
		}
		if err != nil {
			if errors.As(err, &le) {
				tbxPost(func() { showToast("Last.fm: "+le.Message, ToastWarn()) })
				return
			}
			continue // the network: try again
		}
		enc, eerr := EncryptAPIToken(r.Session.Key, "Last.fm session")
		if eerr != nil || r.Session.Key == "" {
			return
		}
		name := r.Session.Name
		_ = musicServersUpdate(func(s *musicServersFile) {
			s.LastFM.User, s.LastFM.Session = name, enc
		})
		tbxPost(func() {
			phoneCardArrived(phoneCardLastFM)
			showToast("Last.fm: signed in as "+name, ToastSuccess())
		})
		scrobbleKick()
		return
	}
}

// lastfmSignInOnScreen starts the sign-in and shows its code full screen.
// Main thread.
func lastfmSignInOnScreen() {
	if !IsOnline() {
		showToast("Connect to Wi-Fi to sign in to Last.fm", ToastWarn())
		return
	}
	if key, _, _ := lastfmCredentials(); key == "" {
		showToast("This build has no Last.fm API account — add yours from your phone", ToastInfo())
		musicServersPhoneOpen("lastfm")
		return
	}
	showToast("Asking Last.fm…", ToastInfo())
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		link, err := lastfmStartSignIn(ctx)
		tbxPost(func() {
			if err != nil {
				showToast("Last.fm: "+firstLine(err.Error()), ToastWarn())
				return
			}
			title := "Last.fm · scan to sign in"
			if qrZoomOpenCardDetail(tbxQRModules(link), title, "Allow access on your phone; the handheld notices by itself") {
				phoneCard.kind, phoneCard.title = phoneCardLastFM, title
			}
		})
	}()
}

// ── ListenBrainz ────────────────────────────────────────────────────────────

// listenBrainzValidate checks a user token and answers the user name.
func listenBrainzValidate(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listenBrainzAPI+"validate-token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Token "+token)
	resp, err := scrobbleHTTP().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Valid    bool   `json:"valid"`
		UserName string `json:"user_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r); err != nil {
		return "", errors.New("ListenBrainz sent something unexpected")
	}
	if !r.Valid {
		return "", errors.New("ListenBrainz does not know that token — copy it again from listenbrainz.org/settings")
	}
	return r.UserName, nil
}

type lbTrack struct {
	ArtistName  string                 `json:"artist_name"`
	TrackName   string                 `json:"track_name"`
	ReleaseName string                 `json:"release_name,omitempty"`
	Info        map[string]interface{} `json:"additional_info,omitempty"`
}

type lbListen struct {
	ListenedAt int64   `json:"listened_at,omitempty"`
	Meta       lbTrack `json:"track_metadata"`
}

// listenBrainzSubmit sends listens (kind "single", "import" or "playing_now").
func listenBrainzSubmit(ctx context.Context, token, kind string, listens []lbListen) error {
	body, err := json.Marshal(map[string]interface{}{"listen_type": kind, "payload": listens})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, listenBrainzAPI+"submit-listens", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := scrobbleHTTP().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return errScrobbleRefused
	case resp.StatusCode == http.StatusBadRequest:
		return errScrobbleBad
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("ListenBrainz answered %d", resp.StatusCode)
	}
	return nil
}

var (
	errScrobbleRefused = errors.New("the service refused the account")
	errScrobbleBad     = errors.New("the service refused that listen")
)

// ── The queue (offline scrobble cache) ──────────────────────────────────────

// scrobbleEntry is one listen waiting to be sent, with the services it still
// has to reach.
type scrobbleEntry struct {
	Artist   string   `json:"artist"`
	Track    string   `json:"track"`
	Album    string   `json:"album,omitempty"`
	Duration int      `json:"duration,omitempty"`
	At       int64    `json:"at"`
	Targets  []string `json:"targets"` // "lastfm", "listenbrainz", "navidrome:<id>", "jellyfin:<id>"
}

var scrobbleQ struct {
	mu      sync.Mutex
	loaded  bool
	items   []scrobbleEntry
	sending bool
	kick    chan struct{}
}

func scrobblePath() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "scrobbles.json")
}

func scrobbleLoadLocked() {
	if scrobbleQ.loaded {
		return
	}
	scrobbleQ.loaded = true
	if p := scrobblePath(); p != "" {
		var items []scrobbleEntry
		if _, err := readJSONWithBackup(p, &items); err == nil {
			scrobbleQ.items = items
		}
	}
}

func scrobbleSaveLocked() {
	p := scrobblePath()
	if p == "" {
		return
	}
	data, err := json.Marshal(scrobbleQ.items)
	if err == nil {
		_ = AtomicWrite(p, data, 0o600)
	}
}

// scrobblePending is how many listens wait to be sent.
func scrobblePending() int {
	scrobbleQ.mu.Lock()
	defer scrobbleQ.mu.Unlock()
	scrobbleLoadLocked()
	return len(scrobbleQ.items)
}

// scrobbleAdd queues a listen for every service it should reach.
func scrobbleAdd(e scrobbleEntry) {
	if len(e.Targets) == 0 {
		return
	}
	scrobbleQ.mu.Lock()
	scrobbleLoadLocked()
	scrobbleQ.items = append(scrobbleQ.items, e)
	if len(scrobbleQ.items) > scrobbleQueueLimit {
		scrobbleQ.items = scrobbleQ.items[len(scrobbleQ.items)-scrobbleQueueLimit:]
	}
	scrobbleSaveLocked()
	scrobbleQ.mu.Unlock()
	scrobbleKick()
}

// scrobbleKick wakes the sender.
func scrobbleKick() {
	scrobbleQ.mu.Lock()
	if scrobbleQ.kick == nil {
		scrobbleQ.kick = make(chan struct{}, 1)
		go scrobbleSender(scrobbleQ.kick)
	}
	k := scrobbleQ.kick
	scrobbleQ.mu.Unlock()
	select {
	case k <- struct{}{}:
	default:
	}
}

// scrobbleSender sends the queue when kicked and every five minutes, while
// the handheld is online.
func scrobbleSender(kick chan struct{}) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-kick:
		case <-t.C:
		}
		if IsOnline() {
			scrobbleFlush()
		}
	}
}

// scrobbleFlush sends what waits, service by service, and keeps what failed
// for next time.
func scrobbleFlush() {
	scrobbleQ.mu.Lock()
	scrobbleLoadLocked()
	if scrobbleQ.sending || len(scrobbleQ.items) == 0 {
		scrobbleQ.mu.Unlock()
		return
	}
	scrobbleQ.sending = true
	items := append([]scrobbleEntry(nil), scrobbleQ.items...)
	scrobbleQ.mu.Unlock()
	defer func() {
		scrobbleQ.mu.Lock()
		scrobbleQ.sending = false
		scrobbleQ.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sent := map[int]map[string]bool{} // entry -> targets done (sent or dropped)
	mark := func(i int, target string) {
		if sent[i] == nil {
			sent[i] = map[string]bool{}
		}
		sent[i][target] = true
	}
	servers := musicServersGet()

	// Last.fm: 50 a call.
	var lfm []int
	for i, e := range items {
		if scrobbleHas(e, "lastfm") {
			lfm = append(lfm, i)
		}
	}
	if len(lfm) > 0 && servers.LastFM.Session == "" {
		for _, i := range lfm {
			mark(i, "lastfm") // signed out: these cannot be sent any more
		}
		lfm = nil
	}
	sk := musicDecryptField(servers.LastFM.Session)
	for len(lfm) > 0 && sk != "" {
		n := len(lfm)
		if n > 50 {
			n = 50
		}
		batch := lfm[:n]
		lfm = lfm[n:]
		p := url.Values{"method": {"track.scrobble"}, "sk": {sk}}
		for j, i := range batch {
			e := items[i]
			idx := "[" + strconv.Itoa(j) + "]"
			p.Set("artist"+idx, e.Artist)
			p.Set("track"+idx, e.Track)
			p.Set("timestamp"+idx, strconv.FormatInt(e.At, 10))
			if e.Album != "" {
				p.Set("album"+idx, e.Album)
			}
			if e.Duration > 0 {
				p.Set("duration"+idx, strconv.Itoa(e.Duration))
			}
		}
		err := lastfmCall(ctx, p, true, nil)
		var le *lastfmError
		switch {
		case err == nil:
			for _, i := range batch {
				mark(i, "lastfm")
			}
		case errors.As(err, &le) && (le.Code == 9 || le.Code == 4):
			// The session was revoked: sign out so the queue stops growing
			// for Last.fm, and say so.
			_ = musicServersUpdate(func(s *musicServersFile) { s.LastFM.Session = "" })
			tbxPost(func() { showToast("Last.fm signed this handheld out — sign in again", ToastWarn()) })
			lfm = nil
		case errors.As(err, &le):
			for _, i := range batch {
				mark(i, "lastfm") // a listen Last.fm will never take
			}
		default:
			lfm = nil // the network: next time
		}
	}

	// ListenBrainz: one import call.
	lbToken := musicDecryptField(servers.ListenBrainz.Token)
	var lbIdx []int
	var listens []lbListen
	for i, e := range items {
		if !scrobbleHas(e, "listenbrainz") {
			continue
		}
		if lbToken == "" {
			mark(i, "listenbrainz")
			continue
		}
		lbIdx = append(lbIdx, i)
		info := map[string]interface{}{"media_player": "JukaHub", "submission_client": "JukaHub"}
		if e.Duration > 0 {
			info["duration_ms"] = e.Duration * 1000
		}
		listens = append(listens, lbListen{ListenedAt: e.At, Meta: lbTrack{ArtistName: e.Artist, TrackName: e.Track, ReleaseName: e.Album, Info: info}})
		if len(listens) >= 500 {
			break
		}
	}
	if len(listens) > 0 {
		kind := "import"
		if len(listens) == 1 {
			kind = "single"
		}
		err := listenBrainzSubmit(ctx, lbToken, kind, listens)
		switch {
		case err == nil, errors.Is(err, errScrobbleBad):
			for _, i := range lbIdx {
				mark(i, "listenbrainz")
			}
		case errors.Is(err, errScrobbleRefused):
			_ = musicServersUpdate(func(s *musicServersFile) { s.ListenBrainz.Token = "" })
			tbxPost(func() { showToast("ListenBrainz refused the token — send it again from your phone", ToastWarn()) })
		}
	}

	// The servers' own play counts.
	for i, e := range items {
		for _, t := range e.Targets {
			switch {
			case strings.HasPrefix(t, "navidrome:"):
				if !musicSrcReady(musicSrcNavidrome) {
					mark(i, t)
					continue
				}
				err := ndScrobble(ctx, strings.TrimPrefix(t, "navidrome:"), true, time.Unix(e.At, 0))
				network := err != nil && (errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "did not answer"))
				if !network {
					mark(i, t) // sent, or refused for good
				}
			case strings.HasPrefix(t, "jellyfin:"):
				// Jellyfin counts a play from the playback reports already.
				mark(i, t)
			}
		}
	}

	scrobbleQ.mu.Lock()
	defer scrobbleQ.mu.Unlock()
	// Entries may have been added meanwhile: match by position (the queue
	// only grows at the end while sending runs, or is cut from the front).
	keep := scrobbleQ.items[:0]
	for i, e := range scrobbleQ.items {
		if i < len(items) && sent[i] != nil {
			var rest []string
			for _, t := range e.Targets {
				if !sent[i][t] {
					rest = append(rest, t)
				}
			}
			if len(rest) == 0 {
				continue
			}
			e.Targets = rest
		}
		keep = append(keep, e)
	}
	scrobbleQ.items = keep
	scrobbleSaveLocked()
}

func scrobbleHas(e scrobbleEntry, target string) bool {
	for _, t := range e.Targets {
		if t == target {
			return true
		}
	}
	return false
}

// ── The watcher ─────────────────────────────────────────────────────────────

// scrobbleWatch follows the player: the listen in progress and the reports
// for the servers.
var scrobbleWatch struct {
	once      sync.Once
	path      string
	startedAt time.Time
	listened  float64
	lastPos   float64
	lastTick  time.Time
	duration  float64
	done      bool
	jfID      string
	jfSession string
	jfLastRep time.Time
	paused    bool
	// The Jellyfin video being watched, from the video player's address.
	vidID     string
	vidSrc    string
	vidPos    float64
	vidRep    time.Time
	vidSessID string
}

// scrobbleStartWatcher starts the watcher once (the first time a song plays).
func scrobbleStartWatcher() {
	scrobbleWatch.once.Do(func() {
		go func() {
			t := time.NewTicker(500 * time.Millisecond)
			defer t.Stop()
			for range t.C {
				scrobbleTick()
			}
		}()
		scrobbleKick()
	})
}

// scrobbleTargets is where a listen of a track goes.
func scrobbleTargets(meta musicMeta) []string {
	s := musicServersGet()
	var out []string
	if s.LastFM.Session != "" {
		out = append(out, "lastfm")
	}
	if s.ListenBrainz.Token != "" {
		out = append(out, "listenbrainz")
	}
	if meta.Src == musicSrcNavidrome && meta.ID != "" && s.Navidrome.Scrobble {
		out = append(out, "navidrome:"+meta.ID)
	}
	return out
}

func scrobbleTick() {
	music.mu.Lock()
	cur, playing, paused := music.current, music.playing, music.paused
	path, pos, dur := "", music.position, music.duration
	if cur >= 0 && cur < len(music.tracks) {
		path = music.tracks[cur].Path
	}
	music.mu.Unlock()
	now := time.Now()
	w := &scrobbleWatch

	if path != w.path {
		scrobbleTrackEnded()
		w.path, w.startedAt, w.listened, w.lastPos, w.lastTick, w.done = path, now, 0, pos, now, false
		w.jfID, w.jfSession = "", ""
		if path != "" && playing {
			scrobbleNowPlaying(path, dur)
		}
	}
	if path == "" || !playing {
		if w.jfID != "" {
			scrobbleTrackEnded()
		}
		w.lastTick = now
		return
	}
	if dur > 0 {
		w.duration = dur
	}
	if !paused {
		// Count time that really played: a jump (a seek) counts as nothing.
		step := pos - w.lastPos
		wall := now.Sub(w.lastTick).Seconds()
		if step > 0 && step < wall*4+1 {
			w.listened += step
		}
	}
	w.lastPos, w.lastTick = pos, now
	if w.jfID != "" && (paused != w.paused || now.Sub(w.jfLastRep) > 30*time.Second) {
		id, sess := w.jfID, w.jfSession
		w.jfLastRep, w.paused = now, paused
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = jfReport(ctx, "progress", id, pos, paused, sess)
		}()
	}
	if !w.done && w.duration > scrobbleMinLength && w.listened >= minFloat(w.duration/2, scrobbleMaxWait) {
		w.done = true
		meta, ok := musicMetaFor(path)
		if ok && meta.Artist != "" && meta.Title != "" {
			scrobbleAdd(scrobbleEntry{Artist: meta.Artist, Track: meta.Title, Album: meta.Album,
				Duration: int(w.duration), At: w.startedAt.Unix(), Targets: scrobbleTargets(meta)})
		}
	}
	scrobbleVideoTick(now)
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// scrobbleNowPlaying tells the services a song started.
func scrobbleNowPlaying(path string, dur float64) {
	meta, ok := musicMetaFor(path)
	if !ok {
		return
	}
	if meta.Src == musicSrcJellyfin && meta.ID != "" {
		scrobbleWatch.jfID, scrobbleWatch.jfSession = meta.ID, musicPlaySession()
		scrobbleWatch.jfLastRep = time.Now()
	}
	jfID, jfSess := scrobbleWatch.jfID, scrobbleWatch.jfSession
	if !IsOnline() {
		return
	}
	s := musicServersGet()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if jfID != "" {
			_ = jfReport(ctx, "playing", jfID, 0, false, jfSess)
		}
		if meta.Src == musicSrcNavidrome && meta.ID != "" && s.Navidrome.Scrobble {
			_ = ndScrobble(ctx, meta.ID, false, time.Now())
		}
		if meta.Artist == "" || meta.Title == "" {
			return
		}
		if sk := musicDecryptField(s.LastFM.Session); sk != "" {
			p := url.Values{"method": {"track.updateNowPlaying"}, "sk": {sk}, "artist": {meta.Artist}, "track": {meta.Title}}
			if meta.Album != "" {
				p.Set("album", meta.Album)
			}
			if dur > 0 {
				p.Set("duration", strconv.Itoa(int(dur)))
			}
			_ = lastfmCall(ctx, p, true, nil)
		}
		if tok := musicDecryptField(s.ListenBrainz.Token); tok != "" {
			_ = listenBrainzSubmit(ctx, tok, "playing_now", []lbListen{{Meta: lbTrack{ArtistName: meta.Artist,
				TrackName: meta.Title, ReleaseName: meta.Album}}})
		}
	}()
}

// scrobbleTrackEnded closes the Jellyfin report of the song that ended.
func scrobbleTrackEnded() {
	w := &scrobbleWatch
	if w.jfID == "" {
		return
	}
	id, sess, pos := w.jfID, w.jfSession, w.lastPos
	w.jfID = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = jfReport(ctx, "stopped", id, pos, false, sess)
	}()
}

// scrobbleVideoTick reports a Jellyfin video the video player is showing, so
// the server keeps where it was left (and marks it watched at the end).
func scrobbleVideoTick(now time.Time) {
	w := &scrobbleWatch
	ref := musicProxyVideoReport(playbackPageURL())
	id := ref.ID
	st := snapshotVideoState()
	live := id != "" && st.phase != "idle"
	pos := st.progress * st.duration
	if w.vidID != "" && (!live || id != w.vidID || ref.Src != w.vidSrc) {
		vid, src, vpos, sess := w.vidID, w.vidSrc, w.vidPos, w.vidSessID
		w.vidID = ""
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = videoReport(ctx, src, "stopped", vid, vpos, false, sess)
			if src != "plex" {
				musicSrcCacheClear() // Continue watching changed
			}
		}()
	}
	if !live {
		return
	}
	if w.vidID == "" {
		w.vidID, w.vidSrc, w.vidRep = id, ref.Src, now
		w.vidSessID = ref.Session
		if w.vidSessID == "" {
			w.vidSessID = musicPlaySession()
		}
		sess := w.vidSessID
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = videoReport(ctx, ref.Src, "playing", id, pos, false, sess)
		}()
	}
	if pos > 0 {
		w.vidPos = pos
	}
	if now.Sub(w.vidRep) > 30*time.Second {
		w.vidRep = now
		sess := w.vidSessID
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = videoReport(ctx, ref.Src, "progress", id, pos, false, sess)
		}()
	}
}

// videoReport sends one playback report to the server that owns the address.
func videoReport(ctx context.Context, src, what, id string, pos float64, paused bool, session string) error {
	if src == "plex" {
		return plexReport(ctx, what, id, pos, paused, session)
	}
	return jfReport(ctx, what, id, pos, paused, session)
}

// proxyVideoReport is where a loopback video address points: the source, the
// item id and the play session. Pure.
type proxyVideoReport struct {
	Src     string
	ID      string
	Session string
}

func musicProxyVideoReport(addr string) proxyVideoReport {
	u, err := url.Parse(addr)
	if err != nil || u.Hostname() != "127.0.0.1" {
		return proxyVideoReport{}
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(parts) < 3 {
		return proxyVideoReport{}
	}
	switch parts[0] {
	case "jfv", "jfh":
		id, err := url.PathUnescape(parts[2])
		if err != nil {
			return proxyVideoReport{}
		}
		return proxyVideoReport{Src: "jellyfin", ID: id}
	case "plexv":
		id, _ := plexSplitExt(parts[2])
		id, err = url.PathUnescape(id)
		if err != nil {
			return proxyVideoReport{}
		}
		return proxyVideoReport{Src: "plex", ID: id, Session: u.Query().Get("session")}
	case "plexh":
		if len(parts) < 4 || parts[2] != "start" {
			return proxyVideoReport{}
		}
		id := strings.TrimSuffix(parts[3], ".m3u8")
		id, err = url.PathUnescape(id)
		if err != nil {
			return proxyVideoReport{}
		}
		return proxyVideoReport{Src: "plex", ID: id, Session: u.Query().Get("session")}
	}
	return proxyVideoReport{}
}

// musicProxyVideoItem is the Jellyfin item id in a proxy video address, ""
// for any other address. Pure.
func musicProxyVideoItem(addr string) string {
	ref := musicProxyVideoReport(addr)
	if ref.Src != "jellyfin" {
		return ""
	}
	return ref.ID
}

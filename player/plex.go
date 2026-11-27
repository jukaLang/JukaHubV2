package main

// plex.go — Plex as a video source for Media › Movies & TV.
//
// Sign-in is Plex's own PIN flow: the handheld asks plex.tv for a short code,
// shows plex.tv/link and the code, and waits for approval. The same token then
// lists the account's servers and picks the first one that answers, preferring
// a home-network connection over a relay. The token is encrypted in
// musicservers.json and added by the loopback proxy, so it never appears in a
// player address, an ffmpeg command line or a log.
//
// Playback: an 8-bit H.264/MPEG-4 file up to 1080p with no DTS or TrueHD sound
// streams as it is; anything else goes through Plex's universal transcoder at
// 720p H.264/AAC. The player only ever sees 127.0.0.1:47533/plex… addresses.
// Timeline reports keep Plex's Continue Watching place.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	errPlexAuth     = errors.New("Plex signed this handheld out — connect it again")
	errPlexNotFound = errors.New("not found on the Plex server")
)

const (
	plexProduct   = "JukaHub"
	plexTVBase    = "https://plex.tv"
	plexCheckWait = 8 * time.Second
	plexPinWait   = 10 * time.Minute
	plexPinPoll   = 2 * time.Second
)

type plexClient struct {
	base     *url.URL
	token    string
	srv      string
	name     string
	clientID string
}

func plexReady() bool {
	c := musicServersGet().Plex
	return c.URL != "" && c.Token != ""
}

func plexConfigured() bool { return musicServersGet().Plex.URL != "" }

func plexNewClient() (*plexClient, error) {
	conf := musicServersGet().Plex
	if conf.URL == "" || conf.Token == "" {
		return nil, errors.New("no Plex server yet — connect Plex in Media › Movies & TV")
	}
	u, err := url.Parse(conf.URL)
	if err != nil || u.Host == "" {
		return nil, errors.New("the Plex address is broken — connect Plex again")
	}
	token := musicDecryptField(conf.Token)
	if token == "" {
		return nil, errors.New("the saved Plex token cannot be read — connect Plex again")
	}
	name := dlFirstNonEmpty(conf.ServerName, u.Hostname())
	return &plexClient{base: u, token: token, srv: musicServerTag("plex", conf.URL, conf.ServerID),
		name: name, clientID: musicDeviceID()}, nil
}

func plexVersion() string { return strings.TrimPrefix(versionString(nil), "v") }

// plexDo runs one Plex API request. The token goes in X-Plex-Token, never in
// the URL.
func plexDo(ctx context.Context, base *url.URL, token, method, p string, q url.Values, out interface{}) error {
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + p
	if q != nil {
		u.RawQuery = q.Encode()
	}
	var body io.Reader
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Product", plexProduct)
	req.Header.Set("X-Plex-Version", plexVersion())
	req.Header.Set("X-Plex-Platform", plexProduct)
	req.Header.Set("X-Plex-Device", deviceModelName())
	req.Header.Set("X-Plex-Client-Identifier", musicDeviceID())
	req.Header.Set("User-Agent", "JukaHub/"+plexVersion())
	if token != "" {
		req.Header.Set("X-Plex-Token", token)
	}
	resp, err := musicServerClient(base.Scheme).Do(req)
	if err != nil {
		return fmt.Errorf("Plex did not answer: %s", firstLine(err.Error()))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errPlexAuth
	case resp.StatusCode == http.StatusNotFound:
		return errPlexNotFound
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("Plex answered %d", resp.StatusCode)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out); err != nil {
		return errors.New("Plex sent something unexpected — check the server")
	}
	return nil
}

func (c *plexClient) get(ctx context.Context, p string, q url.Values, out interface{}) error {
	return plexDo(ctx, c.base, c.token, http.MethodGet, p, q, out)
}

// plexGet is the proxy's GET: same headers, optional Range.
func plexGet(ctx context.Context, scheme, rawURL, token, rangeHeader string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Plex-Token", token)
	req.Header.Set("X-Plex-Product", plexProduct)
	req.Header.Set("X-Plex-Version", plexVersion())
	req.Header.Set("X-Plex-Platform", plexProduct)
	req.Header.Set("X-Plex-Device", deviceModelName())
	req.Header.Set("X-Plex-Client-Identifier", musicDeviceID())
	req.Header.Set("User-Agent", "JukaHub/"+plexVersion())
	req.Header.Set("Accept", "*/*")
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	return musicServerClient(scheme).Do(req)
}

// ── Sign-in ─────────────────────────────────────────────────────────────────

type plexPin struct {
	ID        int    `json:"id"`
	Code      string `json:"code"`
	AuthToken string `json:"authToken"`
	ExpiresIn int    `json:"expiresIn"`
}

type plexConnection struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	URI      string `json:"uri"`
	Local    bool   `json:"local"`
	Relay    bool   `json:"relay"`
}

type plexResource struct {
	Name             string           `json:"name"`
	Product          string           `json:"product"`
	ClientIdentifier string           `json:"clientIdentifier"`
	Provides         string           `json:"provides"`
	Owned            bool             `json:"owned"`
	Connections      []plexConnection `json:"connections"`
}

var plexAuth struct {
	mu     sync.Mutex
	active bool
	code   string
	id     int
}

func plexSignInActive() (bool, string) {
	plexAuth.mu.Lock()
	defer plexAuth.mu.Unlock()
	return plexAuth.active, plexAuth.code
}

// plexStartSignIn asks plex.tv for a code and waits for it to be approved.
// Main thread.
func plexStartSignIn() {
	plexAuth.mu.Lock()
	if plexAuth.active {
		code := plexAuth.code
		plexAuth.mu.Unlock()
		plexShowCode(code)
		return
	}
	plexAuth.active, plexAuth.code, plexAuth.id = true, "", 0
	plexAuth.mu.Unlock()
	go plexSignInWorker()
}

func plexSignInWorker() {
	ctx, cancel := context.WithTimeout(context.Background(), plexPinWait)
	defer cancel()
	pin, err := plexRequestPin(ctx)
	if err != nil {
		plexSignInFailed(err)
		return
	}
	plexAuth.mu.Lock()
	plexAuth.code, plexAuth.id = pin.Code, pin.ID
	plexAuth.mu.Unlock()
	tbxPost(func() { plexShowCode(pin.Code) })
	token, err := plexWaitPin(ctx, pin.ID)
	if err != nil {
		plexSignInFailed(err)
		return
	}
	resources, err := plexResources(ctx, token)
	if err != nil {
		plexSignInFailed(err)
		return
	}
	res, conn, err := plexChooseServer(ctx, resources, token)
	if err != nil {
		plexSignInFailed(err)
		return
	}
	addr := strings.TrimRight(plexConnectionURL(conn), "/")
	enc, err := EncryptAPIToken(token, "Plex token")
	if err != nil {
		plexSignInFailed(errors.New("the handheld could not store the Plex token"))
		return
	}
	if err := musicServersUpdate(func(s *musicServersFile) {
		s.Plex = musicPlexConf{URL: addr, Token: enc, ServerID: res.ClientIdentifier,
			ServerName: dlFirstNonEmpty(res.Name, plexHostOf(addr))}
	}); err != nil {
		plexSignInFailed(err)
		return
	}
	plexAuth.mu.Lock()
	plexAuth.active = false
	plexAuth.mu.Unlock()
	tbxPost(func() {
		qrZoomCloseCard()
		ShowToast("Plex connected: "+dlFirstNonEmpty(res.Name, "server"), ToastKindSuccess)
		mtvLoadPlex(false)
	})
}

func plexSignInFailed(err error) {
	plexAuth.mu.Lock()
	plexAuth.active = false
	plexAuth.mu.Unlock()
	msg := "Could not sign in to Plex"
	if err != nil {
		msg += ": " + firstLine(err.Error())
	}
	tbxPost(func() { ShowToast(msg, ToastKindWarning) })
}

func plexShowCode(code string) {
	if code == "" {
		ShowToast("Asking Plex for a code…", ToastKindInfo)
		return
	}
	detail := "Open plex.tv/link on your phone or computer and enter " + code
	mtvChanged()
	if !qrZoomOpenCardDetail(tbxQRModules("https://plex.tv/link"), "Sign in to Plex", detail) {
		ShowToast(detail, ToastKindInfo)
	}
}

func plexRequestPin(ctx context.Context) (plexPin, error) {
	base, _ := url.Parse(plexTVBase)
	var pin plexPin
	err := plexDo(ctx, base, "", http.MethodPost, "/api/v2/pins", url.Values{"strong": {"true"}}, &pin)
	if err == nil && (pin.ID <= 0 || pin.Code == "") {
		err = errors.New("plex.tv sent no sign-in code")
	}
	return pin, err
}

func plexPollPin(ctx context.Context, id int) (string, error) {
	base, _ := url.Parse(plexTVBase)
	var pin plexPin
	err := plexDo(ctx, base, "", http.MethodGet, "/api/v2/pins/"+strconv.Itoa(id), nil, &pin)
	return pin.AuthToken, err
}

func plexWaitPin(ctx context.Context, id int) (string, error) {
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return "", errors.New("the Plex code expired — start again")
		case <-time.After(plexPinPoll):
		}
		token, err := plexPollPin(ctx, id)
		if err == nil && token != "" {
			return token, nil
		}
		if errors.Is(err, errPlexNotFound) {
			return "", errors.New("the Plex code expired — start again")
		}
		if err != nil {
			fails++
			if fails >= 5 {
				return "", err
			}
		}
	}
}

func plexResources(ctx context.Context, token string) ([]plexResource, error) {
	base, _ := url.Parse(plexTVBase)
	var res []plexResource
	err := plexDo(ctx, base, token, http.MethodGet, "/api/v2/resources",
		url.Values{"includeHttps": {"1"}, "includeRelay": {"1"}}, &res)
	if err == nil && len(res) == 0 {
		err = errors.New("Plex found no servers on this account")
	}
	return res, err
}

// plexConnectionURL is a connection's address: its URI, or protocol, address
// and port. Pure.
func plexConnectionURL(c plexConnection) string {
	if strings.TrimSpace(c.URI) != "" {
		return c.URI
	}
	scheme := strings.ToLower(strings.TrimSpace(c.Protocol))
	if scheme != "http" && scheme != "https" {
		scheme = "http"
	}
	host := c.Address
	port := c.Port
	if port <= 0 {
		port = 32400
	}
	host = net.JoinHostPort(c.Address, strconv.Itoa(port))
	return scheme + "://" + host
}

// plexConnectionRank prefers a home-network connection, then a direct remote
// one, and a relay last. Pure.
func plexConnectionRank(c plexConnection) int {
	switch {
	case c.Relay:
		return 2
	case c.Local:
		return 0
	}
	return 1
}

type plexCandidate struct {
	res  plexResource
	conn plexConnection
	rank int
}

// plexCandidates lists every server connection from best to worst. Pure.
func plexCandidates(resources []plexResource) []plexCandidate {
	var out []plexCandidate
	for _, r := range resources {
		if !strings.Contains(strings.ToLower(r.Provides), "server") {
			continue
		}
		for _, c := range r.Connections {
			u := plexConnectionURL(c)
			if u == "" {
				continue
			}
			parsed, err := url.Parse(u)
			if err != nil || parsed.Host == "" ||
				(parsed.Scheme != "http" && parsed.Scheme != "https") {
				continue
			}
			out = append(out, plexCandidate{res: r, conn: c, rank: plexConnectionRank(c)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].rank != out[j].rank {
			return out[i].rank < out[j].rank
		}
		// A named, owned server first among equals.
		if out[i].res.Owned != out[j].res.Owned {
			return out[i].res.Owned
		}
		return out[i].res.Name < out[j].res.Name
	})
	return out
}

// plexChooseServer tests the candidate connections in preference order and
// returns the first that answers the library API.
func plexChooseServer(ctx context.Context, resources []plexResource, token string) (plexResource, plexConnection, error) {
	cands := plexCandidates(resources)
	if len(cands) == 0 {
		return plexResource{}, plexConnection{}, errors.New("Plex found no server addresses on this account")
	}
	var last error
	for _, cand := range cands {
		u, err := url.Parse(plexConnectionURL(cand.conn))
		if err != nil {
			continue
		}
		check, cancel := context.WithTimeout(ctx, plexCheckWait)
		err = plexDo(check, u, token, http.MethodGet, "/library/sections",
			url.Values{"X-Plex-Container-Size": {"1"}}, nil)
		cancel()
		if err == nil {
			return cand.res, cand.conn, nil
		}
		last = err
	}
	if errors.Is(last, errPlexAuth) {
		return plexResource{}, plexConnection{}, errPlexAuth
	}
	if last == nil {
		last = errors.New("no address answered")
	}
	return plexResource{}, plexConnection{}, fmt.Errorf("Plex found your server but none of its addresses answered (%s)", firstLine(last.Error()))
}

// ── Library ─────────────────────────────────────────────────────────────────

type plexSection struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Type  string `json:"type"`
}

type plexStream struct {
	StreamType int    `json:"streamType"`
	Codec      string `json:"codec"`
	BitDepth   int    `json:"bitDepth"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	Channels   int    `json:"channels"`
	Selected   bool   `json:"selected"`
}

type plexPart struct {
	ID        int          `json:"id"`
	Key       string       `json:"key"`
	File      string       `json:"file"`
	Container string       `json:"container"`
	Size      int64        `json:"size"`
	Streams   []plexStream `json:"Stream"`
}

type plexMedia struct {
	ID              int        `json:"id"`
	Duration        int64      `json:"duration"`
	Bitrate         int        `json:"bitrate"`
	Width           int        `json:"width"`
	Height          int        `json:"height"`
	VideoCodec      string     `json:"videoCodec"`
	AudioCodec      string     `json:"audioCodec"`
	Container       string     `json:"container"`
	VideoResolution string     `json:"videoResolution"`
	Parts           []plexPart `json:"Part"`
}

type plexItem struct {
	RatingKey            string      `json:"ratingKey"`
	Key                  string      `json:"key"`
	Type                 string      `json:"type"`
	Title                string      `json:"title"`
	Year                 int         `json:"year"`
	Duration             int64       `json:"duration"`
	ViewOffset           int64       `json:"viewOffset"`
	ViewCount            int         `json:"viewCount"`
	Index                int         `json:"index"`
	ParentIndex          int         `json:"parentIndex"`
	LeafCount            int         `json:"leafCount"`
	ChildCount           int         `json:"childCount"`
	ParentTitle          string      `json:"parentTitle"`
	GrandparentTitle     string      `json:"grandparentTitle"`
	ParentRatingKey      string      `json:"parentRatingKey"`
	GrandparentRatingKey string      `json:"grandparentRatingKey"`
	Thumb                string      `json:"thumb"`
	Art                  string      `json:"art"`
	ParentThumb          string      `json:"parentThumb"`
	GrandparentThumb     string      `json:"grandparentThumb"`
	Media                []plexMedia `json:"Media"`
}

type plexMediaContainer struct {
	MediaContainer struct {
		Size      int           `json:"size"`
		Directory []plexSection `json:"Directory"`
		Metadata  []plexItem    `json:"Metadata"`
	} `json:"MediaContainer"`
}

func (c *plexClient) sections(ctx context.Context) ([]plexSection, error) {
	var r plexMediaContainer
	err := c.get(ctx, "/library/sections", url.Values{"X-Plex-Container-Size": {"64"}}, &r)
	return r.MediaContainer.Directory, err
}

func (c *plexClient) metadata(ctx context.Context, p string, q url.Values) ([]plexItem, error) {
	var r plexMediaContainer
	if q == nil {
		q = url.Values{}
	}
	if q.Get("X-Plex-Container-Size") == "" {
		q.Set("X-Plex-Container-Size", "1000")
	}
	err := c.get(ctx, p, q, &r)
	return r.MediaContainer.Metadata, err
}

func (c *plexClient) onDeck(ctx context.Context) ([]plexItem, error) {
	return c.metadata(ctx, "/library/onDeck", url.Values{"X-Plex-Container-Size": {"40"}})
}

func (c *plexClient) sectionItems(ctx context.Context, key, kind string) ([]plexItem, error) {
	return c.metadata(ctx, "/library/sections/"+url.PathEscape(key)+"/all",
		url.Values{"type": {kind}, "sort": {"titleSort"}, "X-Plex-Container-Size": {"3000"}})
}

func (c *plexClient) children(ctx context.Context, key string) ([]plexItem, error) {
	return c.metadata(ctx, "/library/metadata/"+url.PathEscape(key)+"/children", nil)
}

// plexHostOf is a host name for a server address. Pure.
func plexHostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// plexCanDirectPlay is the handheld's decoder's reach: 8-bit H.264 or MPEG-4
// up to 1080p, no DTS or TrueHD sound. Pure.
func plexCanDirectPlay(it plexItem) bool {
	if len(it.Media) == 0 {
		return false
	}
	m := it.Media[0]
	video, streams := false, false
	for _, p := range m.Parts {
		for _, s := range p.Streams {
			streams = true
			codec := strings.ToLower(s.Codec)
			switch s.StreamType {
			case 1:
				if codec != "h264" && codec != "mpeg4" && codec != "msmpeg4v3" {
					return false
				}
				if s.BitDepth > 8 || s.Width > 1920 || s.Height > 1088 {
					return false
				}
				video = true
			case 2:
				if strings.HasPrefix(codec, "dts") || codec == "truehd" || codec == "mlp" {
					return false
				}
			}
		}
	}
	if streams {
		return video
	}
	vc := strings.ToLower(m.VideoCodec)
	if vc != "h264" && vc != "mpeg4" && vc != "msmpeg4v3" {
		return false
	}
	if m.Width > 1920 || m.Height > 1088 {
		return false
	}
	ac := strings.ToLower(m.AudioCodec)
	if strings.HasPrefix(ac, "dts") || ac == "truehd" || ac == "mlp" {
		return false
	}
	return vc != ""
}

// plexFirstPart is the file's first part and media. Pure.
func plexFirstPart(it plexItem) (plexPart, plexMedia, bool) {
	for _, m := range it.Media {
		if len(m.Parts) > 0 {
			return m.Parts[0], m, true
		}
	}
	return plexPart{}, plexMedia{}, false
}

// plexVideoExt is the extension the player should see. Pure.
func plexVideoExt(it plexItem) string {
	if p, m, ok := plexFirstPart(it); ok {
		if ext := path.Ext(p.Key); ext != "" {
			return musicSafeExt(ext)
		}
		if ext := path.Ext(p.File); ext != "" {
			return musicSafeExt(ext)
		}
		if m.Container != "" {
			return musicSafeExt(strings.Split(m.Container, ",")[0])
		}
		if p.Container != "" {
			return musicSafeExt(strings.Split(p.Container, ",")[0])
		}
	}
	return "mkv"
}

// plexDirectURL is a file as it is, through the proxy. Pure.
func plexDirectURL(srv, ratingKey, partKey, ext, session string) string {
	if partKey == "" {
		return ""
	}
	ext = musicSafeExt(ext)
	switch ext {
	case "mp4", "m4v", "mkv", "webm", "mov", "ts":
	default:
		ext = "mkv"
	}
	q := url.Values{"part": {partKey}}
	if session != "" {
		q.Set("session", session)
	}
	return musicProxyBase() + "/plexv/" + srv + "/" + url.PathEscape(ratingKey) + "." + ext + "?" + q.Encode()
}

// plexTranscodeURL is Plex's universal HLS transcode at 720p H.264/AAC,
// through the proxy. Pure.
func plexTranscodeURL(srv, ratingKey, session string, offset float64) string {
	q := url.Values{"session": {session}}
	if offset > 0 {
		q.Set("offset", strconv.FormatInt(int64(offset), 10))
	}
	return musicProxyBase() + "/plexh/" + srv + "/start/" + url.PathEscape(ratingKey) + ".m3u8?" + q.Encode()
}

// plexPlayURL is where an item plays: direct when the handheld can decode it,
// the universal transcode otherwise. Pure.
func plexPlayURL(c *plexClient, it plexItem, session string) (string, bool) {
	ratingKey := plexRatingKey(it)
	if p, _, ok := plexFirstPart(it); ok {
		partKey, clean := plexCleanPath(p.Key, "/library/parts/")
		if clean && plexCanDirectPlay(it) {
			return plexDirectURL(c.srv, ratingKey, partKey, plexVideoExt(it), session), true
		}
	}
	return plexTranscodeURL(c.srv, ratingKey, session, float64(it.ViewOffset)/1000), false
}

// plexCleanPath keeps a server path to the library: no traversal, no control
// characters, and the expected prefix. Pure.
func plexCleanPath(raw, prefix string) (string, bool) {
	p := strings.TrimSpace(raw)
	if p == "" || len(p) > 2048 || strings.ContainsAny(p, "\r\n\x00?#") ||
		strings.Contains(p, "..") || !strings.HasPrefix(p, "/") ||
		!strings.HasPrefix(p, prefix) {
		return "", false
	}
	return p, true
}

// ── Playback reports ────────────────────────────────────────────────────────

// plexReport tells Plex what the player is doing, so Continue Watching keeps
// the place. time is in milliseconds, as Plex expects.
func plexReport(ctx context.Context, what, ratingKey string, pos float64, paused bool, session string) error {
	c, err := plexNewClient()
	if err != nil {
		return err
	}
	state := "playing"
	switch {
	case what == "stopped":
		state = "stopped"
	case what == "paused" || paused:
		state = "paused"
	}
	q := url.Values{
		"ratingKey": {ratingKey},
		"key":       {"/library/metadata/" + ratingKey},
		"state":     {state},
		"time":      {strconv.FormatInt(int64(pos*1000), 10)},
	}
	if session != "" {
		q.Set("playSessionId", session)
	}
	err = c.get(ctx, "/:/timeline", q, nil)
	if what == "stopped" && session != "" {
		// The universal transcoder stops asking for segments once this is
		// sent; closing its session here keeps a stopped video from holding
		// the server's transcoder slot.
		_ = c.get(ctx, "/video/transcode/universal/stop", url.Values{"session": {session}}, nil)
	}
	return err
}

// ── Proxy ───────────────────────────────────────────────────────────────────

// plexProxy serves the three Plex routes: a direct file, the HLS transcode,
// and a picture.
func plexProxy(w http.ResponseWriter, r *http.Request, route, srv, rest string) {
	c, err := plexNewClient()
	if err != nil {
		http.Error(w, "Plex is not available: "+firstLine(err.Error()), http.StatusBadGateway)
		return
	}
	if c.srv != srv {
		http.Error(w, "this video belongs to another Plex server", http.StatusNotFound)
		return
	}
	switch route {
	case "plexi":
		p, ok := plexCleanPath(r.URL.Query().Get("path"), "/library/")
		if !ok {
			if p, ok = plexCleanPath(r.URL.Query().Get("path"), "/photo/"); !ok {
				http.NotFound(w, r)
				return
			}
		}
		target := musicJoinURL(c.base, p, nil)
		musicProxyPass(w, r, func(ctx context.Context, rg string) (*http.Response, error) {
			return plexGet(ctx, c.base.Scheme, target, c.token, rg)
		})
	case "plexv":
		idEsc, ext := plexSplitExt(rest)
		id, err := url.PathUnescape(idEsc)
		if err != nil || id == "" || strings.ContainsAny(id, "/\\") {
			http.NotFound(w, r)
			return
		}
		part, ok := plexCleanPath(r.URL.Query().Get("part"), "/library/parts/")
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = ext
		target := musicJoinURL(c.base, part, nil)
		musicProxyPass(w, r, func(ctx context.Context, rg string) (*http.Response, error) {
			return plexGet(ctx, c.base.Scheme, target, c.token, rg)
		})
	case "plexh":
		plexProxyTranscode(w, r, c, rest)
	default:
		http.NotFound(w, r)
	}
}

// plexSplitExt splits "name.ext" into its name and extension. Pure.
func plexSplitExt(rest string) (string, string) {
	dot := strings.LastIndex(rest, ".")
	if dot <= 0 {
		return rest, ""
	}
	return rest[:dot], rest[dot+1:]
}

// plexProxyTranscode starts a universal transcode (start/<ratingKey>.m3u8) or
// passes one of its playlists/segments through. Playlists are rewritten to the
// loopback proxy, so every segment keeps the token off the player's address.
func plexProxyTranscode(w http.ResponseWriter, r *http.Request, c *plexClient, rest string) {
	if strings.HasPrefix(rest, "start/") {
		key := strings.TrimSuffix(strings.TrimPrefix(rest, "start/"), ".m3u8")
		ratingKey, err := url.PathUnescape(key)
		if err != nil || ratingKey == "" || strings.ContainsAny(ratingKey, "/\\") {
			http.NotFound(w, r)
			return
		}
		session := r.URL.Query().Get("session")
		if session == "" {
			session = musicPlaySession()
		}
		offset, _ := strconv.ParseFloat(r.URL.Query().Get("offset"), 64)
		q := url.Values{
			"path":             {"/library/metadata/" + ratingKey},
			"mediaIndex":       {"0"},
			"partIndex":        {"0"},
			"protocol":         {"hls"},
			"fastSeek":         {"1"},
			"directPlay":       {"0"},
			"directStream":     {"0"},
			"videoCodec":       {"h264"},
			"audioCodec":       {"aac"},
			"maxVideoBitrate":  {"3000"},
			"videoResolution":  {"1280x720"},
			"maxAudioChannels": {"2"},
			"session":          {session},
		}
		if offset > 0 {
			q.Set("offset", strconv.FormatInt(int64(offset), 10))
		}
		target := musicJoinURL(c.base, "/video/transcode/universal/start.m3u8", q)
		resp, err := plexGet(r.Context(), c.base.Scheme, target, c.token, r.Header.Get("Range"))
		if err != nil {
			http.Error(w, "Plex transcode did not start: "+firstLine(err.Error()), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			http.Error(w, "Plex transcode did not start: server answered "+strconv.Itoa(resp.StatusCode), http.StatusBadGateway)
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			http.Error(w, "Plex transcode did not send a playlist", http.StatusBadGateway)
			return
		}
		body = []byte(plexRewritePlaylist(string(body), c.srv, resp.Request.URL))
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(body)
		}
		return
	}
	tail, ok := plexCleanPath("/video/transcode/universal/"+rest, "/video/transcode/universal/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	target := musicJoinURL(c.base, tail, r.URL.Query())
	musicProxyPass(w, r, func(ctx context.Context, rg string) (*http.Response, error) {
		return plexGet(ctx, c.base.Scheme, target, c.token, rg)
	})
}

// plexRewritePlaylist points every absolute and relative playlist entry back
// at the loopback proxy. Pure.
func plexRewritePlaylist(body, srv string, base *url.URL) string {
	const prefix = "/video/transcode/universal/"
	if base == nil {
		return body
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		ref, err := url.Parse(trim)
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		p := abs.EscapedPath()
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		out := musicProxyBase() + "/plexh/" + srv + "/" + strings.TrimPrefix(p, prefix)
		if q := abs.Query(); len(q) > 0 {
			// Plex sometimes puts its token in segment addresses; the proxy
			// adds it in a header instead.
			q.Del("X-Plex-Token")
			q.Del("x-plex-token")
			if len(q) > 0 {
				out += "?" + q.Encode()
			}
		}
		lines[i] = out
	}
	return strings.Join(lines, "\n")
}

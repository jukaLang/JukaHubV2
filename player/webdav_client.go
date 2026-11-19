package main

// webdav_client.go — a WebDAV server as the other side of Save sync, for
// players with no Google Cloud project (docs/CLOUD_SYNC.md → WebDAV).
//
// Nextcloud, ownCloud, Koofr, InfiniCLOUD and most NAS boxes speak WebDAV:
// files over a few HTTP verbs (PROPFIND lists a folder, GET, PUT, MKCOL). The
// player types the server's address, a user name and an app password on the
// phone (companion_cloud.go); the password is kept encrypted in webdav.json
// beside the other settings (0600) and goes with Basic auth on every request,
// which is why an address on the internet must be HTTPS: in plain http
// anyone on the way (a café's Wi-Fi, the provider) reads the password and the
// saves. Only a server on the home network may be plain http:// — many NAS
// boxes have no certificate a handheld would trust — and even then the
// connection is refused unless the name really resolves to a private
// address, goes through no proxy and follows no redirect.
//
// Google Drive has no WebDAV. Drive without a Google project goes through a
// computer instead (Syncthing into a Google Drive for desktop folder).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// webdavTopFolder is the one folder JukaHub makes under the address.
	webdavTopFolder = "JukaHub Saves"
	// webdavRecordName is the manifest inside it (webdav_sync.go).
	webdavRecordName = ".jukahub-manifest.json"
	// webdavAnswerLimit bounds an error body or another small answer.
	webdavAnswerLimit = 64 << 10
	// webdavListLimit bounds one PROPFIND answer and the manifest.
	webdavListLimit = 16 << 20
	// webdavMaxItems bounds the entries of one listing.
	webdavMaxItems = 2 * ssyncMaxFiles
	// webdavURLMax, webdavUserMax and webdavPasswordMax bound what the phone
	// may send.
	webdavURLMax      = 1024
	webdavUserMax     = 256
	webdavPasswordMax = 512
)

var (
	errWebdavNotSet  = errors.New("no WebDAV server is connected")
	errWebdavRefused = errors.New("the WebDAV server refused the user name or password — send them again from your phone (Tools → Save sync)")
	errWebdavNotLAN  = errors.New("a plain http:// address must be on your home network — use https:// for a server on the internet")
)

// ── Stored connection ───────────────────────────────────────────────────────

// webdavSettings is webdav.json. Password is stored encrypted ("ENC:…").
// Refused records a 401 to the stored password, so automatic syncs stop
// asking until the phone sends a new one.
type webdavSettings struct {
	V          int       `json:"v"`
	URL        string    `json:"url,omitempty"`
	User       string    `json:"user,omitempty"`
	Password   string    `json:"password,omitempty"`
	Auto       bool      `json:"auto"`
	Refused    bool      `json:"refused,omitempty"`
	NoInfinity bool      `json:"no_infinity,omitempty"`
	LastResult string    `json:"last_result,omitempty"`
	LastAt     time.Time `json:"last_at"`
}

// connected reports whether a server is set.
func (s webdavSettings) connected() bool { return s.URL != "" }

// ready reports whether syncs can run without asking the player anything.
func (s webdavSettings) ready() bool { return s.connected() && !s.Refused }

// label is how the page names the server: "user · host".
func (s webdavSettings) label() string {
	host := s.URL
	if u, err := url.Parse(s.URL); err == nil && u.Host != "" {
		host = u.Host
	}
	if s.User != "" {
		return s.User + " · " + host
	}
	return host
}

// webdavIdentity names one server and account: the sync base and the
// "did the connection change during the sync" checks hang on it.
func webdavIdentity(s webdavSettings) string {
	if s.URL == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("webdav:" + s.URL + "\n" + s.User))
	return hex.EncodeToString(sum[:8])
}

var webdavStore struct {
	mu     sync.Mutex
	loaded bool
	value  webdavSettings
}

// webdavSettingsPath is a test seam.
var webdavSettingsPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "webdav.json")
}

func webdavLoadLocked() {
	if webdavStore.loaded {
		return
	}
	webdavStore.loaded = true
	p := webdavSettingsPath()
	if p == "" {
		return
	}
	var s webdavSettings
	if _, err := readJSONWithBackup(p, &s); err == nil {
		webdavStore.value = s
	} else if !os.IsNotExist(err) {
		logWarn("webdav", "ignoring webdav.json: %v", err)
	}
}

// webdavSettingsGet is the stored connection (read from disk once).
func webdavSettingsGet() webdavSettings {
	webdavStore.mu.Lock()
	defer webdavStore.mu.Unlock()
	webdavLoadLocked()
	return webdavStore.value
}

// webdavSettingsUpdate changes the stored connection and writes it.
func webdavSettingsUpdate(change func(*webdavSettings)) error {
	webdavStore.mu.Lock()
	defer webdavStore.mu.Unlock()
	webdavLoadLocked()
	next := webdavStore.value
	change(&next)
	next.V = 1
	p := webdavSettingsPath()
	if p == "" {
		return errors.New("the settings folder is unavailable")
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := AtomicWrite(p, append(data, '\n'), 0o600); err != nil {
		return err
	}
	webdavStore.value = next
	return nil
}

// webdavForget deletes the stored connection, backup included (it holds the
// password too). The saves on the server and the sync base stay.
func webdavForget() {
	webdavStore.mu.Lock()
	defer webdavStore.mu.Unlock()
	webdavStore.loaded, webdavStore.value = true, webdavSettings{}
	if p := webdavSettingsPath(); p != "" {
		_ = os.Remove(p)
		_ = os.Remove(p + ".bak")
	}
	logInfo("webdav", "WebDAV server forgotten")
}

// webdavAutoReady reports whether the automatic game syncs should use WebDAV.
func webdavAutoReady() bool {
	s := webdavSettingsGet()
	return s.ready() && s.Auto
}

// webdavNoteRefused marks the stored password refused, unless the connection
// changed since the request that got the 401 was made.
func webdavNoteRefused(identity string) {
	err := webdavSettingsUpdate(func(s *webdavSettings) {
		if webdavIdentity(*s) == identity {
			s.Refused = true
		}
	})
	if err != nil {
		logWarn("webdav", "could not record the refused password: %v", err)
	}
	logWarn("webdav", "the server refused the stored user name or password")
}

// ── Addresses ───────────────────────────────────────────────────────────────

// webdavCleanURL checks a server address typed on the phone (outside data):
// http or https (dav:// and davs:// as apps write them), a host, no user name,
// query or ".." in it, a folder (a "/" is added), and HTTPS unless the host is
// on the home network. A bare "cloud.example.com/dav" gets https://.
func webdavCleanURL(raw string) (*url.URL, error) {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "\"'`<>"))
	if s == "" {
		return nil, errors.New("type the server's WebDAV address first")
	}
	if len(s) > webdavURLMax {
		return nil, errors.New("that address is too long")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("that is not a web address — it looks like https://cloud.example.com/remote.php/dav/files/you/")
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "davs", "webdavs":
		u.Scheme = "https"
	case "http", "dav", "webdav":
		u.Scheme = "http"
	default:
		return nil, errors.New("the address must start with https://")
	}
	if u.User != nil {
		return nil, errors.New("put the user name and password in their own fields, not in the address")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Opaque != "" {
		return nil, errors.New("the address must not have a ? part — copy the WebDAV address the server shows")
	}
	u.Fragment, u.RawFragment = "", ""
	segs, ok := webdavSegments(u.EscapedPath())
	if !ok {
		return nil, errors.New("the address has a broken %-escape in it")
	}
	for _, seg := range segs {
		if seg == "." || seg == ".." || seg == "" {
			return nil, errors.New("the address must not contain . or .. folders")
		}
	}
	if u.Path == "" {
		u.Path = "/"
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
		if u.RawPath != "" {
			u.RawPath += "/"
		}
	}
	if u.Scheme == "http" && !webdavLANHost(u.Hostname()) {
		return nil, errWebdavNotLAN
	}
	return u, nil
}

// webdavCGNAT is 100.64.0.0/10: Tailscale's addresses (and carrier NAT,
// which no customer can reach). WireGuard already encrypts Tailscale's
// traffic, so a home server there may be plain http.
var webdavCGNAT = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// webdavLANIP reports whether ip is on a home network: private, loopback,
// link-local or Tailscale's range.
func webdavLANIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || webdavCGNAT.Contains(ip)
}

// webdavLANHost reports whether host names a home-network server: a private
// address, or a name only a home network answers (a single word like "nas",
// .local, .lan, .home.arpa, .internal). The dialer still checks where a name
// leads (webdavLANDial).
func webdavLANHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return webdavLANIP(ip)
	}
	if h == "localhost" || !strings.Contains(h, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".lan", ".home.arpa", ".internal", ".localdomain", ".home"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}

// webdavLANDial dials a plain-http server only at a home-network address: a
// name that resolves anywhere else is refused before the password leaves.
func webdavLANDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	for _, ip := range ips {
		if webdavLANIP(ip.IP) {
			return d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		}
	}
	return nil, errWebdavNotLAN
}

// webdavSegments splits an escaped path into its unescaped folder names.
func webdavSegments(escaped string) ([]string, bool) {
	trimmed := strings.Trim(escaped, "/")
	if trimmed == "" {
		return nil, true
	}
	parts := strings.Split(trimmed, "/")
	for i, p := range parts {
		s, err := url.PathUnescape(p)
		if err != nil {
			return nil, false
		}
		parts[i] = s
	}
	return parts, true
}

// ── Connection ──────────────────────────────────────────────────────────────

// webdavConn is one server and account, ready for requests.
type webdavConn struct {
	base     *url.URL
	top      *url.URL
	user     string
	password string
	identity string
	// refused is set by the first 401: the rest of the sync stops asking
	// rather than sending the refused password once per file.
	refused atomic.Bool
}

// webdavConnFrom opens the stored connection (it decrypts the password).
func webdavConnFrom(s webdavSettings) (*webdavConn, error) {
	if !s.connected() {
		return nil, errWebdavNotSet
	}
	base, err := webdavCleanURL(s.URL)
	if err != nil {
		return nil, err
	}
	pw, err := DecryptString(s.Password)
	if err != nil {
		return nil, fmt.Errorf("the stored WebDAV password cannot be read (%v) — send it again from your phone", err)
	}
	return webdavNewConn(base, s.User, pw, webdavIdentity(s))
}

// webdavNewConn is a connection to base with an account.
func webdavNewConn(base *url.URL, user, password, identity string) (*webdavConn, error) {
	top, err := url.Parse(base.String() + url.PathEscape(webdavTopFolder) + "/")
	if err != nil {
		return nil, err
	}
	return &webdavConn{base: base, top: top, user: user, password: password, identity: identity}, nil
}

// keyURL is the address of a folder key ("." is the top folder) or a file key
// below the top folder, every name escaped.
func (c *webdavConn) keyURL(key string, dir bool) string {
	var b strings.Builder
	b.WriteString(c.top.String())
	if key != gdriveTopKey && key != "" {
		for i, seg := range strings.Split(key, "/") {
			if i > 0 {
				b.WriteByte('/')
			}
			b.WriteString(url.PathEscape(seg))
		}
		if dir {
			b.WriteByte('/')
		}
	}
	return b.String()
}

var webdavHTTP struct {
	once sync.Once
	tls  *http.Client
	lan  *http.Client
}

// webdavClient is the HTTP client for the connection's scheme: HTTPS with the
// firmware CA fallback a handheld needs (bugrepRootCAs), or plain http that
// dials home-network addresses only, with no proxy. Neither follows
// redirects: a redirect would carry the password to an address nobody typed.
// There is no overall timeout; every call carries a context deadline.
func (c *webdavConn) client() *http.Client {
	webdavHTTP.once.Do(func() {
		noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		tr := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			MaxIdleConns:          4,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       60 * time.Second,
		}
		if pool := bugrepRootCAs(); pool != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		webdavHTTP.tls = &http.Client{Transport: tr, CheckRedirect: noRedirect}
		lan := &http.Transport{
			DialContext:           webdavLANDial,
			ResponseHeaderTimeout: 60 * time.Second,
			MaxIdleConns:          4,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       60 * time.Second,
		}
		webdavHTTP.lan = &http.Client{Transport: lan, CheckRedirect: noRedirect}
	})
	if c.base.Scheme == "http" {
		return webdavHTTP.lan
	}
	return webdavHTTP.tls
}

// webdavHTTPError is an answer outside 2xx.
type webdavHTTPError struct {
	Status   int
	Location string
	Digest   bool
}

func (e *webdavHTTPError) Error() string {
	switch {
	case e.Status == http.StatusUnauthorized && e.Digest:
		return "the WebDAV server asks for Digest sign-in, which JukaHub does not speak — turn on Basic auth (over HTTPS) on the server"
	case e.Status == http.StatusUnauthorized:
		return errWebdavRefused.Error()
	case e.Status >= 300 && e.Status < 400:
		where := "another address"
		if u, err := url.Parse(e.Location); err == nil && u.Host != "" {
			where = u.Host
		}
		return "the WebDAV server sent JukaHub to " + where + " — enter the exact WebDAV address the server shows"
	case e.Status == http.StatusForbidden:
		return "the WebDAV server does not allow this here (HTTP 403) — check the folder address and the app password's permissions"
	case e.Status == http.StatusNotFound:
		return "not found on the WebDAV server"
	case e.Status == http.StatusPreconditionFailed:
		return "it changed on the WebDAV server since the comparison — compare again"
	case e.Status == http.StatusLocked:
		return "the file is locked on the WebDAV server — try again later"
	case e.Status == http.StatusInsufficientStorage, e.Status == http.StatusRequestEntityTooLarge:
		return "the WebDAV server is full or refuses a file this large"
	}
	return fmt.Sprintf("the WebDAV server answered HTTP %d", e.Status)
}

// Is lets errors.Is(err, errWebdavRefused) find a plain 401.
func (e *webdavHTTPError) Is(target error) bool {
	return target == errWebdavRefused && e.Status == http.StatusUnauthorized && !e.Digest
}

// webdavStatusIs reports whether err is an answer with one of the statuses.
func webdavStatusIs(err error, statuses ...int) bool {
	var he *webdavHTTPError
	if !errors.As(err, &he) {
		return false
	}
	for _, s := range statuses {
		if he.Status == s {
			return true
		}
	}
	return false
}

// webdavReq is one request. body is called once per attempt so a retry
// sends the whole body again; once turns retries off (a file streamed from
// the card).
type webdavReq struct {
	method string
	url    string
	body   func() (io.Reader, error)
	size   int64
	header http.Header
	once   bool
}

// send makes one request with the account's Basic auth. 429 and 5xx back off
// (honouring Retry-After) and try again up to three times, as does a dropped
// connection; a 2xx answer (207 included) comes back open, anything else as
// *webdavHTTPError. A 401 marks the stored password refused.
func (c *webdavConn) send(ctx context.Context, r webdavReq) (*http.Response, error) {
	const attempts = 3
	backoff := time.Second
	if c.refused.Load() {
		return nil, &webdavHTTPError{Status: http.StatusUnauthorized}
	}
	for attempt := 1; ; attempt++ {
		var body io.Reader
		if r.body != nil {
			b, err := r.body()
			if err != nil {
				return nil, err
			}
			body = b
			if r.size == 0 {
				// An empty save: Content-Length 0, not the chunked body Go
				// sends for a reader of unknown length (some servers answer
				// 411 or store it wrongly).
				body = http.NoBody
			}
		}
		req, err := http.NewRequestWithContext(ctx, r.method, r.url, body)
		if err != nil {
			return nil, err
		}
		if body != nil && r.size >= 0 {
			req.ContentLength = r.size
		}
		for k, vs := range r.header {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		if c.user != "" || c.password != "" {
			req.SetBasicAuth(c.user, c.password)
		}
		req.Header.Set("User-Agent", "JukaHub")
		resp, err := c.client().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, errWebdavNotLAN) {
				// The dialer refused before anything was sent: no retry.
				return nil, errWebdavNotLAN
			}
			if r.once || attempt >= attempts {
				return nil, gdriveNetError(err)
			}
			if err := gdriveSleep(ctx, backoff); err != nil {
				return nil, err
			}
			backoff *= 2
			continue
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, webdavAnswerLimit))
		resp.Body.Close()
		he := &webdavHTTPError{Status: resp.StatusCode, Location: resp.Header.Get("Location")}
		if resp.StatusCode == http.StatusUnauthorized {
			auth := strings.ToLower(strings.Join(resp.Header.Values("WWW-Authenticate"), ","))
			he.Digest = strings.Contains(auth, "digest") && !strings.Contains(auth, "basic")
			if !he.Digest && c.refused.CompareAndSwap(false, true) && c.identity != "" {
				webdavNoteRefused(c.identity)
			}
			return nil, he
		}
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout
		if !retry || r.once || attempt >= attempts {
			return nil, he
		}
		wait := backoff
		if ra := gdriveRetryAfter(resp.Header.Get("Retry-After")); ra > 0 {
			wait = ra
		}
		if err := gdriveSleep(ctx, wait); err != nil {
			return nil, err
		}
		backoff *= 2
	}
}

// ── Listing ─────────────────────────────────────────────────────────────────

// webdavPropfindBody asks for the four properties a listing needs.
const webdavPropfindBody = `<?xml version="1.0" encoding="utf-8"?>` + "\n" +
	`<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:getcontentlength/><d:getetag/><d:getlastmodified/></d:prop></d:propfind>`

// webdavItem is one entry of a listing, relative to the folder it was asked
// about ("" is that folder itself).
type webdavItem struct {
	Rel     string
	Dir     bool
	Size    int64
	ETag    string
	LastMod string
}

// The PROPFIND answer (RFC 4918 multistatus), matched by the DAV: namespace
// whatever prefix the server uses.
type webdavMultistatus struct {
	XMLName   xml.Name         `xml:"DAV: multistatus"`
	Responses []webdavResponse `xml:"DAV: response"`
}

type webdavResponse struct {
	Href     string           `xml:"DAV: href"`
	Propstat []webdavPropstat `xml:"DAV: propstat"`
}

type webdavPropstat struct {
	Prop   webdavProp `xml:"DAV: prop"`
	Status string     `xml:"DAV: status"`
}

type webdavProp struct {
	ResourceType struct {
		Collection *struct{} `xml:"DAV: collection"`
	} `xml:"DAV: resourcetype"`
	ContentLength string `xml:"DAV: getcontentlength"`
	ETag          string `xml:"DAV: getetag"`
	LastModified  string `xml:"DAV: getlastmodified"`
}

// webdavRelOf turns a PROPFIND href into the path below ref ("" for ref
// itself), refusing an href outside ref and any ".", ".." or empty name. The
// host is not looked at: servers behind a proxy name themselves oddly, and
// JukaHub only ever requests addresses it builds from ref.
func webdavRelOf(ref *url.URL, href string) (rel string, dir bool, ok bool) {
	h, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", false, false
	}
	hp := h.EscapedPath()
	hs, okH := webdavSegments(hp)
	rs, okR := webdavSegments(ref.EscapedPath())
	if !okH || !okR || len(hs) < len(rs) {
		return "", false, false
	}
	for i := range rs {
		if hs[i] != rs[i] {
			return "", false, false
		}
	}
	rest := hs[len(rs):]
	for _, seg := range rest {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, "/\\\x00") {
			return "", false, false
		}
	}
	return strings.Join(rest, "/"), strings.HasSuffix(hp, "/"), true
}

// webdavStatusOK reports whether a propstat's "HTTP/1.1 200 OK" is a 200.
func webdavStatusOK(s string) bool {
	f := strings.Fields(s)
	return len(f) >= 2 && f[1] == "200"
}

// webdavCleanETag trims an ETag as a server sent it.
func webdavCleanETag(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return ""
	}
	return s
}

// webdavSameETag compares two ETags of one server, weak or strong.
func webdavSameETag(a, b string) bool {
	norm := func(s string) string { return strings.Trim(strings.TrimPrefix(strings.TrimSpace(s), "W/"), `"`) }
	return norm(a) == norm(b)
}

// webdavStrongETag reports whether an ETag may go into If-Match, which
// compares strongly: a weak one would never match.
func webdavStrongETag(s string) bool {
	return s != "" && !strings.HasPrefix(s, "W/")
}

// webdavIfMatch is a strong ETag as If-Match carries it: quoted. A few
// servers list getetag bare, and a server that parses If-Match as RFC 9110
// says would never match a bare one — every upload refused as "changed".
func webdavIfMatch(s string) string {
	if strings.HasPrefix(s, `"`) {
		return s
	}
	return `"` + s + `"`
}

// webdavParseMultistatus reads a PROPFIND answer (outside data) into entries
// relative to ref. Entries outside ref, with a broken size or without a 200
// propstat are left out.
func webdavParseMultistatus(body []byte, ref *url.URL) ([]webdavItem, error) {
	var ms webdavMultistatus
	if err := xml.NewDecoder(bytes.NewReader(body)).Decode(&ms); err != nil {
		return nil, errors.New("the WebDAV server sent a folder list this version cannot read — is this a WebDAV address?")
	}
	if len(ms.Responses) > webdavMaxItems {
		return nil, errSsyncTooMany
	}
	out := make([]webdavItem, 0, len(ms.Responses))
	for _, r := range ms.Responses {
		rel, dirHref, ok := webdavRelOf(ref, r.Href)
		if !ok {
			continue
		}
		var prop *webdavProp
		for i := range r.Propstat {
			if webdavStatusOK(r.Propstat[i].Status) {
				prop = &r.Propstat[i].Prop
				break
			}
		}
		if prop == nil {
			continue
		}
		it := webdavItem{Rel: rel, Dir: prop.ResourceType.Collection != nil || dirHref, ETag: webdavCleanETag(prop.ETag)}
		if lm := strings.TrimSpace(prop.LastModified); len(lm) <= 100 {
			it.LastMod = lm
		}
		if !it.Dir {
			n, err := strconv.ParseInt(strings.TrimSpace(prop.ContentLength), 10, 64)
			if err != nil || n < 0 {
				continue
			}
			it.Size = n
		}
		out = append(out, it)
	}
	return out, nil
}

// propfind lists target (depth "0", "1" or "infinity") relative to ref.
func (c *webdavConn) propfind(ctx context.Context, target string, ref *url.URL, depth string) ([]webdavItem, error) {
	hdr := http.Header{}
	hdr.Set("Depth", depth)
	hdr.Set("Content-Type", "application/xml; charset=utf-8")
	resp, err := c.send(ctx, webdavReq{
		method: "PROPFIND",
		url:    target,
		body:   func() (io.Reader, error) { return strings.NewReader(webdavPropfindBody), nil },
		size:   int64(len(webdavPropfindBody)),
		header: hdr,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMultiStatus {
		// A web page answering 200 is not a WebDAV folder.
		return nil, &webdavHTTPError{Status: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, webdavListLimit+1))
	if err != nil {
		return nil, gdriveNetError(err)
	}
	if len(data) > webdavListLimit {
		return nil, errors.New("the WebDAV folder list is larger than one sync can read")
	}
	return webdavParseMultistatus(data, ref)
}

// ── Files ───────────────────────────────────────────────────────────────────

// mkcol makes one folder; one that is already there (405) is fine.
func (c *webdavConn) mkcol(ctx context.Context, target string) error {
	resp, err := c.send(ctx, webdavReq{method: "MKCOL", url: target})
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, webdavAnswerLimit))
		resp.Body.Close()
		return nil
	}
	if webdavStatusIs(err, http.StatusMethodNotAllowed) {
		return nil
	}
	return err
}

// get opens a file's bytes.
func (c *webdavConn) get(ctx context.Context, target string) (*http.Response, error) {
	return c.send(ctx, webdavReq{method: http.MethodGet, url: target})
}

// put sends size bytes from body (once: it is a stream) and returns the
// server's ETag for what it stored ("" when it sends none).
func (c *webdavConn) put(ctx context.Context, target string, body io.Reader, size int64, hdr http.Header) (string, error) {
	if hdr == nil {
		hdr = http.Header{}
	}
	hdr.Set("Content-Type", "application/octet-stream")
	resp, err := c.send(ctx, webdavReq{
		method: http.MethodPut,
		url:    target,
		body:   func() (io.Reader, error) { return body, nil },
		size:   size,
		header: hdr,
		once:   true,
	})
	if err != nil {
		return "", err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, webdavAnswerLimit))
	resp.Body.Close()
	return webdavCleanETag(resp.Header.Get("ETag")), nil
}

// stat lists one file (depth 0): its size, ETag and Last-Modified as the
// server reports them now.
func (c *webdavConn) stat(ctx context.Context, target string) (webdavItem, error) {
	ref, err := url.Parse(target)
	if err != nil {
		return webdavItem{}, err
	}
	items, err := c.propfind(ctx, target, ref, "0")
	if err != nil {
		return webdavItem{}, err
	}
	for _, it := range items {
		if it.Rel == "" && !it.Dir {
			return it, nil
		}
	}
	return webdavItem{}, errors.New("the WebDAV server did not describe the file")
}

// webdavCheck asks the server for the folder at the connection's address
// (depth 0), as the phone page does before saving: rejected for a refused
// password, a folder that is not there or an address that is not WebDAV;
// unchecked when the server cannot be reached right now.
func webdavCheck(ctx context.Context, c *webdavConn) (groqKeyVerdict, string) {
	items, err := c.propfind(ctx, c.base.String(), c.base, "0")
	switch {
	case err == nil:
		for _, it := range items {
			if it.Rel == "" {
				if !it.Dir {
					return groqKeyRejected, "That address is a file, not a folder — give the folder's WebDAV address"
				}
				return groqKeyAccepted, ""
			}
		}
		return groqKeyRejected, "The server answered, but not about that folder — check the address"
	case webdavStatusIs(err, http.StatusUnauthorized):
		var he *webdavHTTPError
		if errors.As(err, &he) && he.Digest {
			return groqKeyRejected, he.Error()
		}
		return groqKeyRejected, "The server refused the user name or password — with two-step sign-in, make an app password for JukaHub"
	case webdavStatusIs(err, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusOK, http.StatusNotImplemented, http.StatusBadRequest):
		return groqKeyRejected, "No WebDAV folder answered at that address — Nextcloud's looks like https://SERVER/remote.php/dav/files/USER/"
	case errors.Is(err, errWebdavNotLAN):
		return groqKeyRejected, "That name leads outside your home network, so http:// is not allowed — use https://"
	}
	var he *webdavHTTPError
	if errors.As(err, &he) {
		return groqKeyRejected, "The server said: " + he.Error()
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "certificate has expired or is not yet valid"):
		return groqKeyUnchecked, ""
	case strings.Contains(msg, "x509:"), strings.Contains(msg, "certificate"):
		return groqKeyRejected, "The server's certificate is not one this device trusts (self-signed?) — a server at home can use its http:// address"
	case strings.Contains(msg, "cannot read"):
		return groqKeyRejected, "That address answered, but not as a WebDAV folder — copy the WebDAV address the server shows"
	}
	return groqKeyUnchecked, ""
}

// webdavErrorText is a sync error as a sentence for a toast.
func webdavErrorText(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the WebDAV server did not answer in time"
	case errors.Is(err, context.Canceled):
		return "stopped"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "certificate has expired or is not yet valid"):
		return "this device's clock looks wrong — connect to Wi-Fi so it can set the time, then try again"
	case strings.Contains(msg, "x509:"):
		return "the server's certificate is not one this device trusts"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "network is unreachable"):
		return "the WebDAV server cannot be reached — check the network"
	case strings.Contains(msg, "connection refused"):
		return "nothing answers at the WebDAV address"
	}
	return firstLine(msg)
}

// webdavHashBody reads at most limit+1 bytes of r and returns their SHA-256
// and count.
func webdavHashBody(r io.Reader, limit int64) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, limit+1))
	if err != nil {
		return "", n, gdriveNetError(err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

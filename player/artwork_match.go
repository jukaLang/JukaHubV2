package main

// artwork_match.go — which cover on the thumbnail server belongs to a ROM, and
// getting it onto the card.
//
// The first fetcher only guessed names: the ROM's file name, then the same name
// with its tags, subtitle or article dropped, each asked for in turn. The server
// names a cover after the No-Intro title ("Super Mario World (USA)",
// "Legend of Zelda, The - A Link to the Past (USA)"), so most cards — ROMs
// called "Super Mario World.sfc", "Pokemon Emerald (U).gba" or
// "The Legend of Zelda - A Link to the Past.smc" — got no covers at all, and the
// month-long not-found cache then kept them that way. Half of the eight guesses
// were also spent on an underscored twin of each name that the server does not
// use, while the substitution it does use (& * / : ` < > ? \ | become "_") was
// never made, so "Sonic & Knuckles" could not match even when exact.
//
// Now the fetcher reads the server's list of covers for a console once (one
// directory page, cached on the card for two weeks) and matches the ROM against
// it: the exact name, then the title with tags, articles, "and"/"&",
// punctuation and Roman numerals folded, then the same with spaces and filler
// words ("version", "edition") dropped, then a title whose subtitle the ROM's
// name left out when only one game carries it. When several releases share the
// title, the ROM's own tags (region, disc, revision) pick the release; without
// any, USA, World, Europe and Japan win in that order and betas or demos lose.
// A title the list does not know gets one more chance through the game
// database's CRC match (gmeta_dat.go) when that database is already on the card.
// When the server gives no usable list, the old name guesses still run.
//
// Two more reasons covers never arrived:
//
//   - The handheld's firmware may ship no certificate bundle, and the fetcher
//     used Go's default client, so every request failed verification and the
//     automatic pass backed off for ever. It now uses the fallback bundle the
//     other downloads use (bugrepRootCAs) and, when the server still cannot be
//     verified, fetches these public pictures over plain http as RetroArch's
//     own thumbnail downloader does. Only the thumbnail server is asked that way.
//   - Covers already on the card were ignored: the stock firmware and CrossMix
//     keep scraped covers in Roms/<console>/Imgs, MinUI in .res. A game with one
//     is shown with it and is not looked up (romCoverPath).

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"html"
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
	"unicode"
)

const (
	// artworkIndexMaxBytes bounds one directory page. The biggest consoles list
	// a few tens of thousands of covers, a few megabytes of HTML.
	artworkIndexMaxBytes = 32 << 20
	// artworkIndexRefresh is how old the card's copy of a list may get before
	// it is fetched again; a stale copy is still used when that fails.
	artworkIndexRefresh = 14 * 24 * time.Hour
	// artworkIndexRetry holds a list that could not be fetched (a network error)
	// back for a while, so a batch of 200 games offline fails fast instead of
	// waiting out a timeout per game.
	artworkIndexRetry = 10 * time.Minute
	// artworkIndexMemory is how many consoles' lists stay in memory. A batch is
	// sorted by path, so it works through one console at a time.
	artworkIndexMemory = 3
	// artworkIndexDirName is the cache folder beside the covers, so deleting
	// data/game-art starts everything over.
	artworkIndexDirName = ".server-index"
	// artworkIndexTimeout and artworkImageTimeout bound one request each (the
	// client itself has no overall timeout: a list is much bigger than a cover).
	artworkIndexTimeout = 2 * time.Minute
	artworkImageTimeout = 30 * time.Second
	// artworkImageMaxBytes bounds one cover.
	artworkImageMaxBytes = 8 << 20
)

// errArtworkTooBig is a response larger than the caller allowed.
var errArtworkTooBig = errors.New("response is larger than expected")

// ── The connection ────────────────────────────────────────────────────────

var artworkClient struct {
	once   sync.Once
	client *http.Client
}

// artworkHTTPClient is the client every cover request uses: keep-alives on (a
// fetch is hundreds of small requests to one host) and the firmware CA
// fallback (bugrepRootCAs) when the system store is empty.
func artworkHTTPClient() *http.Client {
	artworkClient.once.Do(func() {
		tr := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConns:          2,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       60 * time.Second,
		}
		if pool := bugrepRootCAs(); pool != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		artworkClient.client = &http.Client{Transport: tr}
	})
	return artworkClient.client
}

// artworkPlainHTTP is set once https to the thumbnail server failed
// certificate verification; later requests go straight to plain http.
var artworkPlainHTTP atomic.Bool

// artworkCertError reports a failure to verify the server's certificate, as
// opposed to a network that is down.
func artworkCertError(err error) bool {
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var host x509.HostnameError
	var roots x509.SystemRootsError
	var verify *tls.CertificateVerificationError
	return errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &host) ||
		errors.As(err, &roots) || errors.As(err, &verify)
}

// artworkPlainBase is the plain-http twin of the libretro thumbnail server's
// https address. Any other address (a test server, a mirror) has none: only
// public box art is ever asked for without TLS.
func artworkPlainBase(base string) (string, bool) {
	const secure = "https://thumbnails.libretro.com"
	if !strings.EqualFold(base, secure) {
		return "", false
	}
	return "http://thumbnails.libretro.com", true
}

// artworkGet fetches base+rel. It returns the status and, for a 200, the body
// (at most limit bytes; more is errArtworkTooBig). A certificate failure on the
// thumbnail server switches this run of the app to plain http (see the file
// comment) and retries once.
func artworkGet(ctx context.Context, client *http.Client, base, rel string, limit int64, timeout time.Duration) (int, []byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = artworkHTTPClient()
	}
	try := func(root string) (int, []byte, error) {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		req, err := http.NewRequestWithContext(rctx, http.MethodGet, root+rel, nil)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("User-Agent", "JukaHub")
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			// Drained (a little) so the connection can be reused.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			return resp.StatusCode, nil, nil
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if err != nil {
			return resp.StatusCode, nil, err
		}
		if int64(len(data)) > limit {
			return resp.StatusCode, nil, errArtworkTooBig
		}
		return resp.StatusCode, data, nil
	}
	base = strings.TrimRight(base, "/")
	plain, canDowngrade := artworkPlainBase(base)
	if canDowngrade && artworkPlainHTTP.Load() {
		return try(plain)
	}
	status, data, err := try(base)
	if err != nil && canDowngrade && artworkCertError(err) {
		if artworkPlainHTTP.CompareAndSwap(false, true) {
			logWarn("art", "cannot verify %s (%v); fetching box art over plain http from now on", base, err)
		}
		return try(plain)
	}
	return status, data, err
}

// artworkIsPNG reports whether data starts like a PNG file.
func artworkIsPNG(data []byte) bool {
	return len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n"
}

// artworkServerName applies the thumbnail server's file-name rule: the
// characters & * / : ` < > ? \ | are written as "_".
func artworkServerName(name string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '&', '*', '/', ':', '`', '<', '>', '?', '\\', '|':
			return '_'
		}
		return r
	}, name)
}

// artworkDownloadCover fetches one cover by its exact server name. A 404, or
// something that is not a PNG of sane size, is os.ErrNotExist: a real "not
// found". Anything else that went wrong is returned as is, so the caller does
// not file a network hiccup as a missing cover for a month.
func artworkDownloadCover(ctx context.Context, client *http.Client, base, collection, name string) ([]byte, error) {
	rel := "/" + url.PathEscape(collection) + "/Named_Boxarts/" + url.PathEscape(name) + ".png"
	status, data, err := artworkGet(ctx, client, base, rel, artworkImageMaxBytes, artworkImageTimeout)
	switch {
	case errors.Is(err, errArtworkTooBig):
		return nil, os.ErrNotExist
	case err != nil:
		return nil, err
	case status == http.StatusNotFound:
		return nil, os.ErrNotExist
	case status != http.StatusOK:
		// A 5xx or a throttling response is not a missing cover.
		return nil, fmt.Errorf("thumbnail server returned %d", status)
	case !artworkIsPNG(data):
		return nil, os.ErrNotExist
	}
	return data, nil
}

// fetchROMArtworkImage asks for the names artworkTitleVariants guesses from a
// ROM's file name, best first. It is the fallback for a console whose cover
// list could not be read.
func fetchROMArtworkImage(ctx context.Context, client *http.Client, baseURL, collection, title string) ([]byte, error) {
	base := strings.TrimRight(baseURL, "/")
	if base == "" || collection == "" || strings.TrimSpace(title) == "" {
		return nil, os.ErrNotExist
	}
	for _, variant := range artworkTitleVariants(title) {
		data, err := artworkDownloadCover(ctx, client, base, collection, variant)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, os.ErrNotExist
}

// fetchROMArtworkFor finds and downloads one ROM's cover: matched against the
// console's cover list when the server gives one, else by name guesses.
// os.ErrNotExist means the server has no cover for it.
func fetchROMArtworkFor(ctx context.Context, client *http.Client, baseURL string, rom scannedROM, stem string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		client = artworkHTTPClient()
	}
	base := strings.TrimRight(baseURL, "/")
	if base == "" || strings.TrimSpace(rom.Collection) == "" || strings.TrimSpace(stem) == "" {
		return nil, os.ErrNotExist
	}
	idx, err := artworkIndexFor(ctx, client, base, rom.Collection)
	if err != nil {
		return nil, err
	}
	if idx == nil {
		return fetchROMArtworkImage(ctx, client, base, rom.Collection, stem)
	}
	name, ok := idx.match(stem)
	if !ok {
		if known := artworkDatabaseName(ctx, rom, stem); known != "" {
			name, ok = idx.match(known)
		}
	}
	if !ok {
		// A stop during the database look must not be filed as "not found"
		// for a month.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, os.ErrNotExist
	}
	logDebug("art", "%s: cover %q", stem, name)
	data, err := artworkDownloadCover(ctx, client, base, rom.Collection, name)
	if errors.Is(err, os.ErrNotExist) {
		// The list named a cover the server no longer has: the list is out of
		// date. Not a "not found" to remember for a month; the next fetch reads
		// a fresh list.
		artworkForgetIndex(base, rom.Collection)
		return nil, fmt.Errorf("cover %q is listed but missing; the list is refreshed", name)
	}
	return data, err
}

// artworkDatabaseName is the No-Intro title the game database gives a ROM by
// its CRC, "" when that database is not on the card or does not know the ROM.
// It never downloads anything: the library's game page fetches the database.
func artworkDatabaseName(ctx context.Context, rom scannedROM, stem string) string {
	if ctx == nil || ctx.Err() != nil {
		return ""
	}
	idx, res := gmetaIndexFor(ctx, rom.Collection, false)
	if idx == nil || res != gmetaFetchOK {
		return ""
	}
	info, ok := gmetaLookup(idx, gmetaROMCRCs(rom.Path), stem)
	if !ok {
		return ""
	}
	return strings.TrimSpace(info.Name)
}

// ── The server's list ─────────────────────────────────────────────────────

// artworkIndex is one console's cover names with the keys they are matched by.
type artworkIndex struct {
	names  []string
	keys   []string // strict key per name
	exact  map[string]int
	strict map[string][]int
	loose  map[string][]int
	head   map[string][]int
}

func newArtworkIndex(names []string) *artworkIndex {
	idx := &artworkIndex{
		names:  names,
		keys:   make([]string, len(names)),
		exact:  make(map[string]int, len(names)),
		strict: make(map[string][]int, len(names)),
		loose:  make(map[string][]int, len(names)),
		head:   make(map[string][]int),
	}
	for i, name := range names {
		lower := strings.ToLower(name)
		if _, ok := idx.exact[lower]; !ok {
			idx.exact[lower] = i
		}
		tokens := artworkMatchTokens(name)
		if len(tokens) == 0 {
			continue
		}
		key := strings.Join(tokens, " ")
		idx.keys[i] = key
		idx.strict[key] = append(idx.strict[key], i)
		if loose := artworkLooseKey(tokens); loose != "" {
			idx.loose[loose] = append(idx.loose[loose], i)
		}
		if head := artworkHeadKey(name); head != "" && head != key {
			idx.head[head] = append(idx.head[head], i)
		}
	}
	return idx
}

// match picks the cover for a ROM stem (or a database title), "" and false
// when no step below finds one. Each step is stricter than the next, and the
// first that finds candidates decides.
func (idx *artworkIndex) match(stem string) (string, bool) {
	stem = strings.TrimSpace(stem)
	if idx == nil || len(idx.names) == 0 || stem == "" {
		return "", false
	}
	if i, ok := idx.exact[strings.ToLower(artworkServerName(stem))]; ok {
		return idx.names[i], true
	}
	tags := artworkTags(stem)
	tokens := artworkMatchTokens(stem)
	if len(tokens) == 0 {
		return "", false
	}
	key := strings.Join(tokens, " ")
	if cands := idx.strict[key]; len(cands) > 0 {
		return idx.best(cands, tags), true
	}
	if alias, ok := artworkAliasFor(stem); ok {
		if cands := idx.strict[strings.Join(artworkMatchTokens(alias), " ")]; len(cands) > 0 {
			return idx.best(cands, tags), true
		}
	}
	if cands := idx.loose[artworkLooseKey(tokens)]; len(cands) > 0 {
		return idx.best(cands, tags), true
	}
	// "Zelda II" for "Zelda II - The Adventure of Link": only for a numbered
	// title (a bare "Metroid" must not become "Metroid - Zero Mission"), and
	// only when every cover with that head is the same game.
	if cands := idx.head[key]; len(cands) > 0 && artworkNumbered(tokens) && idx.oneTitle(cands) {
		return idx.best(cands, tags), true
	}
	return "", false
}

// artworkNumbered reports a title whose last word is a number (Roman numerals
// are numbers by then): "Zelda 2", "Final Fantasy 7".
func artworkNumbered(tokens []string) bool {
	if len(tokens) < 2 {
		return false
	}
	last := tokens[len(tokens)-1]
	for _, r := range last {
		if r < '0' || r > '9' {
			return false
		}
	}
	return last != ""
}

// oneTitle reports whether the candidates are releases of one game.
func (idx *artworkIndex) oneTitle(cands []int) bool {
	for _, i := range cands[1:] {
		if idx.keys[i] != idx.keys[cands[0]] {
			return false
		}
	}
	return true
}

// best ranks releases of one title by the ROM's tags (artworkCandidateScore);
// a tie goes to the shorter, then the alphabetically first name, so the choice
// does not depend on the order of the server's page.
func (idx *artworkIndex) best(cands []int, romTags []string) string {
	bestI, bestScore := -1, 0
	for _, i := range cands {
		score := artworkCandidateScore(romTags, idx.names[i])
		if bestI < 0 || score > bestScore || (score == bestScore && artworkNameBefore(idx.names[i], idx.names[bestI])) {
			bestI, bestScore = i, score
		}
	}
	return idx.names[bestI]
}

func artworkNameBefore(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// artworkDropWords are left out of every key: "The" moves between the front
// and ", The" in No-Intro titles, and "and" is "&" on one side and "_" (the
// server's rule) on the other.
var artworkDropWords = map[string]bool{"the": true, "and": true}

// artworkLooseDropWords are also left out of the loose key: "Pokemon Emerald"
// is "Pokemon - Emerald Version" on the server.
var artworkLooseDropWords = map[string]bool{"a": true, "an": true, "version": true, "edition": true}

// artworkMatchTokens reduces a title to comparable words: no tags, no accents,
// lower case, apostrophes dropped, other punctuation a space, the drop words
// gone and Roman numerals II–XX (not the one-letter V and X) written as
// numbers.
func artworkMatchTokens(name string) []string {
	s := strings.ToLower(artworkFoldDiacritics(stripArtworkTagGroups(name)))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\'' || r == '’' || r == '‘':
			continue
		case r == '&':
			b.WriteString(" and ")
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	out := words[:0]
	for _, w := range words {
		if artworkDropWords[w] {
			continue
		}
		// One-letter numerals stay letters: "Mega Man X" is not "Mega Man 10".
		if v, ok := artworkRomanValues[strings.ToUpper(w)]; ok && len(w) > 1 {
			w = strconv.Itoa(v)
		}
		out = append(out, w)
	}
	return out
}

// artworkLooseKey is the tokens without the loose drop words and without
// spaces, so "Megaman" and "Mega Man" meet.
func artworkLooseKey(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		if !artworkLooseDropWords[t] {
			b.WriteString(t)
		}
	}
	return b.String()
}

// artworkHeadKey is the strict key of a title's part before its subtitle
// (" - ", ": " or " ~ "), "" when it has none.
func artworkHeadKey(name string) string {
	s := stripArtworkTagGroups(name)
	cut := -1
	for _, sep := range []string{" - ", ": ", " ~ "} {
		if i := strings.Index(s, sep); i > 0 && (cut < 0 || i < cut) {
			cut = i
		}
	}
	if cut <= 0 {
		return ""
	}
	return strings.Join(artworkMatchTokens(s[:cut]), " ")
}

// artworkGoodToolsTags maps the one-letter region codes of GoodTools-named
// ROMs ("(U)", "(E)", "(J)", "(UE)") to No-Intro's words.
var artworkGoodToolsTags = map[string][]string{
	"u": {"usa"}, "e": {"europe"}, "j": {"japan"}, "w": {"world"},
	"ue": {"usa", "europe"}, "eu": {"europe", "usa"}, "ju": {"japan", "usa"},
	"uj": {"usa", "japan"}, "je": {"japan", "europe"}, "jue": {"japan", "usa", "europe"},
	"f": {"france"}, "g": {"germany"}, "s": {"spain"}, "i": {"italy"}, "k": {"korea"},
	"b": {"brazil"}, "a": {"australia"},
}

// artworkTags lists a name's bracketed tags, lower case, one per
// comma-separated part: "Game (USA, Europe) (Rev 1)" → usa, europe, rev 1.
func artworkTags(name string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	flush := func() {
		for _, part := range strings.Split(cur.String(), ",") {
			t := strings.Join(strings.Fields(strings.ToLower(part)), " ")
			if t == "" || t == "!" {
				continue
			}
			if mapped, ok := artworkGoodToolsTags[t]; ok {
				out = append(out, mapped...)
				continue
			}
			out = append(out, t)
		}
		cur.Reset()
	}
	for _, r := range name {
		switch r {
		case '(', '[':
			if depth == 0 {
				cur.Reset()
			} else {
				cur.WriteRune(r)
			}
			depth++
			continue
		case ')', ']':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 {
				flush()
			} else {
				cur.WriteRune(r)
			}
			continue
		}
		if depth > 0 {
			cur.WriteRune(r)
		}
	}
	return out
}

// artworkJunkTag reports a tag no one wants as the cover of a plain ROM.
func artworkJunkTag(t string) bool {
	for _, p := range []string{"beta", "proto", "demo", "sample", "kiosk", "unl", "pirate", "hack", "aftermarket"} {
		if t == p || strings.HasPrefix(t, p+" ") {
			return true
		}
	}
	return false
}

// artworkCandidateScore ranks one release for a ROM: every tag the two share
// counts most (region, disc, revision, language); after that USA, World,
// Europe, Japan are preferred in that order; another disc of a multi-disc game,
// and betas, demos and the like the ROM is not, lose; fewer tags win ties.
func artworkCandidateScore(romTags []string, candidate string) int {
	candTags := artworkTags(candidate)
	has := make(map[string]bool, len(candTags))
	for _, t := range candTags {
		has[t] = true
	}
	rom := make(map[string]bool, len(romTags))
	romDisc := false
	score := -len(candTags)
	for _, t := range romTags {
		rom[t] = true
		if strings.HasPrefix(t, "disc ") {
			romDisc = true
		}
		if has[t] {
			score += 20
		}
	}
	// Also with a region of its own: a "(USA)" ROM whose title the server only
	// has as Europe and Japan should get the European box, not the shorter name.
	switch {
	case has["usa"]:
		score += 6
	case has["world"]:
		score += 5
	case has["europe"]:
		score += 4
	case has["japan"]:
		score++
	}
	for _, t := range candTags {
		if rom[t] {
			continue
		}
		switch {
		case strings.HasPrefix(t, "disc "):
			if romDisc {
				score -= 50
			} else if t != "disc 1" {
				score -= 3
			}
		case artworkJunkTag(t):
			score -= 30
		}
	}
	return score
}

// parseArtworkListing reads the cover names (without ".png") off a directory
// page. Links are read from href, not from the text, which a server shortens.
func parseArtworkListing(page []byte) []string {
	s := string(page)
	seen := make(map[string]bool)
	var out []string
	for {
		i := strings.Index(s, "href=")
		if i < 0 {
			break
		}
		s = s[i+len("href="):]
		if s == "" {
			break
		}
		var raw string
		if q := s[0]; q == '"' || q == '\'' {
			end := strings.IndexByte(s[1:], q)
			if end < 0 {
				break
			}
			raw, s = s[1:1+end], s[1+end:]
		} else {
			end := strings.IndexAny(s, " >")
			if end < 0 {
				break
			}
			raw, s = s[:end], s[end:]
		}
		raw = html.UnescapeString(raw)
		if j := strings.IndexAny(raw, "?#"); j >= 0 {
			raw = raw[:j]
		}
		raw = raw[strings.LastIndexByte(raw, '/')+1:]
		name, err := url.PathUnescape(raw)
		if err != nil || len(name) <= len(".png") || !strings.EqualFold(name[len(name)-4:], ".png") {
			continue
		}
		name = name[:len(name)-4]
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\n\r") || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

var artworkIndexes struct {
	sync.Mutex
	byKey  map[string]*artworkIndex // nil value: the server gave no usable list
	order  []string
	failed map[string]artworkIndexFailure
}

type artworkIndexFailure struct {
	at  time.Time
	err error
}

// artworkIndexDir is where the lists are kept, "" when the app has no data
// folder. A variable so a test can point it elsewhere.
var artworkIndexDir = func() string {
	dir := romArtworkDir()
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, artworkIndexDirName)
}

// artworkIndexCachePath is one console's list file, "" with no data folder.
func artworkIndexCachePath(collection string) string {
	dir := artworkIndexDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, artworkServerName(strings.TrimSpace(collection))+".txt")
}

// artworkIndexFor returns one console's cover list. (nil, nil) means the
// server gave no usable list, and the caller guesses names instead; an error
// is a network failure, which the caller reports as no answer.
func artworkIndexFor(ctx context.Context, client *http.Client, base, collection string) (*artworkIndex, error) {
	key := base + "|" + collection
	now := time.Now()
	artworkIndexes.Lock()
	if idx, ok := artworkIndexes.byKey[key]; ok {
		artworkIndexes.Unlock()
		return idx, nil
	}
	if f, ok := artworkIndexes.failed[key]; ok {
		if age := now.Sub(f.at); age >= 0 && age < artworkIndexRetry {
			artworkIndexes.Unlock()
			return nil, f.err
		}
	}
	artworkIndexes.Unlock()

	idx, err := loadArtworkIndex(ctx, client, base, collection)

	artworkIndexes.Lock()
	defer artworkIndexes.Unlock()
	if err != nil {
		// A stop from the user is not a server problem worth remembering.
		if ctx.Err() == nil {
			if artworkIndexes.failed == nil {
				artworkIndexes.failed = make(map[string]artworkIndexFailure)
			}
			artworkIndexes.failed[key] = artworkIndexFailure{at: time.Now(), err: err}
		}
		return nil, err
	}
	delete(artworkIndexes.failed, key)
	if artworkIndexes.byKey == nil {
		artworkIndexes.byKey = make(map[string]*artworkIndex)
	}
	if _, ok := artworkIndexes.byKey[key]; !ok {
		artworkIndexes.order = append(artworkIndexes.order, key)
	}
	artworkIndexes.byKey[key] = idx
	for len(artworkIndexes.order) > artworkIndexMemory {
		delete(artworkIndexes.byKey, artworkIndexes.order[0])
		artworkIndexes.order = artworkIndexes.order[1:]
	}
	return idx, nil
}

// artworkForgetIndex drops a console's list from memory and from the card.
func artworkForgetIndex(base, collection string) {
	key := base + "|" + collection
	artworkIndexes.Lock()
	delete(artworkIndexes.byKey, key)
	kept := artworkIndexes.order[:0]
	for _, k := range artworkIndexes.order {
		if k != key {
			kept = append(kept, k)
		}
	}
	artworkIndexes.order = kept
	artworkIndexes.Unlock()
	if cache := artworkIndexCachePath(collection); cache != "" {
		_ = os.Remove(cache)
	}
}

// loadArtworkIndex reads the card's copy of a list while it is fresh, else
// fetches it (keeping the stale copy when that fails).
func loadArtworkIndex(ctx context.Context, client *http.Client, base, collection string) (*artworkIndex, error) {
	cache := artworkIndexCachePath(collection)
	var stale []string
	if cache != "" {
		if info, err := os.Stat(cache); err == nil {
			if data, rerr := os.ReadFile(cache); rerr == nil {
				names := artworkIndexLines(data)
				// The handheld has no clock battery: a copy dated in the future
				// has no usable age and is refreshed.
				age := time.Since(info.ModTime())
				if len(names) > 0 && age >= 0 && age < artworkIndexRefresh {
					return newArtworkIndex(names), nil
				}
				if len(names) > 0 {
					stale = names
				}
			}
		}
	}
	rel := "/" + url.PathEscape(collection) + "/Named_Boxarts/"
	status, page, err := artworkGet(ctx, client, base, rel, artworkIndexMaxBytes, artworkIndexTimeout)
	if err == nil && status == http.StatusOK {
		if names := parseArtworkListing(page); len(names) > 0 {
			if cache != "" {
				if werr := gmetaWriteCache(cache, []byte(strings.Join(names, "\n")+"\n")); werr != nil {
					logWarn("art", "cannot keep the cover list for %s: %v", collection, werr)
				}
			}
			logInfo("art", "cover list for %s: %d covers", collection, len(names))
			return newArtworkIndex(names), nil
		}
	}
	if stale != nil {
		return newArtworkIndex(stale), nil
	}
	switch {
	case err != nil && !errors.Is(err, errArtworkTooBig):
		return nil, err
	case err == nil && (status >= 500 || status == http.StatusTooManyRequests):
		return nil, fmt.Errorf("thumbnail server returned %d for the %s list", status, collection)
	}
	// A 403 or 404 for the folder, an empty page, or one too big to read: no
	// list, so guess names one by one as before.
	logInfo("art", "no cover list for %s (status %d); asking for names one by one", collection, status)
	return nil, nil
}

// artworkIndexLines splits a cached list into names.
func artworkIndexLines(data []byte) []string {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// ── Covers already on the card ────────────────────────────────────────────

// romLocalCoverPath is a cover another front end left beside the ROM: the
// stock firmware's and CrossMix's Imgs folder (<console>/Imgs/<name>.png, or
// for a game kept in its own folder <console>/Imgs/<folder>.png) and MinUI's
// .res folder (.res/<file>.png). "" when there is none.
func romLocalCoverPath(rom scannedROM) string {
	file := filepath.Base(rom.Path)
	stem := strings.TrimSuffix(file, filepath.Ext(file))
	if strings.TrimSpace(stem) == "" || stem == "." || stem == string(filepath.Separator) {
		return ""
	}
	dir := filepath.Dir(rom.Path)
	candidates := []string{
		filepath.Join(dir, "Imgs", stem+".png"),
		filepath.Join(dir, ".res", file+".png"),
	}
	// A game in its own folder: the console folder's Imgs names it after the
	// folder. Only for a folder that is one game (the ROM is named after it,
	// or is a disc sheet): a folder of many ROMs ("Hacks") has a picture of
	// its own that is no game's cover. Never for a ROM straight in the console
	// folder, whose parent's Imgs would hold the console's own picture.
	_, isConsole := romSystemAliases[strings.ToLower(filepath.Base(dir))]
	switch ext := strings.ToLower(filepath.Ext(file)); {
	case isConsole:
	case strings.EqualFold(stem, filepath.Base(dir)), ext == ".cue", ext == ".m3u", ext == ".gdi":
		if parent := filepath.Dir(dir); parent != dir {
			candidates = append(candidates, filepath.Join(parent, "Imgs", filepath.Base(dir)+".png"))
		}
	}
	for _, path := range candidates {
		if artworkLocalPNG(path) {
			return path
		}
	}
	return ""
}

// artworkLocalPNG reports a regular file of sane size that starts like a PNG.
func artworkLocalPNG(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 8 || info.Size() > artworkImageMaxBytes {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 8)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return artworkIsPNG(head)
}

// romCoverPath is the picture to show for a ROM: the downloaded cover when
// there is one, else one already on the card (romLocalCoverPath), else the
// download path, so a fetch that fills it later shows up on the next recount.
func romCoverPath(rom scannedROM, root string) string {
	art := romArtworkPath(rom, root)
	if artworkCoverPresent(art) {
		return art
	}
	if local := romLocalCoverPath(rom); local != "" {
		return local
	}
	return art
}

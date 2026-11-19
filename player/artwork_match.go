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
	// artworkIndexMemory is how many consoles' lists stay in memory. A batch
	// takes the consoles in turns of artworkOrderChunk games (artworkFetchOrder)
	// and several downloads run at once, so a few lists are in use together;
	// a game with no box art also reads its sister consoles' lists and the
	// title-screen and in-game picture lists (fetchROMArtworkFor).
	artworkIndexMemory = 10
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
			MaxIdleConns:          artworkFetchWorkers,
			MaxIdleConnsPerHost:   artworkFetchWorkers,
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

// artworkStripReleaseNumber drops the release-list number scene sets put in
// front of a title ("0381 - 007 - From Russia with Love", "1617_-_Ben 10"):
// three to five digits, then a dash or dot. It reports false, and returns name
// unchanged, when there is no such number or nothing would be left after it.
// Callers try the name as it is first: "1943 - The Battle of Midway" is a title.
func artworkStripReleaseNumber(name string) (string, bool) {
	s := strings.TrimSpace(name)
	digits := 0
	for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	if digits < 3 || digits > 5 {
		return name, false
	}
	i := digits
	for i < len(s) && (s[i] == ' ' || s[i] == '_') {
		i++
	}
	if i >= len(s) || (s[i] != '-' && s[i] != '.') {
		return name, false
	}
	i++
	for i < len(s) && (s[i] == ' ' || s[i] == '_') {
		i++
	}
	rest := strings.TrimSpace(s[i:])
	if !strings.ContainsFunc(rest, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return name, false
	}
	return rest, true
}

// artworkDownloadCover fetches one cover by its exact server name. A 404, or
// something that is not a PNG of sane size, is os.ErrNotExist: a real "not
// found". Anything else that went wrong is returned as is, so the caller does
// not file a network hiccup as a missing cover for a month.
func artworkDownloadCover(ctx context.Context, client *http.Client, base, collection, name string) ([]byte, error) {
	return artworkDownloadFrom(ctx, client, base, collection, artworkBoxarts, name)
}

// The thumbnail server's three picture folders per console. Box art is the
// cover; a title screen, then an in-game picture, stand in for a game the
// box-art folder has nothing for (homebrew, hacks, rare releases): some
// picture beats none.
const (
	artworkBoxarts = "Named_Boxarts"
	artworkTitles  = "Named_Titles"
	artworkSnaps   = "Named_Snaps"
)

// artworkDownloadFrom is artworkDownloadCover for one of the picture folders.
func artworkDownloadFrom(ctx context.Context, client *http.Client, base, collection, kind, name string) ([]byte, error) {
	rel := "/" + url.PathEscape(collection) + "/" + kind + "/" + url.PathEscape(name) + ".png"
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
	// A name the user gave this game's covers (Game options → Name for covers,
	// artwork_choose.go) replaces the file's name in every step below, and the
	// game database's name is not asked: the user said what the game is.
	lookup := artworkLookupName(stem)
	userNamed := lookup != stem
	stem = lookup
	// "0381 - 007 - From Russia with Love": a release-list number in front of
	// the title (PSP and DS ISO sets) is no part of any cover's name. The name
	// as it is goes first, so "1943 - The Battle of Midway" stays a title.
	bare, numbered := artworkStripReleaseNumber(stem)
	idx, err := artworkIndexFor(ctx, client, base, rom.Collection)
	if err != nil {
		return nil, err
	}
	if idx == nil {
		data, err := fetchROMArtworkImage(ctx, client, base, rom.Collection, stem)
		if numbered && errors.Is(err, os.ErrNotExist) {
			data, err = fetchROMArtworkImage(ctx, client, base, rom.Collection, bare)
		}
		return data, err
	}
	// The database's No-Intro name, looked up once and only when the file's
	// own name finds nothing exact.
	known, knownDone := "", userNamed
	databaseName := func() string {
		if !knownDone {
			knownDone = true
			look := stem
			if numbered {
				look = bare
			}
			known = artworkDatabaseName(ctx, rom, look)
		}
		return known
	}
	// exact runs the exact steps on one list: the file's name, the name
	// without its release number, then the database's.
	exact := func(list *artworkIndex) (string, bool) {
		name, ok := list.match(stem)
		if !ok && numbered {
			name, ok = list.match(bare)
		}
		if !ok {
			if db := databaseName(); db != "" {
				name, ok = list.match(db)
			}
		}
		return name, ok
	}
	// closest is the "some picture beats none" step on one list, by the
	// database's name first (it is the cleaner one), else the file's.
	closest := func(list *artworkIndex) (string, bool) {
		var name string
		ok := false
		if db := databaseName(); db != "" {
			name, ok = list.closest(db)
		}
		if !ok && numbered {
			name, ok = list.closest(bare)
		}
		if !ok {
			name, ok = list.closest(stem)
		}
		return name, ok
	}

	// otherErr is the first failure to read another list or picture: when
	// nothing is found, the ROM is then "no answer", not a miss to remember.
	var otherErr error
	other := func(collections, kinds []string, pick func(*artworkIndex) (string, bool)) []byte {
		data, err := artworkFromOtherLists(ctx, client, base, stem, collections, kinds, pick)
		if err != nil && !errors.Is(err, os.ErrNotExist) && otherErr == nil {
			otherErr = err
		}
		return data
	}
	siblings := artworkSiblingCollections(rom.Collection)
	pictures := []string{artworkTitles, artworkSnaps}

	name, ok := exact(idx)
	if !ok {
		// The same game is often filed under a sister console (a Game Boy
		// game under Game Boy Color, a Mega Drive one under Sega CD): an
		// exact title there beats a near one here.
		if data := other(siblings, []string{artworkBoxarts}, exact); data != nil {
			return data, nil
		}
		name, ok = closest(idx)
		if ok {
			logDebug("art", "%s: no exact cover, closest is %q", stem, name)
		}
	}
	if !ok {
		// A near title on a sister console, then the console's title-screen
		// and in-game pictures: more pictures, fewer blank cards.
		if data := other(siblings, []string{artworkBoxarts}, closest); data != nil {
			return data, nil
		}
		if data := other([]string{rom.Collection}, pictures, exact); data != nil {
			return data, nil
		}
		if data := other([]string{rom.Collection}, pictures, closest); data != nil {
			return data, nil
		}
		// A stop during the database look must not be filed as "not found"
		// for a month.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if otherErr != nil {
			return nil, otherErr
		}
		return nil, os.ErrNotExist
	}
	logDebug("art", "%s: cover %q", stem, name)
	data, err := artworkDownloadCover(ctx, client, base, rom.Collection, name)
	if errors.Is(err, os.ErrNotExist) {
		if idx.recent(time.Now()) {
			// A list this new still names it: the server lists a cover it does
			// not give. The names guessed from the file are the last try, and
			// then it is a real "not found" — fetching the list again would
			// only name the same file (artworkIndexTrustFor).
			logDebug("art", "%s: listed cover %q is not served; guessing names", stem, name)
			data, err = fetchROMArtworkImage(ctx, client, base, rom.Collection, stem)
			if numbered && errors.Is(err, os.ErrNotExist) {
				data, err = fetchROMArtworkImage(ctx, client, base, rom.Collection, bare)
			}
			return data, err
		}
		// The list named a cover the server no longer has: the list is out of
		// date. Not a "not found" to remember for a month; the next fetch reads
		// a fresh list.
		artworkForgetIndex(base, rom.Collection, artworkBoxarts, idx)
		return nil, fmt.Errorf("cover %q is listed but missing; the list is refreshed", name)
	}
	return data, err
}

// artworkFromOtherLists looks a ROM up with pick in each of the given
// consoles' lists of each picture folder, in order, and downloads the first
// picture found. os.ErrNotExist means none had one; any other error (a list
// or a picture that could not be read) is returned, so the caller does not
// file a network hiccup as a miss.
func artworkFromOtherLists(ctx context.Context, client *http.Client, base, stem string, collections, kinds []string, pick func(*artworkIndex) (string, bool)) ([]byte, error) {
	var firstErr error
	for _, kind := range kinds {
		for _, coll := range collections {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			list, err := artworkIndexOf(ctx, client, base, coll, kind)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if list == nil {
				continue
			}
			name, ok := pick(list)
			if !ok {
				continue
			}
			data, err := artworkDownloadFrom(ctx, client, base, coll, kind, name)
			if err == nil {
				logDebug("art", "%s: picture %q from %s/%s", stem, name, coll, kind)
				return data, nil
			}
			if !errors.Is(err, os.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, os.ErrNotExist
}

// artworkSiblingCollections are the consoles whose covers also fit a game of
// collection: the same games are often filed under either (Game Boy and Game
// Boy Color, Master System and Game Gear) or the machine plays the other's
// games. Nil for a console with none.
func artworkSiblingCollections(collection string) []string {
	return artworkSiblings[strings.TrimSpace(collection)]
}

var artworkSiblings = map[string][]string{
	"Nintendo - Game Boy":                            {"Nintendo - Game Boy Color"},
	"Nintendo - Game Boy Color":                      {"Nintendo - Game Boy"},
	"Nintendo - Nintendo Entertainment System":       {"Nintendo - Family Computer Disk System"},
	"Sega - Master System - Mark III":                {"Sega - Game Gear"},
	"Sega - Game Gear":                               {"Sega - Master System - Mark III"},
	"Sega - Mega Drive - Genesis":                    {"Sega - Mega-CD - Sega CD", "Sega - 32X"},
	"Sega - Mega-CD - Sega CD":                       {"Sega - Mega Drive - Genesis"},
	"Sega - 32X":                                     {"Sega - Mega Drive - Genesis"},
	"SNK - Neo Geo Pocket":                           {"SNK - Neo Geo Pocket Color"},
	"SNK - Neo Geo Pocket Color":                     {"SNK - Neo Geo Pocket"},
	"SNK - Neo Geo":                                  {"SNK - Neo Geo CD", "FBNeo - Arcade Games"},
	"SNK - Neo Geo CD":                               {"SNK - Neo Geo"},
	"MAME":                                           {"FBNeo - Arcade Games"},
	"Bandai - WonderSwan":                            {"Bandai - WonderSwan Color"},
	"Bandai - WonderSwan Color":                      {"Bandai - WonderSwan"},
	"NEC - PC Engine - TurboGrafx 16":                {"NEC - PC Engine CD - TurboGrafx-CD", "NEC - PC Engine SuperGrafx"},
	"Atari - 5200":                                   {"Atari - 2600"},
	"Atari - 7800":                                   {"Atari - 2600"},
	"Nintendo - GameCube":                            {"Nintendo - Wii"},
	"Nintendo - Wii":                                 {"Nintendo - GameCube"},
	"Microsoft - Xbox":                               {"Microsoft - Xbox 360"},
	"Microsoft - Xbox 360":                           {"Microsoft - Xbox"},
	"Nintendo - Super Nintendo Entertainment System": {"Nintendo - Satellaview"},
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

	// byWord finds the titles sharing a word with a ROM, for the closest
	// title (artwork_closest.go); built on first use.
	closeOnce sync.Once
	byWord    map[string][]int

	// fetched is when this list came from the server (the card copy's date
	// for a fresh copy); zero for a stale copy kept because a fetch failed.
	fetched time.Time
}

// artworkIndexTrustFor is how long a list read from the server is believed
// when it names a cover the server then does not give (a link to a file that
// is gone, a file that is no PNG). Before, each such game threw the list away
// and fetched it again, and the next game did the same: a tester's Atari Lynx
// batch read its 434-cover list over and over and left 23 games unanswered
// for every later pass (report JH-SQRMFB, 2026-11-15).
const artworkIndexTrustFor = 24 * time.Hour

// recent reports whether the list came from the server within
// artworkIndexTrustFor of now.
func (idx *artworkIndex) recent(now time.Time) bool {
	if idx == nil || idx.fetched.IsZero() {
		return false
	}
	age := now.Sub(idx.fetched)
	return age >= 0 && age < artworkIndexTrustFor
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
	// loading holds the lists being read right now; a second download for the
	// same console waits on the channel instead of fetching the list again.
	loading map[string]chan struct{}
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
	return artworkIndexCachePathOf(collection, artworkBoxarts)
}

// artworkIndexCachePathOf is artworkIndexCachePath for one of the picture
// folders: the box-art list keeps its old name, the others get a suffix.
func artworkIndexCachePathOf(collection, kind string) string {
	dir := artworkIndexDir()
	if dir == "" {
		return ""
	}
	name := artworkServerName(strings.TrimSpace(collection))
	switch kind {
	case artworkTitles:
		name += ".titles"
	case artworkSnaps:
		name += ".snaps"
	}
	return filepath.Join(dir, name+".txt")
}

// artworkIndexFor returns one console's cover list. (nil, nil) means the
// server gave no usable list, and the caller guesses names instead; an error
// is a network failure, which the caller reports as no answer.
func artworkIndexFor(ctx context.Context, client *http.Client, base, collection string) (*artworkIndex, error) {
	return artworkIndexOf(ctx, client, base, collection, artworkBoxarts)
}

// artworkIndexKey names one list in memory: server, console and folder.
func artworkIndexKey(base, collection, kind string) string {
	if kind == artworkBoxarts {
		return base + "|" + collection
	}
	return base + "|" + collection + "|" + kind
}

// artworkIndexOf is artworkIndexFor for one of the picture folders.
func artworkIndexOf(ctx context.Context, client *http.Client, base, collection, kind string) (*artworkIndex, error) {
	key := artworkIndexKey(base, collection, kind)
	if ctx == nil {
		ctx = context.Background()
	}
	var done chan struct{}
	for {
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
		wait, busy := artworkIndexes.loading[key]
		if !busy {
			if artworkIndexes.loading == nil {
				artworkIndexes.loading = make(map[string]chan struct{})
			}
			done = make(chan struct{})
			artworkIndexes.loading[key] = done
			artworkIndexes.Unlock()
			break
		}
		artworkIndexes.Unlock()
		// Another download is reading this list: wait for it and look again
		// (its result, or its failure, is then in the maps above).
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	idx, err := loadArtworkIndex(ctx, client, base, collection, kind)

	artworkIndexes.Lock()
	defer artworkIndexes.Unlock()
	delete(artworkIndexes.loading, key)
	close(done)
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
// stale is the list the caller found out of date: when another download has
// already put a newer one in its place (several run at once), that one is
// kept, in memory and on the card.
func artworkForgetIndex(base, collection, kind string, stale *artworkIndex) {
	key := artworkIndexKey(base, collection, kind)
	cache := artworkIndexCachePathOf(collection, kind)
	artworkIndexes.Lock()
	if cur, ok := artworkIndexes.byKey[key]; ok && cur != stale {
		artworkIndexes.Unlock()
		return
	}
	if _, busy := artworkIndexes.loading[key]; busy {
		// A fresh list is being read right now.
		artworkIndexes.Unlock()
		return
	}
	if _, inMemory := artworkIndexes.byKey[key]; !inMemory && stale != nil && cache != "" {
		// Gone from memory already (only a few lists stay there): a card copy
		// written after this list was read is a newer one.
		if info, err := os.Stat(cache); err == nil && info.ModTime().After(stale.fetched) {
			artworkIndexes.Unlock()
			return
		}
	}
	delete(artworkIndexes.byKey, key)
	kept := artworkIndexes.order[:0]
	for _, k := range artworkIndexes.order {
		if k != key {
			kept = append(kept, k)
		}
	}
	artworkIndexes.order = kept
	artworkIndexes.Unlock()
	if cache != "" {
		_ = os.Remove(cache)
	}
}

// loadArtworkIndex reads the card's copy of a list while it is fresh, else
// fetches it (keeping the stale copy when that fails).
func loadArtworkIndex(ctx context.Context, client *http.Client, base, collection, kind string) (*artworkIndex, error) {
	cache := artworkIndexCachePathOf(collection, kind)
	var stale []string
	if cache != "" {
		if info, err := os.Stat(cache); err == nil {
			if data, rerr := os.ReadFile(cache); rerr == nil {
				names := artworkIndexLines(data)
				// The handheld has no clock battery: a copy dated in the future
				// has no usable age and is refreshed.
				age := time.Since(info.ModTime())
				if len(names) > 0 && age >= 0 && age < artworkIndexRefresh {
					idx := newArtworkIndex(names)
					idx.fetched = info.ModTime()
					return idx, nil
				}
				if len(names) > 0 {
					stale = names
				}
			}
		}
	}
	rel := "/" + url.PathEscape(collection) + "/" + kind + "/"
	status, page, err := artworkGet(ctx, client, base, rel, artworkIndexMaxBytes, artworkIndexTimeout)
	if err == nil && status == http.StatusOK {
		if names := parseArtworkListing(page); len(names) > 0 {
			if cache != "" {
				if werr := gmetaWriteCache(cache, []byte(strings.Join(names, "\n")+"\n")); werr != nil {
					logWarn("art", "cannot keep the cover list for %s: %v", collection, werr)
				}
			}
			logInfo("art", "%s list for %s: %d pictures", kind, collection, len(names))
			idx := newArtworkIndex(names)
			idx.fetched = time.Now()
			return idx, nil
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
// for a game kept in its own folder <console>/Imgs/<folder>.png), MinUI's
// .res folder (.res/<file>.png) and NextUI's / NX Redux's .media folder
// (.media/<name>.png). Each place is read as .png first, then .jpg/.jpeg,
// then .webp when this build decodes WebP (NX Redux 2.5 art packs ship
// .webp). "" when there is none.
func romLocalCoverPath(rom scannedROM) string {
	file := filepath.Base(rom.Path)
	stem := strings.TrimSuffix(file, filepath.Ext(file))
	if strings.TrimSpace(stem) == "" || stem == "." || stem == string(filepath.Separator) {
		return ""
	}
	dir := filepath.Dir(rom.Path)
	// Extension-less bases: artworkLocalCover adds each image extension.
	candidates := []string{
		filepath.Join(dir, "Imgs", stem),
		filepath.Join(dir, ".res", file),
		filepath.Join(dir, ".media", stem),
	}
	// A game in its own folder: the console folder's Imgs names it after the
	// folder. Only for a folder that is one game (the ROM is named after it,
	// or is a disc sheet): a folder of many ROMs ("Hacks") has a picture of
	// its own that is no game's cover. Never for a ROM straight in the console
	// folder, whose parent's Imgs would hold the console's own picture.
	// NextUI's "Name (TAG)" folders are console folders too: their parent's
	// .media holds the console's icon, never a game's cover.
	_, isConsole := romSystemAliases[strings.ToLower(filepath.Base(dir))]
	if !isConsole {
		_, _, isConsole = nextuiFolderTag(filepath.Base(dir))
		isConsole = isConsole && strings.EqualFold(filepath.Base(filepath.Dir(dir)), "Roms")
	}
	switch ext := strings.ToLower(filepath.Ext(file)); {
	case isConsole:
	case strings.EqualFold(stem, filepath.Base(dir)), ext == ".cue", ext == ".m3u", ext == ".gdi":
		if parent := filepath.Dir(dir); parent != dir {
			candidates = append(candidates,
				filepath.Join(parent, "Imgs", filepath.Base(dir)),
				filepath.Join(parent, ".media", filepath.Base(dir)))
		}
	}
	for _, base := range candidates {
		if path := artworkLocalCover(base); path != "" {
			return path
		}
	}
	return ""
}

// artworkLocalCoverExts are the picture types a cover on the card may be, in
// the order they are preferred, matched without regard to case (.PNG, .Jpg).
// ".webp" is only used when webpDecodable says the loaded SDL_image reads it,
// so a cover never shows up as a broken tile.
var artworkLocalCoverExts = []string{".png", ".jpg", ".jpeg", ".webp"}

// artworkLocalCover is base+ext for the first extension whose file is a
// real picture of sane size, or "". The art folder is listed once (cached),
// so a ROM with no cover costs a map lookup instead of a stat per extension,
// and the extension's case does not matter on a case-sensitive card.
func artworkLocalCover(base string) string {
	names := artworkDirNames(filepath.Dir(base))
	if len(names) == 0 {
		return ""
	}
	stem := strings.ToLower(filepath.Base(base))
	for _, ext := range artworkLocalCoverExts {
		onDisk, ok := names[stem+ext]
		if !ok {
			continue
		}
		path := filepath.Join(filepath.Dir(base), onDisk)
		switch ext {
		case ".webp":
			// SDL_image's, or a converted copy from the card's ffmpeg (webpdecode.go).
			if cover := artworkWebPCover(path); cover != "" {
				return cover
			}
		case ".jpg", ".jpeg":
			if artworkLocalImage(path, artworkIsJPEG) {
				return path
			}
		default:
			if artworkLocalImage(path, artworkIsPNG) {
				return path
			}
		}
	}
	return ""
}

// artworkDirCache remembers for a short while what an art folder holds: a
// library scan asks once per ROM, and a card of 3,000 ROMs in a dozen console
// folders would otherwise stat the same missing .media folder thousands of
// times. The TTL keeps a folder the user just copied over from being ignored
// for long. The scan runs off the render thread, so every touch is locked.
var artworkDirCache struct {
	sync.Mutex
	seen map[string]artworkDirSeen
}

type artworkDirSeen struct {
	names map[string]string // lower-case file name -> name on disk
	at    time.Time
}

const (
	artworkDirCacheTTL = 30 * time.Second
	// artworkDirCacheMax bounds how many folders are remembered; past it the
	// cache starts over rather than growing with every folder ever scanned.
	artworkDirCacheMax = 512
)

// artworkDirNames lists dir's files by lower-case name, cached for
// artworkDirCacheTTL. A missing or unreadable folder is cached as empty.
func artworkDirNames(dir string) map[string]string {
	now := time.Now()
	artworkDirCache.Lock()
	if s, ok := artworkDirCache.seen[dir]; ok && now.Sub(s.at) < artworkDirCacheTTL && now.Sub(s.at) >= 0 {
		artworkDirCache.Unlock()
		return s.names
	}
	artworkDirCache.Unlock()
	var names map[string]string
	if entries, err := os.ReadDir(dir); err == nil {
		names = make(map[string]string, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			lower := strings.ToLower(e.Name())
			if _, dup := names[lower]; !dup {
				names[lower] = e.Name()
			}
		}
	}
	artworkDirCache.Lock()
	if artworkDirCache.seen == nil || len(artworkDirCache.seen) >= artworkDirCacheMax {
		artworkDirCache.seen = make(map[string]artworkDirSeen)
	}
	artworkDirCache.seen[dir] = artworkDirSeen{names: names, at: now}
	artworkDirCache.Unlock()
	return names
}

// artworkIsJPEG reports JPEG's start-of-image marker.
func artworkIsJPEG(data []byte) bool {
	return len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF
}

// artworkIsWebP reports a RIFF container of type WEBP.
func artworkIsWebP(data []byte) bool {
	return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

// artworkLocalImage reports a regular file of sane size whose first bytes
// pass magic.
func artworkLocalImage(path string, magic func([]byte) bool) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 12 || info.Size() > artworkImageMaxBytes {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 12)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return magic(head)
}

// artworkLocalPNG reports a regular file of sane size that starts like a PNG.
// Test seam (ARCH-8): only tests call it.
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
// On NextUI the card's own cover goes before the downloaded one.
func romCoverPath(rom scannedROM, root string) string {
	// Game options → Remove cover: no picture at all (artwork_nocover.go).
	if artworkNoCover(rom, root) {
		return ""
	}
	// On NextUI the cover the user put in .media (or MinUI's .res) comes
	// first: it is the one NextUI shows.
	if nextuiActive() {
		if local := romLocalCoverPath(rom); local != "" {
			return local
		}
	}
	art := romArtworkPath(rom, root)
	if artworkCoverPresent(art) {
		return art
	}
	if local := romLocalCoverPath(rom); local != "" {
		return local
	}
	// Last, a picture taken while the game played (playshot_cover.go): any
	// cover above replaces it as soon as there is one.
	if shot := playShotCoverPath(rom, root); shot != "" {
		return shot
	}
	return art
}

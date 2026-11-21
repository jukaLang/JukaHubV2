package main

// artwork_screenscraper.go — box art and game details from ScreenScraper, for
// a user who signed in.
//
// ScreenScraper (screenscraper.fr) identifies a game by its checksum, size,
// console and file name, and answers with its box art and details. It asks two
// sets of credentials: the developer's (devid, devpassword, softname: given
// to a front end by ScreenScraper's staff, stamped into a release build) and
// the user's (a free account, which carries the user's own daily quota). So:
//
//   - The developer's: -ldflags "-X main.screenScraperDevID=… -X
//     main.screenScraperDevPassword=…" (build.bat, release.sh and the
//     handheld build read JUKAHUB_SS_DEVID and JUKAHUB_SS_DEVPASSWORD, the
//     way the Google and Microsoft clients are stamped in). A build without
//     them shows the account row as not available and never asks.
//   - The user's: Settings → Games & emulators → ScreenScraper account, the
//     user name on the shared prompt and the password on its own masked,
//     submit-only keyboard (artwork_screenscraper_ui.go). The account is
//     checked once (ssuserInfos.php) and stored in data/screenscraper.json,
//     mode 0600, like retroachievements.json; neither value is ever logged,
//     and a request's address (it carries both) is never logged either.
//
// When signed in, ScreenScraper is the first cover source
// (fetchROMCoverAllSources): jeuInfos.php with the ROM's CRC32 (the game's in
// a zip or 7z, gmetaROMCRCs), its size, the console (screenScraperSystems)
// and its file name. The answer's "box-2D" picture is taken in the region the
// file's name asks for (USA → us, Europe → eu …, then world, us, eu, the
// site's own, Japan), and its year, genre, developer, players and synopsis
// fill the game's details (gmeta_store.go) where they are empty — a
// description the user wrote is never replaced.
//
// One request at a time (screenScraperOne): a free account gets one thread.
// A 429 (threads), 430 (daily quota) or 431 (too many unknown games) backs
// off (screenScraperBackoff), a refused account or key stops ScreenScraper
// until the next sign-in or start; a game ScreenScraper does not know is not
// asked again for screenScraperMissTTL (.screenscraper-missing.json beside the
// covers). Meanwhile the other sources carry on.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The developer credentials, stamped in at build time (see the file comment).
var (
	screenScraperDevID       string
	screenScraperDevPassword string
	screenScraperSoftName    = "JukaHub"
)

const (
	// screenScraperAPI is the API's address.
	screenScraperAPI = "https://api.screenscraper.fr/api2/"
	// screenScraperTimeout bounds one request.
	screenScraperTimeout = 30 * time.Second
	// screenScraperMaxJSON bounds one answer.
	screenScraperMaxJSON = 2 << 20
	// screenScraperMissTTL is how long a game ScreenScraper does not know is
	// not asked again.
	screenScraperMissTTL = 30 * 24 * time.Hour
	// screenScraperAccountFile is the account, in JukaHub's data folder.
	screenScraperAccountFile = "screenscraper.json"
	// screenScraperFieldMax bounds a user name or a password.
	screenScraperFieldMax = 128
)

// screenScraperSystems names each libretro collection's ScreenScraper
// console id.
var screenScraperSystems = map[string]int{
	"Sega - Mega Drive - Genesis":                    1,
	"Sega - Master System - Mark III":                2,
	"Nintendo - Nintendo Entertainment System":       3,
	"Nintendo - Super Nintendo Entertainment System": 4,
	"Nintendo - Game Boy":                            9,
	"Nintendo - Game Boy Color":                      10,
	"Nintendo - Virtual Boy":                         11,
	"Nintendo - Game Boy Advance":                    12,
	"Nintendo - GameCube":                            13,
	"Nintendo - Nintendo 64":                         14,
	"Nintendo - Nintendo DS":                         15,
	"Nintendo - Wii":                                 16,
	"Nintendo - Nintendo 3DS":                        17,
	"Sega - 32X":                                     19,
	"Sega - Mega-CD - Sega CD":                       20,
	"Sega - Game Gear":                               21,
	"Sega - Saturn":                                  22,
	"Sega - Dreamcast":                               23,
	"SNK - Neo Geo Pocket":                           25,
	"Atari - 2600":                                   26,
	"Atari - Jaguar":                                 27,
	"Atari - Lynx":                                   28,
	"NEC - PC Engine - TurboGrafx 16":                31,
	"Microsoft - Xbox":                               32,
	"Microsoft - Xbox 360":                           33,
	"Atari - 5200":                                   40,
	"Atari - 7800":                                   41,
	"Bandai - WonderSwan":                            45,
	"Bandai - WonderSwan Color":                      46,
	"Coleco - ColecoVision":                          48,
	"Sony - PlayStation":                             57,
	"Sony - PlayStation 2":                           58,
	"Sony - PlayStation Portable":                    61,
	"SNK - Neo Geo CD":                               70,
	"MAME":                                           75,
	"FBNeo - Arcade Games":                           75,
	"SNK - Neo Geo Pocket Color":                     82,
	"GCE - Vectrex":                                  102,
	"Nintendo - Family Computer Disk System":         106,
	"Sega - SG-1000":                                 109,
	"NEC - PC Engine CD - TurboGrafx-CD":             114,
	"Mattel - Intellivision":                         115,
	"SNK - Neo Geo":                                  142,
	"Nintendo - Pokemon Mini":                        211,
}

// ── The account ─────────────────────────────────────────────────────────────

// screenScraperAccount is data/screenscraper.json.
type screenScraperAccount struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

var screenScraperAcct struct {
	sync.Mutex
	loaded bool
	acct   screenScraperAccount
}

// screenScraperAvailable reports a build with the developer credentials.
func screenScraperAvailable() bool {
	return strings.TrimSpace(screenScraperDevID) != "" && strings.TrimSpace(screenScraperDevPassword) != ""
}

func screenScraperAccountPath() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, screenScraperAccountFile)
}

// screenScraperLoadAccount is the stored account (empty when none). Any
// goroutine; the file is read once.
func screenScraperLoadAccount() screenScraperAccount {
	screenScraperAcct.Lock()
	defer screenScraperAcct.Unlock()
	if !screenScraperAcct.loaded {
		screenScraperAcct.loaded = true
		if path := screenScraperAccountPath(); path != "" {
			if raw, err := os.ReadFile(path); err == nil && len(raw) < 64<<10 {
				var a screenScraperAccount
				if json.Unmarshal(raw, &a) == nil {
					screenScraperAcct.acct = screenScraperAccount{User: strings.TrimSpace(a.User), Password: a.Password}
				} else {
					// Never the content: it holds the password.
					logWarn("art", "%s is not valid JSON; ScreenScraper is signed out", screenScraperAccountFile)
				}
			}
		}
	}
	return screenScraperAcct.acct
}

// screenScraperSaveAccount stores the account (an empty one signs out).
func screenScraperSaveAccount(a screenScraperAccount) error {
	path := screenScraperAccountPath()
	if path == "" {
		return errors.New("JukaHub's data folder is unavailable")
	}
	if a.User == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		raw, err := json.MarshalIndent(a, "", "  ")
		if err != nil {
			return err
		}
		if err := AtomicWrite(path, append(raw, '\n'), 0o600); err != nil {
			return err
		}
	}
	screenScraperAcct.Lock()
	screenScraperAcct.loaded, screenScraperAcct.acct = true, a
	screenScraperAcct.Unlock()
	screenScraperInfo.Lock()
	screenScraperInfo.blocked, screenScraperInfo.note = "", ""
	screenScraperInfo.Unlock()
	return nil
}

// screenScraperSignedIn is the user name when ScreenScraper can be asked.
func screenScraperSignedIn() (string, bool) {
	if !screenScraperAvailable() {
		return "", false
	}
	a := screenScraperLoadAccount()
	return a.User, a.User != "" && a.Password != ""
}

// ── Requests ────────────────────────────────────────────────────────────────

// screenScraperOne is held for a whole lookup: one thread per account.
var screenScraperOne sync.Mutex

// screenScraperInfo is what the account row shows and the back-off; its own
// lock, so the row never waits for a request. until is a monotonic deadline.
var screenScraperInfo struct {
	sync.Mutex
	until   time.Time
	blocked string // why ScreenScraper is not asked until the next sign-in
	note    string // the last answer worth showing
}

// screenScraperStatusNote is the account row's detail beyond the sign-in.
func screenScraperStatusNote() string {
	screenScraperInfo.Lock()
	defer screenScraperInfo.Unlock()
	if screenScraperInfo.blocked != "" {
		return screenScraperInfo.blocked
	}
	if time.Now().Before(screenScraperInfo.until) && screenScraperInfo.note != "" {
		return screenScraperInfo.note
	}
	return ""
}

// screenScraperHold reports why ScreenScraper is not asked now ("" when it
// may be).
func screenScraperHold() string {
	screenScraperInfo.Lock()
	defer screenScraperInfo.Unlock()
	if screenScraperInfo.blocked != "" {
		return screenScraperInfo.blocked
	}
	if time.Now().Before(screenScraperInfo.until) {
		return "backing off"
	}
	return ""
}

func screenScraperBackoff(d time.Duration, note string) {
	screenScraperInfo.Lock()
	screenScraperInfo.until = time.Now().Add(d)
	screenScraperInfo.note = note
	screenScraperInfo.Unlock()
	logInfo("art", "ScreenScraper: %s", note)
}

func screenScraperBlock(why string) {
	screenScraperInfo.Lock()
	first := screenScraperInfo.blocked == ""
	screenScraperInfo.blocked = why
	screenScraperInfo.Unlock()
	if first {
		logWarn("art", "ScreenScraper: %s", why)
	}
}

// errScreenScraperHeld is a lookup not made: backing off or refused.
var errScreenScraperHeld = errors.New("ScreenScraper is not asked now")

// screenScraperCall asks one API page. The address carries the credentials
// and is never logged. The status decides: 200 is the body, 404 (and 400, a
// name the API cannot take) os.ErrNotExist, a quota or a refusal backs off
// or blocks.
func screenScraperCall(ctx context.Context, page string, q url.Values, acct screenScraperAccount) ([]byte, error) {
	q.Set("devid", screenScraperDevID)
	q.Set("devpassword", screenScraperDevPassword)
	q.Set("softname", screenScraperSoftName)
	q.Set("output", "json")
	q.Set("ssid", acct.User)
	q.Set("sspassword", acct.Password)
	rctx, cancel := context.WithTimeout(ctx, screenScraperTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, screenScraperAPI+page+"?"+q.Encode(), nil)
	if err != nil {
		return nil, errors.New("ScreenScraper request could not be made")
	}
	req.Header.Set("User-Agent", "JukaHub")
	resp, err := artworkHTTPClient().Do(req)
	if err != nil {
		// A url.Error quotes the address: only its cause is kept.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return nil, uerr.Err
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, screenScraperMaxJSON+1))
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if len(body) > screenScraperMaxJSON {
			return nil, errArtworkTooBig
		}
		return body, nil
	case http.StatusNotFound, http.StatusBadRequest:
		return nil, os.ErrNotExist
	case 429:
		screenScraperBackoff(30*time.Second, "busy (one request at a time) · tries again shortly")
	case 430:
		screenScraperBackoff(6*time.Hour, "today's ScreenScraper quota is used up · tries again later")
	case 431:
		screenScraperBackoff(6*time.Hour, "too many unknown games today · tries again later")
	case http.StatusUnauthorized, 423:
		screenScraperBackoff(time.Hour, "ScreenScraper is closed to non-members for now · tries again later")
	case http.StatusForbidden:
		screenScraperBlock("ScreenScraper refused the sign-in · check the user name and password")
	case 426:
		screenScraperBlock("ScreenScraper does not accept this JukaHub build any more · update JukaHub")
	default:
		if resp.StatusCode >= 500 {
			screenScraperBackoff(5*time.Minute, "ScreenScraper is not answering · tries again later")
		}
	}
	return nil, fmt.Errorf("ScreenScraper returned %d", resp.StatusCode)
}

// screenScraperVerify checks an account before it is stored.
func screenScraperVerify(ctx context.Context, acct screenScraperAccount) error {
	if !screenScraperAvailable() {
		return errors.New("this build has no ScreenScraper key")
	}
	body, err := screenScraperCall(ctx, "ssuserInfos.php", url.Values{}, acct)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("ScreenScraper does not know this account")
		}
		return err
	}
	var doc struct {
		Response struct {
			User struct {
				ID string `json:"id"`
			} `json:"ssuser"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &doc) != nil || strings.TrimSpace(doc.Response.User.ID) == "" {
		return errors.New("ScreenScraper did not accept the user name and password")
	}
	return nil
}

// ── The answer ──────────────────────────────────────────────────────────────

type screenScraperText struct {
	Region string `json:"region"`
	Lang   string `json:"langue"`
	Text   string `json:"text"`
}

// screenScraperGame is the part of jeuInfos.php's "jeu" this reads. The
// fields whose shape the documentation leaves open are kept raw
// (screenScraperLoose), so one odd field never loses the whole answer.
type screenScraperGame struct {
	Synopsis []screenScraperText `json:"synopsis"`
	Dates    []screenScraperText `json:"dates"`
	Genres   []struct {
		Main  json.RawMessage     `json:"principale"`
		Names []screenScraperText `json:"noms"`
	} `json:"genres"`
	Developer json.RawMessage `json:"developpeur"`
	Players   json.RawMessage `json:"joueurs"`
	Medias    []struct {
		Type   string `json:"type"`
		URL    string `json:"url"`
		Region string `json:"region"`
		Format string `json:"format"`
	} `json:"medias"`
}

// screenScraperLoose reads a field that is a string, a number or an object
// with a "text": its text, "" for anything else.
func screenScraperLoose(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	var t screenScraperText
	if json.Unmarshal(raw, &t) == nil {
		return strings.TrimSpace(t.Text)
	}
	return ""
}

// screenScraperRegions maps a file name's region tags to ScreenScraper's
// region codes.
var screenScraperRegions = map[string]string{
	"usa": "us", "canada": "us", "europe": "eu", "uk": "uk", "japan": "jp", "world": "wor",
	"france": "fr", "germany": "de", "spain": "sp", "italy": "it", "korea": "kr",
	"brazil": "br", "australia": "au", "china": "cn", "netherlands": "nl",
}

// screenScraperRegionOrder is the regions to try for a file's tags.
func screenScraperRegionOrder(romTags []string) []string {
	var out []string
	add := func(r string) {
		for _, have := range out {
			if have == r {
				return
			}
		}
		out = append(out, r)
	}
	for _, t := range romTags {
		if r, ok := screenScraperRegions[strings.ToLower(t)]; ok {
			add(r)
		}
	}
	for _, r := range []string{"wor", "us", "eu", "ss", "jp"} {
		add(r)
	}
	return out
}

// screenScraperBoxURL is the game's front box in the best region, "" for
// none. Only pictures on ScreenScraper's own hosts are taken.
func screenScraperBoxURL(g *screenScraperGame, romTags []string) string {
	ok := func(raw string) bool {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" {
			return false
		}
		host := strings.ToLower(u.Hostname())
		return host == "screenscraper.fr" || strings.HasSuffix(host, ".screenscraper.fr")
	}
	for _, region := range screenScraperRegionOrder(romTags) {
		for _, m := range g.Medias {
			if m.Type == "box-2D" && strings.EqualFold(m.Region, region) && ok(m.URL) {
				return m.URL
			}
		}
	}
	for _, m := range g.Medias {
		if m.Type == "box-2D" && ok(m.URL) {
			return m.URL
		}
	}
	return ""
}

// screenScraperPick is the text in English, else the first.
func screenScraperPick(list []screenScraperText) string {
	for _, t := range list {
		if strings.EqualFold(t.Lang, "en") && strings.TrimSpace(t.Text) != "" {
			return strings.TrimSpace(t.Text)
		}
	}
	for _, t := range list {
		if strings.TrimSpace(t.Text) != "" {
			return strings.TrimSpace(t.Text)
		}
	}
	return ""
}

// screenScraperYear is the earliest four-digit year among the release dates.
func screenScraperYear(dates []screenScraperText) string {
	best := ""
	for _, d := range dates {
		s := strings.TrimSpace(d.Text)
		if len(s) < 4 {
			continue
		}
		y := s[:4]
		if _, err := strconv.Atoi(y); err != nil {
			continue
		}
		if best == "" || y < best {
			best = y
		}
	}
	return best
}

// screenScraperStoreDetails fills the game's empty details from the answer.
func screenScraperStoreDetails(rom scannedROM, g *screenScraperGame) {
	title := gameDisplayTitle(rom.Path)
	if title == "" || strings.TrimSpace(rom.System) == "" {
		return
	}
	genre := ""
	for _, gen := range g.Genres {
		main := screenScraperLoose(gen.Main) == "1"
		if name := screenScraperPick(gen.Names); name != "" && (genre == "" || main) {
			genre = name
			if main {
				break
			}
		}
	}
	year := screenScraperYear(g.Dates)
	dev := screenScraperLoose(g.Developer)
	players := screenScraperLoose(g.Players)
	synopsis := screenScraperPick(g.Synopsis)
	if genre == "" && year == "" && dev == "" && players == "" && synopsis == "" {
		return
	}
	gmetaUpdate(rom.System, title, func(r *gmetaRecord) {
		fill := func(to *string, from string) {
			if strings.TrimSpace(*to) == "" && from != "" {
				*to = from
			}
		}
		fill(&r.Genre, genre)
		fill(&r.Year, year)
		fill(&r.Developer, dev)
		fill(&r.Players, players)
		// The description is the user's: only an empty one is filled.
		fill(&r.Description, synopsis)
	})
}

// ── The cover source ────────────────────────────────────────────────────────

// screenScraperMisses is ScreenScraper's not-found cache.
var screenScraperMisses = &artworkSourceMisses{file: ".screenscraper-missing.json"}

// fetchScreenScraperCover asks ScreenScraper for a game when the user signed
// in: its front box, and its details on the way. os.ErrNotExist when it is
// not signed in, does not know the game or has no box for it; another error
// when it could not be asked (backing off, offline).
func fetchScreenScraperCover(ctx context.Context, rom scannedROM, stem string) ([]byte, error) {
	if _, ok := screenScraperSignedIn(); !ok {
		return nil, os.ErrNotExist
	}
	system := screenScraperSystems[strings.TrimSpace(rom.Collection)]
	info, err := os.Stat(rom.Path)
	if system == 0 || err != nil || !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	key := rom.Collection + "/" + filepath.Base(rom.Path)
	if screenScraperMisses.recent(key, screenScraperMissTTL) {
		return nil, os.ErrNotExist
	}
	if screenScraperHold() != "" {
		return nil, errScreenScraperHeld
	}
	q := url.Values{}
	q.Set("systemeid", strconv.Itoa(system))
	q.Set("romtype", "rom")
	q.Set("romnom", filepath.Base(rom.Path))
	q.Set("romtaille", strconv.FormatInt(info.Size(), 10))
	if crcs := gmetaROMCRCs(rom.Path); len(crcs) > 0 {
		q.Set("crc", crcs[0])
	}

	screenScraperOne.Lock()
	defer screenScraperOne.Unlock()
	if screenScraperHold() != "" {
		return nil, errScreenScraperHeld
	}
	acct := screenScraperLoadAccount()
	body, err := screenScraperCall(ctx, "jeuInfos.php", q, acct)
	if errors.Is(err, os.ErrNotExist) {
		screenScraperMisses.note(key, true)
		return nil, os.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		Response struct {
			Game *screenScraperGame `json:"jeu"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || doc.Response.Game == nil {
		return nil, errors.New("ScreenScraper's answer could not be read")
	}
	game := doc.Response.Game
	screenScraperStoreDetails(rom, game)
	boxURL := screenScraperBoxURL(game, artworkTags(stem))
	if boxURL == "" {
		screenScraperMisses.note(key, true)
		return nil, os.ErrNotExist
	}
	data, err := screenScraperPicture(ctx, boxURL)
	if err != nil {
		return nil, err
	}
	screenScraperMisses.note(key, false)
	logInfo("art", "%s: cover and details from ScreenScraper", stem)
	return data, nil
}

// screenScraperPicture downloads a media address from the answer, asked at
// most at the size the library draws. The address carries the credentials
// and is never logged.
func screenScraperPicture(ctx context.Context, raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, os.ErrNotExist
	}
	q := u.Query()
	edge := strconv.Itoa(libraryArtDecodeMaxEdge)
	q.Set("maxwidth", edge)
	q.Set("maxheight", edge)
	u.RawQuery = q.Encode()
	rctx, cancel := context.WithTimeout(ctx, screenScraperTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, os.ErrNotExist
	}
	req.Header.Set("User-Agent", "JukaHub")
	resp, err := artworkHTTPClient().Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return nil, uerr.Err
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode == http.StatusNotFound {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("ScreenScraper picture returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, artworkImageMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > artworkImageMaxBytes {
		return nil, os.ErrNotExist
	}
	return artworkAsCoverPNG(ctx, data)
}

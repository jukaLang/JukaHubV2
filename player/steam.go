package main

// steam.go — the Steam library behind Apps → Steam.
//
// Where the games come from:
//
//   - The Steam app on this computer (Windows, or a Linux desktop): its
//     libraryfolders.vdf and appmanifest_*.acf files list the installed games,
//     loginusers.vdf names the account signed in there, and localconfig.vdf
//     has each game's play time. No sign-in is needed for any of it.
//   - The account the user signed in with (steam_signin.go: Steam's own
//     OpenID page, opened on the phone or the PC's browser; JukaHub only ever
//     learns the 17-digit Steam ID, never a password). Its whole library is
//     read from the public profile's games page when Steam serves it without
//     a login, or — when the user also sent a Steam Web API key — from
//     IPlayerService/GetOwnedGames, which works for a private profile too.
//
// The merged list is kept in data/steam/library.json, so the screen opens on
// the last list at once and offline. Cover art is the capsule image Steam's
// CDN serves for every app (the Steam app's own cache first), saved in
// data/steam/art and drawn through the Apps launcher's icon cache.
//
// Starting a game is a steam:// link handed to the Steam app: rungameid for
// an installed game, install for one that is not. A handheld has no Steam, so
// there the press asks a paired PC (Nearby devices, peernet.go) to start it;
// the PC answers on /peer/v1/steam/run.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// steamFileVersion is the format of account.json and library.json.
	steamFileVersion = 1
	// steamMaxGames bounds a library read from disk or the web; the largest
	// real libraries hold a few thousand games.
	steamMaxGames = 20000
	// steamPageLimit bounds a downloaded profile page or API answer.
	steamPageLimit = 16 << 20
	// steamArtLimit bounds one downloaded cover.
	steamArtLimit = 1 << 20
	// steamAccountIDBase turns a 32-bit account id into a SteamID64.
	steamAccountIDBase = 76561197960265728
	// steamPeerRunPath is the paired-device call that starts a game on a PC.
	steamPeerRunPath = "/peer/v1/steam/run"
)

// steamGame is one game of the library.
type steamGame struct {
	AppID      int    `json:"appid"`
	Name       string `json:"name"`
	Installed  bool   `json:"installed,omitempty"`
	Playtime   int    `json:"playtime,omitempty"`   // minutes
	LastPlayed int64  `json:"lastPlayed,omitempty"` // Unix seconds
	Size       int64  `json:"size,omitempty"`       // bytes on disk here

	// Download state on this computer, from the app's manifest; not saved.
	DlDone   int64 `json:"-"`
	DlTotal  int64 `json:"-"`
	DlPaused bool  `json:"-"`

	// Where else it is: a Steam PC of the account (steam_remote.go) or a
	// paired JukaHub PC that has it installed or downloading; not saved.
	Remote steamRemoteState `json:"-"`
	// RemoteOnly marks a row only a remote PC listed (not in the account's
	// library); steamApplyRemote replaces those rows on every look.
	RemoteOnly bool `json:"-"`
}

// steamDownloading reports whether g is downloading (or updating) here.
func (g steamGame) steamDownloading() bool {
	return g.DlTotal > 0 && g.DlDone < g.DlTotal
}

// steamPercent is done/total as a whole percent, 0..100.
func steamPercent(done, total int64) int {
	if total <= 0 || done <= 0 {
		return 0
	}
	if done >= total {
		return 100
	}
	// Steam's byte counts come from outside data. Multiplying an int64 by
	// 100 can overflow for a large download and draw a negative progress bar.
	return min(99, int(float64(done)*100/float64(total)))
}

// steamAccountFile is account.json: who signed in and how. The Web API key is
// stored encrypted (EncryptAPIToken), like the Groq key.
type steamAccountFile struct {
	Version int    `json:"version"`
	SteamID string `json:"steamId,omitempty"`
	Persona string `json:"persona,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
	// Via is how the Steam ID was given: "openid" (Steam's sign-in page) or
	// "profile" (a profile link pasted on the phone).
	Via string `json:"via,omitempty"`
	// NoLocal is set by Sign out: the account the Steam app is signed in with
	// is not used by itself until the user picks it again.
	NoLocal bool `json:"noLocal,omitempty"`
	// The Steam app sign-in (steam_auth.go): both tokens encrypted, the
	// access token's expiry, and the account's login name.
	RefreshToken string `json:"refreshToken,omitempty"`
	AccessToken  string `json:"accessToken,omitempty"`
	AccessExp    int64  `json:"accessExp,omitempty"`
	AccountName  string `json:"accountName,omitempty"`
	// TokenLost is set when Steam stopped accepting the sign-in, so the
	// screen can ask for a new one.
	TokenLost bool      `json:"tokenLost,omitempty"`
	Updated   time.Time `json:"updated,omitempty"`
}

// steamLibraryFile is library.json, the last merged list.
type steamLibraryFile struct {
	Version int         `json:"version"`
	SteamID string      `json:"steamId,omitempty"`
	Persona string      `json:"persona,omitempty"`
	Updated time.Time   `json:"updated"`
	Games   []steamGame `json:"games"`
}

// steamAccount is the account the library is read for.
type steamAccount struct {
	SteamID string
	Persona string
	// Via is "qr" (the Steam app's QR sign-in), "openid", "profile", "app"
	// (the account of the Steam app on this computer) or "" when nobody is
	// signed in.
	Via    string
	HasKey bool
	// HasToken is set while a Steam app sign-in is stored; TokenLost when
	// Steam stopped accepting it.
	HasToken  bool
	TokenLost bool
}

// steamLocalUser is the account the Steam app on this computer is signed in
// with (loginusers.vdf).
type steamLocalUser struct {
	SteamID string
	Persona string
}

// steamDirOverride is a test seam for the data folder.
var steamDirOverride string

func steamDataDir() string {
	if steamDirOverride != "" {
		return steamDirOverride
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "steam")
	}
	return filepath.Join("data", "steam")
}

func steamAccountPath() string { return filepath.Join(steamDataDir(), "account.json") }
func steamLibraryPath() string { return filepath.Join(steamDataDir(), "library.json") }
func steamArtDir() string      { return filepath.Join(steamDataDir(), "art") }

// steamStateMu serialises every load-change-save of account.json and
// library.json: the phone's sign-in, the panel and a refresh run on
// different goroutines.
var steamStateMu sync.Mutex

// steamAccountGeneration changes when the selected account or its credentials
// change. A refresh started before sign-out must not restore its old library
// after sign-out removed the cache. Guarded by steamStateMu.
var steamAccountGeneration uint64

// steamValidID reports whether s is a SteamID64 of an individual account.
func steamValidID(s string) bool {
	if len(s) != 17 || !strings.HasPrefix(s, "7656119") {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func steamLoadAccountLocked() steamAccountFile {
	var a steamAccountFile
	if _, err := readJSONWithBackup(steamAccountPath(), &a); err != nil {
		return steamAccountFile{Version: steamFileVersion}
	}
	a.Version = steamFileVersion
	if !steamValidID(a.SteamID) {
		a.SteamID, a.Persona, a.Via = "", "", ""
		a.RefreshToken, a.AccessToken, a.AccessExp, a.AccountName = "", "", 0, ""
	}
	a.Persona = steamCleanText(a.Persona, 64)
	return a
}

func steamSaveAccountLocked(a steamAccountFile) error {
	if err := os.MkdirAll(steamDataDir(), 0o700); err != nil {
		return err
	}
	a.Version = steamFileVersion
	a.Updated = time.Now()
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the file can hold the Web API key (encrypted, but still).
	return AtomicWrite(steamAccountPath(), append(data, '\n'), 0o600)
}

// steamUpdateAccount changes account.json under the lock.
func steamUpdateAccount(change func(a *steamAccountFile)) error {
	steamStateMu.Lock()
	defer steamStateMu.Unlock()
	a := steamLoadAccountLocked()
	change(&a)
	if err := steamSaveAccountLocked(a); err != nil {
		return err
	}
	steamAccountGeneration++
	return nil
}

// steamDecryptKey returns the stored Web API key in the clear, or "".
func steamDecryptKey(stored string) string {
	stored = strings.TrimSpace(stored)
	if stored == "" {
		return ""
	}
	if !strings.HasPrefix(stored, "ENC:") {
		return stored
	}
	key, err := DecryptAPIToken(stored, "steam_api_key")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(key)
}

// steamCurrentAccount is the account the library belongs to and the Web API
// key, if one is saved: the signed-in account first, else the Steam app's own
// (unless the user signed out of it). Any goroutine; it reads files.
func steamCurrentAccount() (steamAccount, string) {
	account, key, _ := steamCurrentAccountSnapshot()
	return account, key
}

func steamCurrentAccountSnapshot() (steamAccount, string, uint64) {
	steamStateMu.Lock()
	a := steamLoadAccountLocked()
	generation := steamAccountGeneration
	steamStateMu.Unlock()
	key := steamDecryptKey(a.APIKey)
	if a.SteamID != "" {
		persona := a.Persona
		if persona == "" {
			persona = a.AccountName
		}
		return steamAccount{SteamID: a.SteamID, Persona: persona, Via: a.Via, HasKey: key != "",
			HasToken: a.RefreshToken != "", TokenLost: a.TokenLost}, key, generation
	}
	if !a.NoLocal {
		if u, ok := steamFindLocalUser(); ok {
			return steamAccount{SteamID: u.SteamID, Persona: u.Persona, Via: "app", HasKey: key != ""}, key, generation
		}
	}
	return steamAccount{HasKey: key != ""}, key, generation
}

// steamSignInAs saves the account a sign-in produced. A different account
// forgets the old one's name and cached library.
func steamSignInAs(id, via string) error {
	if !steamValidID(id) {
		return errors.New("that is not a Steam ID")
	}
	steamStateMu.Lock()
	defer steamStateMu.Unlock()
	a := steamLoadAccountLocked()
	if a.SteamID != id {
		a.Persona, a.AccountName = "", ""
		a.RefreshToken, a.AccessToken, a.AccessExp, a.TokenLost = "", "", 0, false
		_ = steamRemoveLibraryLocked()
	}
	a.SteamID, a.NoLocal = id, false
	// A Steam app sign-in of the same account stays the way it was made.
	if a.RefreshToken == "" {
		a.Via = via
	}
	if err := steamSaveAccountLocked(a); err != nil {
		return err
	}
	steamAccountGeneration++
	return nil
}

// steamRemoveLibraryLocked forgets the cached library (steamStateMu held).
func steamRemoveLibraryLocked() error {
	err := os.Remove(steamLibraryPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// steamSaveKey stores the Web API key (encrypted); "" removes it.
func steamSaveKey(key string) error {
	stored := ""
	if key != "" {
		enc, err := EncryptAPIToken(key, "steam_api_key")
		if err != nil {
			return err
		}
		stored = enc
	}
	return steamUpdateAccount(func(a *steamAccountFile) { a.APIKey = stored })
}

// steamSignOut forgets the account, the key and the cached library. The Steam
// app's own account stays unused until the user picks it again.
func steamSignOut() error {
	steamStateMu.Lock()
	defer steamStateMu.Unlock()
	_ = steamRemoveLibraryLocked()
	if err := steamSaveAccountLocked(steamAccountFile{NoLocal: true}); err != nil {
		return err
	}
	steamAccountGeneration++
	return nil
}

// steamUseAppAccount switches to the account the Steam app is signed in with.
func steamUseAppAccount() error {
	return steamUpdateAccount(func(a *steamAccountFile) {
		if a.SteamID != "" {
			_ = steamRemoveLibraryLocked()
		}
		a.SteamID, a.Persona, a.Via, a.NoLocal = "", "", "", false
		a.RefreshToken, a.AccessToken, a.AccessExp, a.AccountName, a.TokenLost = "", "", 0, "", false
	})
}

// steamSavePersona remembers the name a refresh found for the signed-in id.
func steamSavePersona(id, persona string) {
	persona = steamCleanText(persona, 64)
	if persona == "" || id == "" {
		return
	}
	steamStateMu.Lock()
	defer steamStateMu.Unlock()
	a := steamLoadAccountLocked()
	if a.SteamID != id || a.Persona == persona {
		return
	}
	a.Persona = persona
	if err := steamSaveAccountLocked(a); err != nil {
		logWarn("steam", "saving the account name failed: %v", err)
	}
}

// steamCleanText makes a name from the web or a VDF file safe to draw: no
// control characters, no runs of spaces, at most limit runes.
func steamCleanText(s string, limit int) string {
	var b strings.Builder
	n := 0
	space := false
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			continue
		}
		if unicode.IsSpace(r) {
			if space {
				continue
			}
			space = true
			r = ' '
		} else {
			space = false
		}
		if n >= limit {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

// ── Library cache ─────────────────────────────────────────────────────────

func steamLoadLibrary() steamLibraryFile {
	steamStateMu.Lock()
	defer steamStateMu.Unlock()
	var lib steamLibraryFile
	if _, err := readJSONWithBackup(steamLibraryPath(), &lib); err != nil {
		return steamLibraryFile{Version: steamFileVersion}
	}
	if len(lib.Games) > steamMaxGames {
		lib.Games = lib.Games[:steamMaxGames]
	}
	games := lib.Games[:0]
	for _, g := range lib.Games {
		if g.AppID <= 0 {
			continue
		}
		g.Name = steamCleanText(g.Name, 120)
		games = append(games, g)
	}
	lib.Games = games
	return lib
}

func steamSaveLibrary(lib steamLibraryFile, generation uint64) error {
	steamStateMu.Lock()
	defer steamStateMu.Unlock()
	if generation != steamAccountGeneration {
		return nil // a newer sign-in or sign-out owns the cache now
	}
	if err := os.MkdirAll(steamDataDir(), 0o700); err != nil {
		return err
	}
	lib.Version = steamFileVersion
	data, err := json.Marshal(lib)
	if err != nil {
		return err
	}
	return AtomicWrite(steamLibraryPath(), append(data, '\n'), 0o600)
}

// steamMergeGames joins the web library with the games installed here: one
// row per app, installed and the larger play time and last-played date from
// either source.
func steamMergeGames(web, local []steamGame) []steamGame {
	out := make([]steamGame, 0, len(web)+len(local))
	at := make(map[int]int, len(web)+len(local))
	add := func(g steamGame) {
		if g.AppID <= 0 || len(out) >= steamMaxGames {
			return
		}
		if i, ok := at[g.AppID]; ok {
			o := &out[i]
			o.Installed = o.Installed || g.Installed
			if o.Name == "" {
				o.Name = g.Name
			}
			if g.Playtime > o.Playtime {
				o.Playtime = g.Playtime
			}
			if g.LastPlayed > o.LastPlayed {
				o.LastPlayed = g.LastPlayed
			}
			if g.Size > o.Size {
				o.Size = g.Size
			}
			if g.DlTotal > 0 {
				o.DlDone, o.DlTotal, o.DlPaused = g.DlDone, g.DlTotal, g.DlPaused
			}
			if o.Remote.where == "" && g.Remote.where != "" {
				o.Remote = g.Remote
			}
			return
		}
		at[g.AppID] = len(out)
		out = append(out, g)
	}
	for _, g := range web {
		add(g)
	}
	for _, g := range local {
		add(g)
	}
	for i := range out {
		if out[i].Name == "" {
			out[i].Name = "App " + strconv.Itoa(out[i].AppID)
		}
	}
	steamSortGames(out)
	return out
}

// steamSortGames orders the library the way Steam's "Recent" shelf does: the
// last played first, then the never-played ones by name.
func steamSortGames(games []steamGame) {
	sort.SliceStable(games, func(i, j int) bool {
		if games[i].LastPlayed != games[j].LastPlayed {
			return games[i].LastPlayed > games[j].LastPlayed
		}
		return strings.ToLower(games[i].Name) < strings.ToLower(games[j].Name)
	})
}

// steamRefreshResult is one refresh: the merged list and what went wrong
// reading the web part, if anything.
type steamRefreshResult struct {
	account steamAccount
	games   []steamGame
	local   bool
	webErr  error
	when    time.Time
	// remote is what the account's Steam PCs and the paired JukaHub PCs
	// reported (steam_remote.go), already folded into games.
	remote steamRemoteSnapshot
}

// steamRefreshLibrary reads the installed games and the account's library
// and saves the merged list. When the web cannot be read, the last list it
// gave is kept, so the screen still shows the whole library offline. Runs on
// a goroutine.
func steamRefreshLibrary(ctx context.Context) steamRefreshResult {
	acct, key, generation := steamCurrentAccountSnapshot()
	res := steamRefreshResult{account: acct, when: time.Now()}
	var local []steamGame
	if root := steamRoot(); root != "" {
		// Local means games start here: the folder alone may be a leftover.
		res.local = steamClientPresent()
		local = steamInstalledGames(root, acct.SteamID, true)
	}
	var web []steamGame
	token := ""
	if acct.HasToken {
		tok, tokID, err := steamAccessToken(ctx)
		switch {
		case err == nil && tokID == acct.SteamID:
			token = tok
		case errors.Is(err, errSteamSignedOut):
			res.account.HasToken, res.account.TokenLost = false, true
		case err != nil:
			logWarn("steam", "renewing the Steam app sign-in failed: %v", err)
		}
	}
	if acct.SteamID != "" {
		games, persona, err := steamFetchOwned(ctx, acct.SteamID, key, token)
		web, res.webErr = games, err
		if persona != "" {
			res.account.Persona = persona
			if acct.Via != "app" {
				steamSavePersona(acct.SteamID, persona)
			}
		}
		if err != nil {
			logWarn("steam", "reading the library of %s failed: %v", acct.SteamID, err)
			if cached := steamLoadLibrary(); cached.SteamID == acct.SteamID {
				for _, g := range cached.Games {
					g.Installed = false
					web = append(web, g)
				}
				if res.account.Persona == "" {
					res.account.Persona = cached.Persona
				}
			}
		}
	}
	res.games = steamMergeGames(web, local)
	if acct.SteamID != "" || len(local) > 0 {
		lib := steamLibraryFile{SteamID: acct.SteamID, Persona: res.account.Persona, Updated: res.when, Games: res.games}
		if err := steamSaveLibrary(lib, generation); err != nil {
			logWarn("steam", "saving library.json failed: %v", err)
		}
	}
	if !res.local {
		// No Steam app here: ask the account's Steam PCs and the paired
		// JukaHub PCs what they have installed or are downloading. Saved
		// above without them: a PC's games are not the account's.
		res.remote = steamRemoteSnapshotFetch(ctx, token)
		res.games = steamApplyRemote(res.games, res.remote)
	}
	logDebug("steam", "library: %d games (%d installed here), web error: %v", len(res.games), len(local), res.webErr)
	return res
}

// ── VDF (Valve KeyValues text) ────────────────────────────────────────────

// vdfNode is one key of a KeyValues file: a string value or a block of keys.
// Keys are kept lower-case, as Steam itself compares them.
type vdfNode struct {
	Value string
	Kids  map[string]*vdfNode
	Order []string
}

func (n *vdfNode) child(key string) *vdfNode {
	if n == nil || n.Kids == nil {
		return nil
	}
	return n.Kids[strings.ToLower(key)]
}

func (n *vdfNode) get(key string) string {
	if c := n.child(key); c != nil {
		return c.Value
	}
	return ""
}

// parseVDF reads Valve's KeyValues text: quoted (or bare) keys followed by a
// quoted value or a { block }, with // comments. It never fails: what cannot
// be read is skipped, and nesting is bounded.
func parseVDF(text string) *vdfNode {
	toks := vdfTokens(text)
	root := &vdfNode{Kids: map[string]*vdfNode{}}
	stack := []*vdfNode{root}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		cur := stack[len(stack)-1]
		if t.brace == '}' {
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if t.brace == '{' {
			// A block with no key: read into a throwaway node.
			if len(stack) < 32 {
				stack = append(stack, &vdfNode{Kids: map[string]*vdfNode{}})
			}
			continue
		}
		key := strings.ToLower(t.text)
		if i+1 >= len(toks) {
			break
		}
		next := toks[i+1]
		i++
		node := &vdfNode{}
		if next.brace == '{' {
			node.Kids = map[string]*vdfNode{}
			if len(stack) < 32 {
				stack = append(stack, node)
			}
		} else if next.brace == '}' {
			// "key" } — a key with no value ends the block.
			i--
			continue
		} else {
			node.Value = next.text
		}
		if _, seen := cur.Kids[key]; !seen {
			cur.Order = append(cur.Order, key)
		}
		cur.Kids[key] = node
	}
	return root
}

type vdfToken struct {
	text  string
	brace byte
}

func vdfTokens(s string) []vdfToken {
	var toks []vdfToken
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '{' || c == '}':
			toks = append(toks, vdfToken{brace: c})
			i++
		case c == '"':
			i++
			var b strings.Builder
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					switch s[i+1] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					default:
						b.WriteByte(s[i+1])
					}
					i += 2
					continue
				}
				b.WriteByte(s[i])
				i++
			}
			i++ // the closing quote
			toks = append(toks, vdfToken{text: b.String()})
		case c == '[':
			// A platform condition such as [$WIN32]: not a token.
			for i < len(s) && s[i] != ']' {
				i++
			}
			i++
		default:
			start := i
			for i < len(s) && !strings.ContainsRune(" \t\r\n{}\"", rune(s[i])) {
				i++
			}
			toks = append(toks, vdfToken{text: s[start:i]})
		}
	}
	return toks
}

// steamReadSmall reads at most limit bytes of a file.
func steamReadSmall(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, limit))
}

// ── The Steam app on this computer ────────────────────────────────────────

// steamRoot is the Steam app's folder on this computer, or "".
func steamRoot() string {
	for _, dir := range steamRootDirs() {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, "steamapps")); err == nil && st.IsDir() {
			return filepath.Clean(dir)
		}
	}
	return ""
}

// steamFindLocalUser is the account the Steam app last signed in with.
func steamFindLocalUser() (steamLocalUser, bool) {
	root := steamRoot()
	if root == "" {
		return steamLocalUser{}, false
	}
	data, err := steamReadSmall(filepath.Join(root, "config", "loginusers.vdf"), 1<<20)
	if err != nil {
		return steamLocalUser{}, false
	}
	users := parseVDF(string(data)).child("users")
	if users == nil {
		return steamLocalUser{}, false
	}
	var best steamLocalUser
	var bestStamp int64 = -1
	for _, id := range users.Order {
		if !steamValidID(id) {
			continue
		}
		u := users.Kids[id]
		stamp, _ := strconv.ParseInt(u.get("timestamp"), 10, 64)
		if u.get("mostrecent") == "1" {
			stamp = 1 << 62
		}
		if stamp > bestStamp {
			bestStamp = stamp
			best = steamLocalUser{SteamID: id, Persona: steamCleanText(u.get("personaname"), 64)}
		}
	}
	return best, best.SteamID != ""
}

// steamLibraryFolders lists every steamapps folder the Steam app installs into.
func steamLibraryFolders(root string) []string {
	mainDir := filepath.Join(root, "steamapps")
	dirs := []string{mainDir}
	seen := map[string]bool{strings.ToLower(filepath.Clean(mainDir)): true}
	data, err := steamReadSmall(filepath.Join(mainDir, "libraryfolders.vdf"), 1<<20)
	if err != nil {
		data, err = steamReadSmall(filepath.Join(root, "config", "libraryfolders.vdf"), 1<<20)
	}
	if err != nil {
		return dirs
	}
	lf := parseVDF(string(data)).child("libraryfolders")
	if lf == nil {
		return dirs
	}
	for _, k := range lf.Order {
		kid := lf.Kids[k]
		path := kid.get("path")
		if path == "" && kid.Kids == nil {
			// The old format: "1" "D:\\SteamLibrary".
			if _, err := strconv.Atoi(k); err == nil {
				path = kid.Value
			}
		}
		if strings.TrimSpace(path) == "" {
			continue
		}
		dir := filepath.Join(filepath.FromSlash(path), "steamapps")
		key := strings.ToLower(filepath.Clean(dir))
		if seen[key] {
			continue
		}
		seen[key] = true
		dirs = append(dirs, dir)
	}
	return dirs
}

// steamSkipLocalApp hides what Steam installs alongside games: runtimes,
// redistributables and Proton.
func steamSkipLocalApp(appid int, name string) bool {
	switch appid {
	case 228980, 1070560, 1391110, 1628350, 1493710:
		return true
	}
	lower := strings.ToLower(name)
	for _, p := range []string{"proton ", "proton-", "steam linux runtime", "steamworks common", "steamvr"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

// steamInstalledGames lists the games the Steam app has installed or is
// downloading; withPlaytime adds play time and last-played dates from the
// account's localconfig.vdf (a large file: the download polls skip it).
func steamInstalledGames(root, steamID string, withPlaytime bool) []steamGame {
	var games []steamGame
	seen := map[int]bool{}
	for _, dir := range steamLibraryFolders(root) {
		manifests, _ := filepath.Glob(filepath.Join(dir, "appmanifest_*.acf"))
		for _, m := range manifests {
			if len(games) >= steamMaxGames {
				break
			}
			data, err := steamReadSmall(m, 256<<10)
			if err != nil {
				continue
			}
			st := parseVDF(string(data)).child("appstate")
			if st == nil {
				continue
			}
			appid, err := strconv.Atoi(st.get("appid"))
			if err != nil || appid <= 0 || seen[appid] {
				continue
			}
			name := steamCleanText(st.get("name"), 120)
			if steamSkipLocalApp(appid, name) {
				continue
			}
			seen[appid] = true
			games = append(games, steamManifestGame(appid, name, st))
		}
	}
	if !withPlaytime {
		return games
	}
	if played := steamLocalPlaytime(root, steamID); len(played) > 0 {
		for i := range games {
			p, ok := played[games[i].AppID]
			if !ok {
				continue
			}
			if p.Playtime > games[i].Playtime {
				games[i].Playtime = p.Playtime
			}
			if p.LastPlayed > games[i].LastPlayed {
				games[i].LastPlayed = p.LastPlayed
			}
		}
	}
	return games
}

// steamManifestIDs is every app id with a manifest in any library folder,
// whether or not the manifest can be read right now (Steam rewrites them).
func steamManifestIDs(root string) map[int]bool {
	ids := map[int]bool{}
	for _, dir := range steamLibraryFolders(root) {
		manifests, _ := filepath.Glob(filepath.Join(dir, "appmanifest_*.acf"))
		for _, m := range manifests {
			base := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "appmanifest_"), ".acf")
			if id, err := strconv.Atoi(base); err == nil && id > 0 {
				ids[id] = true
			}
		}
	}
	return ids
}

// Steam's EAppState bits, as appmanifest_*.acf stores them.
const (
	steamStateFullyInstalled = 4
	steamStateUpdateRunning  = 256
	steamStateUpdatePaused   = 512
	steamStateUpdateStarted  = 1024
	steamStateDownloading    = 1 << 20
)

// steamManifestGame reads one app manifest: installed, size on disk, and the
// download in progress (a new install or an update) with its bytes.
func steamManifestGame(appid int, name string, st *vdfNode) steamGame {
	flags, _ := strconv.ParseInt(st.get("stateflags"), 10, 64)
	last, _ := strconv.ParseInt(st.get("lastplayed"), 10, 64)
	size, _ := strconv.ParseInt(st.get("sizeondisk"), 10, 64)
	g := steamGame{
		AppID:      appid,
		Name:       name,
		Installed:  flags&steamStateFullyInstalled != 0,
		LastPlayed: last,
		Size:       max(size, 0),
	}
	// UpdateRequired alone is a queued or scheduled update, not a download.
	if flags&(steamStateUpdateRunning|steamStateUpdateStarted|steamStateDownloading) != 0 {
		total, _ := strconv.ParseInt(st.get("bytestodownload"), 10, 64)
		done, _ := strconv.ParseInt(st.get("bytesdownloaded"), 10, 64)
		if total > 0 && done >= 0 && done < total {
			g.DlDone, g.DlTotal = done, total
			g.DlPaused = flags&steamStateUpdatePaused != 0
		}
	}
	return g
}

// steamLocalPlaytime reads play time and last-played dates from the account's
// localconfig.vdf (userdata/<account id>/config).
func steamLocalPlaytime(root, steamID string) map[int]steamGame {
	if !steamValidID(steamID) {
		return nil
	}
	id64, err := strconv.ParseUint(steamID, 10, 64)
	if err != nil || id64 < steamAccountIDBase {
		return nil
	}
	accountID := strconv.FormatUint(id64-steamAccountIDBase, 10)
	data, err := steamReadSmall(filepath.Join(root, "userdata", accountID, "config", "localconfig.vdf"), steamPageLimit)
	if err != nil {
		return nil
	}
	apps := parseVDF(string(data)).child("userlocalconfigstore").child("software").child("valve").child("steam").child("apps")
	if apps == nil {
		return nil
	}
	out := make(map[int]steamGame, len(apps.Order))
	for _, k := range apps.Order {
		appid, err := strconv.Atoi(k)
		if err != nil || appid <= 0 {
			continue
		}
		a := apps.Kids[k]
		minutes, _ := strconv.Atoi(a.get("playtime"))
		last, _ := strconv.ParseInt(a.get("lastplayed"), 10, 64)
		if minutes <= 0 && last <= 0 {
			continue
		}
		out[appid] = steamGame{AppID: appid, Playtime: minutes, LastPlayed: last}
	}
	return out
}

// steamClientPresent reports whether a Steam app here can start games.
func steamClientPresent() bool {
	return steamClientBinary() != ""
}

// steamAction is what a steam:// link asks the Steam app to do.
type steamAction string

const (
	steamActRun       steamAction = "run"
	steamActInstall   steamAction = "install"
	steamActUninstall steamAction = "uninstall"
)

// steamRunURI is the steam:// link for action on appid: start it, open its
// install dialog, or its uninstall confirmation. appid is a number and the
// action one of three words, so the link is never anything but these forms.
func steamRunURI(appid int, action steamAction) string {
	switch action {
	case steamActInstall:
		return "steam://install/" + strconv.Itoa(appid)
	case steamActUninstall:
		return "steam://uninstall/" + strconv.Itoa(appid)
	}
	return "steam://rungameid/" + strconv.Itoa(appid)
}

var errSteamNoClient = errors.New("Steam is not installed here")

// steamLaunchLocal hands a game to the Steam app on this computer.
func steamLaunchLocal(appid int, action steamAction) error {
	if appid <= 0 {
		return errors.New("no game")
	}
	if !steamClientPresent() {
		return errSteamNoClient
	}
	logInfo("steam", "opening %s", steamRunURI(appid, action))
	return steamOpenURI(steamRunURI(appid, action))
}

// ── The web library ───────────────────────────────────────────────────────

var steamHTTP struct {
	once   sync.Once
	client *http.Client
}

// steamHTTPClient is the client for Steam's web pages, API and CDN, with the
// firmware CA fallback (bugrepRootCAs) a handheld needs.
func steamHTTPClient() *http.Client {
	steamHTTP.once.Do(func() {
		tr := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConns:          4,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       60 * time.Second,
		}
		if pool := bugrepRootCAs(); pool != nil {
			tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
		}
		steamHTTP.client = &http.Client{Transport: tr, Timeout: 45 * time.Second}
	})
	return steamHTTP.client
}

// steamHTTPError is a non-200 answer.
type steamHTTPError struct{ code int }

func (e *steamHTTPError) Error() string { return fmt.Sprintf("Steam answered HTTP %d", e.code) }

// net/http includes the full URL in a *url.Error. Steam API URLs contain an
// access token or Web API key, so keep the underlying cause without the URL.
func steamRequestError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("Steam request failed: %w", ue.Err)
	}
	return err
}

// steamGet fetches url and returns at most limit bytes of a 200 answer.
func steamGet(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	return steamGetCookie(ctx, rawURL, limit, "")
}

// steamGetCookie is steamGet with a Cookie header (the signed-in owner's
// session for steamcommunity.com).
func steamGetCookie(ctx context.Context, rawURL string, limit int64, cookie string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (JukaHub) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124 Safari/537.36")
	resp, err := steamHTTPClient().Do(req)
	if err != nil {
		return nil, steamRequestError(err)
	}
	defer resp.Body.Close()
	if steamGamesLoginRedirect(req.URL, resp.Request.URL) {
		return nil, errSteamLoginRequired
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, &steamHTTPError{code: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("Steam's answer was too large")
	}
	return data, nil
}

func steamGamesLoginRedirect(start, end *url.URL) bool {
	return start != nil && end != nil &&
		strings.EqualFold(start.Hostname(), "steamcommunity.com") && strings.Contains(start.Path, "/games") &&
		strings.EqualFold(end.Hostname(), "steamcommunity.com") && strings.HasPrefix(end.Path, "/login")
}

// errSteamPrivate is a profile whose game list Steam does not show to others.
var errSteamPrivate = errors.New("your Steam profile's game details are private")

var errSteamLoginRequired = errors.New("Steam requires a login to show this profile's games")

// steamFetchOwned reads the account's whole library: with the Steam app
// sign-in's token or a saved Web API key (a private profile works then), else
// from the public profile.
func steamFetchOwned(ctx context.Context, id, key, token string) ([]steamGame, string, error) {
	if token != "" {
		games, err := steamFetchOwnedAuth(ctx, id, "access_token="+url.QueryEscape(token))
		if err == nil {
			return games, steamFetchPersonaXML(ctx, id), nil
		}
		logWarn("steam", "GetOwnedGames with the sign-in failed: %v; reading the profile as its owner", err)
		// The games page shows a private library to its signed-in owner.
		data, err := steamGetCookie(ctx, "https://steamcommunity.com/profiles/"+id+"/games/?tab=all", steamPageLimit,
			"steamLoginSecure="+id+"%7C%7C"+token)
		if err == nil {
			if games, name, err := steamParseProfileGames(data); err == nil {
				return games, name, nil
			}
		}
	}
	if key != "" {
		games, err := steamFetchOwnedAPI(ctx, id, key)
		if err == nil {
			return games, steamFetchPersonaAPI(ctx, id, key), nil
		}
		var he *steamHTTPError
		if errors.As(err, &he) && (he.code == http.StatusUnauthorized || he.code == http.StatusForbidden) {
			logWarn("steam", "the Web API key was refused (HTTP %d); reading the public profile", he.code)
		} else if !errors.Is(err, errSteamPrivate) {
			logWarn("steam", "GetOwnedGames failed: %v; reading the public profile", err)
		}
	}
	return steamFetchOwnedProfile(ctx, id)
}

// steamFetchOwnedAPI is IPlayerService/GetOwnedGames with a Web API key.
func steamFetchOwnedAPI(ctx context.Context, id, key string) ([]steamGame, error) {
	return steamFetchOwnedAuth(ctx, id, "key="+url.QueryEscape(key))
}

// steamFetchOwnedAuth is IPlayerService/GetOwnedGames; auth is the
// "key=…" or "access_token=…" query part.
func steamFetchOwnedAuth(ctx context.Context, id, auth string) ([]steamGame, error) {
	u := "https://api.steampowered.com/IPlayerService/GetOwnedGames/v1/?format=json&include_appinfo=1&include_played_free_games=1" +
		"&" + auth + "&steamid=" + url.QueryEscape(id)
	data, err := steamGet(ctx, u, steamPageLimit)
	if err != nil {
		return nil, err
	}
	return steamParseOwnedGames(data)
}

func steamParseOwnedGames(data []byte) ([]steamGame, error) {
	var body struct {
		Response struct {
			GameCount *int `json:"game_count"`
			Games     []struct {
				AppID      int    `json:"appid"`
				Name       string `json:"name"`
				Playtime   int    `json:"playtime_forever"`
				LastPlayed int64  `json:"rtime_last_played"`
			} `json:"games"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("reading GetOwnedGames: %w", err)
	}
	if len(body.Response.Games) == 0 {
		if body.Response.GameCount != nil && *body.Response.GameCount == 0 {
			return []steamGame{}, nil
		}
		return nil, errSteamPrivate
	}
	games := make([]steamGame, 0, len(body.Response.Games))
	for _, g := range body.Response.Games {
		if g.AppID <= 0 || len(games) >= steamMaxGames {
			continue
		}
		games = append(games, steamGame{AppID: g.AppID, Name: steamCleanText(g.Name, 120), Playtime: g.Playtime, LastPlayed: g.LastPlayed})
	}
	return games, nil
}

// steamFetchPersonaAPI is the account's display name, or "".
func steamFetchPersonaAPI(ctx context.Context, id, key string) string {
	u := "https://api.steampowered.com/ISteamUser/GetPlayerSummaries/v2/?key=" + url.QueryEscape(key) + "&steamids=" + url.QueryEscape(id)
	data, err := steamGet(ctx, u, 1<<20)
	if err != nil {
		return ""
	}
	var body struct {
		Response struct {
			Players []struct {
				Name string `json:"personaname"`
			} `json:"players"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &body) != nil || len(body.Response.Players) == 0 {
		return ""
	}
	return steamCleanText(body.Response.Players[0].Name, 64)
}

var steamXMLNameRe = regexp.MustCompile(`<steamID>\s*(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?\s*</steamID>`)

// steamFetchPersonaXML is the account's display name from the profile's XML
// view, which names even a private profile; "" when it cannot be read.
func steamFetchPersonaXML(ctx context.Context, id string) string {
	data, err := steamGet(ctx, "https://steamcommunity.com/profiles/"+id+"/?xml=1", 1<<20)
	if err != nil {
		return ""
	}
	m := steamXMLNameRe.FindSubmatch(data)
	if m == nil {
		return ""
	}
	return steamCleanText(html.UnescapeString(string(m[1])), 64)
}

// steamCheckKey asks Steam whether a Web API key works: GetPlayerSummaries
// answers 403 for a key it does not know. Only that refusal rejects the key;
// a handheld that is offline right now saves it anyway.
var steamCheckKey = func(ctx context.Context, key string) (ok, checked bool) {
	_, err := steamGet(ctx, "https://api.steampowered.com/ISteamUser/GetPlayerSummaries/v2/?key="+url.QueryEscape(key)+"&steamids=76561197960435530", 1<<20)
	if err == nil {
		return true, true
	}
	var he *steamHTTPError
	if errors.As(err, &he) && (he.code == http.StatusUnauthorized || he.code == http.StatusForbidden) {
		return false, true
	}
	return false, false
}

// steamFetchOwnedProfile reads the public profile's games page.
func steamFetchOwnedProfile(ctx context.Context, id string) ([]steamGame, string, error) {
	data, err := steamGet(ctx, "https://steamcommunity.com/profiles/"+id+"/games/?tab=all", steamPageLimit)
	if err != nil {
		return nil, "", err
	}
	return steamParseProfileGames(data)
}

// steamWebGame is one game as the profile page lists it. Play time has been a
// number of minutes and, on the old page, a string of hours.
type steamWebGame struct {
	AppID      int             `json:"appid"`
	Name       string          `json:"name"`
	Playtime   json.RawMessage `json:"playtime_forever"`
	Hours      json.RawMessage `json:"hours_forever"`
	LastPlayed json.RawMessage `json:"rtime_last_played"`
	OldLast    json.RawMessage `json:"last_played"`
}

func steamRawNumber(raw json.RawMessage) float64 {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	s = strings.ReplaceAll(s, ",", "")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || math.IsNaN(f) {
		return 0
	}
	// Bounded so a garbage number cannot overflow the int it becomes.
	return min(f, 1e12)
}

var steamRgGamesRe = regexp.MustCompile(`(?s)var\s+rgGames\s*=\s*(\[.*?\]);\s*\n`)

// steamParseProfileGames reads the games list out of the profile's games
// page: the data-profile-gameslist attribute the current page carries, or the
// rgGames script of the older one.
func steamParseProfileGames(page []byte) ([]steamGame, string, error) {
	s := string(page)
	var list []steamWebGame
	name := ""
	found := false
	const attr = `data-profile-gameslist="`
	if i := strings.Index(s, attr); i >= 0 {
		rest := s[i+len(attr):]
		if j := strings.IndexByte(rest, '"'); j >= 0 {
			var cfg struct {
				Name  string         `json:"strProfileName"`
				Games []steamWebGame `json:"rgGames"`
			}
			if err := json.Unmarshal([]byte(html.UnescapeString(rest[:j])), &cfg); err == nil {
				list, name, found = cfg.Games, cfg.Name, true
			}
		}
	}
	if !found {
		if m := steamRgGamesRe.FindStringSubmatch(s); m != nil {
			if err := json.Unmarshal([]byte(m[1]), &list); err == nil {
				found = true
			}
		}
	}
	if !found || len(list) == 0 {
		lower := strings.ToLower(s)
		if strings.Contains(lower, "profile_private_info") || strings.Contains(lower, "this profile is private") || found {
			return nil, "", errSteamPrivate
		}
		return nil, "", errors.New("Steam's profile page did not list any games")
	}
	games := make([]steamGame, 0, len(list))
	for _, g := range list {
		if g.AppID <= 0 || len(games) >= steamMaxGames {
			continue
		}
		minutes := int(steamRawNumber(g.Playtime))
		if minutes == 0 {
			minutes = int(steamRawNumber(g.Hours) * 60)
		}
		last := int64(steamRawNumber(g.LastPlayed))
		if last == 0 {
			last = int64(steamRawNumber(g.OldLast))
		}
		games = append(games, steamGame{AppID: g.AppID, Name: steamCleanText(g.Name, 120), Playtime: minutes, LastPlayed: last})
	}
	return games, steamCleanText(name, 64), nil
}

// steamErrorText is a refresh failure as a short sentence for the screen.
func steamErrorText(err error) string {
	if err == nil {
		return ""
	}
	var he *steamHTTPError
	switch {
	case errors.Is(err, errSteamLoginRequired):
		return "Steam requires a login to show games · sign in with the Steam app or add a Web API key"
	case errors.Is(err, errSteamPrivate):
		return "Steam keeps this profile's games private · set Game details to Public, or send a Web API key"
	case errors.As(err, &he) && he.code == http.StatusTooManyRequests:
		return "Steam asked to slow down · try again in a few minutes"
	case errors.As(err, &he):
		return "Steam could not be read (HTTP " + strconv.Itoa(he.code) + ")"
	case errors.Is(err, context.DeadlineExceeded):
		return "Steam did not answer in time"
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return "No connection to Steam"
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "no such host") || strings.Contains(lower, "network is unreachable") || strings.Contains(lower, "connection refused") {
		return "No connection to Steam"
	}
	return capitalizeFirst(firstLine(err.Error()))
}

// ── Cover art ─────────────────────────────────────────────────────────────

const (
	steamArtUnknown uint8 = iota
	steamArtQueued
	steamArtReady
	steamArtFailed
)

// steamArt tracks each app's cover: unknown, being fetched, on disk, or not
// available. One worker fetches them in order, so a long library never opens
// dozens of connections at once.
var steamArt struct {
	mu    sync.Mutex
	state map[int]uint8
	queue chan int
	once  sync.Once
}

func steamArtPath(appid int) string {
	return filepath.Join(steamArtDir(), strconv.Itoa(appid)+".jpg")
}

func steamArtSet(appid int, st uint8) {
	steamArt.mu.Lock()
	if steamArt.state == nil {
		steamArt.state = map[int]uint8{}
	}
	steamArt.state[appid] = st
	steamArt.mu.Unlock()
}

// steamArtFor is the cover file to draw for appid, or "" while there is none.
// Asking for one that is not on disk queues its download. Main thread; one
// stat per app per session.
func steamArtFor(appid int) string {
	if appid <= 0 {
		return ""
	}
	steamArt.mu.Lock()
	if steamArt.state == nil {
		steamArt.state = map[int]uint8{}
	}
	st := steamArt.state[appid]
	if st == steamArtUnknown {
		steamArt.state[appid] = steamArtQueued
	}
	steamArt.mu.Unlock()
	switch st {
	case steamArtReady:
		return steamArtPath(appid)
	case steamArtQueued, steamArtFailed:
		return ""
	}
	path := steamArtPath(appid)
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		steamArtSet(appid, steamArtReady)
		return path
	}
	steamArt.once.Do(func() {
		steamArt.queue = make(chan int, 256)
		go steamArtWorker()
	})
	select {
	case steamArt.queue <- appid:
	default:
		// The queue is full: ask again on a later frame.
		steamArtSet(appid, steamArtUnknown)
	}
	return ""
}

func steamArtWorker() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("steam", "cover worker stopped: %v", r)
		}
	}()
	for appid := range steamArt.queue {
		if err := steamFetchArt(appid); err != nil {
			logDebug("steam", "no cover for %d: %v", appid, err)
			steamArtSet(appid, steamArtFailed)
			continue
		}
		steamArtSet(appid, steamArtReady)
	}
}

// steamArtLooksLikeImage accepts JPEG and PNG data.
func steamArtLooksLikeImage(b []byte) bool {
	return len(b) > 8 && ((b[0] == 0xFF && b[1] == 0xD8) || (b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G'))
}

// steamFetchArt saves appid's cover: the Steam app's own cached picture when
// there is one, else the capsule from Steam's CDN (plain http when this
// device cannot verify the CDN's certificate — it is a public picture).
func steamFetchArt(appid int) error {
	var data []byte
	if root := steamRoot(); root != "" {
		cache := filepath.Join(root, "appcache", "librarycache")
		id := strconv.Itoa(appid)
		for _, p := range []string{
			filepath.Join(cache, id+"_header.jpg"),
			filepath.Join(cache, id, "header.jpg"),
		} {
			if b, err := steamReadSmall(p, steamArtLimit); err == nil && steamArtLooksLikeImage(b) {
				data = b
				break
			}
		}
	}
	if data == nil {
		var lastErr error
		id := strconv.Itoa(appid)
		for _, rel := range []string{
			"cdn.cloudflare.steamstatic.com/steam/apps/" + id + "/header.jpg",
			"cdn.cloudflare.steamstatic.com/steam/apps/" + id + "/capsule_231x87.jpg",
			"shared.akamai.steamstatic.com/store_item_assets/steam/apps/" + id + "/header.jpg",
		} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			b, err := steamGet(ctx, "https://"+rel, steamArtLimit)
			if err != nil && artworkCertError(err) {
				b, err = steamGet(ctx, "http://"+rel, steamArtLimit)
			}
			cancel()
			if err == nil && steamArtLooksLikeImage(b) {
				data = b
				break
			}
			if err == nil {
				err = errors.New("not an image")
			}
			lastErr = err
		}
		if data == nil {
			return lastErr
		}
	}
	if err := os.MkdirAll(steamArtDir(), 0o755); err != nil {
		return err
	}
	return AtomicWrite(steamArtPath(appid), data, 0o644)
}

// ── Starting a game on a paired PC ────────────────────────────────────────

// steamPeerRun is the body of /peer/v1/steam/run. Action is "run" (also when
// empty, as older versions send it) or "install".
type steamPeerRun struct {
	AppID  int    `json:"appid"`
	Name   string `json:"name"`
	Action string `json:"action,omitempty"`
}

// steamHandlePeerRun starts a game a paired device asked for, or opens
// Steam's install dialog for it, when this computer has the Steam app. Only
// paired devices reach it (peerGuard).
func steamHandlePeerRun(w http.ResponseWriter, r *http.Request, call peerCall) {
	var req steamPeerRun
	if !peerDecodeBody(w, r, 4<<10, &req) {
		return
	}
	if req.AppID <= 0 || req.AppID > 1<<31-1 {
		http.Error(w, "bad app id", http.StatusBadRequest)
		return
	}
	action := steamActRun
	switch req.Action {
	case "", string(steamActRun):
	case string(steamActInstall):
		action = steamActInstall
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if !steamClientPresent() {
		http.Error(w, "Steam is not installed on this device", http.StatusConflict)
		return
	}
	if err := steamOpenURI(steamRunURI(req.AppID, action)); err != nil {
		logWarn("steam", "%s %d for %s failed: %v", action, req.AppID, call.rec.Name, err)
		http.Error(w, "Steam could not be started", http.StatusConflict)
		return
	}
	name := steamCleanText(req.Name, 80)
	if name == "" {
		name = "a Steam game"
	}
	from := steamCleanText(call.rec.Name, 40)
	if from == "" {
		from = "a paired device"
	}
	logInfo("steam", "%s app %d (%s) for %s", action, req.AppID, name, from)
	msg := "Starting " + name + " for " + from
	if action == steamActInstall {
		msg = from + " asked to install " + name + " · confirm it in Steam"
	}
	tbxPost(func() { ShowToast(msg, ToastKindInfo) })
	peerWriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

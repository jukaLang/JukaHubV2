package main

// homebrew_store_itch.go — what Get Homebrew (homebrew_store.go) reads from
// itch.io, and the pure rules it applies to it.
//
// Four kinds of outside data, all parsed defensively (a field that is missing
// or has another type is skipped, never trusted):
//
//   - Browse feeds. Every itch.io browse page has an RSS twin: the same URL
//     with ".xml" on the end (itch.io/docs/api/overview), e.g.
//     https://itch.io/games/newest/tag-game-boy.xml. An item carries title,
//     plainTitle (the title without itch's "[Free] [Puzzle]" prefixes), link
//     (the game page), description (HTML), pubDate, imageurl (the 315×250
//     cover), price ("$0.00" for a free game), currency, and on newer feeds
//     createDate / updateDate. Browse filters chain in the path
//     (/games/newest/free/tag-homebrew), and a tag that does not exist answers
//     404 or an empty feed — both just mean "no games from this list".
//   - A game's data.json (https://<author>.itch.io/<game>/data.json, the call
//     itch.io's own JavaScript API makes): id, title, price, cover_image and
//     authors. The id is what the download API needs.
//   - The API at api.itch.io with the user's own key in an "Authorization:
//     Bearer" header (the v2 form itch.io asks clients to use instead of the
//     key-in-the-path v1 calls): /profile (whose key it is),
//     /profile/owned-keys (games the user bought or claimed, paged, each with
//     its download key id), /games/ID/uploads (the files, each with filename,
//     size, type, demo/preorder flags, platform flags, md5_hash, build_id and
//     updated_at) and /uploads/ID/download (a redirect to the file). Fields
//     are read in snake_case and in camelCase, platforms as p_windows-style
//     flags, a "traits" list or a "platforms" object.
//   - The description, which is HTML and becomes plain text with paragraphs.
//
// hbPickUpload decides which upload is the cartridge: the console's own ROM
// extension first, a .zip that may hold one second; Windows, Mac, Linux and
// Android builds, soundtracks, books, web builds and pre-orders are skipped.
//
// Sources: itch.io/docs/api/overview (RSS feeds), itch.io/docs/api/javascript
// (data.json), itch.io/docs/api/oauth (the upload and download endpoints), and
// itch.io's notes to handheld clients (real User-Agent, v2 endpoints with a
// Bearer key, data.json, one download session per install). Nothing here has
// been fetched from this sandbox: the field names are from those documents.

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"html"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// hbFeedItem is one game from a browse feed.
type hbFeedItem struct {
	Title    string
	Link     string // the game page, normalised (hbNormalizeLink)
	Image    string
	Price    string
	Currency string
	Desc     string // plain text
	Pub      time.Time
	Created  time.Time
	Updated  time.Time
}

// hbRSS is the feed's shape for encoding/xml.
type hbRSS struct {
	Items []struct {
		Title      string `xml:"title"`
		PlainTitle string `xml:"plainTitle"`
		Link       string `xml:"link"`
		GUID       string `xml:"guid"`
		Desc       string `xml:"description"`
		PubDate    string `xml:"pubDate"`
		CreateDate string `xml:"createDate"`
		UpdateDate string `xml:"updateDate"`
		Image      string `xml:"imageurl"`
		Price      string `xml:"price"`
		Currency   string `xml:"currency"`
	} `xml:"channel>item"`
}

// hbFeedItemMax bounds what one feed may add.
const hbFeedItemMax = 120

// hbParseFeed reads a browse feed. Items without a usable itch.io game link
// are dropped. Pure.
func hbParseFeed(raw []byte) ([]hbFeedItem, error) {
	var doc hbRSS
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	// Lenient about stray entities, but no HTML auto-closing: that list has
	// "link", which would end every <link>…</link> before its text.
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	out := make([]hbFeedItem, 0, len(doc.Items))
	seen := map[string]bool{}
	for _, it := range doc.Items {
		link := hbNormalizeLink(it.Link)
		if link == "" {
			link = hbNormalizeLink(it.GUID)
		}
		if link == "" || seen[link] {
			continue
		}
		title := strings.TrimSpace(html.UnescapeString(it.PlainTitle))
		if title == "" {
			title = hbStripTitleTags(html.UnescapeString(it.Title))
		}
		title = hbOneLine(title, 120)
		if title == "" {
			continue
		}
		seen[link] = true
		item := hbFeedItem{
			Title:    title,
			Link:     link,
			Image:    hbImageURL(it.Image),
			Price:    hbOneLine(html.UnescapeString(it.Price), 24),
			Currency: hbOneLine(it.Currency, 8),
			Desc:     hbHTMLText(it.Desc, 4000),
			Pub:      hbParseTime(it.PubDate),
			Created:  hbParseTime(it.CreateDate),
			Updated:  hbParseTime(it.UpdateDate),
		}
		out = append(out, item)
		if len(out) >= hbFeedItemMax {
			break
		}
	}
	return out, nil
}

// hbTitleTag is one "[Free] " group at the start of a feed title.
var hbTitleTag = regexp.MustCompile(`^\s*\[[^\]]{0,40}\]\s*`)

// hbStripTitleTags drops itch's "[Free] [Puzzle] [Windows]" prefixes. Pure.
func hbStripTitleTags(s string) string {
	for i := 0; i < 8; i++ {
		loc := hbTitleTag.FindStringIndex(s)
		if loc == nil {
			break
		}
		s = s[loc[1]:]
	}
	return strings.TrimSpace(s)
}

// hbOneLine is s on one line, spaces collapsed, at most max runes. Pure.
func hbOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}

// hbNormalizeLink is an itch.io game page as https://<author>.itch.io/<game>,
// or "" for anything else (another site, a profile, a query). Pure.
func hbNormalizeLink(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if u.Port() != "" || !strings.HasSuffix(host, ".itch.io") || strings.Count(host, ".") != 2 {
		return ""
	}
	author := strings.TrimSuffix(host, ".itch.io")
	if !hbSlugOK(author) {
		return ""
	}
	game := strings.Trim(u.EscapedPath(), "/")
	if game == "" || strings.Contains(game, "/") || !hbSlugOK(game) {
		return ""
	}
	return "https://" + host + "/" + game
}

// hbSlugOK accepts the letters, digits, - and _ itch.io slugs are made of.
func hbSlugOK(s string) bool {
	if s == "" || len(s) > 100 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// hbLinkAuthor is the author slug of a normalised game link ("" if none).
func hbLinkAuthor(link string) string {
	rest := strings.TrimPrefix(link, "https://")
	host, _, _ := strings.Cut(rest, "/")
	return strings.TrimSuffix(host, ".itch.io")
}

// hbImageURL keeps an https picture address on itch.io's image hosts (or any
// https host for a cover the API named), "" otherwise. Pure.
func hbImageURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(raw) > 600 {
		return ""
	}
	return u.String()
}

// hbParseTime reads the date forms the feeds and the API use; zero when none
// fits. Pure.
func hbParseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC1123Z, time.RFC1123, time.RFC3339Nano, time.RFC3339,
		"2006-01-02 15:04:05.000000", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02",
		"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// hbPriceFree reports whether a price string ("$0.00", "0,00 €", "Free",
// "") costs nothing. A price with any non-zero digit costs something. Pure.
func hbPriceFree(p string) bool {
	p = strings.TrimSpace(strings.ToLower(p))
	if p == "" || p == "free" || strings.Contains(p, "name your own price") {
		return true
	}
	digits := false
	for _, r := range p {
		if r >= '1' && r <= '9' {
			return false
		}
		if r == '0' {
			digits = true
		}
	}
	return digits || !strings.ContainsAny(p, "0123456789")
}

// hbHTMLText turns a description's HTML into plain text: paragraphs and line
// breaks kept, list items as bullets, tags dropped, entities decoded, at most
// max runes. Pure.
func hbHTMLText(raw string, max int) string {
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(raw); {
		c := raw[i]
		if c != '<' {
			j := strings.IndexByte(raw[i:], '<')
			if j < 0 {
				j = len(raw) - i
			}
			b.WriteString(raw[i : i+j])
			i += j
			continue
		}
		end := strings.IndexByte(raw[i:], '>')
		if end < 0 {
			break
		}
		tag := strings.ToLower(strings.TrimSpace(raw[i+1 : i+end]))
		i += end + 1
		closing := strings.HasPrefix(tag, "/")
		name := strings.TrimPrefix(tag, "/")
		if k := strings.IndexAny(name, " \t\r\n/"); k >= 0 {
			name = name[:k]
		}
		switch name {
		case "br":
			b.WriteString("\n")
		case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "table", "tr", "blockquote", "pre":
			b.WriteString("\n\n")
		case "li":
			if !closing {
				b.WriteString("\n• ")
			}
		case "script", "style":
			if !closing {
				// Skip to the matching close tag. Compared in place: lower-
				// casing the rest first can change its length (invalid
				// UTF-8, some capitals) and misplace the cut.
				end := len(raw)
				for k := i; k+2+len(name) <= len(raw); k++ {
					if raw[k] == '<' && raw[k+1] == '/' && strings.EqualFold(raw[k+2:k+2+len(name)], name) {
						end = k
						break
					}
				}
				i = end
			}
		}
	}
	text := html.UnescapeString(b.String())
	// Spaces collapse within lines; runs of blank lines become one.
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	var out []string
	blank := true
	for _, ln := range lines {
		ln = strings.Join(strings.Fields(ln), " ")
		if ln == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, ln)
		blank = false
	}
	s := strings.TrimSpace(strings.Join(out, "\n"))
	if r := []rune(s); len(r) > max {
		s = strings.TrimSpace(string(r[:max])) + "…"
	}
	return s
}

// ── JSON helpers ─────────────────────────────────────────────────────────

// hbJSONField is the first of names present in m.
func hbJSONField(m map[string]interface{}, names ...string) (interface{}, bool) {
	for _, n := range names {
		if v, ok := m[n]; ok && v != nil {
			return v, true
		}
	}
	return nil, false
}

// hbJSONString reads a string (or a number, as text).
func hbJSONString(m map[string]interface{}, names ...string) string {
	v, ok := hbJSONField(m, names...)
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}

// hbJSONInt reads a whole number given as a number or as digits.
func hbJSONInt(m map[string]interface{}, names ...string) int64 {
	v, ok := hbJSONField(m, names...)
	if !ok {
		return 0
	}
	switch t := v.(type) {
	case float64:
		if t > 0 && t < 1e15 && t == float64(int64(t)) {
			return int64(t)
		}
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// hbJSONBool reads true, 1 or "true".
func hbJSONBool(m map[string]interface{}, names ...string) bool {
	v, ok := hbJSONField(m, names...)
	if !ok {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return strings.EqualFold(t, "true") || t == "1"
	}
	return false
}

// ── data.json ────────────────────────────────────────────────────────────

// hbGameData is what a game's data.json says.
type hbGameData struct {
	ID     int64
	Title  string
	Price  string
	Cover  string
	Author string
}

// hbParseGameData reads data.json; an answer without an id is an error. Pure.
func hbParseGameData(raw []byte) (hbGameData, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return hbGameData{}, err
	}
	if inner, ok := m["game"].(map[string]interface{}); ok {
		m = inner
	}
	d := hbGameData{
		ID:    hbJSONInt(m, "id"),
		Title: hbOneLine(hbJSONString(m, "title"), 120),
		Price: hbOneLine(hbJSONString(m, "price"), 24),
		Cover: hbImageURL(hbJSONString(m, "cover_image", "cover_url", "coverUrl", "cover")),
	}
	if d.ID <= 0 {
		return hbGameData{}, errors.New("itch.io's game data has no id")
	}
	if list, ok := m["authors"].([]interface{}); ok {
		for _, a := range list {
			if am, ok := a.(map[string]interface{}); ok {
				if name := hbOneLine(hbJSONString(am, "name", "display_name", "username"), 60); name != "" {
					d.Author = name
					break
				}
			}
		}
	}
	return d, nil
}

// ── The API ──────────────────────────────────────────────────────────────

// hbUpload is one file of a game.
type hbUpload struct {
	ID       int64
	Filename string
	Display  string
	Type     string
	Storage  string
	MD5      string
	Updated  string // as the API sent it, compared as text
	BuildID  int64
	Size     int64
	Demo     bool
	Preorder bool
	PC       bool // Windows, Mac or Linux
	Android  bool
}

// hbUploadMax bounds the uploads one game may list.
const hbUploadMax = 200

// hbParseUploads reads /games/ID/uploads: {"uploads": [...]}. Pure.
func hbParseUploads(raw []byte) ([]hbUpload, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if msg := hbAPIErrors(doc); msg != "" {
		return nil, errors.New(msg)
	}
	list, _ := doc["uploads"].([]interface{})
	out := make([]hbUpload, 0, len(list))
	for _, v := range list {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		u := hbUpload{
			ID:       hbJSONInt(m, "id"),
			Filename: hbOneLine(hbJSONString(m, "filename"), 200),
			Display:  hbOneLine(hbJSONString(m, "display_name", "displayName"), 120),
			Type:     strings.ToLower(hbJSONString(m, "type")),
			Storage:  strings.ToLower(hbJSONString(m, "storage")),
			MD5:      strings.ToLower(hbJSONString(m, "md5_hash", "md5Hash", "md5")),
			Updated:  hbJSONString(m, "updated_at", "updatedAt"),
			BuildID:  hbJSONInt(m, "build_id", "buildId"),
			Size:     hbJSONInt(m, "size"),
			Demo:     hbJSONBool(m, "demo"),
			Preorder: hbJSONBool(m, "preorder"),
		}
		if u.ID <= 0 {
			continue
		}
		if b, ok := m["build"].(map[string]interface{}); ok && u.BuildID == 0 {
			u.BuildID = hbJSONInt(b, "id")
		}
		u.PC = hbJSONBool(m, "p_windows", "pWindows") || hbJSONBool(m, "p_osx", "pOsx") || hbJSONBool(m, "p_linux", "pLinux")
		u.Android = hbJSONBool(m, "p_android", "pAndroid")
		if traits, ok := m["traits"].([]interface{}); ok {
			for _, t := range traits {
				switch s, _ := t.(string); strings.ToLower(s) {
				case "p_windows", "p_osx", "p_linux":
					u.PC = true
				case "p_android":
					u.Android = true
				case "demo":
					u.Demo = true
				}
			}
		}
		if p, ok := m["platforms"].(map[string]interface{}); ok {
			for k, v := range p {
				if v == nil || v == false || v == "" {
					continue
				}
				switch strings.ToLower(k) {
				case "windows", "osx", "linux", "macos":
					u.PC = true
				case "android":
					u.Android = true
				}
			}
		}
		out = append(out, u)
		if len(out) >= hbUploadMax {
			break
		}
	}
	return out, nil
}

// hbAPIErrors is the API's {"errors": ["..."]} as one line, "" when none.
func hbAPIErrors(doc map[string]interface{}) string {
	list, ok := doc["errors"].([]interface{})
	if !ok || len(list) == 0 {
		return ""
	}
	var parts []string
	for _, e := range list {
		if s, ok := e.(string); ok && s != "" {
			parts = append(parts, hbOneLine(s, 120))
		}
		if len(parts) >= 3 {
			break
		}
	}
	if len(parts) == 0 {
		return "itch.io answered with an error"
	}
	return "itch.io: " + strings.Join(parts, "; ")
}

// hbOwnedGame is one game the user owns (bought or claimed).
type hbOwnedGame struct {
	GameID int64
	KeyID  int64
	Title  string
	Link   string
	Cover  string
	Author string
}

// hbParseOwnedKeys reads one page of /profile/owned-keys: the games and
// whether another page may follow. Pure.
func hbParseOwnedKeys(raw []byte) ([]hbOwnedGame, bool, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, false, err
	}
	if msg := hbAPIErrors(doc); msg != "" {
		return nil, false, errors.New(msg)
	}
	list, _ := doc["owned_keys"].([]interface{})
	out := make([]hbOwnedGame, 0, len(list))
	for _, v := range list {
		m, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		o := hbOwnedGame{KeyID: hbJSONInt(m, "id"), GameID: hbJSONInt(m, "game_id", "gameId")}
		if g, ok := m["game"].(map[string]interface{}); ok {
			if o.GameID == 0 {
				o.GameID = hbJSONInt(g, "id")
			}
			o.Title = hbOneLine(hbJSONString(g, "title"), 120)
			o.Link = hbNormalizeLink(hbJSONString(g, "url"))
			o.Cover = hbImageURL(hbJSONString(g, "cover_url", "coverUrl", "still_cover_url"))
			if u, ok := g["user"].(map[string]interface{}); ok {
				o.Author = hbOneLine(hbJSONString(u, "display_name", "displayName", "username"), 60)
			}
		}
		if o.GameID <= 0 || o.KeyID <= 0 {
			continue
		}
		out = append(out, o)
	}
	// A short page is not always the last one (keys of deleted games drop
	// out of a page), so only an empty page ends the list; hbFetchOwned also
	// stops on a page that brings nothing new.
	return out, len(list) > 0, nil
}

// hbParseProfile reads /profile: the account's name, "" when the answer is
// not a profile. Pure.
func hbParseProfile(raw []byte) (string, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	if msg := hbAPIErrors(doc); msg != "" {
		return "", errors.New(msg)
	}
	u, ok := doc["user"].(map[string]interface{})
	if !ok {
		return "", errors.New("itch.io sent no profile")
	}
	name := hbOneLine(hbJSONString(u, "display_name", "displayName"), 60)
	if name == "" {
		name = hbOneLine(hbJSONString(u, "username"), 60)
	}
	if name == "" {
		return "", errors.New("itch.io sent no profile")
	}
	return name, nil
}

// hbParseDownloadURL reads a download answer that is JSON instead of a
// redirect: {"url": "https://…"}. Pure.
func hbParseDownloadURL(raw []byte) string {
	var m map[string]interface{}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	s := hbJSONString(m, "url")
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ""
	}
	return u.String()
}

// ── Which file is the cartridge ──────────────────────────────────────────

// hbSkipUploadTypes are upload types that are never a cartridge.
var hbSkipUploadTypes = map[string]bool{
	"soundtrack": true, "book": true, "video": true, "documentation": true, "mod": true,
	"audio_assets": true, "graphical_assets": true, "sourcecode": true, "html": true,
	"flash": true, "unity": true, "java": true,
}

// hbRomExt is a file name's ROM extension, lower case, ".p8.png" kept whole.
// Pure.
func hbRomExt(name string) string {
	low := strings.ToLower(path.Base(strings.ReplaceAll(name, `\`, "/")))
	if strings.HasSuffix(low, ".p8.png") {
		return ".p8.png"
	}
	return path.Ext(low)
}

// hbConsoleForExt is the console (hbConsoles index) a ROM extension belongs
// to, or -1. A Mega Drive ".bin" only counts when want includes the Mega
// Drive: elsewhere .bin is anything. Pure.
func hbConsoleForExt(ext string, want uint16) int {
	for i, c := range hbConsoles {
		for _, e := range c.exts {
			if e != ext {
				continue
			}
			if ext == ".bin" && want&(1<<uint(i)) == 0 {
				continue
			}
			return i
		}
	}
	return -1
}

// hbPick is the upload chosen for an install.
type hbPick struct {
	Upload  hbUpload
	Console int  // -1 for a zip, decided once it is opened
	Zip     bool // the cartridge is inside a .zip
}

// hbPickUpload chooses the cartridge among a game's uploads: a ROM for one
// of the consoles the game is listed under (want, a bit per hbConsoles
// index) first, a ROM for another of the ten next, a .zip last; a full game
// before a demo. ok is false when nothing can be a cartridge. Pure.
func hbPickUpload(ups []hbUpload, want uint16) (hbPick, bool) {
	best, bestScore := hbPick{}, 0
	for _, u := range ups {
		if u.Preorder || hbSkipUploadTypes[u.Type] || u.Storage == "external" {
			continue
		}
		ext := hbRomExt(u.Filename)
		score, console, zip := 0, -1, false
		if c := hbConsoleForExt(ext, want); c >= 0 {
			console = c
			score = 20
			if want&(1<<uint(c)) != 0 {
				score = 30
			}
			// A platform flag on a .gb is a mistake on the page; on a zip it
			// is a PC build, below.
		} else if ext == ".zip" && !u.PC && !u.Android {
			score, zip = 10, true
		}
		if score == 0 {
			continue
		}
		if !u.Demo {
			score += 5
		}
		if score > bestScore {
			best, bestScore = hbPick{Upload: u, Console: console, Zip: zip}, score
		}
	}
	return best, bestScore > 0
}

// hbZipRomPick chooses the cartridge inside a zip: entry names and sizes in,
// the index of the best ROM out (the game's own consoles first, then the
// biggest), or -1. Folders, hidden files and Mac resource forks are skipped.
// Pure.
func hbZipRomPick(names []string, sizes []int64, want uint16) (int, int) {
	best, bestConsole, bestScore := -1, -1, int64(-1)
	for i, n := range names {
		clean := strings.ReplaceAll(n, `\`, "/")
		base := path.Base(clean)
		if strings.HasSuffix(clean, "/") || strings.HasPrefix(base, ".") || strings.Contains(clean, "__MACOSX") {
			continue
		}
		c := hbConsoleForExt(hbRomExt(base), want)
		if c < 0 {
			continue
		}
		var size int64
		if i < len(sizes) {
			size = sizes[i]
		}
		score := size
		if want&(1<<uint(c)) != 0 {
			score += 1 << 40
		}
		if score > bestScore {
			best, bestConsole, bestScore = i, c, score
		}
	}
	return best, bestConsole
}

// hbCleanFileName is a title as a file name stem: no path or reserved
// characters, spaces collapsed, no leading or trailing dots, at most 80
// runes; fallback when nothing is left. Pure.
func hbCleanFileName(title, fallback string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r < 32 || r == 127:
			continue
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	s = strings.Trim(s, ". ")
	if s == "" || hbReservedName(s) {
		if fallback != "" && fallback != title {
			return hbCleanFileName(fallback, "")
		}
		return "Homebrew game"
	}
	return s
}

// hbReservedName reports a name Windows keeps for devices (CON, NUL, COM1 …),
// which no file may have there. Pure.
func hbReservedName(s string) bool {
	switch u := strings.ToUpper(s); u {
	case "CON", "NUL", "PRN", "AUX":
		return true
	default:
		return len(u) == 4 && (strings.HasPrefix(u, "COM") || strings.HasPrefix(u, "LPT")) && u[3] >= '1' && u[3] <= '9'
	}
}

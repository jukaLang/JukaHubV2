package main

// wasm4_carts.go — the WASM-4 carts list, and putting a cart on the shelf.
//
// wasm4.org lists the console's published games ("carts") on its Play page.
// The site is built from github.com/aduros/wasm4, folder site/: each cart is
// three files in site/static/carts — <id>.wasm (the game), <id>.png (a
// 160×160 screenshot) and <id>.md (its manual, whose front matter names the
// author and the date and whose first heading is the title) — served at
// wasm4.org/carts/<id>.* and played at wasm4.org/play/<id>. Each cart is its
// author's work under the licence its page states; JukaHub installs one for
// the player's own play, names the author and links the page, and changes
// nothing in the file.
//
// The list, first that answers:
//  1. wasm4.org/carts.json, the catalogue the site publishes (WASM-4's own
//     in-runtime store reads it). Its shape is read loosely
//     (wasm4ParseCartsJSON): a list of objects, or one under "carts", with
//     the id under slug/id/cart (else the .wasm file's name), the title under
//     title/name, the author as a string or an object, and a date.
//  2. GitHub's listing of site/static/carts (git trees API): every <id>.wasm
//     is a cart, titled from its id until its manual has been read
//     (wasm4ParseCartTree, wasm4ParseCartMD).
// The list is saved as data/wasm4/carts.json in JukaHub's own shape, so a
// visit offline shows the last one; it is read again at most once per
// wasm4CartsTTL of this run (a monotonic clock: a TrimUI boots before NTP).
//
// Installing (one at a time, on a goroutine, results posted back with
// tbxPost): the .wasm (at most wasm4CartMax, starting with WebAssembly's
// magic and version) into the WASM-4 Roms folder — the card's own (any
// alias, a NextUI tagged folder), else Roms/WASM4 — under the cart's clean
// title, through a hidden .part file, flushed and renamed; its screenshot,
// scaled up 3× by whole pixels, as the cover where the library looks first
// (romArtworkPath under the game-art folder), unless a cover is already there
// that this screen did not put. data/wasm4/installed.json records the file,
// its size and SHA-256, and the cover, so Delete (and a reinstall) touches
// only what this screen put (a player's own file that later takes the name is
// left alone) and never the saves.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// wasm4CartsJSONURL is the site's catalogue.
	wasm4CartsJSONURL = "https://wasm4.org/carts.json"
	// wasm4CartsTreeURL lists site/static/carts through GitHub's API.
	wasm4CartsTreeURL = "https://api.github.com/repos/aduros/wasm4/git/trees/main:site/static/carts"
	// wasm4SiteCarts and wasm4RawCarts serve a cart's files.
	wasm4SiteCarts = "https://wasm4.org/carts/"
	wasm4RawCarts  = "https://raw.githubusercontent.com/aduros/wasm4/main/site/static/carts/"
	// wasm4PlayPage is a cart's page on the site.
	wasm4PlayPage = "https://wasm4.org/play/"
	// wasm4CartsTTL is how long a list read in this run is fresh.
	wasm4CartsTTL = 12 * time.Hour
	// wasm4ListMax bounds a list download; wasm4CartMax a cart (the console
	// allows 64 KB); wasm4ShotMax a screenshot; wasm4MDMax a manual.
	wasm4ListMax = 8 << 20
	wasm4CartMax = 128 << 10
	wasm4ShotMax = 1 << 20
	wasm4MDMax   = 256 << 10
	// wasm4ListMaxCarts bounds the carts kept from one list (wasm4.org has a
	// few hundred): the screen lays every row out each frame.
	wasm4ListMaxCarts = 5000
	// wasm4CoverScale is the whole-pixel scale of a saved cover.
	wasm4CoverScale = 3
	// wasm4UserAgent names JukaHub to the sites.
	wasm4UserAgent = "JukaHub-WASM4Carts/1.0 (+https://jukalang.com)"
)

// wasm4Cart is one cart of the list.
type wasm4Cart struct {
	Slug   string `json:"slug"`
	Title  string `json:"title"`
	Author string `json:"author,omitempty"`
	Date   string `json:"date,omitempty"`
}

// wasm4CartsFile is data/wasm4/carts.json.
type wasm4CartsFile struct {
	Version int         `json:"version"`
	Source  string      `json:"source"`
	Carts   []wasm4Cart `json:"carts"`
}

// wasm4Installed is one installed cart in data/wasm4/installed.json: the
// file, its size and SHA-256 as written (Delete and a reinstall touch the file
// only while it still is that one), and the cover this screen saved.
type wasm4Installed struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
	Cover  string `json:"cover,omitempty"`
}

type wasm4InstalledFile struct {
	Version int                       `json:"version"`
	Carts   map[string]wasm4Installed `json:"carts"`
}

// wasm4Files guards the two JSON files; every read and write holds it.
var wasm4Files sync.Mutex

var wasm4SlugRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// wasm4SlugOK reports an id that is safe in a URL and a file name (the
// screenshot cache is named after it: no Windows device name such as "con").
func wasm4SlugOK(s string) bool { return wasm4SlugRe.MatchString(s) && !hbReservedName(s) }

// wasm4PageURL is a cart's page on wasm4.org.
func wasm4PageURL(slug string) string { return wasm4PlayPage + slug }

// ── Parsers (outside data) ───────────────────────────────────────────────

// wasm4TitleFromSlug turns "rolly-dango" into "Rolly Dango". Pure.
func wasm4TitleFromSlug(slug string) string {
	words := strings.FieldsFunc(slug, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// wasm4AuthorName keeps the name of a "Name <contact>" author, the form the
// carts' manuals use (the contact is a GitHub user, a mail address or a site).
// Pure.
func wasm4AuthorName(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '<'); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	return hbOneLine(s, 80)
}

// wasm4CleanDate keeps a YYYY-MM-DD date ("" otherwise). Pure.
func wasm4CleanDate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 10 {
		s = s[:10]
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return ""
	}
	return s
}

// wasm4JSONText is the first non-empty string field of m among names. Pure.
func wasm4JSONText(m map[string]interface{}, names ...string) string {
	for _, n := range names {
		if s, ok := m[n].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// wasm4JSONAuthor reads an author given as a string, an object or a list.
// Pure.
func wasm4JSONAuthor(v interface{}) string {
	switch a := v.(type) {
	case string:
		return wasm4AuthorName(a)
	case map[string]interface{}:
		if s := wasm4JSONText(a, "name", "username", "github", "login"); s != "" {
			return wasm4AuthorName(s)
		}
	case []interface{}:
		var names []string
		for _, one := range a {
			if s := wasm4JSONAuthor(one); s != "" {
				names = append(names, s)
			}
		}
		return hbOneLine(strings.Join(names, ", "), 80)
	}
	return ""
}

// wasm4ParseCartsJSON reads the site's carts.json loosely (the file comment).
// Entries without a usable id are skipped; ids are unique; the list is sorted
// by title. An answer with no cart at all is an error. Pure.
func wasm4ParseCartsJSON(raw []byte) ([]wasm4Cart, error) {
	var doc interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var items []interface{}
	switch d := doc.(type) {
	case []interface{}:
		items = d
	case map[string]interface{}:
		for _, key := range []string{"carts", "games", "items"} {
			if list, ok := d[key].([]interface{}); ok {
				items = list
				break
			}
		}
	}
	var out []wasm4Cart
	for _, it := range items {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		slug := wasm4JSONText(m, "slug", "id", "cart")
		if slug == "" {
			// The id from the cart's file ("/carts/rolly-dango.wasm").
			if file := wasm4JSONText(m, "wasm", "file", "url", "path"); strings.HasSuffix(strings.ToLower(file), wasm4Ext) {
				base := file[strings.LastIndexAny(file, "/\\")+1:]
				slug = base[:len(base)-len(wasm4Ext)]
			}
		}
		if !wasm4SlugOK(slug) {
			continue
		}
		c := wasm4Cart{Slug: slug}
		c.Title = hbOneLine(wasm4JSONText(m, "title", "name"), 80)
		if c.Title == "" {
			c.Title = wasm4TitleFromSlug(slug)
		}
		if v, ok := m["author"]; ok {
			c.Author = wasm4JSONAuthor(v)
		} else if v, ok := m["authors"]; ok {
			c.Author = wasm4JSONAuthor(v)
		}
		c.Date = wasm4CleanDate(wasm4JSONText(m, "date", "updated", "published"))
		out = append(out, c)
	}
	out = wasm4UniqueCarts(out)
	if len(out) == 0 {
		return nil, errors.New("the carts list names no carts")
	}
	return out, nil
}

// wasm4ParseCartTree reads GitHub's listing of site/static/carts: the git
// trees API's {"tree": [{"path", "type"}…]}, or the contents API's
// [{"name", "type"}…]. Every <id>.wasm file is a cart. Pure.
func wasm4ParseCartTree(raw []byte) ([]wasm4Cart, error) {
	type entry struct {
		Path string `json:"path"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	var tree struct {
		Tree      []entry `json:"tree"`
		Truncated bool    `json:"truncated"`
	}
	var entries []entry
	if err := json.Unmarshal(raw, &tree); err == nil && tree.Tree != nil {
		entries = tree.Tree
	} else if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	var out []wasm4Cart
	for _, e := range entries {
		name := e.Path
		if name == "" {
			name = e.Name
		}
		if (e.Type != "blob" && e.Type != "file") || strings.Contains(name, "/") || !strings.HasSuffix(name, wasm4Ext) {
			continue
		}
		slug := strings.TrimSuffix(name, wasm4Ext)
		if !wasm4SlugOK(slug) {
			continue
		}
		out = append(out, wasm4Cart{Slug: slug, Title: wasm4TitleFromSlug(slug)})
	}
	out = wasm4UniqueCarts(out)
	if len(out) == 0 {
		return nil, errors.New("the carts folder lists no carts")
	}
	return out, nil
}

// wasm4ParseCartMD reads a cart's manual: the front matter's author and date
// and the first "# " heading as the title. Missing parts are "". Pure.
func wasm4ParseCartMD(raw []byte) (title, author, date string) {
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(text, "\n")
	i := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i = 1; i < len(lines); i++ {
			line := strings.TrimSpace(lines[i])
			if line == "---" {
				i++
				break
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "author":
				author = wasm4AuthorName(value)
			case "date":
				date = wasm4CleanDate(value)
			}
		}
	}
	for ; i < len(lines); i++ {
		if line := strings.TrimSpace(lines[i]); strings.HasPrefix(line, "# ") {
			title = hbOneLine(strings.TrimSpace(line[2:]), 80)
			break
		}
	}
	return title, author, date
}

// wasm4UniqueCarts drops repeated ids (the first wins), keeps at most
// wasm4ListMaxCarts and sorts by title. Pure.
func wasm4UniqueCarts(in []wasm4Cart) []wasm4Cart {
	seen := map[string]bool{}
	out := in[:0]
	for _, c := range in {
		key := strings.ToLower(c.Slug)
		if seen[key] {
			continue
		}
		if len(out) >= wasm4ListMaxCarts {
			break
		}
		seen[key] = true
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Title), strings.ToLower(out[j].Title)
		if a != b {
			return a < b
		}
		return out[i].Slug < out[j].Slug
	})
	return out
}

// wasm4CartOK reports a WebAssembly module: the magic "\0asm" and version 1.
// Pure.
func wasm4CartOK(data []byte) bool {
	return len(data) >= 8 && bytes.Equal(data[:8], []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})
}

// ── Files ────────────────────────────────────────────────────────────────

// wasm4DataDir is data/wasm4, made on demand.
func wasm4DataDir() string {
	dir := filepath.Join("data", "wasm4")
	if base, err := P().DataDir(); err == nil && base != "" {
		dir = filepath.Join(base, "wasm4")
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// wasm4LoadCarts reads the saved list (nil when there is none).
func wasm4LoadCarts() ([]wasm4Cart, string) {
	wasm4Files.Lock()
	defer wasm4Files.Unlock()
	raw, err := os.ReadFile(filepath.Join(wasm4DataDir(), "carts.json"))
	if err != nil || len(raw) > wasm4ListMax {
		return nil, ""
	}
	var f wasm4CartsFile
	if json.Unmarshal(raw, &f) != nil {
		return nil, ""
	}
	var out []wasm4Cart
	for _, c := range f.Carts {
		if wasm4SlugOK(c.Slug) && strings.TrimSpace(c.Title) != "" {
			out = append(out, c)
		}
	}
	return wasm4UniqueCarts(out), f.Source
}

// wasm4SaveCarts writes the list.
func wasm4SaveCarts(carts []wasm4Cart, source string) {
	data, err := json.MarshalIndent(wasm4CartsFile{Version: 1, Source: source, Carts: carts}, "", " ")
	if err != nil {
		return
	}
	wasm4Files.Lock()
	defer wasm4Files.Unlock()
	if err := AtomicWrite(filepath.Join(wasm4DataDir(), "carts.json"), append(data, '\n'), 0o644); err != nil {
		logDebug("wasm4", "saving the carts list: %v", err)
	}
}

// wasm4LoadInstalledLocked reads installed.json; the caller holds wasm4Files.
func wasm4LoadInstalledLocked() map[string]wasm4Installed {
	out := map[string]wasm4Installed{}
	raw, err := os.ReadFile(filepath.Join(wasm4DataDir(), "installed.json"))
	if err != nil || len(raw) > 4<<20 {
		return out
	}
	var f wasm4InstalledFile
	if json.Unmarshal(raw, &f) != nil {
		return out
	}
	for slug, rec := range f.Carts {
		if wasm4SlugOK(slug) && filepath.IsAbs(rec.Path) && strings.EqualFold(filepath.Ext(rec.Path), wasm4Ext) {
			if rec.Cover != "" && !wasm4CoverOK(rec.Cover) {
				rec.Cover = "" // never a file outside the game-art folder
			}
			out[slug] = rec
		}
	}
	return out
}

// wasm4CoverOK reports a cover path this screen may have written: a .png
// inside the library's game-art folder.
func wasm4CoverOK(p string) bool {
	root := libraryArtworkRoot()
	if root == "" || !filepath.IsAbs(p) || !strings.EqualFold(filepath.Ext(p), ".png") {
		return false
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != "." && !filepath.IsAbs(rel) && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// wasm4StillOurs reports whether the file at path is still the one rec
// recorded: a regular file of that size and, when the record has it, that
// SHA-256. Reads the file (at most wasm4CartMax): never on the main thread.
func wasm4StillOurs(path string, rec wasm4Installed) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != rec.Size || rec.Size > wasm4CartMax {
		return false
	}
	if rec.SHA256 == "" {
		return true
	}
	data, err := os.ReadFile(path)
	if err != nil || int64(len(data)) != rec.Size {
		return false
	}
	sum := sha256.Sum256(data)
	return strings.EqualFold(hex.EncodeToString(sum[:]), rec.SHA256)
}

func wasm4SaveInstalledLocked(m map[string]wasm4Installed) error {
	data, err := json.MarshalIndent(wasm4InstalledFile{Version: 1, Carts: m}, "", " ")
	if err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(wasm4DataDir(), "installed.json"), append(data, '\n'), 0o644)
}

// wasm4InstalledNow is every recorded cart whose file is still the one this
// screen wrote (same size). Reads the disk: never on the main thread.
func wasm4InstalledNow() map[string]wasm4Installed {
	wasm4Files.Lock()
	m := wasm4LoadInstalledLocked()
	wasm4Files.Unlock()
	for slug, rec := range m {
		if info, err := os.Stat(rec.Path); err != nil || !info.Mode().IsRegular() || info.Size() != rec.Size {
			delete(m, slug)
		}
	}
	return m
}

// wasm4CartsDir is the WASM-4 Roms folder as an absolute path: one the card
// already has (any alias, a NextUI tagged folder), else WASM4 made in the
// first Roms folder that is there, else in the first absolute one.
func wasm4CartsDir() (string, error) {
	roots := romLibraryRoots()
	for _, root := range roots {
		for _, name := range rommListDirs(root) {
			// A NextUI "Name (TAG)" folder counts only on NextUI: elsewhere
			// the library does not read it (scanROMLibrary).
			if s, tagged, found := romFolderSystem(name, nil); found && s.Name == wasm4System && (!tagged || nextuiActive()) {
				return filepath.Abs(filepath.Join(root, name))
			}
		}
	}
	for _, mustExist := range []bool{true, false} {
		for _, root := range roots {
			if !filepath.IsAbs(root) || (mustExist && !dirExists(root)) {
				continue
			}
			dir := filepath.Join(root, wasm4Folder)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			return dir, nil
		}
	}
	return "", errors.New("there is no Roms folder on the card")
}

// ── Network ──────────────────────────────────────────────────────────────

// wasm4Client is the screen's HTTP client: https redirects only, five at
// most (hbClient's rule).
func wasm4Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: ytTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return errors.New("the file is on an insecure address")
			}
			return nil
		}}
}

// wasm4Get reads url with a size bound. A status other than 200 is an error.
func wasm4Get(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", wasm4UserAgent)
	if strings.HasPrefix(url, "https://api.github.com/") {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	resp, err := wasm4Client(45 * time.Second).Do(req)
	if err != nil {
		return nil, hbNetError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", req.URL.Host, resp.StatusCode)
	}
	if resp.ContentLength > max {
		return nil, fmt.Errorf("%s sent more than expected", req.URL.Host)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, hbNetError(err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s sent more than expected", req.URL.Host)
	}
	return data, nil
}

// wasm4GetCartFile reads one of a cart's files (ext ".wasm", ".png", ".md"):
// from the site, else from the repository.
func wasm4GetCartFile(ctx context.Context, slug, ext string, max int64) ([]byte, error) {
	if !wasm4SlugOK(slug) {
		return nil, errors.New("not a cart id")
	}
	data, err := wasm4Get(ctx, wasm4SiteCarts+slug+ext, max)
	if err == nil || ctx.Err() != nil {
		return data, err
	}
	data, err2 := wasm4Get(ctx, wasm4RawCarts+slug+ext, max)
	if err2 != nil {
		return nil, err
	}
	return data, nil
}

// wasm4FetchCarts reads the list from the network (the file comment's order)
// and returns it with its source's name.
func wasm4FetchCarts(ctx context.Context) ([]wasm4Cart, string, error) {
	raw, err := wasm4Get(ctx, wasm4CartsJSONURL, wasm4ListMax)
	if err == nil {
		carts, perr := wasm4ParseCartsJSON(raw)
		if perr == nil {
			return carts, "wasm4.org", nil
		}
		err = perr
	}
	logInfo("wasm4", "wasm4.org's carts.json: %v; reading GitHub's listing", err)
	raw, err2 := wasm4Get(ctx, wasm4CartsTreeURL, wasm4ListMax)
	if err2 != nil {
		return nil, "", err2
	}
	carts, perr := wasm4ParseCartTree(raw)
	if perr != nil {
		return nil, "", perr
	}
	return carts, "github.com/aduros/wasm4", nil
}

// wasm4ShotPath is the cached screenshot of a cart for the screen's preview.
func wasm4ShotPath(slug string) string {
	return filepath.Join(wasm4DataDir(), "shots", slug+".png")
}

// wasm4FetchShot caches a cart's screenshot (a PNG that decodes, at most
// 1024 pixels a side). Off the main thread.
func wasm4FetchShot(ctx context.Context, slug string) error {
	path := wasm4ShotPath(slug)
	if regularFile(path) {
		return nil
	}
	data, err := wasm4GetCartFile(ctx, slug, ".png", wasm4ShotMax)
	if err != nil {
		return err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 1024 || cfg.Height > 1024 {
		return errors.New("the screenshot is not a picture JukaHub can show")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return pstoreWriteFile(path, data)
}

// ── Install and delete ───────────────────────────────────────────────────

// wasm4InstallCart puts one cart on the shelf and returns its file. Off the
// main thread; one install runs at a time (the screen's queue).
func wasm4InstallCart(ctx context.Context, c wasm4Cart) (string, error) {
	data, err := wasm4GetCartFile(ctx, c.Slug, wasm4Ext, wasm4CartMax)
	if err != nil {
		return "", err
	}
	if !wasm4CartOK(data) {
		return "", errors.New("the download is not a WASM-4 cart")
	}
	dir, err := wasm4CartsDir()
	if err != nil {
		return "", err
	}
	wasm4Files.Lock()
	installed := wasm4LoadInstalledLocked()
	wasm4Files.Unlock()
	prev, hadPrev := installed[c.Slug]
	if hadPrev && filepath.Dir(prev.Path) != filepath.Clean(dir) {
		hadPrev = false // another folder: written fresh here
	}
	// The clean title; a file of that name that is not ours gets the id added.
	// Ours is the file this screen wrote last, unchanged since (a player's own
	// file that took the name, or an edit, is left alone).
	clean := hbCleanFileName(c.Title, wasm4TitleFromSlug(c.Slug))
	dest := ""
	for _, name := range []string{clean, clean + " (" + c.Slug + ")"} {
		p := filepath.Join(dir, name+wasm4Ext)
		if !pathExists(p) || (hadPrev && strings.EqualFold(p, prev.Path) && wasm4StillOurs(p, prev)) {
			dest = p
			break
		}
	}
	if dest == "" {
		return "", fmt.Errorf("%s already has a file named %s that JukaHub did not install", filepath.Base(dir), clean+wasm4Ext)
	}
	if err := wasm4WriteFile(dest, data); err != nil {
		return "", err
	}

	// The cover, where the library looks first, unless one is there that
	// this screen did not put.
	cover := ""
	if root := libraryArtworkRoot(); root != "" {
		p := romArtworkPath(scannedROM{Path: dest, System: wasm4System, Collection: wasm4Collection}, root)
		if p != "" && (!artworkCoverPresent(p) || (hadPrev && prev.Cover == p)) {
			if err := wasm4SaveCover(ctx, c.Slug, p); err != nil {
				logDebug("wasm4", "cover for %s: %v", c.Slug, err)
			} else {
				cover = p
			}
		}
	}
	wasm4Files.Lock()
	defer wasm4Files.Unlock()
	installed = wasm4LoadInstalledLocked()
	sum := sha256.Sum256(data)
	installed[c.Slug] = wasm4Installed{Path: dest, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Cover: cover}
	if err := wasm4SaveInstalledLocked(installed); err != nil {
		logWarn("wasm4", "recording %s: %v", c.Slug, err)
	}
	logInfo("wasm4", "%s (%s) installed as %s", c.Title, c.Slug, dest)
	return dest, nil
}

// wasm4WriteFile writes data to dest through a hidden .part file beside it,
// flushed before the rename.
func wasm4WriteFile(dest string, data []byte) error {
	part := filepath.Join(filepath.Dir(dest), "."+filepath.Base(dest)+".part")
	defer os.Remove(part)
	f, err := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	switch {
	case werr != nil:
		return werr
	case serr != nil:
		return serr
	case cerr != nil:
		return cerr
	}
	return os.Rename(part, dest)
}

// wasm4SaveCover saves a cart's screenshot as its cover, scaled up by whole
// pixels so the library's picture stays sharp.
func wasm4SaveCover(ctx context.Context, slug, dest string) error {
	data, err := wasm4GetCartFile(ctx, slug, ".png", wasm4ShotMax)
	if err != nil {
		return err
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 1024 || cfg.Height > 1024 {
		return errors.New("the screenshot is not a picture JukaHub can use")
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, wasm4ScaleNearest(img, wasm4CoverScale)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return pstoreWriteFile(dest, buf.Bytes())
}

// wasm4ScaleNearest scales src up n times, each pixel a block. A picture
// already larger than 256 pixels a side is kept as it is.
func wasm4ScaleNearest(src image.Image, n int) image.Image {
	b := src.Bounds()
	if n <= 1 || b.Dx() > 256 || b.Dy() > 256 {
		return src
	}
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx()*n, b.Dy()*n))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := src.At(b.Min.X+x, b.Min.Y+y)
			for dy := 0; dy < n; dy++ {
				for dx := 0; dx < n; dx++ {
					out.Set(x*n+dx, y*n+dy, c)
				}
			}
		}
	}
	return out
}

// wasm4DeleteCart removes a cart this screen installed and the cover it put;
// its saves stay. Off the main thread.
func wasm4DeleteCart(slug string) error {
	wasm4Files.Lock()
	defer wasm4Files.Unlock()
	installed := wasm4LoadInstalledLocked()
	rec, ok := installed[slug]
	if !ok {
		return errors.New("it was not installed from this screen")
	}
	if info, err := os.Stat(rec.Path); err == nil && info.Mode().IsRegular() {
		if !wasm4StillOurs(rec.Path, rec) {
			return errors.New("the file there is not the one this screen installed — delete it in Files")
		}
		if err := os.Remove(rec.Path); err != nil {
			return err
		}
	}
	if rec.Cover != "" {
		_ = os.Remove(rec.Cover)
	}
	delete(installed, slug)
	return wasm4SaveInstalledLocked(installed)
}

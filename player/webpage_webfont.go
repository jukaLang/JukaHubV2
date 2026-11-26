package main

// webpage_webfont.go — a page's own fonts (@font-face).
//
// The reader used to set every page in its three faces (the UI face, a
// monospace and a serif one): a site's typeface was lost, and an icon font
// drew nothing at all — Font Awesome's code points were dropped, so a toolbar
// of icon buttons was a row of empty chips. Now the @font-face rules in force
// are read with the page's style sheets, a family named in font-family before
// any family the reader knows is the run's web font (webStyle.WebFont), and
// the files of the families text is actually set in — regular and bold, the
// subset that holds Latin letters or an icon font's private-use glyphs — are
// fetched beside the page's pictures. Each file that arrives lays the page out
// again in it; until then, and for any word the file has no glyphs for, the
// reader's own face sets the text.
//
// SDL_ttf reads TrueType and OpenType, and WOFF is those tables zlib-packed,
// unpacked here. WOFF2 — what most sites serve now, often alone — is a Brotli
// stream with its outlines transformed, rebuilt by webfont_woff2.go. Google
// Fonts, which answers a browser with WOFF2, is still asked for its style
// sheet the way a plain HTTP client is and answers with TrueType, which needs
// no unpacking at all.
//
// Files are kept, by address, for the rest of the run (another page of the
// same site reuses them), and faces are opened at the reader's sizes and never
// closed, like every reader face; both are capped, tighter on a handheld, and
// past a cap pages keep the reader's faces. Legible reading font and Bold text
// keep every page in the reader's faces.

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// webMaxFontFaceRules bounds the @font-face rules one page keeps.
	webMaxFontFaceRules = 256
	// webMaxWebFamilies bounds the families those rules name.
	webMaxWebFamilies = 64
	// webFontPassTimeout bounds the whole font pass of a page, and
	// webFontFetchTimeout one file.
	webFontPassTimeout  = 20 * time.Second
	webFontFetchTimeout = 12 * time.Second
	// webFontsSheetAgent is the identity Google Fonts' style sheets are asked
	// for with: a plain HTTP client gets TrueType files, a browser WOFF2.
	webFontsSheetAgent = "curl/8.5.0"
	// webFontCoverPrefix names a web font's character map (registerFontPath).
	webFontCoverPrefix = "webfont:"
)

// webFontFace is one usable @font-face rule: its family (an index into
// webDocument.WebFamilies), weight range, slant and sources in order.
type webFontFace struct {
	family             int
	weightLo, weightHi int
	italic             bool
	srcs               []string
}

// webFontLimits are the caps of the font pass: files fetched for one page,
// bytes of one file, bytes of every file kept, and faces opened.
func webFontLimits() (files int, fileBytes, totalBytes int64, faces int) {
	if IsTrimuiDevice() {
		return 8, 2 << 20, 24 << 20, 160
	}
	return 24, 6 << 20, 96 << 20, 480
}

// webFontStore holds the fonts fetched this run, unpacked to sfnt, by address
// (a data: URI by its hash). bad remembers sources that are no font.
var webFontStore = struct {
	sync.Mutex
	data  map[string][]byte
	bad   map[string]bool
	bytes int64
}{data: map[string][]byte{}, bad: map[string]bool{}}

// The faces opened from them, by file, size and style flags, and the files
// whose character map is registered. Render thread.
var (
	webFontFaces   = map[string]*ttf.Font{}
	webFontFailed  = map[string]bool{}
	webFontCovered = map[string]bool{}
)

// --- Style sheets -------------------------------------------------------------

// webFontSetup reads the bodies of the page's @font-face rules into the
// document's faces and families.
func (p *webParser) webFontSetup(bodies []string) {
	for _, body := range bodies {
		decls, _ := parseWebDeclsSplit(body)
		family := webFontFamilyName(decls["font-family"])
		if family == "" || !webFontRangeUseful(decls["unicode-range"]) {
			continue
		}
		srcs := p.webFontSources(decls["src"])
		if len(srcs) == 0 {
			continue
		}
		idx, ok := p.webFamilies[family]
		if !ok {
			if len(p.doc.WebFamilies) >= webMaxWebFamilies {
				continue
			}
			if p.webFamilies == nil {
				p.webFamilies = make(map[string]int, 4)
			}
			p.doc.WebFamilies = append(p.doc.WebFamilies, family)
			idx = len(p.doc.WebFamilies)
			p.webFamilies[family] = idx
		}
		lo, hi := webFontWeightRange(decls["font-weight"])
		slant := strings.ToLower(decls["font-style"])
		p.doc.WebFonts = append(p.doc.WebFonts, webFontFace{
			family: idx - 1, weightLo: lo, weightHi: hi,
			italic: strings.Contains(slant, "italic") || strings.Contains(slant, "oblique"),
			srcs:   srcs,
		})
	}
	p.doc.webFontUse = make([]uint8, len(p.doc.WebFamilies))
}

// webFontFamilyName is a family name as the reader compares it: unquoted,
// lower case, single-spaced.
func webFontFamilyName(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimSpace(strings.TrimSuffix(v, "!important"))
	v = strings.Trim(v, `"'`)
	return strings.Join(strings.Fields(strings.ToLower(v)), " ")
}

// webFontSources are the addresses of a src list the reader can read —
// TrueType, OpenType, WOFF and WOFF2, as their format() or file name says —
// in the author's order. local() faces and EOT and SVG fonts are skipped.
func (p *webParser) webFontSources(src string) []string {
	var out []string
	for _, part := range webSplitTopLevel(src, ',') {
		lower := strings.ToLower(part)
		at := strings.Index(lower, "url(")
		if at < 0 {
			continue
		}
		end := webCSSParenEnd(part, at+3)
		if end < 0 {
			continue
		}
		raw := strings.Trim(strings.TrimSpace(part[at+4:end]), `"'`)
		format := ""
		if f := strings.Index(lower[end:], "format("); f >= 0 {
			rest := part[end+f+len("format("):]
			if c := strings.IndexByte(rest, ')'); c >= 0 {
				format = strings.ToLower(strings.Trim(strings.TrimSpace(rest[:c]), `"'`))
			}
		}
		switch format {
		case "truetype", "opentype", "woff", "woff2", "truetype-variations", "opentype-variations",
			"woff-variations", "woff2-variations":
		case "":
			if strings.HasPrefix(strings.ToLower(raw), "data:") {
				break
			}
			name := raw
			if i := strings.IndexAny(name, "?#"); i >= 0 {
				name = name[:i]
			}
			switch strings.ToLower(path.Ext(name)) {
			case ".eot", ".svg", ".svgz":
				continue
			}
		default:
			continue
		}
		if address := p.webFontURL(raw); address != "" {
			out = append(out, address)
		}
	}
	return out
}

// webFontURL resolves a font address against the page (a linked sheet's
// addresses are already absolute: webRebaseCSSURLs). A base64 data: URI is kept
// as it is, up to 4 MB.
func (p *webParser) webFontURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		if strings.Contains(raw[:min(len(raw), 128)], ";base64,") && len(raw) <= 4<<20 {
			return raw
		}
		return ""
	}
	base, err := url.Parse(p.doc.URL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	abs := base.ResolveReference(ref)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	abs.Fragment = ""
	return abs.String()
}

// webFontWeightRange reads a font-weight descriptor: a keyword, a number, or
// the range a variable font covers ("100 900").
func webFontWeightRange(v string) (int, int) {
	weight := func(s string) (int, bool) {
		switch s {
		case "normal":
			return 400, true
		case "bold":
			return 700, true
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil || n < 1 || n > 1000 {
			return 0, false
		}
		return int(n), true
	}
	fields := strings.Fields(strings.ToLower(v))
	lo, hi := 400, 400
	if len(fields) > 0 {
		if w, ok := weight(fields[0]); ok {
			lo, hi = w, w
		}
	}
	if len(fields) > 1 {
		if w, ok := weight(fields[1]); ok && w >= lo {
			hi = w
		}
	}
	return lo, hi
}

// webFontRangeUseful reports whether a face's unicode-range is one worth a
// download: none (the whole font), or one that holds the Latin letters or an
// icon font's private-use glyphs. Sites split a family into a dozen subsets
// (Cyrillic, Greek, Vietnamese, …); the reader's own face sets the rest.
func webFontRangeUseful(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	for _, token := range strings.Split(v, ",") {
		lo, hi, ok := webUnicodeRangeToken(strings.TrimSpace(token))
		if !ok {
			continue
		}
		if (lo <= 'a' && hi >= 'a') || (lo <= 0xF8FF && hi >= 0xE000) {
			return true
		}
	}
	return false
}

// webUnicodeRangeToken reads one unicode-range token: U+26, U+0-7F, U+4??.
func webUnicodeRangeToken(t string) (rune, rune, bool) {
	if len(t) < 3 || (t[0] != 'U' && t[0] != 'u') || t[1] != '+' {
		return 0, 0, false
	}
	body := t[2:]
	parse := func(s string) (rune, bool) {
		n, err := strconv.ParseUint(s, 16, 32)
		if err != nil || n > 0x10FFFF {
			return 0, false
		}
		return rune(n), true
	}
	if a, b, found := strings.Cut(body, "-"); found {
		lo, ok1 := parse(a)
		hi, ok2 := parse(b)
		return lo, hi, ok1 && ok2 && lo <= hi
	}
	if strings.Contains(body, "?") {
		lo, ok1 := parse(strings.ReplaceAll(body, "?", "0"))
		hi, ok2 := parse(strings.ReplaceAll(body, "?", "F"))
		return lo, hi, ok1 && ok2
	}
	r, ok := parse(body)
	return r, r, ok
}

// --- Matching -------------------------------------------------------------------

// applyWebFontFamily sets st.WebFont from the element's font-family (or the
// family part of its font shorthand).
func (p *webParser) applyWebFontFamily(st *webStyle, merged map[string]string) {
	if len(p.webFamilies) == 0 {
		return
	}
	list, ok := merged["font-family"]
	if !ok {
		v, has := merged["font"]
		if !has {
			return
		}
		if list = webFontShorthandFamily(v); list == "" {
			return
		}
	}
	st.WebFont = p.webFamilyIn(list, st.WebFont)
}

// webFamilyIn is the web font a font-family list asks for: the first family
// in it that is a web font of the page, unless a family the reader knows comes
// first (it is available, so a browser stops there too). A list of nothing
// but unknown names keeps the inherited choice, as webApplyFontFamily does.
func (p *webParser) webFamilyIn(list string, inherited int) int {
	v := strings.ToLower(strings.TrimSpace(list))
	v = strings.TrimSpace(strings.TrimSuffix(v, "!important"))
	switch v {
	case "", "inherit", "unset", "revert", "revert-layer":
		return inherited
	case "initial":
		return 0
	}
	for _, family := range webSplitTopLevel(v, ',') {
		name := webFontFamilyName(family)
		if idx, ok := p.webFamilies[name]; ok {
			return idx
		}
		if webFontFamilyKind(name) != webFamilyUnknown {
			return 0
		}
	}
	return inherited
}

// webFontShorthandFamily is the family list of a font shorthand: what follows
// its size (and line height), as webFontShorthand reads it.
func webFontShorthandFamily(value string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(value)))
	for i, field := range fields {
		token := field
		if j := strings.IndexByte(field, '/'); j >= 0 {
			token = field[:j]
		}
		if !webIsFontSizeToken(token) {
			continue
		}
		if _, ok := webFontSizePx(token, 16); !ok {
			continue
		}
		rest := fields[i+1:]
		if !strings.Contains(field, "/") && len(rest) > 0 && strings.HasPrefix(rest[0], "/") {
			if rest[0] == "/" && len(rest) > 1 {
				rest = rest[2:]
			} else {
				rest = rest[1:]
			}
		}
		return strings.Join(rest, " ")
	}
	return ""
}

// noteWebFontUse marks the web font text is set in, regular or bold, so the
// font pass fetches only files a page draws with.
func (p *webParser) noteWebFontUse(style webStyle, raw string) {
	if style.WebFont <= 0 || style.IconFont || style.WebFont > len(p.doc.webFontUse) || strings.TrimSpace(raw) == "" {
		return
	}
	bit := uint8(1)
	if style.Bold {
		bit = 2
	}
	p.doc.webFontUse[style.WebFont-1] |= bit
}

// webFontBest is the face of family for regular or bold text, by CSS's
// font matching: the weight closest to 400 (lighter first, then heavier) or
// to 700 (heavier first), an upright face before an italic one. -1 for none.
func (d *webDocument) webFontBest(family int, bold bool) int {
	want := 400
	if bold {
		want = 700
	}
	best, bestScore := -1, 1<<30
	for i := range d.WebFonts {
		f := &d.WebFonts[i]
		if f.family != family {
			continue
		}
		near := min(max(want, f.weightLo), f.weightHi)
		score := near - want
		if score < 0 {
			score = -score
			if bold {
				score += 1000
			}
		} else if score > 0 && !bold && near > 500 {
			score += 1000
		}
		if f.italic {
			score += 10000
		}
		if score < bestScore {
			best, bestScore = i, score
		}
	}
	return best
}

// --- Fetching ----------------------------------------------------------------------

// webFontsSheetRequest asks Google Fonts for a style sheet as a plain HTTP
// client does: it answers a browser with WOFF2 files, which the reader cannot
// read, and anything else with TrueType ones.
func webFontsSheetRequest(req *http.Request) {
	if req == nil || req.URL == nil || !strings.EqualFold(req.URL.Hostname(), "fonts.googleapis.com") {
		return
	}
	for k := range req.Header {
		if strings.HasPrefix(strings.ToLower(k), "sec-ch-") {
			req.Header.Del(k)
		}
	}
	req.Header.Set("User-Agent", webFontsSheetAgent)
}

// fetchWebFontsContext fetches the files of the web fonts the page sets text
// in, a few at a time, and lays the page out again as each arrives.
func fetchWebFontsContext(parent context.Context, doc *webDocument) {
	if doc == nil || len(doc.WebFonts) == 0 || len(doc.webFontUse) == 0 {
		return
	}
	files, fileCap, totalCap, _ := webFontLimits()
	type job struct {
		face  int
		picks [][2]int
	}
	var jobs []job
	byFace := make(map[int]int)
	for family, use := range doc.webFontUse {
		for b := 0; b < 2; b++ {
			if use&(1<<b) == 0 {
				continue
			}
			face := doc.webFontBest(family, b == 1)
			if face < 0 {
				continue
			}
			pick := [2]int{family, b}
			if j, ok := byFace[face]; ok {
				jobs[j].picks = append(jobs[j].picks, pick)
				continue
			}
			if len(jobs) >= files {
				continue
			}
			byFace[face] = len(jobs)
			jobs = append(jobs, job{face: face, picks: [][2]int{pick}})
		}
	}
	if len(jobs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(parent, webFontPassTimeout)
	defer cancel()
	var wg sync.WaitGroup
	slots := make(chan struct{}, 3)
	for _, jb := range jobs {
		wg.Add(1)
		go func(jb job) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			key := webFontLoad(ctx, doc.URL, doc.WebFonts[jb.face].srcs, fileCap, totalCap)
			if key == "" {
				return
			}
			doc.webFontMu.Lock()
			if doc.webFontPick == nil {
				doc.webFontPick = make(map[[2]int]string, 4)
			}
			for _, pick := range jb.picks {
				doc.webFontPick[pick] = key
			}
			doc.webFontMu.Unlock()
			doc.noteSizesArrived()
		}(jb)
	}
	wg.Wait()
}

// webFontLoad returns the store key of the first of srcs that loads as a font
// the reader can open, fetching it when the store does not have it yet; ""
// when none does.
func webFontLoad(ctx context.Context, pageURL string, srcs []string, fileCap, totalCap int64) string {
	for _, src := range srcs {
		key := src
		isData := strings.HasPrefix(strings.ToLower(src), "data:")
		if isData {
			sum := sha1.Sum([]byte(src))
			key = "data:" + hex.EncodeToString(sum[:])
		}
		webFontStore.Lock()
		_, have := webFontStore.data[key]
		bad := webFontStore.bad[key]
		webFontStore.Unlock()
		if have {
			return key
		}
		if bad {
			continue
		}
		var raw []byte
		if isData {
			raw = webFontDataURI(src)
		} else {
			raw = webFontDownload(ctx, pageURL, src, fileCap)
		}
		if ctx.Err() != nil {
			return ""
		}
		sfnt, ok := webFontSFNT(raw, fileCap)
		webFontStore.Lock()
		switch {
		case !ok:
			if len(raw) > 0 {
				webFontStore.bad[key] = true
			}
		case webFontStore.bytes+int64(len(sfnt)) > totalCap:
			ok = false
		default:
			webFontStore.data[key] = sfnt
			webFontStore.bytes += int64(len(sfnt))
		}
		webFontStore.Unlock()
		if ok {
			return key
		}
	}
	return ""
}

// webFontDownload fetches one font file with the page's browser identity, nil
// past limit bytes or on any failure.
func webFontDownload(ctx context.Context, pageURL, address string, limit int64) []byte {
	if browserRequestBlocked(pageURL, address) {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil
	}
	browserChromeResource(req.Header, "font", "*/*", browserURL(pageURL), req.URL)
	resp, err := browserHTTPClient(webFontFetchTimeout).Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil
	}
	return body
}

// webFontDataURI decodes a base64 data: URI.
func webFontDataURI(src string) []byte {
	head, payload, ok := strings.Cut(src, ",")
	if !ok || !strings.Contains(strings.ToLower(head), ";base64") {
		return nil
	}
	payload = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r', '\f':
			return -1
		}
		return r
	}, payload)
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		if data, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "=")); err != nil {
			return nil
		}
	}
	return data
}

// webFontSFNT is data as a font SDL_ttf opens — TrueType or OpenType as it
// is, WOFF and WOFF2 unpacked — and false for anything else or for a font
// larger than limit bytes.
func webFontSFNT(data []byte, limit int64) ([]byte, bool) {
	if len(data) < 12 || int64(len(data)) > limit {
		return nil, false
	}
	switch string(data[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "true", "ttcf":
		return data, true
	case "wOFF":
		return webFontUnWOFF(data, limit)
	case "wOF2":
		return webFontUnWOFF2(data, limit)
	}
	return nil, false
}

// webFontUnWOFF rebuilds the sfnt a WOFF 1.0 file packs: its tables, each
// zlib-compressed when that made it smaller, behind a header and directory of
// its own. The checksums are copied; nothing the reader opens fonts with
// checks them.
func webFontUnWOFF(data []byte, limit int64) ([]byte, bool) {
	be := binary.BigEndian
	if len(data) < 44 {
		return nil, false
	}
	numTables := int(be.Uint16(data[12:14]))
	total := int64(be.Uint32(data[16:20]))
	if numTables == 0 || numTables > 512 || total > limit || 44+numTables*20 > len(data) {
		return nil, false
	}
	head := 12 + 16*numTables
	out := make([]byte, head, max(int64(head), total))
	copy(out[0:4], data[4:8])
	be.PutUint16(out[4:6], uint16(numTables))
	selector := 0
	for 1<<(selector+1) <= numTables {
		selector++
	}
	searchRange := (1 << selector) * 16
	be.PutUint16(out[6:8], uint16(searchRange))
	be.PutUint16(out[8:10], uint16(selector))
	be.PutUint16(out[10:12], uint16(numTables*16-searchRange))
	for i := 0; i < numTables; i++ {
		e := data[44+i*20 : 44+i*20+20]
		off, comp, orig := int64(be.Uint32(e[4:8])), int64(be.Uint32(e[8:12])), int64(be.Uint32(e[12:16]))
		if off+comp > int64(len(data)) || orig > limit || comp > orig {
			return nil, false
		}
		table := data[off : off+comp]
		if comp < orig {
			zr, err := zlib.NewReader(bytes.NewReader(table))
			if err != nil {
				return nil, false
			}
			table, err = io.ReadAll(io.LimitReader(zr, orig+1))
			zr.Close()
			if err != nil || int64(len(table)) != orig {
				return nil, false
			}
		}
		rec := out[12+i*16 : 12+i*16+16]
		copy(rec[0:4], e[0:4])
		be.PutUint32(rec[4:8], be.Uint32(e[16:20]))
		be.PutUint32(rec[8:12], uint32(len(out)))
		be.PutUint32(rec[12:16], uint32(orig))
		out = append(out, table...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		if int64(len(out)) > limit {
			return nil, false
		}
	}
	return out, true
}

// --- Faces -----------------------------------------------------------------------------

// webFontFor is the face of web font web (1-based) a run is measured and drawn
// in at size (the reader's size before the legibility floor and accessibility
// scale), or nil while its file has not arrived, past the face cap, and with
// Legible reading font or Bold text on. A bold run uses the family's bold file
// when it has one and the regular file emboldened otherwise; italic is always
// the face slanted. Render thread.
func (d *webDocument) webFontFor(web, size int, bold, italic bool) *ttf.Font {
	if d == nil || web <= 0 || size <= 0 || a11y.boldText || legibleFontFile() != "" {
		return nil
	}
	family := web - 1
	synthBold := false
	d.webFontMu.Lock()
	key := ""
	if bold {
		key = d.webFontPick[[2]int{family, 1}]
		if key == "" {
			key, synthBold = d.webFontPick[[2]int{family, 0}], true
		}
	} else {
		key = d.webFontPick[[2]int{family, 0}]
	}
	d.webFontMu.Unlock()
	if key == "" {
		return nil
	}
	px := a11yScaleFontSize(uiLegibleFontSize(size))
	style := webFaceStyle(synthBold, italic)
	cacheKey := key + "\n" + strconv.Itoa(px) + "\n" + strconv.Itoa(style)
	if font, ok := webFontFaces[cacheKey]; ok {
		return font
	}
	_, _, _, faceCap := webFontLimits()
	if webFontFailed[cacheKey] || len(webFontFaces) >= faceCap {
		return nil
	}
	webFontStore.Lock()
	data := webFontStore.data[key]
	webFontStore.Unlock()
	if len(data) == 0 {
		return nil
	}
	// The store keeps data for the rest of the run, so the face can read it.
	rw, err := sdl.RWFromMem(data)
	if err != nil {
		webFontFailed[cacheKey] = true
		return nil
	}
	font, err := ttf.OpenFontRW(rw, 1, px)
	if err != nil || font == nil {
		webFontFailed[cacheKey] = true
		logDebug("browser", "web font %s did not open: %v", webFontLogName(key), err)
		return nil
	}
	if style != ttf.STYLE_NORMAL {
		font.SetStyle(style)
	}
	cover := webFontCoverPrefix + key
	if !webFontCovered[cover] {
		registerFontCoverageData(cover, data)
		webFontCovered[cover] = true
	}
	registerFontPath(font, cover)
	webNoteFaceSize(font, px)
	if bold {
		webBoldFaces.Lock()
		webBoldFaces.fonts[font] = true
		webBoldFaces.Unlock()
	}
	webFontFaces[cacheKey] = font
	return font
}

// webIsWebFace reports whether font is a page's web font.
func webIsWebFace(font *ttf.Font) bool {
	if font == nil {
		return false
	}
	p, ok := fontPathFor(font)
	return ok && strings.HasPrefix(p, webFontCoverPrefix)
}

// webFontLogName is a font's store key short enough for the log.
func webFontLogName(key string) string {
	if len(key) > 120 {
		return key[:120] + "…"
	}
	return key
}

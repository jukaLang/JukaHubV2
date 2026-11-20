package main

// gbprinter_companion.go — the Game Boy Printer scrapbook on the phone.
//
// The Phone Companion page has a "Game Boy Printer scrapbook" card (its HTML
// sits in companion.go's page; its script is served here as /gbprint.js, a
// static file with nothing secret in it, run after the page's own script so
// it can use the page's PIN). The card lists the prints, newest first, with
// a picture of each and three downloads: the print as it is, and 2× and 4×
// copies enlarged with sharp pixels (nearest neighbour), big enough to print
// on paper. The scrapbook's Y on the handheld shows a code that opens the
// page at this card (companion_phonecard.go).
//
// Read-only, and behind the same PIN as every other file route
// (companionServer.guard: the header, or "p" in the address for a picture or
// a download link). A request names a print by its id ("p12"), never by a
// path: the id is looked up in the scrapbook's list, the list's file name must
// pass gbpSafeRel, and the file is opened through the scrapbook folder as an
// os.Root — so no request, and no edited list, reaches a file outside the
// scrapbook. Answers carry nosniff and a sandbox CSP, like the Files card's
// downloads; error answers are fixed sentences.

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"strconv"
)

const (
	// gbpUpscaleMaxPixels bounds an enlarged copy (memory on the handheld):
	// a 160×144 print at 4× is 368,640 pixels.
	gbpUpscaleMaxPixels = 6 << 20
)

// gbpUpscaleSlots: one enlarged copy is made at a time, whatever the phone
// asks for at once.
var gbpUpscaleSlots = make(chan struct{}, 1)

// registerGBPrinterRoutes adds the scrapbook card's routes (companion.go's
// handler).
func (s *companionServer) registerGBPrinterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/gbprint", s.guard(s.handleGBPrintList))
	mux.HandleFunc("/api/gbprint/image", s.guard(s.handleGBPrintImage))
	mux.HandleFunc("/gbprint.js", handleGBPrintScript)
}

// gbpCompanionPrint is one print as the phone sees it: no paths.
type gbpCompanionPrint struct {
	ID      string `json:"id"`
	Game    string `json:"game"`
	Console string `json:"console"`
	Order   int    `json:"order"`
	W       int    `json:"w"`
	H       int    `json:"h"`
	Size    int64  `json:"size"`
}

// handleGBPrintList answers GET /api/gbprint with the prints, newest first.
func (s *companionServer) handleGBPrintList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	prints, _ := gbpPrints("")
	out := make([]gbpCompanionPrint, 0, len(prints))
	for _, p := range prints {
		out = append(out, gbpCompanionPrint{ID: p.ID, Game: p.Game, Console: setOr(p.Console, p.System), Order: p.Order, W: p.W, H: p.H, Size: p.Size})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeCompanionJSON(w, map[string]interface{}{"prints": out})
}

// handleGBPrintImage answers GET /api/gbprint/image?id=p12[&scale=2|4][&dl=1]
// with one print: inline for the card's picture, an attachment with dl=1.
func (s *companionServer) handleGBPrintImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	id := q.Get("id")
	if !gbpIDPattern.MatchString(id) {
		http.Error(w, "No such print.", http.StatusBadRequest)
		return
	}
	scale := 1
	switch q.Get("scale") {
	case "", "1":
	case "2":
		scale = 2
	case "4":
		scale = 4
	default:
		http.Error(w, "The size is 1, 2 or 4.", http.StatusBadRequest)
		return
	}
	p, ok := gbpPrintByID(id)
	if !ok {
		http.Error(w, "No such print.", http.StatusNotFound)
		return
	}
	f, err := gbpOpenPrint(p)
	if err != nil {
		logWarn("companion", "gbprint: cannot open %s: %v", p.ID, err)
		http.Error(w, "No such print.", http.StatusNotFound)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	// An error answer is never handed to the phone as a download.
	fail := func(msg string, code int) {
		h.Del("Content-Disposition")
		http.Error(w, msg, code)
	}
	if q.Get("dl") == "1" {
		name := gbpGameFolder(p.Game, p.ROM) + "-" + strconv.Itoa(p.Order)
		if scale > 1 {
			name += "-" + strconv.Itoa(scale) + "x"
		}
		h.Set("Content-Disposition", companionFilesDisposition(name+".png"))
	}
	if scale == 1 {
		info, err := f.Stat()
		if err != nil {
			fail("No such print.", http.StatusNotFound)
			return
		}
		http.ServeContent(w, r, "", info.ModTime(), f)
		return
	}
	// Measured from the header first: the copy's size is refused before the
	// picture is decoded into memory.
	pw, ph, err := gbpPrintSize(f)
	if err != nil {
		fail("That print cannot be read.", http.StatusInternalServerError)
		return
	}
	if pw*ph*scale*scale > gbpUpscaleMaxPixels {
		fail("That print is too large to enlarge here; save it as it is.", http.StatusRequestEntityTooLarge)
		return
	}
	gbpUpscaleSlots <- struct{}{}
	defer func() { <-gbpUpscaleSlots }()
	src, err := png.Decode(io.LimitReader(f, gbpPrintMaxBytes))
	if err != nil {
		fail("That print cannot be read.", http.StatusInternalServerError)
		return
	}
	big, ok := gbpUpscale(src, scale)
	if !ok {
		fail("That print is too large to enlarge here; save it as it is.", http.StatusRequestEntityTooLarge)
		return
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, big); err != nil {
		fail("That print cannot be enlarged.", http.StatusInternalServerError)
		return
	}
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(buf.Bytes())
}

// gbpUpscale enlarges src by a whole factor with nearest-neighbour pixels; a
// paletted print (the printer's four greys) stays paletted, one byte a pixel.
// ok is false when the result would be over gbpUpscaleMaxPixels. Pure.
func gbpUpscale(src image.Image, scale int) (image.Image, bool) {
	b := src.Bounds()
	w, h := b.Dx()*scale, b.Dy()*scale
	if scale < 1 || w <= 0 || h <= 0 || w*h > gbpUpscaleMaxPixels {
		return nil, false
	}
	if pal, ok := src.(*image.Paletted); ok {
		dst := image.NewPaletted(image.Rect(0, 0, w, h), pal.Palette)
		for y := 0; y < h; y++ {
			row := dst.Pix[y*dst.Stride : y*dst.Stride+w]
			sy := b.Min.Y + y/scale
			for x := range row {
				row[x] = pal.ColorIndexAt(b.Min.X+x/scale, sy)
			}
		}
		return dst, true
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := src.At(b.Min.X+x, b.Min.Y+y)
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					dst.Set(x*scale+dx, y*scale+dy, c)
				}
			}
		}
	}
	return dst, true
}

// handleGBPrintScript serves the card's script. It holds no data: every call
// it makes carries the page's PIN.
func handleGBPrintScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, gbpCompanionScript)
}

// gbpCompanionScript fills the card (#gbprintCard). It runs deferred, after
// the page's script, whose pin and hdr() it uses.
const gbpCompanionScript = `(function () {
  const list = document.getElementById('gbprintList');
  const msg = document.getElementById('gbprintMsg');
  const again = document.getElementById('gbprintRefresh');
  if (!list || !msg || typeof pin === 'undefined') return;
  const auth = 'p=' + encodeURIComponent(pin);
  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text) e.textContent = text;
    return e;
  }
  function link(id, scale) {
    return '/api/gbprint/image?id=' + encodeURIComponent(id) + '&scale=' + scale + '&dl=1&' + auth;
  }
  async function load() {
    msg.textContent = '';
    msg.className = 'msg';
    try {
      const res = await fetch('/api/gbprint', {headers: hdr()});
      if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
      const data = await res.json();
      const prints = (data && data.prints) || [];
      list.textContent = '';
      if (!prints.length) {
        list.appendChild(el('div', 'fempty', 'No prints yet. Start a Game Boy game with the printer on the handheld and print from its menu.'));
        return;
      }
      for (const p of prints) {
        const row = el('div', 'gbprow');
        const img = el('img');
        img.loading = 'lazy';
        img.alt = p.game || 'Print';
        img.src = '/api/gbprint/image?id=' + encodeURIComponent(p.id) + '&' + auth;
        row.appendChild(img);
        const info = el('div', 'gbpinfo');
        info.appendChild(el('b', '', p.game || 'Print'));
        info.appendChild(el('span', '', (p.console ? p.console + ' · ' : '') + p.w + '×' + p.h));
        const acts = el('div');
        const names = {1: 'Save', 2: '2×', 4: '4×'};
        for (const s of [1, 2, 4]) {
          const a = el('a', 'gbpbtn', names[s]);
          a.href = link(p.id, s);
          a.rel = 'noopener noreferrer';
          acts.appendChild(a);
        }
        info.appendChild(acts);
        row.appendChild(info);
        list.appendChild(row);
      }
    } catch (e) {
      msg.textContent = e.message || 'The handheld did not answer.';
      msg.className = 'msg bad';
    }
  }
  if (again) again.addEventListener('click', load);
  load();
})();
`

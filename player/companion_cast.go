package main

// companion_cast.go — remote screen cast.
//
// The companion server already answers a single-frame screenshot request. This
// file turns that into a stream: /watch is a small page anyone on the same
// network can open, and /stream is an MJPEG feed of the handheld's screen at a
// fixed frame rate. Both sit behind the same PIN as the rest of the companion
// API, and both are off until the cast is switched on in Menu → Remote Cast.
//
// SDL stays on the render thread: the stream handler only waits on frames that
// captureCompanionScreen publishes, so a browser can never touch the renderer.

import (
	"fmt"
	"html"
	"image"
	"image/jpeg"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

const (
	// castBoundary separates MJPEG parts. It has to be long enough that no
	// JPEG payload can contain it.
	castBoundary = "jukahub-frame-boundary"
	// castFrameInterval caps the encode rate. Ten frames a second looks live on
	// a LAN and costs a fraction of what 30 would on this CPU.
	castFrameInterval = 100 * time.Millisecond
	// castFrameWait bounds one stream iteration's wait for a fresh frame.
	castFrameWait = 400 * time.Millisecond
	// castMaxViewers keeps a classroom from turning the handheld into a video
	// server: each viewer needs its own socket and kernel buffer.
	castMaxViewers = 6
	// castJPEGQuality trades a little detail for a lot of bandwidth.
	castJPEGQuality = 65
)

var (
	// castEnabled gates /watch and /stream. Off by default: broadcasting the
	// screen should be a decision, not something a background server does.
	castEnabled atomic.Bool
	castViewers atomic.Int32
)

// castWatchURL is the shareable address: the page carries the PIN in its query
// string so one link is all a viewer needs.
func castWatchURL(ip string, port int, pin string) string {
	if ip == "" || port <= 0 || pin == "" {
		return ""
	}
	return fmt.Sprintf("http://%s/watch?p=%s", net_joinHostPort(ip, port), url.QueryEscape(pin))
}

// net_joinHostPort keeps the address formatting in one place (and in one
// dependency) for the panel and the tests.
func net_joinHostPort(ip string, port int) string {
	return fmt.Sprintf("%s:%d", ip, port)
}

// castSetEnabled switches the cast on or off. Turning it off disconnects the
// stream readers at their next write, because the handler re-checks the flag.
func castSetEnabled(on bool) {
	castEnabled.Store(on)
}

func castIsEnabled() bool { return castEnabled.Load() }

// castViewerCount is the number of browsers currently watching.
func castViewerCount() int { return int(castViewers.Load()) }

// castAddViewer reserves a slot. It returns false when the stream is off or
// already full.
func castAddViewer() bool {
	if !castEnabled.Load() {
		return false
	}
	for {
		n := castViewers.Load()
		if n >= castMaxViewers {
			return false
		}
		if castViewers.CompareAndSwap(n, n+1) {
			return true
		}
	}
}

func castRemoveViewer() {
	if n := castViewers.Load(); n > 0 {
		castViewers.Add(-1)
	}
}

// handleWatch serves the viewer page. It is deliberately separate from the
// phone-companion page: a viewer gets a picture and nothing else, with no
// keyboard, remote or upload surface.
func (s *companionServer) handleWatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !castEnabled.Load() {
		http.Error(w, "the cast is off on the handheld", http.StatusForbidden)
		return
	}
	pin := r.URL.Query().Get("p")
	if pin == "" {
		pin = r.Header.Get("X-Companion-Pin")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(castWatchPage(html.EscapeString(pin)))
}

// castWatchPage is the whole viewer UI: one image, one caption, and a retry
// loop so a dropped Wi-Fi connection reconnects without a reload.
func castWatchPage(pin string) []byte {
	streamURL := "/stream?p=" + url.QueryEscape(pin)
	page := `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>JukaHub — live screen</title>
<style>
:root { color-scheme: dark; }
body { margin: 0; background: #0b0f17; color: #e6e8ef;
       font: 14px/1.4 system-ui, -apple-system, Segoe UI, sans-serif;
       display: flex; flex-direction: column; min-height: 100vh; }
header { padding: 10px 16px; display: flex; gap: 12px; align-items: baseline;
         border-bottom: 1px solid #1c2537; }
header b { font-weight: 600; letter-spacing: .02em; }
header span { color: #8b93a7; font-size: 12px; }
main { flex: 1; display: flex; align-items: center; justify-content: center; padding: 12px; }
img { max-width: 100%; max-height: 82vh; width: auto; height: auto;
      border-radius: 12px; background: #111827; box-shadow: 0 8px 40px #0008; }
footer { padding: 8px 16px 14px; color: #8b93a7; font-size: 12px; text-align: center; }
#state { color: #ffb86b; }
</style></head>
<body>
<header><b>JukaHub</b><span>live screen</span></header>
<main><img id="cast" alt="Handheld screen" src="` + streamURL + `"></main>
<footer><span id="state">connecting…</span> &middot; same Wi-Fi as the handheld</footer>
<script>
(function () {
  const img = document.getElementById('cast');
  const state = document.getElementById('state');
  let retry = 0;
  img.addEventListener('load', () => { retry = 0; state.textContent = 'live'; });
  img.addEventListener('error', () => {
    state.textContent = 'reconnecting…';
    const wait = Math.min(5000, 500 * Math.pow(2, retry++));
    setTimeout(() => { img.src = '` + streamURL + `&t=' + Date.now(); }, wait);
  });
})();
</script>
</body></html>`
	return []byte(page)
}

// handleStream writes the MJPEG feed. Each part is a complete JPEG so a browser
// can render it without any JavaScript.
func (s *companionServer) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if !castEnabled.Load() {
		http.Error(w, "the cast is off on the handheld", http.StatusForbidden)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported by this connection", http.StatusInternalServerError)
		return
	}
	if !castAddViewer() {
		http.Error(w, "the cast is full", http.StatusServiceUnavailable)
		return
	}
	defer castRemoveViewer()

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+castBoundary)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var last uint64
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}
		if !castEnabled.Load() {
			return
		}
		frame, seq, ok := requestCompanionStreamFrame(last, castFrameWait)
		if !ok {
			continue
		}
		if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n",
			castBoundary, len(frame)); err != nil {
			return
		}
		if _, err := w.Write(frame); err != nil {
			return
		}
		if _, err := w.Write([]byte("\r\n")); err != nil {
			return
		}
		flusher.Flush()
		last = seq
	}
}

// encodeCompanionJPEG turns a captured ARGB8888 frame into a JPEG for the
// stream. The pixel copy is shared with the PNG encoder so both formats agree.
func encodeCompanionJPEG(pixels []byte, w, h int32, pitch int) ([]byte, error) {
	img, err := companionFrameImage(pixels, w, h, pitch)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 0, int(w)*int(h)/4)
	writer := &sliceWriter{buf: buf}
	if err := jpeg.Encode(writer, img, &jpeg.Options{Quality: castJPEGQuality}); err != nil {
		return nil, err
	}
	return writer.buf, nil
}

// sliceWriter is an io.Writer over a byte slice that avoids bytes.Buffer's
// growth copies on every frame.
type sliceWriter struct{ buf []byte }

func (w *sliceWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

// companionFrameImage is the shared SDL-bytes-to-image conversion.
func companionFrameImage(pixels []byte, w, h int32, pitch int) (*image.NRGBA, error) {
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("frame has no size")
	}
	if pitch < int(w)*4 || len(pixels) < pitch*int(h) {
		return nil, fmt.Errorf("frame buffer is %d bytes, need %d", len(pixels), int(w)*4*int(h))
	}
	img := image.NewNRGBA(image.Rect(0, 0, int(w), int(h)))
	for y := 0; y < int(h); y++ {
		row := pixels[y*pitch : y*pitch+int(w)*4]
		dst := img.Pix[y*img.Stride : y*img.Stride+int(w)*4]
		for x := 0; x < int(w); x++ {
			b, g, r, a := row[x*4], row[x*4+1], row[x*4+2], row[x*4+3]
			if a == 0 {
				a = 255
			}
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = r, g, b, a
		}
	}
	return img, nil
}

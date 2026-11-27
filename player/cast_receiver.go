package main

// cast_receiver.go — a small receiver for pictures sent from the phone. It is
// not an AirPlay or Chromecast implementation: the Phone Companion can POST a
// PNG or JPEG to /api/cast-receiver and the handheld shows it fullscreen until
// B. Everything is behind the companion PIN, the file stays on the card, and
// there is no external service.
//
// This pairs with Screen Share (companion_cast.go), which sends the handheld's
// screen the other way.

import (
	"image"
	"image/draw"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	castRecvMaxBytes = 16 << 20
	castRecvNameMax  = 60
)

// castReceiverDir is replaceable in tests.
var castReceiverDir = func() string {
	dir, err := P().DataDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, "cast-receiver")
}

var castRecv struct {
	mu   sync.Mutex
	open bool
	path string
	name string
	err  string
}

// castReceiverShow opens the card. An empty path is the waiting state: the
// card stays open until a picture arrives or the user presses B.
func castReceiverShow(path, name string) {
	castRecv.mu.Lock()
	castRecv.open, castRecv.path, castRecv.name, castRecv.err = true, path, name, ""
	castRecv.mu.Unlock()
	communityPanel.open = false
	PlayActivateSound()
}

func castReceiverClose() {
	castRecv.mu.Lock()
	castRecv.open = false
	castRecv.mu.Unlock()
	PlayBackSound()
}

func castReceiverSnapshot() (open bool, path, name, errText string) {
	castRecv.mu.Lock()
	defer castRecv.mu.Unlock()
	return castRecv.open, castRecv.path, castRecv.name, castRecv.err
}

// castReceiverHandleKey is called before the scene's own key handler.
func castReceiverHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	open, _, _, _ := castReceiverSnapshot()
	if !open || e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE, sdl.K_RETURN, sdl.K_SPACE:
		castReceiverClose()
	}
	return true
}

// castReceiverController is called while a pad is connected.
func castReceiverController(e *sdl.ControllerButtonEvent, config *Config) bool {
	open, _, _, _ := castReceiverSnapshot()
	if !open || e == nil || e.Type != sdl.CONTROLLERBUTTONDOWN {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_A, sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		castReceiverClose()
	}
	return true
}

// handleCastReceiverImage accepts one picture from the phone (PIN guarded by
// the caller) and shows it on the next frame.
func (s *companionServer) handleCastReceiverImage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, castRecvMaxBytes+1))
	if err != nil || len(data) > castRecvMaxBytes {
		http.Error(w, "Picture too large", http.StatusRequestEntityTooLarge)
		return
	}
	ext := ""
	switch contentType := http.DetectContentType(data); {
	case strings.Contains(contentType, "image/png"):
		ext = ".png"
	case strings.Contains(contentType, "image/jpeg"):
		ext = ".jpg"
	default:
		http.Error(w, "Send a PNG or JPEG picture", http.StatusUnsupportedMediaType)
		return
	}
	dir := castReceiverDir()
	if dir == "" {
		http.Error(w, "No data folder", http.StatusServiceUnavailable)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "Could not make the cast folder", http.StatusInternalServerError)
		return
	}
	path := filepath.Join(dir, "incoming"+ext)
	if err := AtomicWrite(path, data, 0o644); err != nil {
		http.Error(w, "Could not save the picture", http.StatusInternalServerError)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if len([]rune(name)) > castRecvNameMax {
		name = string([]rune(name)[:castRecvNameMax])
	}
	shown := path
	tbxPost(func() { castReceiverShow(shown, name) })
	writeCompanionJSON(w, map[string]interface{}{"ok": true, "name": name})
}

// handleCastReceiverPage serves a tiny upload form. The PIN is typed here and
// sent with the POST; the page itself reveals nothing.
func (s *companionServer) handleCastReceiverPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, castReceiverPage)
}

const castReceiverPage = `<!doctype html>
<html lang="en"><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>JukaHub · Send a picture</title>
<style>
body{margin:0;padding:20px;background:#0b0d12;color:#e6e8ef;font:16px system-ui,sans-serif}
input,button{width:100%;padding:12px;margin:10px 0;border-radius:10px;border:1px solid #232a38;background:#151923;color:inherit;font:16px system-ui,sans-serif}
button{background:#6f86ff;border:0;font-weight:600}
p{color:#8b93a7}
</style>
<h1>Send a picture</h1>
<p>A PNG or JPEG you choose goes to the handheld and shows until you press B there.</p>
<form id=f>
<input type="password" id=p placeholder="Companion PIN" inputmode="numeric" autocomplete="off" required>
<input type="file" id=i accept="image/png,image/jpeg" required>
<button type="submit">Send to handheld</button>
</form>
<p id=m></p>
<script>
$('f').onsubmit = async (e) => {
  e.preventDefault();
  const file = $('i').files[0]; if (!file) return;
  $('m').textContent = 'Sending…';
  const res = await fetch('/api/cast-receiver?p=' + encodeURIComponent($('p').value) + '&name=' + encodeURIComponent(file.name),
    {method: 'POST', headers: {'X-Companion-Pin': $('p').value, 'Content-Type': file.type}, body: file});
  $('m').textContent = res.ok ? 'Sent. Press B on the handheld to close it.' : (await res.text()).trim();
};
function $(id) { return document.getElementById(id); }
</script>`

// renderCastReceiver draws the picture, or the waiting card. It is called
// above every scene while the receiver is open.
func renderCastReceiver(renderer *sdl.Renderer, config *Config) {
	if renderer == nil {
		return
	}
	open, path, name, errText := castReceiverSnapshot()
	if !open {
		return
	}
	_ = renderer.SetDrawColor(0, 0, 0, 255)
	_ = renderer.Clear()
	font, _ := getCachedFont(config, "small")
	if path == "" || errText != "" {
		lines := []string{"Cast receiver", "Waiting for a picture from the phone…",
			"Open /cast on the phone, enter the PIN and choose a PNG or JPEG.", "B closes this card."}
		if errText != "" {
			lines = append(lines, errText)
		}
		if font != nil {
			for i, line := range lines {
				w, _, _ := font.SizeUTF8(line)
				renderText(renderer, config, font, line, ColorTextSecondary(),
					(screenWidth-int32(w))/2, screenHeight/2-40+int32(i)*int32(font.Height()+10))
			}
		}
		return
	}
	pic, err := imgvDecodeFile(path)
	if err != nil || pic == nil {
		if font != nil {
			renderText(renderer, config, font, "That picture could not be opened", ColorTextSecondary(),
				20, screenHeight/2)
		}
		return
	}
	bounds := pic.Bounds()
	nrgba := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(nrgba, nrgba.Bounds(), pic, bounds.Min, draw.Src)
	tex := imgvTexture(renderer, nrgba)
	if tex == nil {
		return
	}
	defer tex.Destroy()
	w, h := imgvFitInside(bounds.Dx(), bounds.Dy(), int(screenWidth), int(screenHeight)-40)
	dst := sdl.Rect{X: (screenWidth - int32(w)) / 2, Y: (screenHeight - int32(h)) / 2, W: int32(w), H: int32(h)}
	_ = renderer.Copy(tex, nil, &dst)
	if font != nil && name != "" {
		renderText(renderer, config, font, name, ColorTextSecondary(), 16, screenHeight-28)
	}
	if font != nil {
		w, _, _ := font.SizeUTF8("B to close")
		renderText(renderer, config, font, "B to close", ColorTextTertiary(), screenWidth-int32(w)-16, screenHeight-28)
	}
}

// castReceiverReset clears the card, e.g. when the companion stops: a stale
// incoming file is never shown without a new POST.
func castReceiverReset() {
	castRecv.mu.Lock()
	castRecv.open, castRecv.path, castRecv.name, castRecv.err = false, "", "", ""
	castRecv.mu.Unlock()
}

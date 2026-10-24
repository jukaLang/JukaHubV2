package main

// statusbar_media.go puts what the device is playing in the system corner of
// the top bar: a headphone glyph and the title, beside the network, clock and
// battery readings. Music and video share the one audio output (starting a
// video stops the playlist), so at most one of them is ever live.
//
// With nothing playing the same corner names the output instead — the connected
// headset or headphones — so the chip is never an empty gap. It is the same
// chip, drawn by the same code: only the words and the state change.
//
// The slot is an ordinary member of the shared status cluster: the title travels
// inside the part string, so fitStatusParts can measure it and drop it when the
// bar is crowded, and both headers (Home and every scene) draw it through
// drawStatusClusterPart without knowing what it is.

import (
	"math"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

const (
	// statusMediaPartPrefix marks the now-playing slot. The title follows it,
	// so the part is self-describing and cannot be mistaken for a reading.
	statusMediaPartPrefix = "np|"
	// statusOutputPartPrefix marks the same slot while nothing plays and it
	// names the output device instead: a connected headset, or wired
	// headphones. It is a reading, not a button, so the chip drops out of the
	// focus order even though it is drawn.
	statusOutputPartPrefix = "out|"
)

// --- What is playing ---------------------------------------------------

var (
	videoMediaMu   sync.Mutex
	videoMediaPage string
)

// rememberVideoPage records the page URL of the video session that is starting.
// The page — not the resolved stream URL or the temporary download — is the
// identity that survives from the list the user pressed play on, so the title
// can be found again in the queue that entry came from.
func rememberVideoPage(pageURL string) {
	videoMediaMu.Lock()
	videoMediaPage = strings.TrimSpace(pageURL)
	videoMediaMu.Unlock()
}

// forgetVideoPage drops the session's page URL when playback ends.
func forgetVideoPage() {
	videoMediaMu.Lock()
	videoMediaPage = ""
	videoMediaMu.Unlock()
}

func currentVideoPage() string {
	videoMediaMu.Lock()
	defer videoMediaMu.Unlock()
	return videoMediaPage
}

// liveVideoPage is the page URL of a video session that is actually running —
// playing, paused or still buffering — or "" when the video is not what the
// shared audio output is busy with. A loaded but stopped player does not count.
func liveVideoPage() string {
	switch playerPhase() {
	case "playing", "paused", "downloading":
		return currentVideoPage()
	}
	return ""
}

// currentMediaTitle names the media the bar should advertise, or ok=false when
// nothing is playing.
func currentMediaTitle() (string, bool) {
	if page := liveVideoPage(); page != "" {
		if title := videoTitleForPage(page); title != "" {
			return title, true
		}
	}
	title, _, _, playing, _ := ambientNowPlaying()
	if playing {
		if t := strings.TrimSpace(title); t != "" {
			return t, true
		}
	}
	return "", false
}

// mediaPaused reports whether the session the chip names is paused, so the chip
// can say so at a glance instead of looking like it is still playing. Music and
// video share the audio output, so the live video decides first.
func mediaPaused() bool {
	if liveVideoPage() != "" {
		return playerPhase() == "paused"
	}
	_, _, _, playing, paused := ambientNowPlaying()
	return playing && paused
}

// mediaProgress is how far through the session has played (0..1), or ok=false
// when the length is unknown — a live stream, or a track still being probed.
// The chip draws its progress hint only while this is known, so it never
// invents a position.
func mediaProgress() (float64, bool) {
	if liveVideoPage() != "" {
		embeddedPlayer.mu.Lock()
		progress, duration := embeddedPlayer.progress, embeddedPlayer.duration
		embeddedPlayer.mu.Unlock()
		if duration <= 0 {
			return 0, false
		}
		return clampMediaProgress(progress), true
	}
	_, position, duration, playing, _ := ambientNowPlaying()
	if !playing || duration <= 0 {
		return 0, false
	}
	return clampMediaProgress(position / duration), true
}

// clampMediaProgress keeps a fraction inside the line that draws it.
func clampMediaProgress(f float64) float64 {
	if math.IsNaN(f) || f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// videoTitleForPage returns the real title behind a video page URL: the queue
// entry whose URL matches, or a readable form of the URL when the video did not
// come from a titled list (a Watch Party stream, a pasted link).
func videoTitleForPage(page string) string {
	if playbackQueue.index >= 0 && playbackQueue.index < len(playbackQueue.videos) {
		entry := playbackQueue.videos[playbackQueue.index]
		if sameMediaPage(entry.GetURL(), page) {
			if t := strings.TrimSpace(entry.Title); t != "" {
				return t
			}
		}
	}
	return mediaTitleFromURL(page)
}

func sameMediaPage(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && (a == b || strings.EqualFold(a, b))
}

// mediaTitleFromURL makes a short name out of a link that has no title: the
// last meaningful path segment ("/watch/My-Clip-1.mp4" gives "My Clip 1"),
// else the host without its "www." prefix. It returns "" for anything that is
// not a link, so a bare word never becomes a fake title.
func mediaTitleFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		name := path.Base(strings.TrimSuffix(u.Path, "/"))
		name = strings.TrimSuffix(name, path.Ext(name))
		switch strings.ToLower(name) {
		case "", ".", "/", "watch", "index", "video":
			return strings.TrimPrefix(u.Host, "www.")
		}
		return strings.ReplaceAll(name, "-", " ")
	}
	// Not a link: a local file played by the video scene, named by its stem.
	base := path.Base(raw)
	if base == "" || base == "." || base == "/" {
		return ""
	}
	return strings.TrimSuffix(base, path.Ext(base))
}

// --- The chip as a button ----------------------------------------------

// statusMediaRect is the chip's hit area, measured by whichever header is on
// screen: a scene's status bar or Home's own header. While a player is behind
// it, a click on it — or A while the chip holds chrome focus — opens what is
// playing. The idle output chip is measured here too, so it draws in the same
// place, but it carries no action.
var statusMediaRect sdl.Rect

// openNowPlaying opens the screen behind what the chip advertises: the Media
// scene (and the floating player, if the video was left in its window) for a
// video, the audio player scene for music. Music and video share the one audio
// output, so at most one branch can apply. It reports whether anything was
// playing to open.
func openNowPlaying(config *Config) bool {
	if config == nil {
		return false
	}
	if _, ok := currentMediaTitle(); !ok {
		return false
	}
	// A video wins: starting one stops the playlist, so a live video page is
	// the session the chip is naming.
	if liveVideoPage() != "" {
		// A floated video goes back to the player in its owner scene.
		if videoMiniActive() {
			videoMiniExpand(config)
			return true
		}
		// Playing in a scene the user has since left without floating it:
		// return there rather than leaving them looking at another screen.
		if owner := sceneWithVideoElement(config); owner >= 0 && owner != currentSceneIndex {
			changeSceneTo(config, owner)
		}
		return true
	}
	openMusicPlayerScene(config)
	return true
}

// --- The status part ---------------------------------------------------

// statusMediaPart is the chip's text: what is playing, or — with nothing
// playing — the output the user is listening through. ok=false when there is
// neither, so the bar keeps the corner to the clock and the battery.
func statusMediaPart() (string, bool) {
	if title, ok := currentMediaTitle(); ok {
		return statusMediaPartPrefix + title, true
	}
	if device := connectedAudioOutput(); device != "" {
		return statusOutputPartPrefix + device, true
	}
	return "", false
}

func isMediaStatusPart(part string) bool {
	return strings.HasPrefix(part, statusMediaPartPrefix)
}

// isOutputStatusPart reports whether the chip names an output device rather
// than the media. Such a chip is a reading: no pause state, no progress and no
// press action.
func isOutputStatusPart(part string) bool {
	return strings.HasPrefix(part, statusOutputPartPrefix)
}

// isChipStatusPart reports whether the part belongs in the chip slot, whatever
// it is naming.
func isChipStatusPart(part string) bool {
	return isMediaStatusPart(part) || isOutputStatusPart(part)
}

func statusMediaTitle(part string) string {
	return strings.TrimSpace(strings.TrimPrefix(part, statusMediaPartPrefix))
}

// statusOutputName is the device name behind an output part.
func statusOutputName(part string) string {
	return strings.TrimSpace(strings.TrimPrefix(part, statusOutputPartPrefix))
}

// statusChipText is the words the chip shows, without its marker.
func statusChipText(part string) string {
	if isOutputStatusPart(part) {
		return statusOutputName(part)
	}
	return statusMediaTitle(part)
}

// nowPlayingChipIsButton reports whether the chip has a player behind it. A
// chip naming an idle output device is a reading, so it is neither focusable
// nor clickable: pressing it would open nothing.
func nowPlayingChipIsButton() bool {
	_, ok := currentMediaTitle()
	return ok
}

// statusMediaTitleWidth caps how much of the title the corner spends: the chip
// shares the bar with the clock and the battery, and a full video title would
// push them off the screen. The label is ellipsized to this width, so the
// measurement and the drawing always agree.
func statusMediaTitleWidth() int32 {
	maxW := screenWidth / 4
	if maxW < 90 {
		maxW = 90
	}
	if maxW > 240 {
		maxW = 240
	}
	return maxW
}

// statusMediaLabel is the chip's text as it will be drawn: the media title, or
// the output device's name.
func statusMediaLabel(font *ttf.Font, part string) string {
	text := statusChipText(part)
	if font == nil {
		return text
	}
	if w, _, _ := font.SizeUTF8(text); int32(w) <= statusMediaTitleWidth() {
		return text
	}
	return ellipsize(font, text, statusMediaTitleWidth())
}

// statusMediaIconWidth is the headphone glyph's width for a given icon height.
func statusMediaIconWidth(iconH int32) int32 {
	if iconH < 9 {
		return 9
	}
	return iconH
}

const (
	// The progress hint is a hairline along the bottom edge of the chip: two
	// pixels tall, three below the title's baseline box, which keeps it inside
	// the chip's tap rect without growing the measured width.
	statusMediaProgressH   = int32(2)
	statusMediaProgressGap = int32(3)
)

// --- Drawing -----------------------------------------------------------

// drawStatusHeadphones paints the now-playing glyph inside a size×size box: a
// band over the top and two ear cups. It is drawn from primitives, so it needs
// no font glyph and stays sharp at every bar height.
func drawStatusHeadphones(renderer *sdl.Renderer, x, y, size int32, col sdl.Color) {
	if renderer == nil || size < 6 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	renderer.SetDrawColor(col.R, col.G, col.B, col.A)

	cupW := size / 3
	if cupW < 2 {
		cupW = 2
	}
	cupH := size / 2
	if cupH < 4 {
		cupH = 4
	}
	cx := x + size/2
	cy := y + size/2
	radius := (size - cupW) / 2
	if radius < 3 {
		radius = 3
	}
	// The band is the top half of a circle; a second, tighter arc gives it
	// enough weight to sit next to the battery cell.
	drawStatusBandArc(renderer, cx, cy, radius)
	if radius > 4 {
		drawStatusBandArc(renderer, cx, cy, radius-1)
	}
	// The cups hang from the band's ends and overlap it slightly, so the glyph
	// reads as one shape rather than three pieces.
	cupTop := cy - cupH/4
	fillRoundedRect(renderer, cx-radius-cupW/2, cupTop, cupW, cupH, cupW/2, col)
	fillRoundedRect(renderer, cx+radius-cupW/2, cupTop, cupW, cupH, cupW/2, col)
}

// drawStatusBandArc strokes the top half of a circle in one-pixel segments.
func drawStatusBandArc(renderer *sdl.Renderer, cx, cy, radius int32) {
	const steps = 12
	prevX, prevY := cx-radius, cy
	for i := 1; i <= steps; i++ {
		angle := math.Pi + math.Pi*float64(i)/float64(steps)
		px := cx + int32(math.Round(float64(radius)*math.Cos(angle)))
		py := cy + int32(math.Round(float64(radius)*math.Sin(angle)))
		renderer.DrawLine(prevX, prevY, px, py)
		prevX, prevY = px, py
	}
}

// drawStatusMediaChip paints the state glyph, the ellipsized title and — when
// the length is known — a hairline progress hint, and returns the width it used,
// the same width statusClusterPartWidth measured.
func drawStatusMediaChip(renderer *sdl.Renderer, font *ttf.Font, part string, x, textTop int32, draw statusTextDrawer) int32 {
	iconH := statusIconHeight(font)
	iconW := statusMediaIconWidth(iconH)
	textH := iconH
	if font != nil {
		textH = int32(font.Height())
	}
	iconY := textTop + (textH-iconH)/2
	col := ColorTextSecondary()
	// State only applies to media. An output chip is a reading and always shows
	// the headphone glyph.
	playing := isMediaStatusPart(part)
	// The glyph carries the state: headphones while playing, a pause pair while
	// paused, in the same box so the title never moves.
	if playing && mediaPaused() {
		drawStatusPauseBars(renderer, x, iconY, iconH, col)
	} else {
		drawStatusHeadphones(renderer, x, iconY, iconH, col)
	}
	if draw != nil {
		draw(statusMediaLabel(font, part), col, x+iconW+statusChipTextGap, textTop)
	}
	width := statusClusterPartWidth(font, part)
	// Only a session with a known length gets a hint; a stream or a file still
	// being probed shows none rather than an invented position.
	if playing {
		if fraction, ok := mediaProgress(); ok {
			drawStatusMediaProgress(renderer, x, textTop+textH+statusMediaProgressGap, width, fraction)
		}
	}
	return width
}

// drawStatusPauseBars paints the paused state in the same size×size box the
// headphone glyph uses: two upright bars with a gap between them. Drawn from
// primitives, like the other status icons, so it needs no font glyph.
func drawStatusPauseBars(renderer *sdl.Renderer, x, y, size int32, col sdl.Color) {
	if renderer == nil || size < 6 {
		return
	}
	barW := size / 4
	if barW < 2 {
		barW = 2
	}
	barH := size
	if barH < 6 {
		barH = 6
	}
	gap := barW
	startX := x + (size-(2*barW+gap))/2
	fillRoundedRect(renderer, startX, y, barW, barH, barW/2, col)
	fillRoundedRect(renderer, startX+barW+gap, y, barW, barH, barW/2, col)
}

// drawStatusMediaProgress paints the played portion of the chip as a hairline
// under its text: a dim track with the elapsed part in the secondary text
// colour, so it reads as a hint rather than another gauge competing with the
// battery.
func drawStatusMediaProgress(renderer *sdl.Renderer, x, y, w int32, fraction float64) {
	if renderer == nil || w <= 0 {
		return
	}
	fillRoundedRect(renderer, x, y, w, statusMediaProgressH, statusMediaProgressH/2,
		WithAlpha(ColorTextTertiary(), 70))
	filled := int32(math.Round(float64(w) * clampMediaProgress(fraction)))
	if filled < 1 {
		return
	}
	if filled > w {
		filled = w
	}
	fillRoundedRect(renderer, x, y, filled, statusMediaProgressH, statusMediaProgressH/2,
		WithAlpha(ColorTextSecondary(), 170))
}

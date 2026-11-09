package main

// screensaver_media.go — what the idle screen mirrors while the device is quiet.
//
// The starfield used to show only the clock, the battery and a quote. That left
// the panel useless at a glance: no link state, and nothing about the music or
// video still running underneath it. Now the saver is also a status panel — the
// Wi-Fi and battery chips (saver_status.go), and a card in the middle showing
// the live video frame when a video plays, or the track's cover art when music
// does, with equaliser bars when the track has no artwork to show (or across the
// bottom of the art when it has).

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

type screensaverMediaKind int

const (
	saverMediaNone screensaverMediaKind = iota
	saverMediaVideo
	saverMediaMusic
)

// screensaverMedia is the decision plus the text that goes with it.
type screensaverMedia struct {
	kind      screensaverMediaKind
	title     string
	subtitle  string
	coverPath string
	playing   bool
	paused    bool
}

// saverMusicNow is the music half of the decision, kept as a value so the rule
// can be tested without touching the player globals.
type saverMusicNow struct {
	title     string
	trackPath string
	playing   bool
	paused    bool
	position  float64
	duration  float64
}

// saverMediaDecide picks one thing to mirror. Video wins over music: both share
// the audio output, so the session that is actually drawing frames is the one
// the user walked away from.
func saverMediaDecide(videoPhase, videoTitle string, music saverMusicNow) screensaverMedia {
	switch videoPhase {
	case "playing", "paused", "downloading":
		state := screensaverMedia{kind: saverMediaVideo, title: videoTitle, playing: videoPhase == "playing", paused: videoPhase == "paused"}
		switch videoPhase {
		case "paused":
			state.subtitle = "Paused"
		case "downloading":
			state.subtitle = "Loading"
		default:
			state.subtitle = "Now playing"
		}
		return state
	}
	if music.playing || music.paused {
		state := screensaverMedia{
			kind:     saverMediaMusic,
			title:    music.title,
			playing:  music.playing,
			paused:   music.paused,
			subtitle: "Now playing",
		}
		if music.paused {
			state.subtitle = "Paused"
		}
		if music.duration > 0 {
			state.subtitle += " · " + formatTime(music.position) + " / " + formatTime(music.duration)
		}
		if music.trackPath != "" {
			state.coverPath = musicCoverArtPath(music.trackPath)
		}
		return state
	}
	return screensaverMedia{kind: saverMediaNone}
}

// screensaverNowPlaying collects the live state the saver mirrors. Kept apart
// from saverMediaDecide so the rule stays testable without the globals.
func screensaverNowPlaying() screensaverMedia {
	videoTitle := ""
	if liveVideoPage() != "" {
		videoTitle, _ = currentMediaTitle()
	}

	music.mu.Lock()
	title, trackPath := "", ""
	playing, paused := music.playing, music.paused
	position, duration := music.position, music.duration
	if music.current >= 0 && music.current < len(music.tracks) {
		track := music.tracks[music.current]
		title = strings.TrimSuffix(filepath.Base(track.Name), filepath.Ext(track.Name))
		trackPath = track.Path
	}
	music.mu.Unlock()

	return saverMediaDecide(playerPhase(), videoTitle, saverMusicNow{
		title:     title,
		trackPath: trackPath,
		playing:   playing,
		paused:    paused,
		position:  position,
		duration:  duration,
	})
}

// saverCoverNames are the file names a track folder uses for its artwork, most
// specific first. The track's own stem is checked before these.
var saverCoverNames = []string{"cover", "folder", "front", "album", "artwork"}

var saverCoverExts = []string{".jpg", ".jpeg", ".png", ".webp"}

// coverArtCache remembers a lookup per folder for a while: the saver asks on
// every frame, and a directory scan per frame would be silly. The entry is
// refreshed after coverArtTTL so dropping a cover.jpg in while music plays is
// picked up without a restart.
const coverArtTTL = 10 * time.Second

var coverArtLookups struct {
	mu    sync.Mutex
	byDir map[string]coverArtEntry
}

type coverArtEntry struct {
	path string
	at   time.Time
}

// musicCoverArtPath finds cover art for a track: first a file with the track's
// own name, then the usual folder names. An empty string means the track has no
// artwork, which is what makes the equaliser stand in for it.
func musicCoverArtPath(trackPath string) string {
	trackPath = strings.TrimSpace(trackPath)
	if trackPath == "" {
		return ""
	}
	dir := filepath.Dir(trackPath)
	now := time.Now()

	coverArtLookups.mu.Lock()
	if coverArtLookups.byDir == nil {
		coverArtLookups.byDir = make(map[string]coverArtEntry)
	}
	if entry, ok := coverArtLookups.byDir[dir]; ok && now.Sub(entry.at) < coverArtTTL {
		coverArtLookups.mu.Unlock()
		return entry.path
	}
	coverArtLookups.mu.Unlock()

	found := scanCoverArt(dir, trackPath)

	coverArtLookups.mu.Lock()
	coverArtLookups.byDir[dir] = coverArtEntry{path: found, at: now}
	coverArtLookups.mu.Unlock()
	return found
}

// scanCoverArt is the filesystem half, split out so a test can point it at a
// directory without the cache in the way.
func scanCoverArt(dir, trackPath string) string {
	stem := strings.TrimSuffix(filepath.Base(trackPath), filepath.Ext(trackPath))
	names := append([]string{stem}, saverCoverNames...)
	for _, name := range names {
		for _, ext := range saverCoverExts {
			candidate := filepath.Join(dir, name+ext)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Size() > 0 {
				return candidate
			}
		}
	}
	return ""
}

// saverMediaCard is the rect the media sits in. Video keeps 16:9; artwork is a
// square. Both sit in the band between the clock block at the top (a quarter
// of the screen) and the title, subtitle and hints at the bottom (about the same
// again), centred in it, so nothing on the lock screen overlaps.
func saverMediaCard(kind screensaverMediaKind, sw, sh int32) sdl.Rect {
	if sw <= 0 || sh <= 0 || kind == saverMediaNone {
		return sdl.Rect{}
	}
	top := sh / 4
	avail := sh - top - sh*6/25
	if avail < sh/5 {
		avail = sh / 5
	}
	var w, h int32
	switch kind {
	case saverMediaVideo:
		w = int32(float64(sw) * 0.56)
		h = w * 9 / 16
		if h > avail {
			h = avail
			w = h * 16 / 9
		}
	case saverMediaMusic:
		side := int32(float64(sh) * 0.44)
		if maxSide := int32(float64(sw) * 0.30); side > maxSide {
			side = maxSide
		}
		if side > avail {
			side = avail
		}
		w, h = side, side
	}
	return sdl.Rect{X: (sw - w) / 2, Y: top + (avail-h)/2, W: w, H: h}
}

// saverFitRect fits a source size inside dst, preserving its aspect ratio.
func saverFitRect(srcW, srcH int32, dst sdl.Rect) sdl.Rect {
	if srcW <= 0 || srcH <= 0 || dst.W <= 0 || dst.H <= 0 {
		return dst
	}
	scale := math.Min(float64(dst.W)/float64(srcW), float64(dst.H)/float64(srcH))
	w := int32(float64(srcW) * scale)
	h := int32(float64(srcH) * scale)
	return sdl.Rect{X: dst.X + (dst.W-w)/2, Y: dst.Y + (dst.H-h)/2, W: w, H: h}
}

// saverCoverRect fills dst with a source, cropping the overflow: artwork should
// fill its card rather than float inside it with letterbox bars.
func saverCoverRect(srcW, srcH int32, dst sdl.Rect) sdl.Rect {
	if srcW <= 0 || srcH <= 0 || dst.W <= 0 || dst.H <= 0 {
		return dst
	}
	scale := math.Max(float64(dst.W)/float64(srcW), float64(dst.H)/float64(srcH))
	w := int32(float64(srcW) * scale)
	h := int32(float64(srcH) * scale)
	return sdl.Rect{X: dst.X + (dst.W-w)/2, Y: dst.Y + (dst.H-h)/2, W: w, H: h}
}

// saverFade scales an alpha by the saver's fade-in, capped so the panel never
// goes fully bright on a dark room.
func saverFade(fade uint8, alpha uint8) uint8 {
	a := uint32(alpha) * uint32(fade) / 255
	if a > uint32(alpha) {
		a = uint32(alpha)
	}
	return uint8(a)
}

// drawScreensaverMedia paints the now-playing card. Nothing is drawn when no
// session is live, which leaves the starfield and the quote exactly as before.
func drawScreensaverMedia(renderer *sdl.Renderer, config *Config, media screensaverMedia, fade uint8) {
	if renderer == nil || media.kind == saverMediaNone || fade < 40 {
		return
	}
	card := saverMediaCard(media.kind, screenWidth, screenHeight)
	if card.W <= 0 || card.H <= 0 {
		return
	}

	// Glass plate behind the media so the artwork or frame reads as a card and
	// the stars do not show through it. The plate is opaque: a translucent one
	// let the starfield twinkle through letterboxed video.
	g := saverGlassStyle()
	plate := g.raised
	plate.A = 255
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, 18, WithAlpha(plate, saverFade(fade, 255)))

	switch media.kind {
	case saverMediaVideo:
		drawSaverVideo(renderer, config, card, fade)
	case saverMediaMusic:
		if media.coverPath != "" {
			drawSaverCover(renderer, card, media.coverPath, fade)
		}
		// Bars sit on top of artwork when there is any, and stand in for it when
		// there is not: a track with a cover still deserves the motion.
		if media.playing || media.paused {
			musicUpdateVisualizer()
			barRect := sdl.Rect{X: card.X + 24, Y: card.Y + card.H - 64, W: card.W - 48, H: 40}
			if media.coverPath == "" {
				barRect = sdl.Rect{X: card.X + 24, Y: card.Y + card.H/2 - 20, W: card.W - 48, H: 80}
			}
			drawSaverEqualizer(renderer, barRect, 16, fade, media.coverPath != "", !media.playing)
		}
	}

	strokeRoundedRect(renderer, card.X, card.Y, card.W, card.H, 18, 1, WithAlpha(g.edge, saverFade(fade, 255)))

	// Title under the card, on the idle screen's own dark backdrop: the light
	// inks, whatever the theme (saver_status.go).
	title, _ := getCachedFont(config, "medium")
	sub := saverChipFont(config)
	if title == nil || strings.TrimSpace(media.title) == "" {
		return
	}
	line := ellipsize(title, media.title, card.W+96)
	tw, th := measureText(title, line)
	renderText(renderer, config, title, line, WithAlpha(saverInkClock, saverFade(fade, 240)),
		(screenWidth-tw)/2, card.Y+card.H+14)
	if sub != nil && media.subtitle != "" {
		label := media.subtitle
		if media.kind == saverMediaMusic && media.coverPath == "" && media.playing {
			label += " · no artwork"
		}
		sw2, _ := measureText(sub, label)
		renderText(renderer, config, sub, label, WithAlpha(saverInkSoft, saverFade(fade, 230)),
			(screenWidth-sw2)/2, card.Y+card.H+14+th+4)
	}
}

// drawSaverVideo pulls the newest decoded frame and paints it inside the card.
// It advances progress the way the floating window does: while the saver is up
// it is the only thing drawing the session.
func drawSaverVideo(renderer *sdl.Renderer, config *Config, card sdl.Rect, fade uint8) {
	state := snapshotVideoState()
	if state.phase == "idle" {
		return
	}
	videoAdvanceProgress(&state)
	UpdateCurrentTexture(renderer)

	embeddedPlayer.mu.Lock()
	tex := embeddedPlayer.current
	embeddedPlayer.mu.Unlock()
	if tex == nil {
		return
	}

	dst := card
	if state.width > 0 && state.height > 0 {
		dst = saverFitRect(state.width, state.height, card)
	}
	renderWithClip(renderer, card.X, card.Y, card.W, card.H, func(rr *sdl.Renderer) {
		drawVideoFrameArea(rr, tex, dst.X, dst.Y, dst.W, dst.H)
	})
	if state.phase == "paused" {
		cx, cy := card.X+card.W/2, card.Y+card.H/2
		fillCircle(renderer, cx, cy, 26, sdl.Color{R: 0, G: 0, B: 0, A: saverFade(fade, 150)})
		s := int32(11)
		fillTriangleFilled(renderer, pt{x: cx - s + 2, y: cy - s}, pt{x: cx - s + 2, y: cy + s}, pt{x: cx + s - 1, y: cy}, saverInkClock)
	}
}

// drawSaverCover paints the track's artwork into the card, cropped to fill.
func drawSaverCover(renderer *sdl.Renderer, card sdl.Rect, path string, fade uint8) {
	tex := artTexture(renderer, path)
	if tex == nil {
		return
	}
	_, _, tw, th, qerr := tex.Query()
	if qerr != nil || tw <= 0 || th <= 0 {
		return
	}
	dst := saverCoverRect(tw, th, card)
	_ = tex.SetAlphaMod(saverFade(fade, 255))
	renderWithClip(renderer, card.X, card.Y, card.W, card.H, func(rr *sdl.Renderer) {
		rr.Copy(tex, nil, &dst)
	})
}

// drawSaverEqualizer draws bars from the player's spectrum. On artwork the bars
// sit in a darker strip so they stay readable over a bright cover.
func drawSaverEqualizer(renderer *sdl.Renderer, rect sdl.Rect, bars int, fade uint8, onArt bool, dim bool) {
	if renderer == nil || bars <= 0 || rect.W <= 0 || rect.H <= 0 {
		return
	}
	if onArt {
		fillRoundedRect(renderer, rect.X-8, rect.Y-8, rect.W+16, rect.H+16, 10, sdl.Color{R: 0, G: 0, B: 0, A: saverFade(fade, 110)})
	}

	music.mu.Lock()
	data := append([]float64(nil), music.visualizer[:]...)
	music.mu.Unlock()

	gap := int32(4)
	usable := rect.W - gap*int32(bars-1)
	if usable <= 0 {
		return
	}
	barW := usable / int32(bars)
	if barW < 2 {
		barW = 2
	}
	alpha := saverFade(fade, 235)
	if dim {
		alpha = saverFade(fade, 90)
	}
	baseY := rect.Y + rect.H
	for i := 0; i < bars; i++ {
		v := 0.0
		if len(data) > 0 {
			v = data[(i*visualBars/bars)%visualBars]
		}
		h := int32(v / float64(visualMaxH) * float64(rect.H))
		if h < 4 {
			h = 4
		}
		x := rect.X + int32(i)*(barW+gap)
		fillRoundedRect(renderer, x, baseY-h, barW, h, barW/2, WithAlpha(accentColor, alpha))
	}
}

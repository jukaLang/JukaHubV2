package main

// snappreview.go — optional video snaps in the Games Library.
//
// Box art tells you what a game is; a three-second clip tells you what it looks
// like in motion. Knulli and the Batocera family show one for the focused game,
// and the library already had the half that matters: a preview box next to the
// list. What it did not have was a way to play a video while the D-pad is still
// in use, on a 1 GB device, without disturbing the real video player.
//
// Three rules make that safe:
//
//  1. The snaps are files the user puts on the card, mirroring the box-art
//     layout: <data>/game-snaps/<System_with_underscores>/<rom stem>.mp4 (or
//     .webm). Nothing is downloaded and no scraper account is involved.
//  2. A preview only starts for a game that has been focused for a moment
//     (snapPreviewArmDelay). Scrolling a list must not spawn a process per row.
//  3. Decoding runs in its own ffmpeg process at 320x180, 10fps, no audio,
//     looped. It does not touch the player's playback session, and it stops the
//     instant the focus, the panel or the switch says so.
//
// The threading rule is the same as the video player's: the decoder goroutine
// only ever fills a byte buffer, and the SDL texture is created and updated on
// the render thread (snapPreviewTexture). Creating a texture off the render
// thread is how thumbnails used to come out black.

import (
	"bytes"
	"context"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// snapPreviewWidth/Height is the decode size, not the display size: bigger
	// than the preview box on the 720p panel with a little headroom for the
	// Brick's scaled canvas, and small enough that ffmpeg spends almost nothing
	// on the scale.
	snapPreviewWidth  = int32(320)
	snapPreviewHeight = int32(180)
	// snapPreviewFPS is deliberately below the 30fps UI cadence. These are
	// menu clips, and halving the frames halves the decode cost of a screen the
	// user is scrolling.
	snapPreviewFPS = 10
	// snapPreviewArmDelay is how long the focus must rest on one game before its
	// snap starts. Arrow-key scrolling through 200 titles otherwise starts 200
	// decoders.
	snapPreviewArmDelay = 350 * time.Millisecond
	// snapPreviewRecheck is how often a focused game without a snap is looked up
	// again: a user who drops a clip on the card while the panel is open should
	// see it without closing and reopening the library.
	snapPreviewRecheck = time.Second
)

// snapExtensions are tried in order, so a game with an mp4 and a webm uses the
// mp4. Anything else (mkv, avi) is ignored on purpose: the bundled ffmpeg can
// decode them, but a preview is not worth a custom container matrix.
var snapExtensions = []string{".mp4", ".webm"}

// romSnapDir is where video snaps live: beside the box art, under a different
// folder so the art fetcher never mistakes a clip for a cover.
func romSnapDir() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "game-snaps")
}

// librarySnapRoot is the seam tests use instead of the app data folder.
var librarySnapRoot = romSnapDir

// gameSnapPath returns the first existing snap for a game, or "".
//
// The folder layout mirrors romArtworkPath: the console name with spaces turned
// into underscores, then the ROM's file name without its extension. That way a
// user who already has a box art collection can mirror its tree.
func gameSnapPath(game libraryGame, root string) string {
	root = strings.TrimSpace(root)
	if root == "" || strings.TrimSpace(game.System) == "" {
		return ""
	}
	stem := strings.TrimSuffix(filepath.Base(game.Path), filepath.Ext(game.Path))
	if stem == "" || stem == "." || stem == string(filepath.Separator) {
		return ""
	}
	dir := filepath.Join(root, strings.ReplaceAll(game.System, " ", "_"))
	for _, ext := range snapExtensions {
		path := filepath.Join(dir, stem+ext)
		if artworkCoverPresent(path) {
			return path
		}
	}
	return ""
}

const (
	// snapPreviewSettingKey is where the switch lives in jukauser.json. It is a
	// plain custom key so a config or a plugin can set it without a code path.
	snapPreviewSettingKey = "gameSnaps"
	// snapPreviewLabelKey is the Settings row's substituted value. Substitution
	// reads only the custom block, so the derived text has to be seeded there.
	snapPreviewLabelKey = "gameSnapsLabel"
)

// snapPreviewEnabled reports whether the feature is on. It is on by default:
// a card with no snaps costs nothing but a stat per focused row, and the switch
// exists for the user who wants the library to stay still. Only an explicit
// off value disables it, and the value is read through coerceBool so a key
// written as the string "false" (by a toggle element or a hand-edited file)
// means off rather than "not a bool, so leave it on".
func snapPreviewEnabled(config *Config) bool {
	if config == nil {
		return false
	}
	if v, ok := config.Variables.Custom[snapPreviewSettingKey]; ok {
		return coerceBool(v)
	}
	return true
}

// snapPreviewLabel is the Settings row's value, in the same "On"/"Off" words
// the other switches on that page use.
func snapPreviewLabel(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// snapPreviewRowValue is what the row shows once the card has been counted: the
// switch's state, then how many of the library's games have a clip. A card with
// none says "none" rather than 0, because that is the answer the switch needs to
// give — on, but with nothing to show yet. Until the first count lands the row
// keeps the plain state, which is what it showed before the count existed.
//
// The value is deliberately not "N clips": the row sits in a 296px cell beside
// Swap A/B, and every word after the number pushed the label past the cell on the
// 800px panel. "Video snaps · On · 12" fits with room; the number is the count of
// the snaps the row is named after.
func snapPreviewRowValue(config *Config) string {
	state := snapPreviewLabel(snapPreviewEnabled(config))
	withClip, _, known := snapClipCount()
	switch {
	case !known:
		return state
	case withClip == 0:
		return state + " · none"
	default:
		return state + " · " + itoa(withClip)
	}
}

// snapPreviewSyncRow keeps the Settings row's value current while the page is up:
// it writes the last finished count into the row, and leaves the plain state
// alone until the first scan lands. The scan itself is started when the page is
// entered (snapClipCountRefresh), not from here, so drawing a frame never walks
// the card.
func snapPreviewSyncRow(config *Config) {
	if config == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	value := snapPreviewRowValue(config)
	if current, _ := config.Variables.Custom[snapPreviewLabelKey].(string); current != value {
		config.Variables.Custom[snapPreviewLabelKey] = value
	}
}

// applySnapPreviewEnabled stores the switch and re-seeds the row's label, then
// saves through the debounced writer every other Settings row uses. A preview
// already on screen is not left running: the next frame's snapPreviewSync asks
// snapPreviewEnabled, finds it off, and stops the decoder.
func applySnapPreviewEnabled(config *Config, on bool) bool {
	if config == nil {
		return on
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[snapPreviewSettingKey] = on
	// The count stays on the row across the flip: a card holding clips is worth
	// knowing about while the switch is off, and seeding the full value here is
	// what keeps the label from reading plain "Off" for one frame.
	config.Variables.Custom[snapPreviewLabelKey] = snapPreviewRowValue(config)
	saveUserConfigDebounced(userConfigFromVariables(config))
	return on
}

// toggleSnapPreview flips the switch and reports the new state.
func toggleSnapPreview(config *Config) bool {
	return applySnapPreviewEnabled(config, !snapPreviewEnabled(config))
}

// ensureSnapPreviewSetting puts the switch in Settings → General, in the band
// beside the Swap A/B toggle: both are independent on/off switches, and that is
// the page's last free slot above the Audio backend read-out. The row is a
// button rather than a toggle element because the feature is on by default:
// a toggle would show "off" for an unset key while previews were playing.
func ensureSnapPreviewSetting(config *Config) {
	scene := settingsGeneralScene(config)
	if scene == nil {
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	config.Variables.Custom[snapPreviewLabelKey] = snapPreviewLabel(snapPreviewEnabled(config))
	for _, el := range scene.Elements {
		if strings.EqualFold(strings.TrimSpace(el.Trigger), "toggle_game_snaps") {
			return
		}
	}
	width, height := screenWidth, screenHeight
	if width <= 0 {
		width = 1280
	}
	if height <= 0 {
		height = 720
	}
	scene.Elements = append(scene.Elements, Element{
		Type:    "button",
		Text:    "Video snaps · $gameSnapsLabel",
		X:       scaleX(960, width),
		Y:       scaleY(412, height),
		Width:   StringOrInt(strconv.Itoa(int(scaleX(296, width)))),
		Height:  StringOrInt(strconv.Itoa(int(scaleY(48, height)))),
		Trigger: "toggle_game_snaps",
		Color:   "$labelColor",
		BgColor: "$buttonColor",
	})
}

// snapPreviewDirOverride lets the config point at a different tree (a big
// collection on the card's root, for instance).
func snapPreviewDirOverride(config *Config) string {
	if config == nil {
		return ""
	}
	if v, ok := config.Variables.Custom["gameSnapsDir"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// snapPreviewRoot is the folder looked in: the config override, else the app
// data folder.
func snapPreviewRoot(config *Config) string {
	if dir := snapPreviewDirOverride(config); dir != "" {
		return dir
	}
	return librarySnapRoot()
}

// ── State ─────────────────────────────────────────────────────────────────

type snapPreviewState struct {
	mu sync.Mutex
	// game is the focused game's ROM path, snap the resolved clip for it.
	game string
	snap string
	// since is when the focus landed on this game, lastCheck when the snap path
	// was last resolved.
	since     time.Time
	lastCheck time.Time
	// starting is true between arming a decoder and its first frame; playing is
	// true once frames arrive.
	starting bool
	playing  bool
	cancel   context.CancelFunc
	// broken remembers clips this process could not decode, keyed by the clip
	// path the decoder was given (not the game's ROM path), so a bad file is not
	// retried on every frame.
	broken map[string]bool
	// frame is the newest decoded YUV frame, swapped with the decoder's spare
	// buffer rather than copied.
	frame []byte
	w, h  int32
	seq   uint64
	// tex belongs to the render thread; it is never touched by the decoder.
	tex      *sdl.Texture
	uploaded uint64
	frames   uint64
}

var snapState = snapPreviewState{broken: map[string]bool{}}

// snapDecoderArgs is the ffmpeg command for a looping, silent, pre-scaled
// preview. It is a function so the shape can be asserted without a process.
func snapDecoderArgs(path string, width, height int32, fps int) []string {
	return []string{
		// Loop the file forever so a 3-second clip stays alive while the user
		// reads the list; ffmpeg restarts the input instead of us marshalling
		// seeks.
		"-stream_loop", "-1",
		"-i", path,
		// Silent: a preview that beeps while browsing is a bug, and dropping
		// the audio stream is cheaper than decoding it.
		"-an",
		"-vf", "scale=" + strconv.Itoa(int(width)) + ":" + strconv.Itoa(int(height)) +
			":flags=bilinear,fps=" + strconv.Itoa(fps),
		"-f", "rawvideo",
		"-pix_fmt", "yuv420p",
		"-",
	}
}

// snapFFmpegPath resolves the bundled (or system) ffmpeg. A seam so tests never
// look for a binary.
var snapFFmpegPath = func(config *Config) string { return getToolPath("ffmpeg", config) }

// snapStartDecoder is the seam the controller tests replace: it starts the
// decode loop and returns when it ends. Production uses snapPreviewRun.
var snapStartDecoder = func(ctx context.Context, path string, config *Config) { snapPreviewRun(ctx, path, config) }

// snapPreviewSync is the only entry point the panel needs. It is idempotent and
// called once per frame: it decides whether the focused game deserves a preview
// and starts, keeps or stops the decoder.
func snapPreviewSync(now time.Time) {
	if !gameLib.open || !snapPreviewEnabled(appConfig) {
		snapPreviewStop()
		return
	}
	game, ok := selectedLibraryGame()
	key := ""
	if ok {
		key = game.Path
	}

	snapState.mu.Lock()
	sameGame := gameLib.open && key != "" && key == snapState.game
	snapState.mu.Unlock()

	if !sameGame {
		// Leaving a game (or closing) ends its decoder before the new one is
		// considered, so two processes never run at once.
		snapPreviewStop()
		if key == "" {
			return
		}
		snap := gameSnapPath(game, snapPreviewRoot(appConfig))
		snapState.mu.Lock()
		snapState.game = key
		snapState.snap = snap
		snapState.since = now
		snapState.lastCheck = now
		snapState.mu.Unlock()
		return
	}

	snapState.mu.Lock()
	if snapState.starting || snapState.playing {
		snapState.mu.Unlock()
		return
	}
	// The memo is keyed by the clip the decoder was handed, which is what
	// snapPreviewMarkBroken records. Reading it by the game path instead meant
	// the two keys never matched: a clip ffmpeg could not decode was marked
	// broken, the mark was then looked up under a name nothing wrote, and the
	// panel restarted ffmpeg on the next frame — the process storm this memo
	// exists to prevent.
	if snapState.broken[snapState.snap] {
		snapState.mu.Unlock()
		return
	}
	since := snapState.since
	snap := snapState.snap
	recheck := now.Sub(snapState.lastCheck) >= snapPreviewRecheck
	if recheck {
		snapState.lastCheck = now
	}
	snapState.mu.Unlock()

	if snap == "" && recheck {
		// The clip may have been copied onto the card while the panel was open.
		snap = gameSnapPath(game, snapPreviewRoot(appConfig))
		snapState.mu.Lock()
		snapState.snap = snap
		snapState.mu.Unlock()
	}
	if snap == "" || now.Sub(since) < snapPreviewArmDelay {
		return
	}

	snapState.mu.Lock()
	if snapState.starting || snapState.playing || snapState.game != key || snapState.broken[snap] {
		snapState.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	snapState.cancel = cancel
	snapState.starting = true
	snapState.mu.Unlock()

	go snapStartDecoder(ctx, snap, appConfig)
}

// snapPreviewStop ends the decoder, forgets the frames and drops the texture.
// It runs on the render thread (close, focus change, switch) and is safe to
// call when nothing is playing.
func snapPreviewStop() {
	snapState.mu.Lock()
	cancel := snapState.cancel
	snapState.cancel = nil
	snapState.starting = false
	snapState.playing = false
	snapState.game = ""
	snapState.snap = ""
	snapState.frame = nil
	snapState.w, snapState.h = 0, 0
	snapState.seq++
	tex := snapState.tex
	snapState.tex = nil
	snapState.uploaded = 0
	snapState.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if tex != nil {
		tex.Destroy()
	}
}

// snapPreviewReset is the test hook: it stops the preview and forgets the
// session's broken-file memo.
func snapPreviewReset() {
	snapPreviewStop()
	snapState.mu.Lock()
	snapState.broken = map[string]bool{}
	snapState.since = time.Time{}
	snapState.lastCheck = time.Time{}
	snapState.frames = 0
	snapState.mu.Unlock()
}

// snapPreviewRun decodes the clip until the context is cancelled. It is the
// production body of snapStartDecoder.
func snapPreviewRun(ctx context.Context, path string, config *Config) {
	defer func() {
		snapState.mu.Lock()
		snapState.starting = false
		snapState.playing = false
		snapState.mu.Unlock()
	}()

	ffmpeg := snapFFmpegPath(config)
	if ffmpeg == "" {
		log.Printf("[SNAP] no ffmpeg available; video snaps are off")
		snapPreviewMarkBroken(path)
		return
	}
	args := snapDecoderArgs(path, snapPreviewWidth, snapPreviewHeight, snapPreviewFPS)
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	// ffmpeg's chatter is not the app's log, but the reason a clip produced no
	// frames is worth one line, so stderr is captured and only quoted on
	// failure.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[SNAP] %s: stdout pipe failed: %v", filepath.Base(path), err)
		snapPreviewMarkBroken(path)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[SNAP] %s: ffmpeg failed to start: %v", filepath.Base(path), err)
		snapPreviewMarkBroken(path)
		return
	}

	frameSize := int(snapPreviewWidth * snapPreviewHeight * 3 / 2)
	buf := make([]byte, frameSize)
	got := false
	for {
		if _, err := io.ReadFull(pipe, buf); err != nil {
			break
		}
		got = true
		snapState.mu.Lock()
		if snapState.frame == nil || len(snapState.frame) != frameSize {
			snapState.frame = make([]byte, frameSize)
		}
		// Swap rather than copy: the renderer reads the previous buffer while
		// the decoder fills this one. A 320x180 YUV frame is 86 KB, so a copy
		// would not be fatal — the point is that the render thread never reads
		// a buffer that is being written.
		buf, snapState.frame = snapState.frame, buf
		snapState.w, snapState.h = snapPreviewWidth, snapPreviewHeight
		snapState.seq++
		snapState.frames++
		if !snapState.playing {
			snapState.playing = true
			snapState.starting = false
		}
		snapState.mu.Unlock()
	}
	_ = cmd.Wait()
	if !got {
		// No frame at all: a broken clip. Remember it so the panel does not
		// restart ffmpeg on every frame it draws.
		snapPreviewMarkBroken(path)
		reason := strings.TrimSpace(stderr.String())
		if idx := strings.IndexByte(reason, '\n'); idx >= 0 {
			reason = reason[:idx]
		}
		log.Printf("[SNAP] %s produced no frames (%s); ignoring it this session", filepath.Base(path), reason)
	}
}

// snapPreviewMarkBroken records a clip that cannot be decoded.
func snapPreviewMarkBroken(path string) {
	snapState.mu.Lock()
	if snapState.broken == nil {
		snapState.broken = map[string]bool{}
	}
	snapState.broken[path] = true
	snapState.starting = false
	snapState.playing = false
	snapState.mu.Unlock()
}

// snapPreviewTexture uploads the newest frame and returns it, or nil when there
// is no frame for this game.
//
// This is the render-thread half: the texture is created and updated here, and
// only here, exactly like the video player's UpdateCurrentTexture.
func snapPreviewTexture(renderer *sdl.Renderer, gamePath string) *sdl.Texture {
	if renderer == nil {
		return nil
	}
	snapState.mu.Lock()
	if snapState.game != gamePath || snapState.frame == nil || snapState.w <= 0 || snapState.h <= 0 {
		snapState.mu.Unlock()
		return nil
	}
	frame := snapState.frame
	w, h, seq := snapState.w, snapState.h, snapState.seq
	uploaded := snapState.uploaded
	tex := snapState.tex
	snapState.mu.Unlock()

	if tex != nil && seq == uploaded {
		return tex
	}
	if len(frame) != int(w*h*3/2) {
		return nil
	}
	if tex != nil {
		_, _, tw, th, qerr := tex.Query()
		if qerr != nil || tw != w || th != h {
			tex.Destroy()
			tex = nil
			snapState.mu.Lock()
			snapState.tex = nil
			snapState.uploaded = 0
			snapState.mu.Unlock()
		}
	}
	if tex == nil {
		newTex, err := renderer.CreateTexture(uint32(sdl.PIXELFORMAT_IYUV), sdl.TEXTUREACCESS_STREAMING, w, h)
		if err != nil {
			log.Printf("[SNAP] CreateTexture failed: %v", err)
			return nil
		}
		tex = newTex
		snapState.mu.Lock()
		snapState.tex = tex
		snapState.mu.Unlock()
	}

	yBytes, uvBytes := int(w*h), int(w*h)/4
	uStart, vStart := yBytes, yBytes+uvBytes
	if err := tex.UpdateYUV(nil, frame[:uStart], int(w),
		frame[uStart:vStart], int(w)/2,
		frame[vStart:], int(w)/2); err != nil {
		log.Printf("[SNAP] texture upload failed: %v", err)
		tex.Destroy()
		snapState.mu.Lock()
		if snapState.tex == tex {
			snapState.tex = nil
		}
		snapState.uploaded = 0
		snapState.mu.Unlock()
		return nil
	}
	snapState.mu.Lock()
	snapState.uploaded = seq
	snapState.mu.Unlock()
	return tex
}

// snapPreviewDropTexture releases the texture when its renderer goes away. It
// is called from destroyTestRenderer for the same reason every other
// renderer-owned cache is.
func snapPreviewDropTexture() {
	snapState.mu.Lock()
	tex := snapState.tex
	snapState.tex = nil
	snapState.uploaded = 0
	snapState.frame = nil
	snapState.seq++
	snapState.mu.Unlock()
	if tex != nil {
		tex.Destroy()
	}
}

// drawSnapFrame letterboxes a 16:9 preview inside the square art box and marks
// it, so a moving picture is never mistaken for a box art that happens to be
// animated.
func drawSnapFrame(renderer *sdl.Renderer, config *Config, rect sdl.Rect, tex *sdl.Texture) {
	if renderer == nil || tex == nil {
		return
	}
	_, _, tw, th, err := tex.Query()
	if err != nil || tw <= 0 || th <= 0 {
		return
	}
	inner := rect.W - 16
	if rect.H-16 < inner {
		inner = rect.H - 16
	}
	dw := inner
	dh := inner * th / tw
	if dh > inner {
		dh = inner
		dw = inner * tw / th
	}
	dst := sdl.Rect{
		X: rect.X + (rect.W-dw)/2,
		Y: rect.Y + (rect.H-dh)/2,
		W: dw,
		H: dh,
	}
	renderer.Copy(tex, nil, &dst)

	// Letterbox bars, so the clip reads as a clip inside the frame rather than
	// the art box shrinking.
	if dst.H < rect.H-16 {
		bar := dst.Y - rect.Y
		renderer.SetDrawColor(0, 0, 0, 120)
		renderer.FillRect(&sdl.Rect{X: rect.X + 1, Y: rect.Y + 1, W: rect.W - 2, H: bar - 1})
		bottomY := dst.Y + dst.H
		renderer.FillRect(&sdl.Rect{X: rect.X + 1, Y: bottomY, W: rect.W - 2, H: rect.Y + rect.H - bottomY - 1})
	}

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	const label = "SNAP"
	lw, lh, _ := font.SizeUTF8(label)
	chipW, chipH := int32(lw)+20, int32(lh)+8
	chipX, chipY := dst.X+8, dst.Y+8
	fillRoundedRect(renderer, chipX, chipY, chipW, chipH, chipH/2, WithAlpha(ColorOverlay, 200))
	// A solid dot before the word: the accent reads as "playing", the label says
	// what it is.
	dotR := int32(3)
	fillCircle(renderer, chipX+9, chipY+chipH/2, dotR, installHighlight())
	renderText(renderer, config, font, label, ColorTextPrimary(), chipX+15, chipY+4)
}

// snapPreviewOpen reports whether a frame is being shown for this game. The
// footer uses it to name the feature only when it is actually doing something.
func snapPreviewOpen(gamePath string) bool {
	snapState.mu.Lock()
	defer snapState.mu.Unlock()
	return snapState.playing && snapState.game == gamePath
}

// snapPreviewStatus is one line for the diagnostics view and the tests: what
// the preview is doing right now.
func snapPreviewStatus() string {
	snapState.mu.Lock()
	defer snapState.mu.Unlock()
	switch {
	case snapState.playing:
		return "playing " + filepath.Base(snapState.snap)
	case snapState.starting:
		return "starting " + filepath.Base(snapState.snap)
	case snapState.game == "":
		return "idle"
	default:
		return "waiting for a snap"
	}
}

// The only child process here is ffmpeg, started with an argv — never through a
// shell, so a file name with spaces or quotes is one argument.

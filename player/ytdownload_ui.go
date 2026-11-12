package main

// ytdownload_ui.go — the download on the loading card.
//
// A failed stream's download used to run under the same card as every other
// load: "Loading <title>", a spinner, a slogan. Its progress was parsed and
// then never drawn, so a download of several minutes could not be told from a
// hang, and the tester who reported "never downloads anyways" may well have
// watched one finish its first half. While ytdownload.go fetches the file the
// card now says "Downloading <title>" and, in the band the slogan uses, draws
// a bar with the percentage, the size, the speed and the time left; before the
// size is known the bar is a segment moving to and fro. B cancels, as on every
// load (the card's own badge), and the card is drawn over whatever screen is
// up (main.go draws it whenever a load is active), so it is there after the
// failed player closed as well.

import (
	"fmt"
	"strings"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ytdlSetPlaybackPhase keeps core.go's playback phase variables in step with
// the download, as playWithTempFile kept them.
func ytdlSetPlaybackPhase(phase string, progress float64, errText string) {
	videoPlaybackMutex.Lock()
	videoPlaybackPhase = phase
	videoPlaybackPhaseAt = sdl.GetTicks64()
	videoPlaybackProgress = progress
	videoPlaybackError = errText
	videoPlaybackMutex.Unlock()
}

// ytDownloadCardStatus is the loading card's status line while the download of
// the video being prepared runs, or "" when none does.
func ytDownloadCardStatus(title string) string {
	if _, ok := ytdlSnapshot(); !ok {
		return ""
	}
	if title = strings.TrimSpace(title); title != "" {
		return "Downloading " + title
	}
	return "Downloading the video…"
}

// ytdlDetailLine is the line under the bar.
func ytdlDetailLine(st ytdlState) string {
	switch st.stage {
	case ytdlStageTransfer:
		return ytdlTransferLine(st)
	case ytdlStageJoining:
		return "Joining the picture and the sound…"
	case ytdlStageOpening:
		return "Opening the video…"
	}
	if st.attempt > 1 {
		return fmt.Sprintf("Trying another way (%d of %d)…", st.attempt, st.attempts)
	}
	if st.youTube {
		return "Asking YouTube for the file…"
	}
	return "Asking the site for the file…"
}

// ytdlTransferLine says where a transfer is: "34% · 12.1 MB of 35.6 MB ·
// 1.4 MiB/s · 0:17 left", with whatever of that is known.
func ytdlTransferLine(st ytdlState) string {
	parts := make([]string, 0, 4)
	if st.fraction >= 0 {
		pct := int(st.fraction * 100)
		if pct > 100 {
			pct = 100
		}
		parts = append(parts, fmt.Sprintf("%d%%", pct))
	}
	switch {
	case st.total > 0 && st.estimate:
		parts = append(parts, formatBytes(uint64(st.done))+" of ~"+formatBytes(uint64(st.total)))
	case st.total > 0:
		parts = append(parts, formatBytes(uint64(st.done))+" of "+formatBytes(uint64(st.total)))
	case st.done > 0:
		parts = append(parts, formatBytes(uint64(st.done)))
	}
	if speed := formatDownloadSpeed(st.speed); speed != "" {
		parts = append(parts, speed)
	}
	if st.eta > 0 {
		parts = append(parts, formatDuration(st.eta.Seconds())+" left")
	}
	if len(parts) == 0 {
		return "Downloading…"
	}
	return strings.Join(parts, " · ")
}

// renderYtDownloadCardBody draws the download's bar and its line in the band
// the slogan uses, and reports whether it did. When it did not (no download
// runs) the card draws its slogan as before.
func renderYtDownloadCardBody(renderer *sdl.Renderer, config *Config, font *ttf.Font, cardX, cardY, cardW int32) bool {
	if renderer == nil || font == nil {
		return false
	}
	st, ok := ytdlSnapshot()
	if !ok {
		return false
	}
	w := cardW - 2*loadingCardPad
	if w <= 0 {
		return false
	}
	track := sdl.Rect{X: cardX + loadingCardPad, Y: cardY + 162, W: w, H: 8}
	if st.stage == ytdlStageTransfer && st.fraction >= 0 {
		drawProgressBar(renderer, track, float32(st.fraction), glassAccent())
	} else {
		ytdlDrawIndeterminate(renderer, track)
	}
	line := ellipsize(font, ytdlDetailLine(st), w)
	lw, _, _ := font.SizeUTF8(line)
	renderText(renderer, config, font, line, loadingSloganInk(), cardX+(cardW-int32(lw))/2, track.Y+track.H+12)
	return true
}

// ytdlDrawIndeterminate is the bar while the size is not known yet: a short
// segment travelling to and fro, so the card visibly works.
func ytdlDrawIndeterminate(renderer *sdl.Renderer, track sdl.Rect) {
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	fillRoundedRect(renderer, track.X, track.Y, track.W, track.H, track.H/2, homeGlass(51))
	seg := track.W / 4
	span := track.W - seg
	if seg <= 0 || span <= 0 {
		return
	}
	period := uint64(span) * 2
	at := int32((sdl.GetTicks64() / 4) % period)
	if at > span {
		at = int32(period) - at
	}
	fillRoundedRect(renderer, track.X+at, track.Y, seg, track.H, track.H/2, glassAccent())
}

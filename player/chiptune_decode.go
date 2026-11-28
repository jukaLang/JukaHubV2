package main

// chiptune_decode.go — the decoder's input side for soundtracks.
//
// musicDecode builds its usual command line (musicFFmpegArgs) and, for a
// soundtrack, hands it here to be adjusted before ffmpeg starts:
//
//   - libgme formats get `-f libgme -track_index <n> -t <length>` in front of
//     the input. libgme loops most songs for ever, so the length is the
//     song's own (from its header) or the default 2:30, and an afade at the
//     end goes first in the filter chain, before the equaliser and atempo, so
//     both still apply and the fade happens in the song's own time. A seek
//     (-ss) stays an input option: libgme emulates up to that point.
//   - VGZ is gzip, which ffmpeg's libgme reads only from a plain VGM: the
//     file is inflated here and fed on stdin (`-i pipe:0`).
//   - SID is rendered by sidplay_*.go and fed on stdin as raw 44.1 kHz s16le
//     stereo; seeking happens in the SID player, the fade in ffmpeg.
//   - Tracker modules are left as they are: libopenmpt knows their length.
//
// Whether this ffmpeg has libgme and libopenmpt is asked once per binary
// (`ffmpeg -hide_banner -demuxers`, cached by size and modification time,
// like ffmpegtls.go's protocol probe). The handheld's own firmware ffmpeg
// may have neither; JukaHub's bundled BtbN build should have both. When one
// is missing the player says so plainly, once, and skips the song.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const chiptuneDemuxProbeTimeout = 5 * time.Second

var (
	errChiptuneNoGME    = errors.New("This ffmpeg cannot play NSF/SPC/VGM music (it has no libgme)")
	errChiptuneNoModule = errors.New("This ffmpeg cannot play tracker modules (MOD/XM/S3M/IT): it has no libopenmpt")
)

type chiptuneDemuxEntry struct {
	size  int64
	mtime time.Time
	names map[string]bool
	known bool
}

var (
	chiptuneDemuxMu    sync.Mutex
	chiptuneDemuxCache = map[string]chiptuneDemuxEntry{}
	// chiptuneDemuxRunner prints a tool's demuxer list; a seam for tests.
	chiptuneDemuxRunner = runFFmpegDemuxers
)

func runFFmpegDemuxers(ctx context.Context, tool string) (string, error) {
	cmd := hiddenCommandContext(ctx, tool, "-hide_banner", "-demuxers")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	return string(out), err
}

// chiptuneParseDemuxers reads `-demuxers` output: after the "--" (or "---")
// line each row is the flags ("D", "DE", "D d"…), the comma-separated names
// and a description. ok is false for output it does not recognise, so a new
// ffmpeg layout never reads as "no libgme".
func chiptuneParseDemuxers(out string) (map[string]bool, bool) {
	names := map[string]bool{}
	body := false
	for _, line := range strings.Split(out, "\n") {
		trim := strings.TrimSpace(line)
		if len(trim) >= 2 && strings.Trim(trim, "-") == "" {
			body = true
			continue
		}
		if !body {
			continue
		}
		f := strings.Fields(trim)
		if len(f) < 2 || !strings.Contains(f[0], "D") {
			continue
		}
		name := f[1]
		if len(f) > 2 && f[1] == "d" {
			name = f[2] // a device flag column
		}
		for _, n := range strings.Split(name, ",") {
			names[strings.ToLower(n)] = true
		}
	}
	return names, body && len(names) > 0
}

// chiptuneFFmpegHas reports whether the ffmpeg at tool can demux name.
// known is false when it could not be asked: the song is then tried anyway.
func chiptuneFFmpegHas(tool, name string) (has, known bool) {
	info, err := os.Stat(tool)
	if err != nil || info.IsDir() {
		return false, false
	}
	chiptuneDemuxMu.Lock()
	defer chiptuneDemuxMu.Unlock()
	e, ok := chiptuneDemuxCache[tool]
	if !ok || e.size != info.Size() || !e.mtime.Equal(info.ModTime()) {
		ctx, cancel := context.WithTimeout(context.Background(), chiptuneDemuxProbeTimeout)
		out, runErr := chiptuneDemuxRunner(ctx, tool)
		cancel()
		e = chiptuneDemuxEntry{size: info.Size(), mtime: info.ModTime()}
		if runErr == nil {
			e.names, e.known = chiptuneParseDemuxers(out)
			logInfo("chiptune", "%s: libgme %v, libopenmpt %v", tool, e.names["libgme"], e.names["libopenmpt"])
		} else {
			logWarn("chiptune", "could not ask %s for its demuxers: %v", tool, runErr)
		}
		chiptuneDemuxCache[tool] = e
	}
	return e.names[name], e.known
}

// chiptuneSIDModelSetting is "chiptune_sid_model" as the main thread last read
// it. The decoder that asks runs on a goroutine, and the live Custom map it
// would otherwise read is written by the main thread every second (playtime):
// a fatal concurrent map access (extras.go saveUserConfigDebounced has the
// history).
var chiptuneSIDModelSetting atomic.Value // string

func init() {
	deferStartupTask("chiptune-sid-model", chiptuneSIDModelRefresh)
}

// chiptuneSIDModelRefresh reads the setting. Main thread only.
func chiptuneSIDModelRefresh() {
	v := ""
	if appConfig != nil {
		v, _ = customString(appConfig.Variables.Custom, "chiptune_sid_model")
	}
	switch v = strings.TrimSpace(v); v {
	case "6581", "8580":
	default:
		v = ""
	}
	chiptuneSIDModelSetting.Store(v)
}

// chiptuneSIDModel is the SID chip the user asked for in jukaconfig.json
// ("chiptune_sid_model": "6581" or "8580"); "" follows each tune's header.
// Any goroutine: it answers the last value read and asks the main thread to
// read it again, so an edited setting reaches the next tune.
func chiptuneSIDModel() string {
	tbxPost(chiptuneSIDModelRefresh)
	v, _ := chiptuneSIDModelSetting.Load().(string)
	return v
}

// chiptuneDecodeArgs turns musicDecode's command line into a soundtrack's,
// and returns what has to be attached to the command (a stdin) before it
// starts. An error means the song cannot play here; it has been said.
func chiptuneDecodeArgs(ffmpegPath, path string, startSec, speed float64, rate int32) ([]string, func(*exec.Cmd), error) {
	file := chiptuneFileOf(path)
	ext := chiptuneExt(path)
	if chiptuneTrackerExts[ext] {
		if has, known := chiptuneFFmpegHas(ffmpegPath, "libopenmpt"); known && !has {
			chiptuneSayOnce(errChiptuneNoModule.Error())
			return nil, nil, errChiptuneNoModule
		}
		return musicFFmpegArgs(file, startSec, speed, rate), nil, nil
	}
	meta, _ := chiptuneMetaFor(path) // also fills the cache Now Playing reads
	go chiptuneCoverArt(path)        // warm the cover for Now Playing
	song := chiptuneSongOf(path, meta)
	secs, fade := meta.timing(song)
	total := secs + fade
	// A seek to (or past) the end plays the last half second: the player's
	// position already counts from startSec.
	if startSec < 0 {
		startSec = 0
	}
	if total > 0.5 && startSec > total-0.5 {
		startSec = total - 0.5
	}
	remaining := total - startSec
	filter := chiptuneFadeFilter(secs-startSec, fade, remaining)

	if chiptuneSIDExts[ext] {
		tune, err := sidLoadFile(file)
		if err == nil {
			err = tune.playable()
		}
		if err != nil {
			chiptuneSayOnce(err.Error())
			return nil, nil, err
		}
		// A seek emulates the tune up to the new place, which takes a while
		// deep into a song; a skip or a newer seek meanwhile ends it.
		music.mu.Lock()
		gen := music.gen
		music.mu.Unlock()
		stale := func() bool {
			music.mu.Lock()
			defer music.mu.Unlock()
			return music.gen != gen
		}
		stream, err := newSIDStream(tune, song, startSec, remaining, chiptuneSIDModel(), stale)
		if err != nil {
			return nil, nil, err
		}
		in := []string{"-f", "s16le", "-ar", strconv.Itoa(sidRate), "-ac", "2"}
		args := chiptuneSpliceArgs(musicFFmpegArgs("pipe:0", 0, speed, rate), in, filter)
		return args, func(cmd *exec.Cmd) { cmd.Stdin = stream }, nil
	}

	if has, known := chiptuneFFmpegHas(ffmpegPath, "libgme"); known && !has {
		chiptuneSayOnce(errChiptuneNoGME.Error())
		return nil, nil, errChiptuneNoGME
	}
	input := file
	var feed []byte
	if ext == ".vgz" {
		f, err := os.Open(file)
		if err != nil {
			return nil, nil, err
		}
		feed, err = chiptuneGunzip(f)
		f.Close()
		if err != nil {
			return nil, nil, err
		}
		input = "pipe:0"
	}
	in := []string{"-f", "libgme", "-track_index", strconv.Itoa(meta.track(song)),
		"-t", strconv.FormatFloat(remaining, 'f', 3, 64)}
	args := chiptuneSpliceArgs(musicFFmpegArgs(input, startSec, speed, rate), in, filter)
	if feed == nil {
		return args, nil, nil
	}
	return args, func(cmd *exec.Cmd) { cmd.Stdin = bytes.NewReader(feed) }, nil
}

// chiptuneFadeFilter is the afade that ends a looping song: it starts at
// fadeAt seconds into what ffmpeg decodes (the song's play time less the seek)
// and lasts fade seconds, shortened when the seek landed inside the fade.
func chiptuneFadeFilter(fadeAt, fade, remaining float64) string {
	if fade <= 0 {
		return ""
	}
	if fadeAt < 0 {
		fadeAt, fade = 0, remaining
	}
	if fade < 0.1 {
		return ""
	}
	return "afade=t=out:st=" + strconv.FormatFloat(fadeAt, 'f', 3, 64) +
		":d=" + strconv.FormatFloat(fade, 'f', 3, 64)
}

// chiptuneSpliceArgs puts input options in front of "-i" and the fade in
// front of the existing -af chain (or adds one before the output).
func chiptuneSpliceArgs(args, input []string, filter string) []string {
	out := make([]string, 0, len(args)+len(input)+2)
	filtered := filter == ""
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-i":
			out = append(out, input...)
			out = append(out, args[i])
		case args[i] == "-af" && i+1 < len(args) && !filtered:
			out = append(out, "-af", filter+","+args[i+1])
			filtered = true
			i++
		case i == len(args)-1 && !filtered:
			out = append(out, "-af", filter, args[i])
			filtered = true
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// The SID player is ffmpeg's stdin: it must stay an io.Reader whose io.EOF
// ends the song.
var _ io.Reader = (*sidStream)(nil)

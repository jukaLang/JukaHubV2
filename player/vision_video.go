package main

// vision_video.go — Juka watching a video or listening to a recording sent in
// Chat (vision_chat.go).
//
// ffmpeg does the work: the copy the player uses, from the tools folder or
// PATH, installed the way the player installs it when it is missing (the
// TrimUI firmware's own ffmpeg reads local files fine). One run reads what
// the file holds — "ffmpeg -i" prints its length, its picture size and its
// streams, so no ffprobe is needed — then one run per frame takes three
// frames spread over the clip (a fast seek each, shrunk by ffmpeg when the
// video is larger than what is sent; ffmpeg also turns a phone's portrait
// video upright), and one run writes the sound of the first ten minutes as
// 16 kHz mono WAV: what Whisper wants, at most 19 MB, under Groq's 25 MB
// upload limit. A track that never rises above a murmur is not sent (Whisper
// invents sentences for silence). The rest goes to Whisper streamed from the
// card with its length given up front, so a long clip never sits in memory,
// and with no language set, so a video in any language is written out in it.
//
// The work happens in a folder of its own under the video temp folder — on
// the handheld that is the SD card, since /tmp is RAM there — removed as soon
// as the clip is done; a folder an interrupted run left behind is swept by
// the next one. Every step has its own time limit, and quitting JukaHub stops
// ffmpeg (visionQuitCtx).

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// visionProbeTimeout, visionFrameTimeout and visionSoundTimeout bound one
	// ffmpeg run each: reading what a file holds, one frame (a 4K frame
	// decodes slowly on the handheld's cores), ten minutes of sound.
	visionProbeTimeout = 20 * time.Second
	visionFrameTimeout = 30 * time.Second
	visionSoundTimeout = 2 * time.Minute
	// visionSoundSeconds is how much of a clip is written out.
	visionSoundSeconds = 600
	// visionWhisperMaxBytes is Groq's upload limit for a transcription, with
	// a margin.
	visionWhisperMaxBytes = 24 << 20
	// visionTranscriptMax bounds what is said, as it goes into the question:
	// every word counts against the account's tokens a minute.
	visionTranscriptMax = 8000
	// visionWorkPrefix names the work folders under the video temp folder.
	visionWorkPrefix = "jukahub-vision-"
)

// visionWhisperHTTP is the client the transcriptions use: ten minutes of
// sound is a long upload on the handheld's Wi-Fi.
var visionWhisperHTTP = &http.Client{Timeout: 3 * time.Minute}

// visionClip is what Juka took from a video or a recording.
type visionClip struct {
	name       string
	duration   float64           // seconds; 0 when ffmpeg did not say
	video      bool              // it has pictures, not just a cover
	audio      bool              // it has a sound track
	frames     []jukaVisionImage // a few frames, in order
	times      []float64         // where each frame is, in seconds
	transcript string            // what is said; "" for nothing, or not known
	heard      bool              // the sound was checked: transcript is the answer
}

// visionProbe is what "ffmpeg -i" says about a file.
type visionProbe struct {
	duration      float64
	width, height int
	video, audio  bool
}

var (
	visionDurationRe = regexp.MustCompile(`Duration:\s*(\d+):(\d{2}):(\d{2}(?:\.\d+)?)`)
	visionSizeRe     = regexp.MustCompile(`\b(\d{2,5})x(\d{2,5})\b`)
)

// visionFFmpegHint is the ffmpeg the main thread last resolved with the
// config's tools path (visionNoteFFmpeg): the goroutine that watches a video
// reads no config.
var visionFFmpegHint atomic.Value // string

// visionNoteFFmpeg remembers where ffmpeg is, as the config says. Main
// thread: chatStageFile calls it when a video or a recording is attached.
func visionNoteFFmpeg() {
	if p := strings.TrimSpace(getToolPath("ffmpeg", appConfig)); toolPathRunnable(p) {
		visionFFmpegHint.Store(p)
	}
}

// visionFFmpegPath is the ffmpeg to run: the one the main thread noted, else
// the tools folder or PATH, installed there when it is missing.
func visionFFmpegPath(ctx context.Context) (string, error) {
	if p, _ := visionFFmpegHint.Load().(string); p != "" && toolPathRunnable(p) {
		return p, nil
	}
	return helperToolPathCtx(ctx, "ffmpeg", nil)
}

// visionWatch takes what Juka needs from a video or a recording: a few frames
// and what is said. A failure that is the user's to know is a visionUserError;
// a transcript that failed on the network leaves the clip without words and
// is not remembered, so the next question tries again. Remembered per file
// for follow-ups.
func visionWatch(ctx context.Context, key, name, path string) (visionClip, error) {
	cacheKey, err := visionFileKey(path)
	if err != nil {
		logWarn("groq", "clip %s: %v", name, err)
		return visionClip{}, visionSay(name + " isn't on this device any more")
	}
	if e, ok := visionCacheGet(cacheKey); ok && e.hasClip {
		clip := e.clip
		clip.name = name
		return clip, nil
	}
	ffmpeg, err := visionFFmpegPath(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return visionClip{}, ctx.Err()
		}
		logWarn("groq", "ffmpeg for %s: %v", name, err)
		return visionClip{}, visionSay("Juka needs ffmpeg to watch videos, and it couldn't be installed — try again online")
	}
	dir, err := visionWorkDir()
	if err != nil {
		logWarn("groq", "work folder for %s: %v", name, err)
		return visionClip{}, visionSay("Juka had no room to work on " + name + " — free some space")
	}
	defer os.RemoveAll(dir)
	probe, err := visionProbeFile(ctx, ffmpeg, path)
	if err != nil {
		if ctx.Err() != nil {
			return visionClip{}, ctx.Err()
		}
		logWarn("groq", "probe %s: %v", name, err)
		return visionClip{}, visionSay("Juka couldn't open " + name)
	}
	clip := visionClip{name: name, duration: probe.duration, video: probe.video, audio: probe.audio}
	if probe.video {
		for i, at := range visionFrameTimes(probe.duration) {
			im, err := visionGrabFrame(ctx, ffmpeg, path, at, probe, dir, i)
			if err != nil {
				if ctx.Err() != nil {
					return visionClip{}, ctx.Err()
				}
				logDebug("groq", "frame at %.1fs of %s: %v", at, name, err)
				continue
			}
			clip.frames = append(clip.frames, im)
			clip.times = append(clip.times, at)
		}
	}
	var hearErr error
	if probe.audio {
		clip.transcript, clip.heard, hearErr = visionHear(ctx, key, ffmpeg, path, dir)
		if ctx.Err() != nil {
			return visionClip{}, ctx.Err()
		}
		if hearErr != nil {
			logWarn("groq", "sound of %s: %v", name, hearErr)
		}
	}
	var apiErr *groqError
	var userErr *visionUserError
	switch {
	case len(clip.frames) > 0, clip.transcript != "":
	case !probe.video && clip.heard:
		// A recording with no words in it: that is the answer.
	case hearErr != nil && errors.As(hearErr, &userErr):
		// Already said in words (a recording too long to send in time).
		return visionClip{}, hearErr
	case hearErr != nil && (chatIsNetworkError(hearErr) || errors.As(hearErr, &apiErr)):
		// Nothing to show but its words, and the network or Groq did not
		// write them out: chatRunAI says why (a refused key, a busy minute),
		// or asks again once the network is back.
		return visionClip{}, hearErr
	case probe.video:
		return visionClip{}, visionSay("Juka couldn't watch " + name)
	default:
		return visionClip{}, visionSay("Juka couldn't hear " + name)
	}
	if !probe.audio || clip.heard {
		visionCachePut(visionCacheEntry{key: cacheKey, clip: clip, hasClip: true})
	}
	return clip, nil
}

// visionWorkDir makes the folder one clip's frames and sound are written to,
// under the video temp folder (the SD card on the handheld). Folders an
// interrupted run left behind go first.
func visionWorkDir() (string, error) {
	base := videoTempDir()
	matches, _ := filepath.Glob(filepath.Join(base, visionWorkPrefix+"*"))
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.IsDir() && time.Since(info.ModTime()) > visionChatTimeout {
			_ = os.RemoveAll(m)
		}
	}
	return os.MkdirTemp(base, visionWorkPrefix+"*")
}

// visionRunFFmpeg runs one ffmpeg command line within limit, without a
// console window on Windows. stderr, when not nil, receives what ffmpeg
// prints; otherwise its last line goes into the error.
func visionRunFFmpeg(ctx context.Context, ffmpeg string, args []string, limit time.Duration, stderr io.Writer) error {
	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(runCtx, ffmpeg, args...)
	tail := &textTail{}
	if stderr != nil {
		cmd.Stderr = stderr
	} else {
		cmd.Stderr = tail
	}
	// A child that left the pipe open must not hold the goroutine for good.
	cmd.WaitDelay = 3 * time.Second
	schedHideWindow(cmd)
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if runCtx.Err() != nil && ctx.Err() == nil {
		return fmt.Errorf("ffmpeg ran past %s", limit)
	}
	if line := tail.lastLine(); line != "" {
		return fmt.Errorf("%w: %s", err, line)
	}
	return err
}

// visionHead keeps the first max bytes written to it.
type visionHead struct {
	buf []byte
	max int
}

func (h *visionHead) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		h.buf = append(h.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// visionProbeFile asks ffmpeg what a file holds. "ffmpeg -i" with no output
// describes the input and exits with an error, which is expected.
func visionProbeFile(ctx context.Context, ffmpeg, path string) (visionProbe, error) {
	head := &visionHead{max: 64 << 10}
	_ = visionRunFFmpeg(ctx, ffmpeg, []string{"-hide_banner", "-nostdin", "-i", path}, visionProbeTimeout, head)
	if ctx.Err() != nil {
		return visionProbe{}, ctx.Err()
	}
	p, ok := visionParseProbe(string(head.buf))
	if !ok {
		return visionProbe{}, fmt.Errorf("ffmpeg can't read it (%s)", strings.TrimSpace(firstNonEmptyLine(string(head.buf))))
	}
	if !p.video && !p.audio {
		return visionProbe{}, errors.New("it holds neither pictures nor sound")
	}
	return p, nil
}

// visionParseProbe reads ffmpeg's description of an input: its length, and
// which streams it has. A cover picture in a song ("attached pic") is not a
// video. ok is false when ffmpeg described no input at all.
func visionParseProbe(text string) (visionProbe, bool) {
	if !strings.Contains(text, "Input #0") {
		return visionProbe{}, false
	}
	var p visionProbe
	if m := visionDurationRe.FindStringSubmatch(text); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		s, _ := strconv.ParseFloat(m[3], 64)
		p.duration = float64(h*3600+mi*60) + s
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Stream #") {
			continue
		}
		switch {
		case strings.Contains(line, ": Video: "):
			if strings.Contains(line, "(attached pic)") || p.video {
				continue
			}
			p.video = true
			if m := visionSizeRe.FindStringSubmatch(line); m != nil {
				p.width, _ = strconv.Atoi(m[1])
				p.height, _ = strconv.Atoi(m[2])
			}
		case strings.Contains(line, ": Audio: "):
			p.audio = true
		}
	}
	return p, true
}

// visionFrameTimes is where the frames are taken: a tenth of the way in, the
// middle and nine tenths (the first and last frames are often black). A clip
// under two seconds gives one frame; one of unknown length is tried at the
// start and a few seconds in.
func visionFrameTimes(d float64) []float64 {
	switch {
	case d <= 0:
		return []float64{0, 3, 10}
	case d < 2:
		return []float64{d / 2}
	}
	return []float64{d * 0.1, d * 0.5, d * 0.9}
}

// visionFrameScale is the filter that brings a w×h video within
// visionMaxSide, or "" when it already fits or its size is unknown (the frame
// is shrunk after decoding then). It fits a box rather than naming a size: a
// phone's portrait video is stored on its side and turned by ffmpeg before
// the filter sees it.
func visionFrameScale(w, h int) string {
	if w <= 0 || h <= 0 || max(w, h) <= visionMaxSide {
		return ""
	}
	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", visionMaxSide, visionMaxSide)
}

// visionGrabFrame takes the frame at `at` seconds as the vision model is sent
// it. JPEG first; PNG when this ffmpeg cannot write JPEG.
func visionGrabFrame(ctx context.Context, ffmpeg, path string, at float64, probe visionProbe, dir string, n int) (jukaVisionImage, error) {
	var lastErr error
	for _, ext := range []string{".jpg", ".png"} {
		out := filepath.Join(dir, "frame"+strconv.Itoa(n)+ext)
		args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}
		if at > 0 {
			args = append(args, "-ss", strconv.FormatFloat(at, 'f', 3, 64))
		}
		args = append(args, "-i", path, "-map", "0:v:0", "-frames:v", "1", "-an", "-sn", "-dn")
		if vf := visionFrameScale(probe.width, probe.height); vf != "" {
			args = append(args, "-vf", vf)
		}
		if ext == ".jpg" {
			args = append(args, "-q:v", "2")
		}
		args = append(args, out)
		if err := visionRunFFmpeg(ctx, ffmpeg, args, visionFrameTimeout, nil); err != nil {
			if ctx.Err() != nil {
				return jukaVisionImage{}, ctx.Err()
			}
			lastErr = err
			continue
		}
		data, err := os.ReadFile(out)
		if err != nil || len(data) == 0 {
			// ffmpeg found no frame there (a seek past the end): another
			// format would not find one either.
			return jukaVisionImage{}, errors.New("no frame at that time")
		}
		src, err := visionDecodeBytes(data)
		if err != nil {
			lastErr = err
			continue
		}
		return jukaVisionFromImage(src)
	}
	return jukaVisionImage{}, lastErr
}

// visionHear writes out what is said in the first ten minutes of a file's
// sound. heard is true when the answer is known: the transcript, or nothing
// for a track that stays near silence (not sent to Whisper at all). err says
// why it is not known.
func visionHear(ctx context.Context, key, ffmpeg, path, dir string) (transcript string, heard bool, err error) {
	wav := filepath.Join(dir, "sound.wav")
	if err := visionExtractSound(ctx, ffmpeg, path, wav); err != nil {
		return "", false, err
	}
	speech, err := visionWAVSpeech(wav)
	if err != nil {
		return "", false, err
	}
	if !speech {
		return "", true, nil
	}
	text, err := visionTranscribeFile(ctx, key, wav)
	if err != nil {
		return "", false, err
	}
	return visionCutText(text, visionTranscriptMax), true, nil
}

// visionExtractSound writes the first ten minutes of a file's first sound
// track as 16 kHz mono 16-bit WAV.
func visionExtractSound(ctx context.Context, ffmpeg, path, out string) error {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-i", path, "-map", "0:a:0", "-vn", "-sn", "-dn",
		"-t", strconv.Itoa(visionSoundSeconds), "-ac", "1", "-ar", strconv.Itoa(vkVoiceRate),
		"-c:a", "pcm_s16le", "-f", "wav", out}
	if err := visionRunFFmpeg(ctx, ffmpeg, args, visionSoundTimeout, nil); err != nil {
		return err
	}
	info, err := os.Stat(out)
	if err != nil {
		return err
	}
	switch {
	case info.Size() <= 44:
		return errors.New("no sound came out")
	case info.Size() > visionWhisperMaxBytes:
		return fmt.Errorf("the sound is %s, over Whisper's limit", chatSizeLabel(info.Size()))
	}
	return nil
}

// visionWAVSpeech reports whether a 16-bit mono WAV rises above voice
// typing's silence level (vkVoiceSilence) in any tenth of a second.
func visionWAVSpeech(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	head := make([]byte, 4096)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	off := visionWAVData(head[:n])
	if off < 0 {
		return false, errors.New("not a WAV file")
	}
	if _, err := f.Seek(int64(off), io.SeekStart); err != nil {
		return false, err
	}
	window := make([]byte, vkVoiceRate/10*2)
	for {
		m, err := io.ReadFull(f, window)
		if m >= 2 && vkPCMLevel(window[:m&^1]) >= vkVoiceSilence {
			return true, nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return false, nil
			}
			return false, err
		}
	}
}

// visionWAVData is the offset of a WAV's samples: the RIFF chunks are walked
// to "data", since ffmpeg may put a LIST chunk before it. -1 when head is not
// a WAV or the data chunk is not in it.
func visionWAVData(head []byte) int {
	if len(head) < 12 || string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return -1
	}
	off := 12
	for off+8 <= len(head) {
		id := string(head[off : off+4])
		size := int(binary.LittleEndian.Uint32(head[off+4 : off+8]))
		if id == "data" {
			return off + 8
		}
		off += 8 + size + size&1
	}
	return -1
}

// visionTranscribeFile sends a WAV file to Groq's Whisper (voice typing's
// model and endpoint) and returns the text. The file streams from the card
// inside a multipart body whose length is known up front, so the upload is
// never held in memory and needs no chunked encoding. No language is given:
// Whisper finds it. The key travels only in the Authorization header.
func visionTranscribeFile(ctx context.Context, key, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	// The multipart framing around the file: everything up to its first byte,
	// then everything after its last.
	var frame bytes.Buffer
	mw := multipart.NewWriter(&frame)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="sound.wav"`)
	header.Set("Content-Type", "audio/wav")
	if _, err := mw.CreatePart(header); err != nil {
		return "", err
	}
	headLen := frame.Len()
	for _, field := range [][2]string{{"model", vkVoiceModel}, {"response_format", "json"}, {"temperature", "0"}} {
		if err := mw.WriteField(field[0], field[1]); err != nil {
			return "", err
		}
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	head, tail := frame.Bytes()[:headLen], frame.Bytes()[headLen:]
	body := io.MultiReader(bytes.NewReader(head), io.LimitReader(f, info.Size()), bytes.NewReader(tail))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, vkVoiceEndpoint, body)
	if err != nil {
		return "", err
	}
	req.ContentLength = int64(len(head)) + info.Size() + int64(len(tail))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := visionWhisperHTTP.Do(req)
	if err != nil {
		if ctx.Err() == nil && os.IsTimeout(err) {
			// The upload itself ran out of time (ten minutes of sound on a slow
			// link). That is not a dropped network: passed on raw it read as one,
			// and Chat asked again for ever, re-running ffmpeg and re-sending the
			// whole file each time. Said in words, it ends the question.
			return "", visionSay("That recording took too long to send — try a shorter one")
		}
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", groqErrorFromResponse(resp.StatusCode, raw)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("Groq returned an unreadable transcript")
	}
	return strings.TrimSpace(out.Text), nil
}

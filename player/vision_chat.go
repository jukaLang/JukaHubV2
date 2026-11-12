package main

// vision_chat.go — pictures, videos and recordings for Juka AI in Chat.
//
// A question in the Juka AI tab may carry up to three files (chatAIFilesMax).
// Text files still go with it as excerpts (chatAIFileContext). What can be
// seen or heard goes to Juka's eyes and ears instead:
//
//   - a picture (JPEG, PNG, GIF, WebP, BMP) is decoded, turned upright by its
//     EXIF orientation, shrunk to 1024 pixels on the long side and sent to a
//     vision model with the question (groqvision.go);
//   - a video is shown as three frames spread over the clip, and what is said
//     in its first ten minutes is written out by Whisper and added to the
//     question (vision_video.go);
//   - a recording (an audio file) is only heard: its words go with the
//     question to the usual chat models.
//
// A follow-up with no files of its own shows the latest files sent earlier
// in the conversation again — the rule text files follow — because every
// request starts from nothing, and "what colour is the car?" needs the
// picture. What was taken from the last few files (a picture's JPEG, a
// video's frames and words) is kept, so a follow-up does not decode the photo
// or watch the video again.
//
// It all runs on chatRunAI's goroutine and reads no config: the key is the
// credentials snapshot chatRunAI was handed, and ffmpeg's path the one the
// main thread noted when the file was attached (visionNoteFFmpeg). The typing
// bubble says what Juka is doing ("Watching the video…") until the answer
// starts to arrive.

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// visionKind is what Juka does with a file in a question.
type visionKind int

const (
	visionNone         visionKind = iota // text, or anything else: not seen or heard
	visionPicture                        // looked at
	visionVideo                          // watched: frames and what is said
	visionAudio                          // heard: what is said
	visionPictureOther                   // a picture format nothing here opens
)

// visionChatTimeout bounds everything one question with pictures or videos
// takes: watching (ffmpeg, the transcript) and the answer.
const visionChatTimeout = 6 * time.Minute

// visionMixedQuestion is what a mix of files sent without words asks.
const visionMixedQuestion = "Have a look at these files and tell me what stands out."

// visionPictureExts are the pictures Juka opens: Go's decoders, and BMP and
// WebP through SDL_image (imgvDecodeFile). A plain list on purpose: it is
// read on chatRunAI's goroutine, where isImageExtension must not run (it may
// ask SDL whether WebP decodes).
var visionPictureExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".webp": true,
}

// visionOtherPictureExts are picture formats nothing on the device decodes,
// with the name a refusal gives them.
var visionOtherPictureExts = map[string]string{
	".heic": "HEIC", ".heif": "HEIF", ".avif": "AVIF", ".tif": "TIFF", ".tiff": "TIFF",
	".jxl": "JPEG XL", ".svg": "SVG", ".psd": "PSD", ".dng": "RAW", ".cr2": "RAW",
	".nef": "RAW", ".arw": "RAW",
}

// visionVideoExts are the videos ffmpeg is asked to watch. Which streams a
// file really holds is ffmpeg's answer (an audio-only WebM is heard, not
// watched).
var visionVideoExts = map[string]bool{
	".mp4": true, ".mkv": true, ".avi": true, ".mov": true, ".webm": true, ".flv": true,
	".wmv": true, ".mpg": true, ".mpeg": true, ".ts": true, ".m2ts": true, ".mts": true,
	".m4v": true, ".3gp": true, ".ogv": true,
}

// visionQuitCtx is cancelled when JukaHub quits, so ffmpeg and a request in
// flight stop with it.
var visionQuitCtx, visionQuitStop = context.WithCancel(context.Background())

func init() {
	Cleanup = append(Cleanup, func() { visionQuitStop() })
}

// visionKindOf sorts a file by its extension, or by its MIME type when the
// name has none. Safe from any goroutine.
func visionKindOf(f ChatFile) visionKind {
	ext := chatFileExt(f)
	if ext == "" {
		ctype := strings.ToLower(f.Type)
		switch {
		case strings.HasPrefix(ctype, "image/"):
			return visionPicture
		case strings.HasPrefix(ctype, "video/"):
			return visionVideo
		case strings.HasPrefix(ctype, "audio/"):
			return visionAudio
		}
		return visionNone
	}
	switch {
	case visionPictureExts[ext]:
		return visionPicture
	case visionOtherPictureExts[ext] != "":
		return visionPictureOther
	case isAudioFile("sound" + ext):
		return visionAudio
	case visionVideoExts[ext]:
		return visionVideo
	}
	return visionNone
}

// visionStageCheck is chatStageFile's test for a file going to Juka AI: a
// text file, a picture Juka opens, a video or a recording. What keeps a file
// out is the sentence the toast shows. Main thread: it may ask SDL whether
// WebP decodes, and it notes ffmpeg's path for the goroutine that will need
// it.
func visionStageCheck(path, name string) error {
	f := ChatFile{Name: name, Path: path}
	switch visionKindOf(f) {
	case visionPicture:
		if chatFileExt(f) == ".webp" && !webpDecodable() {
			return errors.New("Juka can't open WebP pictures here — send a JPEG or PNG")
		}
		return nil
	case visionPictureOther:
		return fmt.Errorf("Juka can't open %s pictures — send a JPEG or PNG", visionOtherPictureExts[chatFileExt(f)])
	case visionVideo, visionAudio:
		visionNoteFFmpeg()
		return nil
	}
	if binary, err := chatLooksBinary(path); err != nil || binary {
		return fmt.Errorf("Juka reads text files, pictures and videos — %s isn't one", name)
	}
	return nil
}

// visionPlaceholder is the composer's hint while files wait in the Juka AI
// tray.
func visionPlaceholder(files []ChatFile) string {
	pictures, videos := 0, 0
	for _, f := range files {
		switch visionKindOf(f) {
		case visionPicture:
			pictures++
		case visionVideo:
			videos++
		}
	}
	switch {
	case pictures == len(files) && pictures == 1:
		return "Ask about the picture…"
	case pictures == len(files):
		return "Ask about the pictures…"
	case videos == len(files) && videos == 1:
		return "Ask about the video…"
	}
	return "Ask about the file…"
}

// visionChatMedia picks the files of a question that Juka sees or hears: the
// question's own, or for a follow-up with no files the latest ones sent
// earlier in this conversation (earlier is then true) — chatAIFileContext's
// rule. Only files on this device count. history is the conversation before
// the question.
func visionChatMedia(prompt ChatMessage, history []ChatMessage) (files []ChatFile, earlier bool) {
	src := prompt.Files
	if len(src) == 0 {
		for i := len(history) - 1; i >= 0; i-- {
			msg := history[i]
			if chatIsSystem(msg) {
				break // "New conversation": Juka starts afresh
			}
			if chatIsOwn(msg) && len(msg.Files) > 0 {
				src, earlier = msg.Files, true
				break
			}
		}
	}
	for _, f := range src {
		if f.Path == "" {
			continue
		}
		switch visionKindOf(f) {
		case visionPicture, visionVideo, visionAudio:
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		earlier = false
	}
	return files, earlier
}

// visionChatAsk answers a question for Juka that shows pictures, videos or
// recordings (visionChatMedia). question is the user's words with any text
// file's excerpt already in it. With no picture left to show — a recording
// alone, or a follow-up whose files are gone — it goes to the chat models as
// text. The typing bubble says what Juka is doing until the answer starts.
// Runs on chatRunAI's goroutine.
func visionChatAsk(key string, models []string, promptID string, history []ChatMessage, question string, files []ChatFile, earlier bool) (string, error) {
	ctx, cancel := context.WithTimeout(visionQuitCtx, visionChatTimeout)
	defer cancel()
	var pictures []jukaVisionImage
	var clips []visionClip
	nPictures, nVideos, nAudio := 0, 0, 0
	for _, f := range files {
		kind := visionKindOf(f)
		switch kind {
		case visionPicture:
			chatStreamUpdate(promptID, "Looking at the picture…")
			im, err := visionPictureFile(f.Path)
			if err != nil {
				logWarn("groq", "picture %s: %v", f.Name, err)
				if earlier {
					continue // a follow-up still goes, without it
				}
				return "", visionSay(visionPictureProblem(f.Name, err))
			}
			pictures = append(pictures, im)
			nPictures++
		case visionVideo, visionAudio:
			if kind == visionVideo {
				chatStreamUpdate(promptID, "Watching the video…")
			} else {
				chatStreamUpdate(promptID, "Listening to the recording…")
			}
			clip, err := visionWatch(ctx, key, f.Name, f.Path)
			if err != nil {
				if ctx.Err() != nil {
					return "", visionCtxError(ctx)
				}
				if earlier && visionFriendlyError(err) != "" {
					continue
				}
				return "", err
			}
			clips = append(clips, clip)
			if clip.video {
				nVideos++
			} else {
				nAudio++
			}
		}
	}
	// Typing dots while Juka thinks, until words arrive.
	chatStreamUpdate(promptID, "")
	onDelta := func(partial string) {
		if chatStripThink(partial) != "" {
			chatStreamUpdate(promptID, partial)
		}
	}
	if len(pictures) == 0 && len(clips) == 0 {
		// A follow-up whose files are gone: a plain question.
		return chatGroqAsk(key, models, buildGroqMessagesFrom(history, question), onDelta)
	}
	q := strings.TrimSpace(question)
	if q == "" {
		q = visionChatDefaultQuestion(nPictures, nVideos, nAudio)
	}
	var b strings.Builder
	b.WriteString(q)
	if earlier {
		b.WriteString("\n\n(Shown again from earlier in this conversation, for reference.)")
	}
	for _, c := range clips {
		b.WriteString("\n\n")
		b.WriteString(visionClipNote(c))
	}
	text := b.String()
	// Pictures first, then the videos' frames taken in turn, so a model that
	// shows fewer still sees every picture and a frame of each video.
	images := append([]jukaVisionImage(nil), pictures...)
	for k := 0; ; k++ {
		added := false
		for _, c := range clips {
			if k < len(c.frames) {
				images = append(images, c.frames[k])
				added = true
			}
		}
		if !added {
			break
		}
	}
	messages := buildGroqMessagesFrom(history, text)
	if len(images) == 0 {
		return chatGroqAsk(key, models, messages, onDelta)
	}
	reply, err := groqVisionChat(ctx, key, messages[:len(messages)-1], text, images, onDelta)
	if err != nil && ctx.Err() != nil {
		return "", visionCtxError(ctx)
	}
	return reply, err
}

// visionCtxError words a question that ran out of time: a user error, so
// chatRunAI does not take it for the network and try the same long clip again
// and again. A quit goes on as it is.
func visionCtxError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return visionSay("That took Juka too long — try a shorter clip")
	}
	return ctx.Err()
}

// visionChatDefaultQuestion is what files sent without words ask.
func visionChatDefaultQuestion(pictures, videos, audio int) string {
	switch {
	case videos == 0 && audio == 0:
		return visionDefaultQuestion(pictures)
	case pictures == 0 && audio == 0 && videos == 1:
		return "What happens in this video?"
	case pictures == 0 && videos == 0 && audio == 1:
		return "What is said in this recording?"
	}
	return visionMixedQuestion
}

// visionClipNote tells the model what it is shown of a video or a recording,
// and what is said in it.
func visionClipNote(c visionClip) string {
	var b strings.Builder
	if c.video {
		b.WriteString("The video \"" + c.name + "\"")
	} else {
		b.WriteString("The recording \"" + c.name + "\"")
	}
	if c.duration > 0 {
		b.WriteString(" (" + visionClock(c.duration) + " long)")
	}
	switch {
	case c.video && len(c.frames) > 0:
		b.WriteString(" is shown as " + visionFramesPhrase(c.times) + ".")
	case c.video:
		b.WriteString(" could not be shown.")
	default:
		b.WriteString(".")
	}
	switch {
	case !c.audio:
		b.WriteString(" It has no sound.")
	case c.transcript != "":
		b.WriteString(" What is said in it: \"" + c.transcript + "\"")
	case c.heard:
		b.WriteString(" No words are heard in it.")
	default:
		b.WriteString(" Its sound could not be written out.")
	}
	return b.String()
}

// visionFramesPhrase names where the frames are: "a frame from 0:05",
// "frames from 0:06, 0:31 and 0:56".
func visionFramesPhrase(times []float64) string {
	if len(times) == 1 {
		return "a frame from " + visionClock(times[0])
	}
	labels := make([]string, len(times))
	for i, t := range times {
		labels[i] = visionClock(t)
	}
	return "frames from " + strings.Join(labels[:len(labels)-1], ", ") + " and " + labels[len(labels)-1]
}

// visionClock is a time in a clip: m:ss, or h:mm:ss past an hour.
func visionClock(sec float64) string {
	s := int(sec + 0.5)
	if s < 0 {
		s = 0
	}
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// visionCutText bounds a transcript to limit bytes, cut at a character and
// marked.
func visionCutText(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + " …"
}

// ── Pictures from files ──────────────────────────────────────────────────

// errVisionTooLarge is a picture whose size alone is too much to decode here.
var errVisionTooLarge = errors.New("the picture is too large to open here")

// visionPictureProblem words a picture that could not be read.
func visionPictureProblem(name string, err error) string {
	switch {
	case errors.Is(err, errVisionTooLarge):
		return name + " is too large for Juka to open here"
	case errors.Is(err, os.ErrNotExist):
		return name + " isn't on this device any more"
	}
	return "Juka couldn't open " + name
}

// visionPictureFile is a picture file as the vision model is sent it:
// decoded (JPEG, PNG and GIF by Go, BMP and WebP by SDL_image, the image
// viewer's way), shrunk, turned upright by its EXIF orientation (a phone's
// portrait photo is stored on its side) and encoded. Decodes share the
// app-wide cap (imageDecodeSem), and the result is kept for follow-ups.
func visionPictureFile(path string) (jukaVisionImage, error) {
	cacheKey, err := visionFileKey(path)
	if err != nil {
		return jukaVisionImage{}, err
	}
	if e, ok := visionCacheGet(cacheKey); ok && !e.hasClip {
		return e.picture, nil
	}
	if !imageDecodeSem.Acquire(visionQuitCtx, 1) {
		return jukaVisionImage{}, visionQuitCtx.Err()
	}
	defer imageDecodeSem.Release(1)
	if f, err := os.Open(path); err == nil {
		cfg, _, cfgErr := image.DecodeConfig(f)
		f.Close()
		if cfgErr == nil && int64(cfg.Width)*int64(cfg.Height) > imageDecodeMaxPixels() {
			return jukaVisionImage{}, errVisionTooLarge
		}
	}
	src, err := imgvDecodeFile(path)
	if err != nil {
		return jukaVisionImage{}, err
	}
	pic := visionScale(src, visionMaxSide)
	if pic == nil {
		return jukaVisionImage{}, errors.New("the picture is empty")
	}
	if orient := imgvFileOrientation(path); orient > 1 {
		// Opaque pixels read the same as straight or premultiplied alpha, so
		// the viewer's turn (made for *image.NRGBA) works on the same bytes.
		turned := imgvOrient(&image.NRGBA{Pix: pic.Pix, Stride: pic.Stride, Rect: pic.Rect}, orient)
		pic = &image.RGBA{Pix: turned.Pix, Stride: turned.Stride, Rect: turned.Rect}
	}
	im, err := visionEncodeFit(pic)
	if err != nil {
		return jukaVisionImage{}, err
	}
	visionCachePut(visionCacheEntry{key: cacheKey, picture: im})
	return im, nil
}

// ── What was taken from recent files ─────────────────────────────────────

// visionCacheEntry is what was taken from one file: a picture, or a clip.
type visionCacheEntry struct {
	key     string
	picture jukaVisionImage
	clip    visionClip
	hasClip bool
}

// visionCacheMax is how many files are remembered: a conversation's latest
// question carries at most three.
const visionCacheMax = 6

// visionCache holds the entries, oldest first.
var visionCache struct {
	mu      sync.Mutex
	entries []visionCacheEntry
}

// visionFileKey names a file as it is now: its path, size and time, so an
// edited file is read again.
func visionFileKey(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a file")
	}
	return fmt.Sprintf("%s|%d|%d", path, info.Size(), info.ModTime().UnixNano()), nil
}

func visionCacheGet(key string) (visionCacheEntry, bool) {
	visionCache.mu.Lock()
	defer visionCache.mu.Unlock()
	for _, e := range visionCache.entries {
		if e.key == key {
			return e, true
		}
	}
	return visionCacheEntry{}, false
}

func visionCachePut(e visionCacheEntry) {
	visionCache.mu.Lock()
	defer visionCache.mu.Unlock()
	kept := make([]visionCacheEntry, 0, visionCacheMax)
	for _, old := range visionCache.entries {
		if old.key != e.key {
			kept = append(kept, old)
		}
	}
	if len(kept) >= visionCacheMax {
		kept = kept[len(kept)-visionCacheMax+1:]
	}
	visionCache.entries = append(kept, e)
}

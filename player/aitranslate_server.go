package main

// aitranslate_server.go — JukaHub's own translation service for RetroArch's
// AI service (aitranslate.go): what ai_service_url points at while a game
// runs with "Translate games" on and JukaHub as the service.
//
// The server listens on 127.0.0.1 only, on a free port picked when the game
// is prepared (RetroArch reads the address from retroarch.cfg as it starts,
// so the port must be known before the launch; picking it then and holding
// the listener means no other program can take it in between), and answers
// only a random path written into that address. It lives exactly as long as
// the launch. One request is worked on at a time; one that comes while
// another is in hand is told to wait.
//
// RetroArch's request (its accessibility.h documents it): a POST whose query
// carries output=<formats it wants> (sound/wav, text/subs, image/png/png-a/
// bmp) and maybe source_lang / target_lang, with a JSON body:
//
//	{"image": "<base64 PNG or BMP>", "format": "png"|"bmp",
//	 "coords": [x, y, w, h], "viewport": [w, h],
//	 "label": "<system id>__<content id>", "state": {"paused": 0|1, …}}
//
// Only image and label are read here, and the picture's kind comes from its
// own first bytes (older builds sent a BMP without "format"). The frame goes
// to the vision model JukaHub's AI already uses (groqvision.go, the user's
// Groq key captured at launch, since no goroutine may read the config): a
// small console frame is first enlarged by a whole number with plain pixel
// copies, so pixel text stays sharp and big enough for the model to read.
// The model is asked for the on-screen text translated into the chosen
// language and nothing else.
//
// The reply is JSON with one of RetroArch's fields:
//
//	{"text": "<translation>"}    text mode: RetroArch shows it like subtitles
//	{"sound": "<base64 WAV>"}    speech mode: RetroArch plays it
//	{"error": "<one sentence>"}  shown by RetroArch as a notification
//
// Speech is the handheld's own voice (tts_local*.go: Piper's English voice on
// the handheld), rendered in memory and wrapped as a 16-bit mono WAV here.
// Error sentences stay in plain ASCII — RetroArch's notification font may
// have nothing else — and neither the picture nor the text is kept or
// logged: only sizes, times and failures reach the log.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// aiTrMaxBody bounds RetroArch's request: a 1080p frame as a base64 BMP
	// is about 8 MB.
	aiTrMaxBody = 24 << 20
	// aiTrMaxSide bounds a frame's width and height.
	aiTrMaxSide = 4096
	// aiTrTimeout bounds one translation, the voice included.
	aiTrTimeout = 20 * time.Second
	// aiTrUpscaleTo and aiTrUpscaleMax: a frame whose long side is under half
	// of aiTrUpscaleTo is enlarged by a whole number up to it, at most
	// aiTrUpscaleMax times (a GBA's 240 px becomes 960).
	aiTrUpscaleTo  = 960
	aiTrUpscaleMax = 4
	// aiTrTextMax bounds the text handed back, in characters.
	aiTrTextMax = 600
	// aiTrNoText is what the model says for a screen without text.
	aiTrNoText = "NO_TEXT"
	// aiTrPathPrefix starts the path the server answers.
	aiTrPathPrefix = "/translate/"
)

// aiTrServerSetup is what a launch hands the server, read on the main thread.
type aiTrServerSetup struct {
	key    string // the Groq key ("" answers with the error that says so)
	lang   aiTrLang
	speech bool // the launch reads aloud
	title  string
	system string
}

// aiTrServer is one launch's server.
type aiTrServer struct {
	setup  aiTrServerSetup
	token  string
	port   int
	srv    *http.Server
	ctx    context.Context
	cancel context.CancelFunc
	busy   atomic.Bool
	once   sync.Once
}

// aiTrRequest is the part of RetroArch's request JukaHub reads.
type aiTrRequest struct {
	Image  string `json:"image"`
	Format string `json:"format"`
	Label  string `json:"label"`
}

// aiTrReply is JukaHub's answer, in RetroArch's field names.
type aiTrReply struct {
	Text  string `json:"text,omitempty"`
	Sound string `json:"sound,omitempty"`
	Error string `json:"error,omitempty"`
}

// aiTrWant is what RetroArch's output parameter asks for.
type aiTrWant struct {
	sound, text, imageOnly bool
}

// aiTrServerStart opens the listener and serves on a goroutine. Main thread
// (a bind is quick).
func aiTrServerStart(setup aiTrServerSetup) (*aiTrServer, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || addr.Port <= 0 {
		_ = ln.Close()
		return nil, errors.New("no local port")
	}
	s := &aiTrServer{setup: setup, token: hex.EncodeToString(raw), port: addr.Port}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.srv = &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      aiTrTimeout + 15*time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logWarn("aitranslate", "the translation service stopped: %v", r)
			}
		}()
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logWarn("aitranslate", "the translation service stopped: %v", err)
		}
	}()
	return s, nil
}

// address is the URL RetroArch posts to.
func (s *aiTrServer) address() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.port) + aiTrPathPrefix + s.token
}

// close stops the server and any translation in hand. Any goroutine; twice
// is harmless.
func (s *aiTrServer) close() {
	s.once.Do(func() {
		s.cancel()
		_ = s.srv.Close()
	})
}

// ownPath reports whether path is the server's own (a trailing slash is
// allowed).
func (s *aiTrServer) ownPath(path string) bool {
	want := aiTrPathPrefix + s.token
	return subtle.ConstantTimeCompare([]byte(strings.TrimSuffix(path, "/")), []byte(want)) == 1
}

// ServeHTTP answers RetroArch.
func (s *aiTrServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.ownPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if !s.busy.CompareAndSwap(false, true) {
		aiTrWriteReply(w, aiTrReply{Error: "Still translating the last screen - try again in a moment"})
		return
	}
	defer s.busy.Store(false)
	start := time.Now()
	frame, label, err := aiTrReadRequest(http.MaxBytesReader(w, r.Body, aiTrMaxBody))
	if err != nil {
		logWarn("aitranslate", "RetroArch's request could not be read: %v", err)
		aiTrWriteReply(w, aiTrReply{Error: "JukaHub could not read the game's picture"})
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, aiTrTimeout)
	defer cancel()
	reply := s.translate(ctx, frame, label, aiTrWantFrom(r.URL.Query().Get("output")))
	switch {
	case reply.Error != "":
		logInfo("aitranslate", "no translation after %s: %s", time.Since(start).Round(time.Millisecond), reply.Error)
	case reply.Sound != "":
		logInfo("aitranslate", "translation read aloud after %s", time.Since(start).Round(time.Millisecond))
	default:
		logInfo("aitranslate", "translation shown after %s (%d characters)", time.Since(start).Round(time.Millisecond), utf8.RuneCountInString(reply.Text))
	}
	aiTrWriteReply(w, reply)
}

// translate turns one frame into RetroArch's answer.
func (s *aiTrServer) translate(ctx context.Context, frame image.Image, label string, want aiTrWant) aiTrReply {
	if want.imageOnly {
		return aiTrReply{Error: "JukaHub translates into text or speech: set RetroArch's AI Service Output to Text Mode"}
	}
	speak := want.sound && s.setup.speech
	if want.sound && !want.text && !speak {
		return aiTrReply{Error: "For JukaHub's translation set RetroArch's AI Service Output to Text Mode"}
	}
	if s.setup.key == "" {
		return aiTrReply{Error: "No AI key - add your Groq API key in JukaHub: Settings > General"}
	}
	if !IsOnline() {
		return aiTrReply{Error: "No internet - translating needs Wi-Fi"}
	}
	pic, err := aiTrVisionPicture(frame)
	if err != nil {
		logWarn("aitranslate", "the frame could not be prepared: %v", err)
		return aiTrReply{Error: "JukaHub could not read the game's picture"}
	}
	askFor := aiTrTimeout - time.Second
	if speak {
		// The voice needs the rest.
		askFor = aiTrTimeout * 3 / 4
	}
	actx, cancel := context.WithTimeout(ctx, askFor)
	answer, err := groqVisionAsk(actx, s.setup.key, aiTrSystemPrompt(s.setup.lang), aiTrQuestion(s.setup, label), []jukaVisionImage{pic}, nil)
	timedOut := errors.Is(actx.Err(), context.DeadlineExceeded)
	cancel()
	if err != nil {
		logWarn("aitranslate", "the vision model did not translate: %v", err)
		if timedOut {
			return aiTrReply{Error: "The translation took too long - try again"}
		}
		return aiTrReply{Error: aiTrPlain(chatFriendlyAIError(err))}
	}
	text, ok := aiTrCleanAnswer(answer)
	if !ok {
		return aiTrReply{Error: "No text to translate on this screen"}
	}
	if speak {
		wav, err := aiTrSpeak(ctx, text)
		if err != nil {
			logWarn("aitranslate", "the voice could not read the translation: %v", err)
			return aiTrReply{Error: "Could not read it aloud - is the handheld's voice installed? (JukaHub: Settings > Voice & AI)"}
		}
		return aiTrReply{Sound: base64.StdEncoding.EncodeToString(wav)}
	}
	return aiTrReply{Text: text}
}

// aiTrWantFrom reads RetroArch's output parameter. None at all (an older
// build) means text.
func aiTrWantFrom(output string) aiTrWant {
	var w aiTrWant
	picture := false
	for _, f := range strings.Split(strings.ToLower(output), ",") {
		switch strings.TrimSpace(f) {
		case "sound", "wav":
			w.sound = true
		case "text", "subs":
			w.text = true
		case "image", "png", "png-a", "bmp":
			picture = true
		}
	}
	w.imageOnly = picture && !w.sound && !w.text
	if !picture && !w.sound && !w.text {
		w.text = true
	}
	return w
}

// aiTrWriteReply writes one JSON answer. Non-ASCII text goes as UTF-8, not
// \u escapes, and <, > and & as themselves.
func aiTrWriteReply(w http.ResponseWriter, reply aiTrReply) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(reply); err != nil {
		http.Error(w, "reply failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	_, _ = w.Write(buf.Bytes())
}

// aiTrPlainMarks turns JukaHub's typographic marks into ASCII for
// RetroArch's notification font.
var aiTrPlainMarks = strings.NewReplacer("—", "-", "–", "-", "→", ">", "‘", "'", "’", "'", "“", "\"", "”", "\"", "…", "...")

// aiTrPlain is a JukaHub sentence in plain ASCII marks.
func aiTrPlain(s string) string { return aiTrPlainMarks.Replace(s) }

// ── The request ──────────────────────────────────────────────────────────

// aiTrReadRequest decodes RetroArch's JSON body into its frame and label.
func aiTrReadRequest(body io.Reader) (image.Image, string, error) {
	var req aiTrRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return nil, "", fmt.Errorf("the request is not JSON: %w", err)
	}
	data, err := aiTrBase64(req.Image)
	if err != nil {
		return nil, "", err
	}
	img, err := aiTrDecodeFrame(data)
	if err != nil {
		return nil, "", err
	}
	return img, req.Label, nil
}

// aiTrBase64 decodes the frame's base64, line breaks and missing padding
// allowed.
func aiTrBase64(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		}
		return r
	}, s)
	if s == "" {
		return nil, errors.New("the request has no picture")
	}
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		return nil, fmt.Errorf("the picture is not base64: %w", err)
	}
	return data, nil
}

// aiTrDecodeFrame decodes a BMP or a PNG, by its first bytes, within
// aiTrMaxSide.
func aiTrDecodeFrame(data []byte) (image.Image, error) {
	if len(data) >= 2 && data[0] == 'B' && data[1] == 'M' {
		return aiTrDecodeBMP(data)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the picture is neither PNG nor BMP: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > aiTrMaxSide || cfg.Height > aiTrMaxSide {
		return nil, fmt.Errorf("the picture is %d × %d", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// aiTrDecodeBMP reads the uncompressed 24- or 32-bit BMP RetroArch sends
// (blue, green, red per pixel; rows padded to four bytes; bottom-up unless
// the height is negative) into an opaque picture. Anything else is refused.
// Pure.
func aiTrDecodeBMP(data []byte) (*image.RGBA, error) {
	const fileHeader = 14
	if len(data) < fileHeader+40 || data[0] != 'B' || data[1] != 'M' {
		return nil, errors.New("not a BMP picture")
	}
	le := binary.LittleEndian
	offset := int64(le.Uint32(data[10:14]))
	info := int64(le.Uint32(data[14:18]))
	width := int64(int32(le.Uint32(data[18:22])))
	height := int64(int32(le.Uint32(data[22:26])))
	bpp := int64(le.Uint16(data[28:30]))
	compression := le.Uint32(data[30:34])
	if info < 40 || fileHeader+info > int64(len(data)) {
		return nil, errors.New("the BMP's header is damaged")
	}
	topDown := height < 0
	if topDown {
		height = -height
	}
	if width <= 0 || height <= 0 || width > aiTrMaxSide || height > aiTrMaxSide {
		return nil, fmt.Errorf("the BMP is %d × %d", width, height)
	}
	if (bpp != 24 && bpp != 32) || compression != 0 {
		return nil, fmt.Errorf("a %d-bit BMP with compression %d is not supported", bpp, compression)
	}
	step := bpp / 8
	stride := (width*step + 3) &^ 3
	if offset < fileHeader+info || offset+stride*(height-1)+width*step > int64(len(data)) {
		return nil, errors.New("the BMP is cut short")
	}
	w, h, st, sp := int(width), int(height), int(stride), int(step)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		src := data[int(offset)+y*st:]
		dy := h - 1 - y
		if topDown {
			dy = y
		}
		dst := img.Pix[dy*img.Stride:]
		for x := 0; x < w; x++ {
			p, q := x*sp, x*4
			dst[q], dst[q+1], dst[q+2], dst[q+3] = src[p+2], src[p+1], src[p], 255
		}
	}
	return img, nil
}

// aiTrVisionPicture is the frame as the vision model gets it. A small
// console frame is enlarged by a whole number with plain pixel copies — the
// pixel font stays crisp — and sent as PNG, which keeps those edges exactly
// and stays small for so few colours; anything larger goes the usual way
// (jukaVisionFromImage: at most 1024 px, JPEG).
func aiTrVisionPicture(src image.Image) (jukaVisionImage, error) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return jukaVisionImage{}, errors.New("the picture is empty")
	}
	k := min(aiTrUpscaleTo/max(w, h), aiTrUpscaleMax)
	if k < 2 {
		return jukaVisionFromImage(src)
	}
	read := visionRowReader(src)
	row := make([]uint8, w*3)
	big := image.NewRGBA(image.Rect(0, 0, w*k, h*k))
	for y := 0; y < h; y++ {
		read(b.Min.Y+y, row)
		top := y * k * big.Stride
		line := big.Pix[top : top+w*k*4]
		for x := 0; x < w; x++ {
			r, g, bl := row[x*3], row[x*3+1], row[x*3+2]
			for i := 0; i < k; i++ {
				p := (x*k + i) * 4
				line[p], line[p+1], line[p+2], line[p+3] = r, g, bl, 255
			}
		}
		for i := 1; i < k; i++ {
			copy(big.Pix[top+i*big.Stride:], line)
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, big); err == nil && buf.Len() <= visionMaxImageBytes {
		return jukaVisionImage{Data: buf.Bytes(), MIME: "image/png"}, nil
	}
	return jukaVisionFromImage(big)
}

// ── The question and the answer ──────────────────────────────────────────

// aiTrSystemPrompt tells the model its one job.
func aiTrSystemPrompt(lang aiTrLang) string {
	return "You translate the text on a video game screen for the player. " +
		"Read all the text in the picture (dialogue, menus, names, signs) and translate it into " + lang.English + ". " +
		"Reply with only the translation as plain text: no quotes, notes, romanisation or description of the picture. " +
		"Keep the game's order and line breaks. Text already in " + lang.English + " stays as it is. " +
		"If the picture has no readable text, reply exactly " + aiTrNoText + "."
}

// aiTrQuestion is the question that goes with the picture, naming the game
// when it is known (a name helps with its terms).
func aiTrQuestion(setup aiTrServerSetup, label string) string {
	q := "Translate the text on this game screen into " + setup.lang.English + "."
	if game := aiTrGameName(setup, label); game != "" {
		q += " The game: " + game + "."
	}
	return q
}

// aiTrGameName is the game's title and system from the launch, else
// RetroArch's label ("<system>__<content>"), cleaned and kept short.
func aiTrGameName(setup aiTrServerSetup, label string) string {
	name := strings.TrimSpace(setup.title)
	if name != "" && strings.TrimSpace(setup.system) != "" {
		name += " (" + strings.TrimSpace(setup.system) + ")"
	}
	if name == "" {
		name = strings.ReplaceAll(strings.TrimSpace(label), "__", " / ")
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	return strings.TrimSpace(name)
}

// aiTrCleanAnswer is the model's answer as RetroArch shows it: no wrapping
// quotes or code fence, no control characters, no runs of blank lines, at
// most aiTrTextMax characters. ok is false for a screen without text. Pure.
func aiTrCleanAnswer(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 && !strings.ContainsAny(s[:i], " \t") {
			s = s[i+1:] // the fence's language word
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
		s = strings.TrimSpace(s)
	}
	for _, q := range [][2]string{{"\"", "\""}, {"“", "”"}, {"«", "»"}} {
		if len(s) > len(q[0])+len(q[1]) && strings.HasPrefix(s, q[0]) && strings.HasSuffix(s, q[1]) &&
			!strings.Contains(s[len(q[0]):len(s)-len(q[1])], q[0]) {
			s = strings.TrimSpace(s[len(q[0]) : len(s)-len(q[1])])
		}
	}
	if strings.HasPrefix(strings.ToUpper(strings.Trim(s, " .!\"'`*")), aiTrNoText) {
		return "", false
	}
	var lines []string
	blank := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, line))
		if line == "" {
			blank = true
			continue
		}
		if blank && len(lines) > 0 {
			lines = append(lines, "")
		}
		blank = false
		lines = append(lines, line)
	}
	s = strings.Join(lines, "\n")
	if utf8.RuneCountInString(s) > aiTrTextMax {
		cut := string([]rune(s)[:aiTrTextMax])
		if i := strings.LastIndexAny(cut, " \n"); i > len(cut)/2 {
			cut = cut[:i]
		}
		s = strings.TrimSpace(cut) + "..."
	}
	return s, s != ""
}

// ── The voice ────────────────────────────────────────────────────────────

// aiTrSpeak renders the translation with the handheld's own voice into a WAV.
func aiTrSpeak(ctx context.Context, text string) ([]byte, error) {
	v, ok := ttsLocal()
	if !ok {
		return nil, errors.New("no voice on this device")
	}
	samples, rate, err := ttsLocalSpeak(ctx, v, strings.Join(strings.Fields(text), " "), "")
	if err != nil {
		return nil, err
	}
	samples = ttsTrim(samples, rate)
	if len(samples) == 0 || rate <= 0 {
		return nil, errors.New("the voice made no sound")
	}
	return aiTrWAV(samples, rate), nil
}

// aiTrWAV wraps mono 16-bit samples as a PCM WAV file. Pure.
func aiTrWAV(samples []int16, rate int) []byte {
	n := len(samples) * 2
	out := make([]byte, 44+n)
	le := binary.LittleEndian
	copy(out[0:], "RIFF")
	le.PutUint32(out[4:], uint32(36+n))
	copy(out[8:], "WAVE")
	copy(out[12:], "fmt ")
	le.PutUint32(out[16:], 16)
	le.PutUint16(out[20:], 1) // PCM
	le.PutUint16(out[22:], 1) // mono
	le.PutUint32(out[24:], uint32(rate))
	le.PutUint32(out[28:], uint32(rate*2))
	le.PutUint16(out[32:], 2)
	le.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	le.PutUint32(out[40:], uint32(n))
	for i, v := range samples {
		le.PutUint16(out[44+2*i:], uint16(v))
	}
	return out
}

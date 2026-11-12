package main

// groqvision.go — Juka looking at pictures: one question about a few images,
// sent to a vision model on Groq with the user's Groq key. Chat attachments
// (pictures, frames of a video: vision_chat.go, vision_video.go) and the game
// assistant's look at the screen both come through here.
//
// Two models can see. qwen/qwen3.8-27b answers first: it takes up to three
// images a request, and with reasoning_effort "none" — its instruct mode, the
// one Groq's model page recommends for everyday questions — it answers without
// thinking at length first, which is what a handheld waiting on Wi-Fi wants.
// meta-models/muse-spark-open takes up to five, so it goes first when there
// are more than three, and it is the fallback when Qwen is unavailable. Groq
// opens Muse only to some accounts, so a refusal from Muse is a reason to ask
// Qwen, never "your key is wrong". Only parameters Groq documents for these
// models are sent.
//
// Groq refuses a request with base64 pictures over 4 MB (HTTP 413), and every
// picture counts against the account's tokens a minute, which the free tier
// also answers with a 413. So pictures go small — at most 1024 pixels on the
// long side as JPEG (jukaVisionFromImage: a photo comes to 100–300 KB) — the
// request stays under 3.5 MB, and a 413 is met once by sending one picture
// (the token budget) or smaller pictures (the bytes) before it is put to the
// user in one sentence.
//
// Everything here is safe from any goroutine: nothing reads the config or
// touches SDL, and the HTTP client is shared.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // a caller's own PNG bytes decode for a smaller copy
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	visionModelQwen = "qwen/qwen3.8-27b"
	visionModelMuse = "meta-models/muse-spark-open"
	// visionQwenMaxImages and visionMuseMaxImages are what one request to each
	// model may carry (Groq's vision docs).
	visionQwenMaxImages = 3
	visionMuseMaxImages = 5
	// visionMaxSide is the long side a picture is sent at: enough to read a
	// screenshot's text, small enough to stay far under Groq's limits.
	visionMaxSide = 1024
	// visionJPEGQuality is the quality pictures are encoded at.
	visionJPEGQuality = 80
	// visionMaxImageBytes bounds one encoded picture; a busier one is encoded
	// again at a lower quality, then smaller.
	visionMaxImageBytes = 1 << 20
	// visionMaxRequestB64 keeps a request's pictures, as base64, under Groq's
	// 4 MB limit with room for the words.
	visionMaxRequestB64 = 3_500_000
	// visionDecodeMaxPixels bounds a picture decoded from bytes handed over
	// here (a retry's smaller copy, a caller's own encoding).
	visionDecodeMaxPixels = 40 << 20
	// visionRequestTimeout bounds one request, the streamed answer included.
	visionRequestTimeout = 2 * time.Minute
)

// jukaVisionImage is one picture for the vision model: encoded bytes and
// their MIME type ("image/jpeg" or "image/png").
type jukaVisionImage struct {
	Data []byte
	MIME string
}

// visionHTTP is the client vision requests share.
var visionHTTP = &http.Client{Timeout: visionRequestTimeout}

// visionUserError is a failure that is already one sentence for the user: a
// picture Juka cannot open, a video without ffmpeg, a request Groq found too
// large. Its text never carries the cause (that is logged where it happens),
// so nothing in it reads as a network failure worth retrying
// (chatIsNetworkError).
type visionUserError struct{ msg string }

func (e *visionUserError) Error() string { return e.msg }

// visionSay is a visionUserError.
func visionSay(msg string) error { return &visionUserError{msg: msg} }

// visionFriendlyError is the sentence for a vision failure, or "" when err is
// not one (chatFriendlyAIError words the rest).
func visionFriendlyError(err error) string {
	var vu *visionUserError
	if errors.As(err, &vu) {
		return vu.msg
	}
	return ""
}

// groqVisionAsk asks Juka about images. system is the system prompt
// (jukaSystemPrompt plus any context; "" sends none); question is the user's
// words ("" asks what the picture shows). onDelta, when not nil, receives the
// visible answer so far as it streams, on the caller's goroutine. The answer
// comes back without any <think> block. Blocking: call it from a goroutine;
// cancelling ctx stops it.
func groqVisionAsk(ctx context.Context, key, system, question string, images []jukaVisionImage, onDelta func(string)) (string, error) {
	prefix := []map[string]string{{"role": "system", "content": system}}
	return groqVisionChat(ctx, key, prefix, question, images, onDelta)
}

// groqVisionChat is groqVisionAsk after a conversation: prefix is the system
// message and the earlier turns (buildGroqMessagesFrom's messages without the
// question), and the question goes last, with the pictures.
func groqVisionChat(ctx context.Context, key string, prefix []map[string]string, question string, images []jukaVisionImage, onDelta func(string)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(key) == "" {
		return "", visionSay("Add your Groq API key in Settings to ask Juka")
	}
	images = visionUsable(images)
	if len(images) == 0 {
		return "", visionSay("There's no picture for Juka to look at")
	}
	if strings.TrimSpace(question) == "" {
		question = visionDefaultQuestion(len(images))
	}
	models := []string{visionModelQwen, visionModelMuse}
	if len(images) > visionQwenMaxImages {
		models = []string{visionModelMuse, visionModelQwen}
	}
	var lastErr, qwenErr error
	lastModel := ""
	for _, model := range models {
		reply, err := visionAskModel(ctx, key, model, prefix, question, images, onDelta)
		if err == nil {
			return reply, nil
		}
		if model == visionModelQwen {
			qwenErr = err
		}
		lastErr, lastModel = err, model
		if ctx.Err() != nil || !visionNextModel(model, err) {
			break
		}
		logWarn("groq", "vision model %s unavailable: %v", model, err)
	}
	// Muse refusing an account it is not open to says nothing about the key:
	// when Qwen was asked too, its answer is the one to tell.
	if qwenErr != nil && lastModel == visionModelMuse && visionNextModel(visionModelMuse, lastErr) {
		lastErr = qwenErr
	}
	return "", visionFinalError(lastErr)
}

// visionDefaultQuestion is what a picture sent without words asks.
func visionDefaultQuestion(n int) string {
	if n > 1 {
		return "What's in these pictures?"
	}
	return "What's in this picture?"
}

// visionAskModel asks one model, within its picture limit. A refusal of
// reasoning_effort is met with the request without it, and a 413 once with
// one picture (the minute's token budget) or smaller pictures (the bytes).
func visionAskModel(ctx context.Context, key, model string, prefix []map[string]string, question string, images []jukaVisionImage, onDelta func(string)) (string, error) {
	limit := visionQwenMaxImages
	if model == visionModelMuse {
		limit = visionMuseMaxImages
	}
	if len(images) > limit {
		images = images[:limit]
	}
	images = visionFitRequest(images)
	instruct := model == visionModelQwen
	fewer, smaller := false, false
	for {
		reply, err := visionStream(ctx, key, model, prefix, question, images, instruct, onDelta)
		if err == nil {
			return reply, nil
		}
		var apiErr *groqError
		if !errors.As(err, &apiErr) || ctx.Err() != nil {
			return "", err
		}
		lower := strings.ToLower(apiErr.msg)
		tooLarge := apiErr.status == http.StatusRequestEntityTooLarge
		switch {
		case instruct && apiErr.status == http.StatusBadRequest && strings.Contains(lower, "reasoning"):
			instruct = false
		case tooLarge && visionTokenLimit(lower) && !fewer && len(images) > 1:
			fewer = true
			images = images[:1]
		case tooLarge && !visionTokenLimit(lower) && !smaller:
			smaller = true
			images = visionShrinkAll(images)
		default:
			return "", err
		}
		logDebug("groq", "vision request refused by %s (%s); asking again", model, apiErr.msg)
	}
}

// visionNextModel reports whether the other model is worth asking after err:
// a model Groq does not serve, or — Muse being open only to some accounts —
// any refusal of a request to Muse.
func visionNextModel(model string, err error) bool {
	var apiErr *groqError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.model {
		return true
	}
	if model != visionModelMuse {
		return false
	}
	switch apiErr.status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound,
		http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// visionFinalError words a 413 that outlived the retries; any other failure
// goes on as it is (chatFriendlyAIError knows Groq's statuses).
func visionFinalError(err error) error {
	var apiErr *groqError
	if errors.As(err, &apiErr) && apiErr.status == http.StatusRequestEntityTooLarge {
		logWarn("groq", "vision request too large: %s", apiErr.msg)
		if visionTokenLimit(strings.ToLower(apiErr.msg)) {
			return visionSay("That's more than Juka can look at in one go — try one picture")
		}
		return visionSay("The picture is too large for Juka — try a smaller one")
	}
	return err
}

// visionTokenLimit reports a 413 about the account's tokens a minute rather
// than the request's size in bytes.
func visionTokenLimit(lowerMsg string) bool {
	return strings.Contains(lowerMsg, "token") || strings.Contains(lowerMsg, "tpm")
}

// visionContent is one part of the question: its words, or a picture.
type visionContent struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *visionImageURL `json:"image_url,omitempty"`
}

type visionImageURL struct {
	URL string `json:"url"`
}

// visionMessage is a chat message whose content is a string (the system
// prompt, earlier turns) or a list of parts (the question).
type visionMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// visionStream performs one streamed request. instruct asks Qwen for its
// instruct mode (reasoning_effort "none").
func visionStream(ctx context.Context, key, model string, prefix []map[string]string, question string, images []jukaVisionImage, instruct bool, onDelta func(string)) (string, error) {
	messages := make([]visionMessage, 0, len(prefix)+1)
	for _, m := range prefix {
		if strings.TrimSpace(m["content"]) == "" {
			continue
		}
		messages = append(messages, visionMessage{Role: m["role"], Content: m["content"]})
	}
	parts := make([]visionContent, 0, len(images)+1)
	parts = append(parts, visionContent{Type: "text", Text: question})
	for _, im := range images {
		parts = append(parts, visionContent{Type: "image_url", ImageURL: &visionImageURL{URL: visionDataURL(im)}})
	}
	messages = append(messages, visionMessage{Role: "user", Content: parts})
	maxTokens := 1024
	if model == visionModelMuse {
		// A reasoning model thinks inside the same budget: room for both.
		maxTokens = 2048
	}
	payload := map[string]any{
		"model":                 model,
		"messages":              messages,
		"temperature":           0.6,
		"max_completion_tokens": maxTokens,
		"stream":                true,
	}
	if instruct {
		payload["reasoning_effort"] = "none"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatGroqEndpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := visionHTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("Groq request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", groqErrorFromResponse(resp.StatusCode, raw)
	}
	reply, err := chatReadGroqStream(resp.Body, func(partial string) {
		if onDelta == nil {
			return
		}
		if visible := chatStripThink(partial); visible != "" {
			onDelta(visible)
		}
	})
	return chatStripThink(reply), err
}

// visionDataURL is a picture as the data: URL a request carries.
func visionDataURL(im jukaVisionImage) string {
	mime := strings.TrimSpace(im.MIME)
	if mime == "" {
		mime = http.DetectContentType(im.Data)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(im.Data)
}

// visionUsable drops empty pictures and makes a smaller copy of any over
// visionMaxImageBytes (a caller that brought its own bytes); one that cannot
// be read is left out.
func visionUsable(images []jukaVisionImage) []jukaVisionImage {
	out := make([]jukaVisionImage, 0, len(images))
	for _, im := range images {
		if len(im.Data) == 0 {
			continue
		}
		if len(im.Data) > visionMaxImageBytes {
			src, err := visionDecodeBytes(im.Data)
			if err != nil {
				logDebug("groq", "picture left out: %v", err)
				continue
			}
			small, err := jukaVisionFromImage(src)
			if err != nil {
				logDebug("groq", "picture left out: %v", err)
				continue
			}
			im = small
		}
		out = append(out, im)
	}
	return out
}

// visionFitRequest keeps the pictures, in order, while their base64 fits the
// request's budget. The first always goes.
func visionFitRequest(images []jukaVisionImage) []jukaVisionImage {
	total := 0
	for i, im := range images {
		total += base64.StdEncoding.EncodedLen(len(im.Data))
		if i > 0 && total > visionMaxRequestB64 {
			return images[:i]
		}
	}
	return images
}

// visionShrinkAll makes every picture three quarters the size at a lower
// quality, for a request Groq found too large. A picture that does not come
// out smaller stays as it was.
func visionShrinkAll(images []jukaVisionImage) []jukaVisionImage {
	out := make([]jukaVisionImage, len(images))
	for i, im := range images {
		out[i] = im
		src, err := visionDecodeBytes(im.Data)
		if err != nil {
			continue
		}
		pic := visionScale(src, visionMaxSide*3/4)
		if pic == nil {
			continue
		}
		if data, err := visionEncodeJPEG(pic, 60); err == nil && len(data) < len(im.Data) {
			out[i] = jukaVisionImage{Data: data, MIME: "image/jpeg"}
		}
	}
	return out
}

// visionDecodeBytes decodes encoded picture bytes, refusing one whose size
// alone would take a large share of the handheld's memory.
func visionDecodeBytes(data []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > visionDecodeMaxPixels {
		return nil, fmt.Errorf("the picture is %d × %d", cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	return src, err
}

// ── Pictures ─────────────────────────────────────────────────────────────

// jukaVisionFromImage scales a picture down to what the vision model needs
// (visionMaxSide on the long side, never enlarged) and encodes it as JPEG,
// transparency laid on white. Safe from any goroutine: the game assistant
// hands it a frame of the screen.
func jukaVisionFromImage(img image.Image) (jukaVisionImage, error) {
	if img == nil {
		return jukaVisionImage{}, errors.New("no picture")
	}
	pic := visionScale(img, visionMaxSide)
	if pic == nil {
		return jukaVisionImage{}, errors.New("the picture is empty")
	}
	return visionEncodeFit(pic)
}

// visionEncodeFit encodes a scaled picture within visionMaxImageBytes: at the
// usual quality, then a lower one, then at three quarters of the size. Noise
// and fine print are what grow a JPEG; anything past that is not worth
// sending.
func visionEncodeFit(pic *image.RGBA) (jukaVisionImage, error) {
	data, err := visionEncodeJPEG(pic, visionJPEGQuality)
	if err == nil && len(data) > visionMaxImageBytes {
		data, err = visionEncodeJPEG(pic, 60)
	}
	if err == nil && len(data) > visionMaxImageBytes {
		b := pic.Bounds()
		if small := visionScale(pic, max(b.Dx(), b.Dy())*3/4); small != nil {
			data, err = visionEncodeJPEG(small, 60)
		}
	}
	if err != nil {
		return jukaVisionImage{}, err
	}
	if len(data) > visionMaxImageBytes {
		return jukaVisionImage{}, errors.New("the picture is too detailed to send")
	}
	return jukaVisionImage{Data: data, MIME: "image/jpeg"}, nil
}

// visionEncodeJPEG encodes an opaque picture as JPEG. An *image.RGBA takes
// the encoder's fast path.
func visionEncodeJPEG(pic *image.RGBA, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, pic, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// visionScale shrinks src to fit maxSide on its longer side (never enlarging
// it) and lays it on white. Every output pixel is the average of all the
// source pixels under it — an area filter, which keeps text and edges clean
// at any ratio where sampling a few pixels would alias — and the source is
// read a row at a time through a reader made for its type, so a 12-megapixel
// photo is not twelve million calls through image.At. JPEG keeps no
// transparency: a clear PNG's black text would come out black on black, so
// alpha is flattened onto white. nil for an empty picture.
func visionScale(src image.Image, maxSide int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return nil
	}
	w, h := sw, sh
	if maxSide > 0 && max(sw, sh) > maxSide {
		if sw >= sh {
			w = maxSide
			h = max(1, int(math.Round(float64(sh)*float64(maxSide)/float64(sw))))
		} else {
			h = maxSide
			w = max(1, int(math.Round(float64(sw)*float64(maxSide)/float64(sh))))
		}
	}
	read := visionRowReader(src)
	// xs[i]..xs[i+1] are the source columns under output column i; w <= sw,
	// so every span holds at least one.
	xs := make([]int, w+1)
	for i := range xs {
		xs[i] = i * sw / w
	}
	row := make([]uint8, sw*3)
	sums := make([]uint32, w*3)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for oy := 0; oy < h; oy++ {
		y0, y1 := oy*sh/h, (oy+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		clear(sums)
		for sy := y0; sy < y1; sy++ {
			read(b.Min.Y+sy, row)
			for ox := 0; ox < w; ox++ {
				var r, g, bl uint32
				for sx := xs[ox]; sx < xs[ox+1]; sx++ {
					p := sx * 3
					r += uint32(row[p])
					g += uint32(row[p+1])
					bl += uint32(row[p+2])
				}
				o := ox * 3
				sums[o] += r
				sums[o+1] += g
				sums[o+2] += bl
			}
		}
		out := dst.Pix[oy*dst.Stride:]
		for ox := 0; ox < w; ox++ {
			n := uint32((y1 - y0) * (xs[ox+1] - xs[ox]))
			if n == 0 {
				n = 1
			}
			o, p := ox*3, ox*4
			out[p] = uint8((sums[o] + n/2) / n)
			out[p+1] = uint8((sums[o+1] + n/2) / n)
			out[p+2] = uint8((sums[o+2] + n/2) / n)
			out[p+3] = 255
		}
	}
	return dst
}

// visionRowReader returns a function that writes source row y (an absolute
// coordinate) into dst as 8-bit RGB laid on white, three bytes a pixel. The
// common decoder types are read straight from their planes.
func visionRowReader(src image.Image) func(y int, dst []uint8) {
	b := src.Bounds()
	x0, w := b.Min.X, b.Dx()
	switch s := src.(type) {
	case *image.YCbCr:
		return func(y int, dst []uint8) {
			for x := 0; x < w; x++ {
				yi := s.YOffset(x0+x, y)
				ci := s.COffset(x0+x, y)
				r, g, bl := color.YCbCrToRGB(s.Y[yi], s.Cb[ci], s.Cr[ci])
				dst[x*3], dst[x*3+1], dst[x*3+2] = r, g, bl
			}
		}
	case *image.RGBA:
		if visionNoAlpha(s.Pix) {
			return visionOpaqueRows(s.Pix, s.Stride, s.Rect, w)
		}
		// Premultiplied: on white a channel is c + (255 - a).
		return func(y int, dst []uint8) {
			row := s.Pix[s.PixOffset(x0, y):]
			for x := 0; x < w; x++ {
				p := x * 4
				bg := 255 - uint32(row[p+3])
				dst[x*3] = uint8(min(uint32(row[p])+bg, 255))
				dst[x*3+1] = uint8(min(uint32(row[p+1])+bg, 255))
				dst[x*3+2] = uint8(min(uint32(row[p+2])+bg, 255))
			}
		}
	case *image.NRGBA:
		if visionNoAlpha(s.Pix) {
			return visionOpaqueRows(s.Pix, s.Stride, s.Rect, w)
		}
		// Straight alpha: on white a channel is (c·a + 255·(255 - a)) / 255.
		return func(y int, dst []uint8) {
			row := s.Pix[s.PixOffset(x0, y):]
			for x := 0; x < w; x++ {
				p := x * 4
				a := uint32(row[p+3])
				if a == 255 {
					dst[x*3], dst[x*3+1], dst[x*3+2] = row[p], row[p+1], row[p+2]
					continue
				}
				bg := 255 * (255 - a)
				dst[x*3] = uint8((uint32(row[p])*a + bg + 127) / 255)
				dst[x*3+1] = uint8((uint32(row[p+1])*a + bg + 127) / 255)
				dst[x*3+2] = uint8((uint32(row[p+2])*a + bg + 127) / 255)
			}
		}
	case *image.Gray:
		return func(y int, dst []uint8) {
			row := s.Pix[s.PixOffset(x0, y):]
			for x := 0; x < w; x++ {
				v := row[x]
				dst[x*3], dst[x*3+1], dst[x*3+2] = v, v, v
			}
		}
	case *image.Paletted:
		// A GIF: its palette laid on white once, then a lookup a pixel.
		pal := make([][3]uint8, len(s.Palette))
		for i, c := range s.Palette {
			pal[i] = visionOnWhite(c)
		}
		return func(y int, dst []uint8) {
			row := s.Pix[s.PixOffset(x0, y):]
			for x := 0; x < w; x++ {
				c := [3]uint8{255, 255, 255}
				if i := int(row[x]); i < len(pal) {
					c = pal[i]
				}
				dst[x*3], dst[x*3+1], dst[x*3+2] = c[0], c[1], c[2]
			}
		}
	}
	return func(y int, dst []uint8) {
		for x := 0; x < w; x++ {
			c := visionOnWhite(src.At(x0+x, y))
			dst[x*3], dst[x*3+1], dst[x*3+2] = c[0], c[1], c[2]
		}
	}
}

// visionNoAlpha reports four-byte pixels whose alpha is 0 everywhere: a copy
// of an XRGB framebuffer, whose fourth byte means nothing. Laid on white, such
// a picture would come out blank.
func visionNoAlpha(pix []uint8) bool {
	if len(pix) < 4 {
		return false
	}
	for i := 3; i < len(pix); i += 4 {
		if pix[i] != 0 {
			return false
		}
	}
	return true
}

// visionOpaqueRows reads four-byte pixels as they are, ignoring alpha.
func visionOpaqueRows(pix []uint8, stride int, rect image.Rectangle, w int) func(y int, dst []uint8) {
	return func(y int, dst []uint8) {
		row := pix[(y-rect.Min.Y)*stride:]
		for x := 0; x < w; x++ {
			p := x * 4
			dst[x*3], dst[x*3+1], dst[x*3+2] = row[p], row[p+1], row[p+2]
		}
	}
}

// visionOnWhite is one colour laid on white, 8 bits a channel.
func visionOnWhite(c color.Color) [3]uint8 {
	r, g, b, a := c.RGBA() // premultiplied, 16 bits a channel
	bg := 0xffff - a
	return [3]uint8{uint8(min(r+bg, 0xffff) >> 8), uint8(min(g+bg, 0xffff) >> 8), uint8(min(b+bg, 0xffff) >> 8)}
}

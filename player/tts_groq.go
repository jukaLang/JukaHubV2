package main

// tts_groq.go — Groq's voice: one request per chunk, and what its refusals
// mean.
//
// The request is OpenAI's speech call on Groq: POST /openai/v1/audio/speech
// with the Groq key as a bearer token and {"model", "input", "voice",
// "response_format": "wav"}; the answer is a WAV file (tts_wav.go reads it).
// The model is Canopy Labs' Orpheus, which takes at most 200 characters of
// input (tts_text.go cuts the chunks) and speaks in six English voices.
//
// Three refusals are expected and each is remembered (ttsGate, tts_api.go) so
// one is not met again on every answer:
//
//   - 400 "model_terms_required": the Groq organisation has not accepted the
//     model's terms. Its admin does that once, on Groq's playground page for
//     the model; until then every request fails the same way, so it is not
//     asked again until the key changes or Settings → Voice & AI → "Hear
//     Juka" is pressed.
//   - 429: the free tier allows about a hundred speech requests and a few
//     minutes of audio a day, and a few requests a minute. Groq says how long
//     to wait (the retry-after header, or "try again in 4m30s" in the
//     message); a wait of a second or two is waited out, a longer one rests
//     the voice until it is over.
//   - 401/403: the key is wrong. Juka cannot answer at all then, so nothing
//     is said about the voice; it is not asked again with that key.
//
// The request goroutine never reads config: the key and the voice were taken
// on the main thread when the answer was asked to be spoken.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	ttsGroqEndpoint = "https://api.groq.com/openai/v1/audio/speech"
	ttsGroqModel    = "canopylabs/orpheus-v1-english"
	// ttsGroqTermsURL is the page where the organisation's admin accepts the
	// model's terms (the address Groq's own error names).
	ttsGroqTermsURL = "https://console.groq.com/playground?model=canopylabs%2Forpheus-v1-english"
	// ttsGroqTimeout bounds one request; ttsGroqMaxBytes one answer (a
	// 200-character chunk is about 0.7 MB of 24 kHz audio).
	ttsGroqTimeout  = 30 * time.Second
	ttsGroqMaxBytes = 16 << 20
	// ttsGroqShortWait is the longest per-minute wait sat out in the middle of
	// an answer; ttsGroqDayRest is how long a daily limit rests the voice when
	// Groq does not say.
	ttsGroqShortWait = 2500 * time.Millisecond
	ttsGroqDayRest   = 30 * time.Minute
	ttsGroqMinRest   = 20 * time.Second
	ttsGroqMaxRest   = 24 * time.Hour
)

// ttsGroqVoices are Orpheus's English voices, in the order Settings lists them;
// the first is the default.
var ttsGroqVoices = []string{"hannah", "autumn", "diana", "austin", "daniel", "troy"}

// ttsGroqHTTP is the voice's client: its own timeout, the default transport.
var ttsGroqHTTP = &http.Client{Timeout: ttsGroqTimeout}

// ttsFail is why a voice did not speak.
type ttsFail int

const (
	ttsFailNone    ttsFail = iota
	ttsFailNoKey           // no Groq key in Settings
	ttsFailOffline         // no internet
	ttsFailKey             // Groq rejected the key
	ttsFailTerms           // the model's terms are not accepted yet
	ttsFailLimit           // the free allowance (or the per-minute limit) is used up
	ttsFailServer          // Groq failed, timed out or sent something unplayable
	ttsFailQuiet           // Quiet Mode is on (Hear Juka says so)
	ttsFailNoSound         // there is no sound device
)

// ttsError is a failed request: what kind, and for a limit how long Groq asked
// to wait and whether it was the day's allowance.
type ttsError struct {
	fail  ttsFail
	wait  time.Duration
	daily bool
	msg   string
	err   error
}

func (e *ttsError) Error() string {
	if e.err != nil {
		return e.msg + ": " + e.err.Error()
	}
	return e.msg
}

func (e *ttsError) Unwrap() error { return e.err }

// ttsFailOf is the kind of a request's error (ttsFailServer for any other).
func ttsFailOf(err error) (ttsFail, time.Duration, bool) {
	var te *ttsError
	if errors.As(err, &te) {
		return te.fail, te.wait, te.daily
	}
	return ttsFailServer, 0, false
}

// ttsGroqSpeak asks Groq's voice to say text and returns the samples and their
// rate. A per-minute limit of a second or two is waited out once.
func ttsGroqSpeak(ctx context.Context, key, voice, text string) ([]int16, int, error) {
	samples, rate, err := ttsGroqRequest(ctx, key, voice, text)
	if fail, wait, daily := ttsFailOf(err); err != nil && fail == ttsFailLimit && !daily && wait <= ttsGroqShortWait {
		if wait <= 0 {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-time.After(wait):
		}
		samples, rate, err = ttsGroqRequest(ctx, key, voice, text)
	}
	return samples, rate, err
}

// ttsGroqRequest is one request.
func ttsGroqRequest(ctx context.Context, key, voice, text string) ([]int16, int, error) {
	body, err := json.Marshal(map[string]string{
		"model":           ttsGroqModel,
		"input":           text,
		"voice":           voice,
		"response_format": "wav",
	})
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ttsGroqEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/wav")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := ttsGroqHTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		fail := ttsFailServer
		if plainNetworkError(err) == offlinePlainNotice {
			fail = ttsFailOffline
		}
		return nil, 0, &ttsError{fail: fail, msg: "Groq's voice could not be reached", err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, 0, ttsGroqFailure(resp.StatusCode, resp.Header, raw)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, ttsGroqMaxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, &ttsError{fail: ttsFailServer, msg: "Groq's voice stopped mid-answer", err: err}
	}
	if len(raw) > ttsGroqMaxBytes {
		return nil, 0, &ttsError{fail: ttsFailServer, msg: "Groq's voice sent too much audio"}
	}
	samples, rate, err := ttsDecodeWAV(raw, true)
	if err != nil {
		return nil, 0, &ttsError{fail: ttsFailServer, msg: "Groq's voice sent unplayable audio", err: err}
	}
	return samples, rate, nil
}

// ttsGroqFailure reads a refusal: its status, its retry-after header and the
// JSON error body ({"error": {"message", "type", "code"}}). Pure.
func ttsGroqFailure(status int, header http.Header, body []byte) *ttsError {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	msg := strings.TrimSpace(payload.Error.Message)
	lower := strings.ToLower(msg)
	code := strings.ToLower(payload.Error.Code)
	e := &ttsError{fail: ttsFailServer, msg: fmt.Sprintf("Groq's voice answered HTTP %d", status)}
	if msg != "" {
		e.msg += ": " + msg
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.fail = ttsFailKey
	case code == "model_terms_required" ||
		(status == http.StatusBadRequest && strings.Contains(lower, "terms") && strings.Contains(lower, "accept")):
		e.fail = ttsFailTerms
	case status == http.StatusTooManyRequests:
		e.fail = ttsFailLimit
		e.daily = strings.Contains(lower, "per day") || strings.Contains(lower, "(rpd)") ||
			strings.Contains(lower, "(tpd)") || strings.Contains(lower, "(asd)")
		e.wait = ttsRetryWait(header, msg)
	}
	return e
}

// ttsRetryWait is how long Groq asked to wait: the retry-after header in
// seconds, else the "try again in 4m30.24s" its message ends with, else 0.
// Pure.
func ttsRetryWait(header http.Header, msg string) time.Duration {
	if header != nil {
		if v := strings.TrimSpace(header.Get("Retry-After")); v != "" {
			if secs, err := strconv.ParseFloat(v, 64); err == nil && secs >= 0 {
				return ttsClampWait(time.Duration(secs * float64(time.Second)))
			}
		}
	}
	const marker = "try again in "
	lower := strings.ToLower(msg)
	i := strings.Index(lower, marker)
	if i < 0 {
		return 0
	}
	rest := lower[i+len(marker):]
	if j := strings.IndexAny(rest, " ,;"); j >= 0 {
		rest = rest[:j]
	}
	if d, err := time.ParseDuration(strings.TrimRight(rest, ".")); err == nil && d >= 0 {
		return ttsClampWait(d)
	}
	return 0
}

// ttsClampWait keeps a wait Groq named within a day.
func ttsClampWait(d time.Duration) time.Duration {
	if d > ttsGroqMaxRest {
		return ttsGroqMaxRest
	}
	return d
}

// ttsRestFor is how long a refusal rests Groq's voice: what Groq asked for,
// at least ttsGroqMinRest, and ttsGroqDayRest for a daily limit that named no
// time.
func ttsRestFor(wait time.Duration, daily bool) time.Duration {
	if wait <= 0 && daily {
		return ttsGroqDayRest
	}
	if wait < ttsGroqMinRest {
		return ttsGroqMinRest
	}
	return wait
}

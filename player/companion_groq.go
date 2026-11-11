package main

// companion_groq.go — the Groq API key, typed on the phone.
//
// Voice typing, the voice commands and the Juka chat need a Groq API key: a
// 56-character string nobody wants to enter on a handheld's on-screen keyboard.
// The companion page has a card for it instead. The phone sends the key to
// /api/groq-key (behind the pairing PIN like every other call); the handheld
// asks Groq whether the key works, and saves it on the main loop exactly as the
// key field in Keyboard settings does (vkSaveGroqKey). The key is never sent
// back: the page only learns whether one is saved and its last four characters.
//
// Like the rest of the companion this file touches no UI state. The setting is
// written by drainCompanionQueue (companionui.go), and the status the page
// reads is a copy that same loop keeps current (companionRefreshGroqStatus).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// companionEventGroqKey carries a key from the phone to the main loop.
const companionEventGroqKey = "groqkey"

const (
	// companionGroqKeyMin and companionGroqKeyMax bound what can be a key.
	// Groq's are "gsk_" and 52 more characters; the bounds leave room for the
	// format to change without letting a pasted paragraph through.
	companionGroqKeyMin = 20
	companionGroqKeyMax = 200
	// companionGroqCheckWait bounds the call that asks Groq about the key.
	companionGroqCheckWait = 8 * time.Second
	// companionGroqModelsURL lists the models a key may use: a free,
	// read-only call that answers 401 for a key Groq does not know.
	companionGroqModelsURL = "https://api.groq.com/openai/v1/models"
)

// groqKeyVerdict is what Groq said about a key.
type groqKeyVerdict int

const (
	groqKeyUnchecked groqKeyVerdict = iota // no answer: offline, Groq down, rate limited
	groqKeyAccepted
	groqKeyRejected
)

// companionCleanGroqKey trims what a phone pastes (spaces, a line break,
// quotes copied along with the key) and refuses what cannot be a key.
func companionCleanGroqKey(raw string) (string, error) {
	key := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "\"'`"))
	if key == "" {
		return "", errors.New("paste your Groq API key first")
	}
	for _, r := range key {
		if r <= ' ' || r > '~' {
			return "", errors.New("the key has spaces or unusual characters in it - copy it again from console.groq.com")
		}
	}
	if len(key) < companionGroqKeyMin || len(key) > companionGroqKeyMax {
		return "", errors.New("that does not look like a Groq API key - copy the whole key from console.groq.com")
	}
	return key, nil
}

// companionGroqKeyTail is the part of a key the page may show: its last four
// characters, or nothing for a value too short to hide the rest.
func companionGroqKeyTail(key string) string {
	key = strings.TrimSpace(key)
	if len(key) < 12 {
		return ""
	}
	return key[len(key)-4:]
}

// companionCheckGroqKey asks Groq whether key works. Only a clear refusal
// (401) rejects it: a handheld that cannot reach Groq right now still saves
// what the phone sent, and a 403 can mean an account limit rather than a bad
// key. A variable, so tests never touch the network.
var companionCheckGroqKey = func(ctx context.Context, key string) groqKeyVerdict {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, companionGroqModelsURL, nil)
	if err != nil {
		return groqKeyUnchecked
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: companionGroqCheckWait}).Do(req)
	if err != nil {
		return groqKeyUnchecked
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 256<<10))
	switch resp.StatusCode {
	case http.StatusOK:
		return groqKeyAccepted
	case http.StatusUnauthorized:
		return groqKeyRejected
	}
	return groqKeyUnchecked
}

// companionGroqStatusInfo is what the page may know about the saved key.
type companionGroqStatusInfo struct {
	Set  bool
	Tail string
}

// companionGroqStatus is the copy of the saved key's status the HTTP handler
// reads; the main loop writes it (companionRefreshGroqStatus).
var companionGroqStatus atomic.Value // companionGroqStatusInfo

// companionGroqSeen is the stored value the copy was made from, so the main
// loop rebuilds it (and decrypts the key) only when the setting changed. Main
// loop only.
var companionGroqSeen struct {
	raw string
	ok  bool
}

// companionRefreshGroqStatus brings the page's copy of the key status up to
// date. It runs every frame from drainCompanionQueue, on the main loop, the
// only place the settings may be read; an unchanged setting costs two map
// lookups.
func companionRefreshGroqStatus(config *Config) {
	name, raw := "", ""
	if config != nil && config.Variables.Custom != nil {
		// The same two settings, in the same order, as groqAPIKey reads —
		// from the config the main loop holds, which a hot reload replaces.
		for _, n := range []string{"groq_api_key", "GroqApiKey"} {
			if v, _ := config.Variables.Custom[n].(string); v != "" {
				name, raw = n, v
				break
			}
		}
	}
	if companionGroqSeen.ok && raw == companionGroqSeen.raw {
		return
	}
	companionGroqSeen.raw, companionGroqSeen.ok = raw, true
	key := strings.TrimSpace(raw)
	if strings.HasPrefix(key, "ENC:") {
		// A key stored encrypted is decrypted as groqAPIKey does; one that will
		// not decrypt is no usable key.
		dec, err := DecryptAPIToken(key, name)
		if err != nil {
			dec = ""
		}
		key = strings.TrimSpace(dec)
	}
	companionGroqStatus.Store(companionGroqStatusInfo{Set: key != "", Tail: companionGroqKeyTail(key)})
}

// handleGroqKey answers the page's key card: GET tells whether a key is saved
// (never the key), POST checks a new one with Groq and saves it.
func (s *companionServer) handleGroqKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		st, _ := companionGroqStatus.Load().(companionGroqStatusInfo)
		writeCompanionJSON(w, map[string]interface{}{"set": st.Set, "tail": st.Tail})
	case http.MethodPost:
		var body struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		key, err := companionCleanGroqKey(body.Key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), companionGroqCheckWait)
		verdict := companionCheckGroqKey(ctx, key)
		cancel()
		if verdict == groqKeyRejected {
			http.Error(w, "Groq says this key is not valid - copy it again from console.groq.com", http.StatusUnprocessableEntity)
			return
		}
		s.sink(companionEvent{Kind: companionEventGroqKey, Text: key})
		logInfo("companion", "Groq API key received from the phone (checked with Groq: %v)", verdict == groqKeyAccepted)
		msg := "Saved on the handheld. Groq accepted the key."
		if verdict != groqKeyAccepted {
			msg = "Saved on the handheld. JukaHub could not reach Groq to check the key just now; it is used as soon as the handheld is online."
		}
		writeCompanionJSON(w, map[string]interface{}{
			"ok":       true,
			"verified": verdict == groqKeyAccepted,
			"tail":     companionGroqKeyTail(key),
			"message":  msg,
		})
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

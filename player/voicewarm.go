package main

// voicewarm.go — the connection to Groq is opened while the user speaks.
//
// A spoken question makes up to three requests to api.groq.com: the clip to
// Whisper, the question to Juka, Juka's sentences to the voice. They share
// Go's default transport, so after the first they reuse one connection — but
// the first paid DNS, TCP and a TLS handshake (slow on the handheld's A53
// cores) after the user had already stopped talking. voiceWarmGroq starts
// that work the moment the orb opens the microphone: a bodiless HEAD request
// that Groq answers at once (no key is sent, nothing is used up), leaving an
// open connection in the pool by the time the clip is ready.

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// voiceWarmEvery bounds how often the connection is warmed: the transport
	// keeps an idle connection for 90 s, so a warm one is still there.
	voiceWarmEvery   = 30 * time.Second
	voiceWarmTimeout = 8 * time.Second
)

// voiceWarmLast is when the last warm-up started (UnixNano; any goroutine).
var voiceWarmLast atomic.Int64

// voiceWarmGroq opens a connection to Groq in the background when Groq will
// be asked (a key, a network) and none was opened lately. Main thread or any
// goroutine; it never waits.
func voiceWarmGroq() {
	if strings.TrimSpace(groqAPIKey()) == "" || !IsOnline() {
		return
	}
	now := time.Now().UnixNano()
	last := voiceWarmLast.Load()
	if last != 0 && time.Duration(now-last) < voiceWarmEvery {
		return
	}
	if !voiceWarmLast.CompareAndSwap(last, now) {
		return
	}
	url := vkVoiceEndpoint
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), voiceWarmTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
		if err != nil {
			return
		}
		resp, err := vkVoiceHTTP.Do(req)
		if err != nil {
			logDebug("voice", "warming the Groq connection: %v", err)
			return
		}
		// Read to the end so the connection goes back to the pool.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}()
}

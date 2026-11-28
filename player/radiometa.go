package main

// radiometa.go — what a live radio station says it is playing.
//
// The stream itself is decoded by ffmpeg, which reports no titles. ICY metadata
// rides in the same HTTP stream beside the audio: the response header
// icy-metaint says how many audio bytes come before each metadata block, and a
// block is a length byte (in 16-byte units) followed by StreamTitle='…';
// padded with NULs. A second, read-only request asks for the header and reads
// one block, so the decoder's connection is untouched; the player polls it
// while the station plays. The title is shown on both radio screens' Now
// playing row, and the row says "Buffering" until the decoder has queued its
// first sample.
//
// A station that sends no ICY header, or a title that is empty, simply has no
// title: the row falls back to the station's name. The poll has its own
// timeout and never touches the player's audio path.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// radioICYAudioMax bounds the audio bytes between two metadata blocks.
	radioICYAudioMax = 16 << 20
	// radioICYMetaMax bounds one metadata block (255 × 16 bytes by protocol).
	radioICYMetaMax = 255 * 16
	// radioICYFirst waits for the stream to settle before the first ask, and
	// radioICYEvery is how often the title is refreshed afterwards.
	radioICYFirst = 2 * time.Second
	radioICYEvery = 20 * time.Second
)

var (
	errRadioICYNoHeader = fmt.Errorf("the station sends no ICY metadata")
	errRadioICYNoTitle  = fmt.Errorf("the station has not named a title")
)

// radioICYHTTP is the metadata client: its own short timeout, never the audio
// path's. A variable so a test can serve a station locally.
var radioICYHTTP = &http.Client{Timeout: 15 * time.Second}

// radioICYWatcher is one station's watcher; the pointer is the identity the
// goroutine checks before removing itself, so a quick stop-and-replay cannot
// delete the new watcher's entry.
type radioICYWatcher struct {
	cancel context.CancelFunc
}

// radioICYState is the title and the watcher per station URL.
var radioICYState struct {
	mu      sync.Mutex
	titles  map[string]string
	cancels map[string]*radioICYWatcher
}

// radioICYTitleFor is the title the station last reported, "" when none.
func radioICYTitleFor(raw string) string {
	radioICYState.mu.Lock()
	defer radioICYState.mu.Unlock()
	return radioICYState.titles[raw]
}

// radioICYWatch starts one watcher for a live station. A station already being
// watched is left alone, so tuning in again does not double the requests.
func radioICYWatch(raw string) {
	if !isHTTPMediaURL(raw) {
		return
	}
	radioICYState.mu.Lock()
	if radioICYState.cancels == nil {
		radioICYState.titles = map[string]string{}
		radioICYState.cancels = map[string]*radioICYWatcher{}
	}
	if _, ok := radioICYState.cancels[raw]; ok {
		radioICYState.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	self := &radioICYWatcher{cancel: cancel}
	radioICYState.cancels[raw] = self
	radioICYState.mu.Unlock()

	go func() {
		defer func() {
			radioICYState.mu.Lock()
			if radioICYState.cancels[raw] == self {
				delete(radioICYState.cancels, raw)
			}
			radioICYState.mu.Unlock()
		}()
		timer := time.NewTimer(radioICYFirst)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			if title, err := radioICYPoll(ctx, radioICYHTTP, raw); err == nil && title != "" {
				radioICYState.mu.Lock()
				radioICYState.titles[raw] = title
				radioICYState.mu.Unlock()
			}
			timer.Reset(radioICYEvery)
		}
	}()
}

// radioICYStop ends a station's watcher; its last title stays for the row.
func radioICYStop(raw string) {
	radioICYState.mu.Lock()
	w := radioICYState.cancels[raw]
	delete(radioICYState.cancels, raw)
	radioICYState.mu.Unlock()
	if w != nil {
		w.cancel()
	}
}

// radioICYPoll asks a station for its current title. It reads the icy-metaint
// header, skips that many audio bytes, and reads up to four metadata blocks
// looking for a non-empty StreamTitle.
func radioICYPoll(ctx context.Context, client *http.Client, raw string) (string, error) {
	if client == nil {
		client = radioICYHTTP
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Icy-MetaData", "1")
	req.Header.Set("User-Agent", radioDirUserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d from the station", resp.StatusCode)
	}
	metaint, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("icy-metaint")))
	if err != nil || metaint <= 0 {
		return "", errRadioICYNoHeader
	}
	if metaint > radioICYAudioMax {
		return "", errRadioICYNoHeader
	}
	if _, err := io.CopyN(io.Discard, resp.Body, int64(metaint)); err != nil {
		return "", err
	}
	for i := 0; i < 4; i++ {
		var n [1]byte
		if _, err := io.ReadFull(resp.Body, n[:]); err != nil {
			return "", err
		}
		size := int(n[0]) * 16
		if size > radioICYMetaMax {
			return "", errRadioICYNoTitle
		}
		if size > 0 {
			block := make([]byte, size)
			if _, err := io.ReadFull(resp.Body, block); err != nil {
				return "", err
			}
			if title := radioICYStreamTitle(string(block)); title != "" {
				return title, nil
			}
		}
		if i < 3 {
			if _, err := io.CopyN(io.Discard, resp.Body, int64(metaint)); err != nil {
				return "", err
			}
		}
	}
	return "", errRadioICYNoTitle
}

// radioICYStreamTitle reads StreamTitle='…' out of one metadata block and cuts
// it to one short line. Pure.
func radioICYStreamTitle(block string) string {
	const key = "StreamTitle='"
	i := strings.Index(block, key)
	if i < 0 {
		return ""
	}
	rest := block[i+len(key):]
	if j := strings.Index(rest, "';"); j >= 0 {
		rest = rest[:j]
	} else {
		rest = strings.TrimRight(rest, "\x00")
	}
	return radioDirOneLine(rest, 120)
}

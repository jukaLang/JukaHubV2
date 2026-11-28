//go:build windows

package main

// narrate_windows.go — narration's pieces on a PC: one Windows PowerShell
// kept running with the speech synthesiser loaded.
//
// Juka's lines start PowerShell for each answer (tts_local_windows.go); its
// cold start is a second or two, which is fine for an answer and far too slow
// to follow the D-pad. Narration starts one PowerShell that reads a piece per
// line on its standard input (base64 of the UTF-8 text, so no console code
// page touches it), writes it to its own numbered WAV in a private folder and
// answers "OK n". It goes after narrSAPIIdle without a piece, and is started
// again with the next.

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// narrSAPIIdle is how long the kept PowerShell waits for another piece.
const narrSAPIIdle = 2 * time.Minute

// narrSAPIScript reads pieces until its input closes (JukaHub gone).
const narrSAPIScript = `$ErrorActionPreference = 'Stop'; ` +
	`Add-Type -AssemblyName System.Speech; ` +
	`$s = New-Object System.Speech.Synthesis.SpeechSynthesizer; ` +
	`if ($env:JUKA_TTS_GENDER) { try { $s.SelectVoiceByHints([System.Speech.Synthesis.VoiceGender]$env:JUKA_TTS_GENDER) } catch {} }; ` +
	`$d = $env:JUKA_NARR_DIR; $n = 0; ` +
	`while ($true) { $l = [Console]::In.ReadLine(); if ($null -eq $l) { break }; $n++; ` +
	`$f = Join-Path $d ('n' + $n + '.wav'); ` +
	`try { $t = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($l)); ` +
	`$s.SetOutputToWaveFile($f); $s.Speak($t); $s.SetOutputToNull(); [Console]::Out.WriteLine('OK ' + $n) } ` +
	`catch { [Console]::Out.WriteLine('ERR ' + $n) }; [Console]::Out.Flush() }; ` +
	`$s.Dispose()`

// narrSAPI is the kept PowerShell. Any goroutine, under mu.
var narrSAPI struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	lines    chan string
	dir      string
	gender   string
	n        int
	last     time.Time
	watching bool
}

// narrSynthPlatform speaks text with the kept PowerShell. handled is false
// when it cannot start (Juka's one-PowerShell-per-line way is tried then).
func narrSynthPlatform(ctx context.Context, v ttsLocalVoice, text, gender string) ([]int16, int, bool, error) {
	if v.kind != "sapi" || v.path == "" {
		return nil, 0, false, nil
	}
	narrSAPI.mu.Lock()
	defer narrSAPI.mu.Unlock()
	if narrSAPI.cmd != nil && narrSAPI.gender != gender {
		narrSAPIStopLocked()
	}
	if narrSAPI.cmd == nil {
		if err := narrSAPIStartLocked(v, gender); err != nil {
			logWarn("narrate", "Windows speech for narration did not start: %v", err)
			return nil, 0, false, nil
		}
	}
	narrSAPI.n++
	want := narrSAPI.n
	if _, err := io.WriteString(narrSAPI.stdin, base64.StdEncoding.EncodeToString([]byte(text))+"\n"); err != nil {
		narrSAPIStopLocked()
		return nil, 0, false, nil
	}
	wav := filepath.Join(narrSAPI.dir, "n"+strconv.Itoa(want)+".wav")
	defer os.Remove(wav)
	timer := time.NewTimer(ttsSAPITimeout)
	defer timer.Stop()
	lines := narrSAPI.lines
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				narrSAPIStopLocked()
				return nil, 0, true, errors.New("windows speech stopped")
			}
			f := strings.Fields(line)
			if len(f) != 2 {
				continue
			}
			n, err := strconv.Atoi(f[1])
			if err != nil || n < want {
				continue
			}
			narrSAPI.last = time.Now()
			if f[0] != "OK" {
				return nil, 0, true, errors.New("windows speech could not say it")
			}
			data, err := os.ReadFile(wav)
			if err != nil {
				return nil, 0, true, err
			}
			samples, rate, err := ttsDecodeWAV(data, false)
			return samples, rate, true, err
		case <-timer.C:
			narrSAPIStopLocked()
			return nil, 0, true, errors.New("windows speech timed out")
		case <-ctx.Done():
			narrSAPIStopLocked()
			return nil, 0, true, ctx.Err()
		}
	}
}

// narrSAPIStartLocked starts the kept PowerShell. narrSAPI.mu is held.
func narrSAPIStartLocked(v ttsLocalVoice, gender string) error {
	dir, err := os.MkdirTemp("", "jukahub-narrate-")
	if err != nil {
		return err
	}
	cmd := hiddenCommand(v.path, "-NoProfile", "-NonInteractive", "-Command", narrSAPIScript)
	g := ""
	switch gender {
	case "female":
		g = "Female"
	case "male":
		g = "Male"
	}
	cmd.Env = append(os.Environ(), "JUKA_NARR_DIR="+dir, "JUKA_TTS_GENDER="+g)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			select {
			case lines <- strings.TrimSpace(sc.Text()):
			default: // an answer nobody waits for
			}
		}
		close(lines)
		_ = cmd.Wait()
		_ = os.RemoveAll(dir)
	}()
	narrSAPI.cmd, narrSAPI.stdin, narrSAPI.lines = cmd, stdin, lines
	narrSAPI.dir, narrSAPI.gender, narrSAPI.n, narrSAPI.last = dir, gender, 0, time.Now()
	if !narrSAPI.watching {
		narrSAPI.watching = true
		go narrSAPIWatch()
	}
	return nil
}

// narrSAPIStopLocked ends the kept PowerShell. narrSAPI.mu is held.
func narrSAPIStopLocked() {
	if narrSAPI.cmd == nil {
		return
	}
	_ = narrSAPI.stdin.Close()
	if narrSAPI.cmd.Process != nil {
		_ = narrSAPI.cmd.Process.Kill()
	}
	narrSAPI.cmd, narrSAPI.stdin, narrSAPI.lines = nil, nil, nil
}

// narrSAPIWatch ends an idle PowerShell. One goroutine while one runs.
func narrSAPIWatch() {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for range tick.C {
		narrSAPI.mu.Lock()
		if narrSAPI.cmd == nil {
			narrSAPI.watching = false
			narrSAPI.mu.Unlock()
			return
		}
		if time.Since(narrSAPI.last) > narrSAPIIdle {
			narrSAPIStopLocked()
		}
		narrSAPI.mu.Unlock()
	}
}

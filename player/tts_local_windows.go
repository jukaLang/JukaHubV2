//go:build windows

package main

// tts_local_windows.go — the PC's own voice: Windows' speech synthesiser.
//
// System.Speech ships with every Windows 10 and 11 (it is part of the .NET
// Framework that Windows PowerShell 5.1 runs on), works offline and costs
// nothing, so it speaks whenever Groq's voice cannot, or always when Settings
// → Voice & AI → Voice is "This PC". Windows PowerShell — never a "pwsh" on
// PATH, which has no System.Speech — renders the text into a temporary WAV
// with no console window, and the file is read and deleted.
//
// Nothing the model wrote reaches the script: the text, the file name and the
// wanted voice gender travel in the child's environment, which Windows hands
// over as UTF-16, so accents survive where a pipe would go through the console
// code page.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ttsSAPITimeout bounds one synthesis: PowerShell's cold start is the slow
// part (a second or two on an older PC); 600 characters render in under one.
const ttsSAPITimeout = 25 * time.Second

// ttsSAPIScript speaks $env:JUKA_TTS_TEXT into the WAV at $env:JUKA_TTS_OUT, in
// a voice of $env:JUKA_TTS_GENDER when one is installed.
const ttsSAPIScript = `$ErrorActionPreference = 'Stop'; ` +
	`Add-Type -AssemblyName System.Speech; ` +
	`$s = New-Object System.Speech.Synthesis.SpeechSynthesizer; ` +
	`try { ` +
	`if ($env:JUKA_TTS_GENDER) { try { $s.SelectVoiceByHints([System.Speech.Synthesis.VoiceGender]$env:JUKA_TTS_GENDER) } catch {} }; ` +
	`$s.SetOutputToWaveFile($env:JUKA_TTS_OUT); ` +
	`$s.Speak($env:JUKA_TTS_TEXT) ` +
	`} finally { $s.Dispose() }`

// ttsLocalFind is Windows' synthesiser, through Windows PowerShell.
func ttsLocalFind() (ttsLocalVoice, bool) {
	path := brightnessPowerShellPath()
	if path == "powershell" {
		found, err := exec.LookPath("powershell")
		if err != nil {
			return ttsLocalVoice{}, false
		}
		path = found
	}
	return ttsLocalVoice{name: "Windows voice", path: path, kind: "sapi"}, true
}

// ttsLocalSynth renders text with Windows' synthesiser. gender ("female",
// "male" or "") picks a matching installed voice when there is one.
func ttsLocalSynth(ctx context.Context, v ttsLocalVoice, text, gender string) ([]int16, int, error) {
	if v.kind != "sapi" || v.path == "" {
		return nil, 0, errors.New("no Windows voice")
	}
	text = strings.ReplaceAll(text, "\x00", "")
	if strings.TrimSpace(text) == "" {
		return nil, 0, errors.New("nothing to say")
	}
	out, err := os.CreateTemp("", "jukahub-voice-*.wav")
	if err != nil {
		return nil, 0, err
	}
	wav := out.Name()
	_ = out.Close()
	defer os.Remove(wav)

	ctx, cancel := context.WithTimeout(ctx, ttsSAPITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, v.path, "-NoProfile", "-NonInteractive", "-Command", ttsSAPIScript)
	auditConfigureHidden(cmd)
	switch gender {
	case "female":
		gender = "Female"
	case "male":
		gender = "Male"
	default:
		gender = ""
	}
	cmd.Env = append(os.Environ(), "JUKA_TTS_TEXT="+text, "JUKA_TTS_OUT="+wav, "JUKA_TTS_GENDER="+gender)
	if msg, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, fmt.Errorf("windows speech: %w (%s)", err, firstLine(strings.TrimSpace(string(msg))))
	}
	data, err := os.ReadFile(wav)
	if err != nil {
		return nil, 0, err
	}
	return ttsDecodeWAV(data, false)
}

// ttsPiperStream is the handheld's chunk-by-chunk Piper; Windows' voice
// renders a line whole (ttsLocalSpeakChunks falls back to ttsLocalSynth).
func ttsPiperStream(context.Context, ttsLocalVoice, []string, func([]int16, int) bool) (int, error) {
	return 0, errors.New("streaming is not used on Windows")
}

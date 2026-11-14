package main

import (
	"context"
	"strings"
	"testing"
)

func TestVoiceCommandOutcomesSpeak(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  voiceOutcome
		want bool
	}{
		{"question answer", voiceOutcome{Done: true, Say: "It is 4:30"}, true},
		{"completed command", voiceOutcome{Done: true, Say: "Volume 50%"}, true},
		{"warning", voiceOutcome{Say: "Nothing called that here", Tone: tbxToneWarn}, true},
		{"choice prompt", voiceOutcome{Say: "Which one did you mean?", Choices: []voiceMatch{{}}}, true},
		{"pending action", voiceOutcome{Say: "Opening Music", Act: func(*Config) {}}, false},
		{"cancelled", voiceOutcome{Say: "Cancelled", Cancel: true}, false},
		{"empty", voiceOutcome{Done: true, Say: "  "}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ttsOutcomeSpoken(tc.out); got != tc.want {
				t.Fatalf("ttsOutcomeSpoken() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFirstSpeechChunkStartsWithFirstSentence(t *testing.T) {
	chunks := ttsPrepare(ttsTestLine)
	if len(chunks) < 2 || chunks[0] != "Hi, I'm Juka." {
		t.Fatalf("first chunk = %q; want the short first sentence", chunks)
	}
}

func TestLongFirstSentenceStartsSpeakingInSmallPiece(t *testing.T) {
	line := "Collect the red gems near the bridge, then use the north path to reach the hidden door before the timer runs out."
	check := func(chunks []string) {
		t.Helper()
		if len(chunks) < 2 || len(chunks[0]) > ttsFirstChunkMax || strings.Join(chunks, " ") != line {
			t.Fatalf("slow or changed first speech chunk: %#v", chunks)
		}
	}
	check(ttsChunks([]string{line}, ttsChunkMax))
	src := ttsOpenSource()
	src.push([]string{line}, true)
	var live []string
	for i := 0; ; i++ {
		chunk, ok := src.at(context.Background(), i)
		if !ok {
			break
		}
		live = append(live, chunk)
	}
	check(live)
}

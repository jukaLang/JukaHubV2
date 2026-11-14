package main

import "testing"

func TestHJMatchResultUsesWordTimes(t *testing.T) {
	words := []sttWord{
		{Word: "hey", Conf: 0.9, End: 0.2},
		{Word: "juka", Conf: 0.9, End: 0.6},
	}
	end, ok := hjMatchResult(words, "hey juka")
	if !ok || end != 0.6 {
		t.Fatalf("matched=%t end=%v; want true 0.6", ok, end)
	}
}

func TestHJMatchResultFallsBackToText(t *testing.T) {
	// Grammar results from some Vosk builds carry text without usable word
	// confidences; the transcript alone must still wake the orb.
	end, ok := hjMatchResult(nil, "hey juka")
	if !ok || end != 0 {
		t.Fatalf("matched=%t end=%v; want true 0", ok, end)
	}
}

func TestHJMatchResultUsesTimesWithoutConfidences(t *testing.T) {
	words := []sttWord{{Word: "hey", End: 0.2}, {Word: "juka", End: 0.6}}
	end, ok := hjMatchResult(words, "hey juka")
	if !ok || end != 0.6 {
		t.Fatalf("matched=%t end=%v; want true 0.6", ok, end)
	}
}

func TestHJMatchResultRejectsOtherSpeech(t *testing.T) {
	if _, ok := hjMatchResult(nil, "open youtube"); ok {
		t.Fatal("a phrase without the wake word matched")
	}
}

func TestHJMatchHandoffCutsTextOnlyWakeAudio(t *testing.T) {
	// One second of wake audio followed by the user's command. Vosk returned
	// text but no word metadata, so the interim audio boundary must be used.
	end, ok := hjMatchHandoff(nil, "hey juka", micRate*2)
	if !ok || end != 1 {
		t.Fatalf("matched=%t end=%v; want true 1", ok, end)
	}
	words := []sttWord{{Word: "hey", Conf: 0.9, End: 0.2}, {Word: "juka", Conf: 0.9, End: 0.6}}
	end, ok = hjMatchHandoff(words, "hey juka", micRate*2)
	if !ok || end != 0.6 {
		t.Fatalf("timed words should take priority: matched=%t end=%v", ok, end)
	}
}

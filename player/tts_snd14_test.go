package main

import (
	"net/http"
	"testing"
	"time"
)

func TestTTSVoiceVolume(t *testing.T) {
	c := &Config{}
	if got := ttsVolumeOf(c); got != 100 || ttsVolumeGain(got) != 1 {
		t.Fatalf("unset voice volume = %d%% (gain %v), want 100%%/1", got, ttsVolumeGain(got))
	}
	for pct, want := range map[int]float64{0: 0, 25: 0.25, 100: 1, -5: 0, 180: 1} {
		if got := ttsVolumeGain(pct); got != want {
			t.Errorf("gain(%d%%) = %v, want %v", pct, got, want)
		}
	}
	c.Variables.Custom = map[string]interface{}{ttsVolumeKey: "40"}
	if got := ttsVolumeOf(c); got != 40 || ttsVolumeGain(got) != 0.4 {
		t.Fatalf("stored voice volume = %d%% (gain %v), want 40%%/0.4", got, ttsVolumeGain(got))
	}
	c.Variables.Custom[ttsVolumeKey] = "nonsense"
	if got := ttsVolumeOf(c); got != 100 {
		t.Fatalf("bad stored value became %d%%", got)
	}
	if len(ttsVolumeOptions) != 11 {
		t.Fatalf("%d volume stops, want 11", len(ttsVolumeOptions))
	}
}

func TestTTSModelRefusal(t *testing.T) {
	for _, tc := range []struct {
		status    int
		code, msg string
		want      bool
	}{
		{http.StatusNotFound, "", "", true},
		{http.StatusBadRequest, "model_not_found", "The model does not exist", true},
		{http.StatusBadRequest, "", "Invalid model: canopylabs/orpheus-v1-english", true},
		{http.StatusBadRequest, "", "Model is deprecated", true},
		{http.StatusBadRequest, "", "model terms must be accepted", false},
		{http.StatusUnauthorized, "model_not_found", "", false},
		{http.StatusTooManyRequests, "", "model rate limit", false},
		{http.StatusInternalServerError, "", "", false},
	} {
		if got := ttsModelRefusal(tc.status, tc.code, tc.msg); got != tc.want {
			t.Errorf("status %d %q/%q => %v, want %v", tc.status, tc.code, tc.msg, got, tc.want)
		}
	}
}

func TestTTSGroqFailureClassifiesMissingModel(t *testing.T) {
	e := ttsGroqFailure(http.StatusNotFound, http.Header{}, []byte(`{"error":{"message":"model not found","code":"model_not_found"}}`))
	if e.fail != ttsFailModel || e.wait != 0 || e.daily {
		t.Fatalf("404 was not remembered as a missing model: %+v", e)
	}
	e = ttsGroqFailure(http.StatusTooManyRequests, http.Header{}, []byte(`{"error":{"message":"rate limit reached"}}`))
	if e.fail != ttsFailLimit {
		t.Fatalf("429 was not a rate limit: %+v", e)
	}
}

// A missing speech model must stand for the rest of the run, and "Hear Juka"
// must not clear it: nothing a retry can do will make that model appear.
func TestTTSGateRemembersModelRefusal(t *testing.T) {
	ttsGateClear()
	defer ttsGateClear()
	id := ttsKeyID("tts_snd14_test")

	ttsGateNote(id, ttsFailModel, 0, false)
	if got := ttsGateCheck(id); got != ttsFailModel {
		t.Fatalf("gate = %v, want ttsFailModel", got)
	}
	if !ttsGateTellOnce(id, ttsFailModel) || ttsGateTellOnce(id, ttsFailModel) {
		t.Fatal("the model notice was not shown exactly once")
	}
	// Another refusal for the same key does not reset the model decision.
	ttsGateNote(id, ttsFailLimit, time.Minute, false)
	if got := ttsGateCheck(id); got != ttsFailModel {
		t.Fatalf("a later rate limit replaced the model refusal: %v", got)
	}
	// A different key starts fresh.
	if got := ttsGateCheck(id + 1); got != ttsFailNone {
		t.Fatalf("another key inherited the refusal: %v", got)
	}
	// "Hear Juka" clears everything, including the model refusal.
	ttsGateClear()
	if got := ttsGateCheck(id); got != ttsFailNone {
		t.Fatalf("clear left the model refusal: %v", got)
	}
}

package main

import (
	"context"
	"errors"
	"testing"
)

func TestSTTTranscribeChoice(t *testing.T) {
	cloudFailed := errors.New("Groq unavailable")
	tests := []struct {
		name          string
		mode          string
		key           string
		online, local bool
		cloudErr      error
		want          string
		wantErr       error
		cloudCalls    int
		localCalls    int
	}{
		{"cloud first", sttModeAuto, "key", true, true, nil, "cloud", nil, 1, 0},
		{"cloud failure falls back", sttModeAuto, "key", true, true, cloudFailed, "local", nil, 1, 1},
		{"offline goes local", sttModeAuto, "key", false, true, nil, "local", nil, 0, 1},
		{"no key goes local", sttModeAuto, "", true, true, nil, "local", nil, 0, 1},
		{"cloud remains usable without local", sttModeAuto, "key", true, false, nil, "cloud", nil, 1, 0},
		{"cloud error without local", sttModeAuto, "key", true, false, cloudFailed, "", cloudFailed, 1, 0},
		{"neither available", sttModeAuto, "", false, false, nil, "", errSTTUnavailable, 0, 0},
		{"offline selected with cloud ready", sttModeOffline, "key", true, true, nil, "local", nil, 0, 1},
		{"offline selected but missing", sttModeOffline, "key", true, false, nil, "", errSTTUnavailable, 0, 0},
		{"Groq selected with local ready", sttModeGroq, "key", true, true, nil, "cloud", nil, 1, 0},
		{"Groq selected and fails", sttModeGroq, "key", true, true, cloudFailed, "", cloudFailed, 1, 0},
		{"Groq selected but no key", sttModeGroq, "", true, true, nil, "", errSTTGroqKey, 0, 0},
		{"Groq selected but offline", sttModeGroq, "key", false, true, nil, "", errSTTGroqOffline, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloudCalls, localCalls := 0, 0
			got, err := sttTranscribeChoice(context.Background(), tt.mode, tt.key, []byte("wav"), tt.online, tt.local,
				func(context.Context, string, []byte) (string, error) {
					cloudCalls++
					if tt.cloudErr != nil {
						return "", tt.cloudErr
					}
					return "cloud", tt.cloudErr
				},
				func(context.Context, []byte) (string, error) {
					localCalls++
					return "local", nil
				})
			if got != tt.want || !errors.Is(err, tt.wantErr) || cloudCalls != tt.cloudCalls || localCalls != tt.localCalls {
				t.Fatalf("got %q, %v, calls %d/%d; want %q, %v, calls %d/%d", got, err, cloudCalls, localCalls,
					tt.want, tt.wantErr, tt.cloudCalls, tt.localCalls)
			}
		})
	}
}

func TestSTTCancellationDoesNotStartLocal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	localCalled := false
	_, err := sttTranscribeChoice(ctx, sttModeAuto, "key", nil, true, true,
		func(context.Context, string, []byte) (string, error) {
			cancel()
			return "", context.Canceled
		},
		func(context.Context, []byte) (string, error) {
			localCalled = true
			return "", nil
		})
	if !errors.Is(err, context.Canceled) || localCalled {
		t.Fatalf("canceled request started local recognition: %v, %t", err, localCalled)
	}
}

func TestSTTPreviewChoiceKeepsOfflineAudioLocal(t *testing.T) {
	cloud, localFirst, fallback := sttPreviewChoice(sttModeOffline, "key", true, true)
	if cloud || !localFirst || fallback {
		t.Fatalf("offline preview chose cloud=%t local=%t fallback=%t", cloud, localFirst, fallback)
	}
	cloud, localFirst, fallback = sttPreviewChoice(sttModeGroq, "key", true, true)
	if !cloud || localFirst || fallback {
		t.Fatalf("Groq preview chose cloud=%t local=%t fallback=%t", cloud, localFirst, fallback)
	}
	cloud, localFirst, fallback = sttPreviewChoice(sttModeAuto, "key", true, true)
	if !cloud || localFirst || !fallback {
		t.Fatalf("automatic preview chose cloud=%t local=%t fallback=%t", cloud, localFirst, fallback)
	}
}

func TestSTTMenuWorksWithoutGroq(t *testing.T) {
	for _, mode := range []string{sttModeAuto, sttModeOffline} {
		if !sttListenAvailable(mode, true, "", false) {
			t.Errorf("%s should accept a ready offline recognizer without a key or Wi-Fi", mode)
		}
		if fix := voiceSTTSetupFix(mode, true, true, ""); fix != voiceFixNone {
			t.Errorf("%s asked for setup with local speech ready: %v", mode, fix)
		}
		if fix := voiceSTTSetupFix(mode, true, false, ""); fix != voiceFixOfflineSpeech {
			t.Errorf("%s offered %v instead of offline installation", mode, fix)
		}
	}
	if fix := voiceSTTSetupFix(sttModeGroq, true, false, ""); fix != voiceFixGroqKey {
		t.Errorf("Groq-only mode should ask for its key: %v", fix)
	}
}

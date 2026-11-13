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
		key           string
		online, local bool
		cloudErr      error
		want          string
		wantErr       error
		cloudCalls    int
		localCalls    int
	}{
		{"cloud first", "key", true, true, nil, "cloud", nil, 1, 0},
		{"cloud failure falls back", "key", true, true, cloudFailed, "local", nil, 1, 1},
		{"offline goes local", "key", false, true, nil, "local", nil, 0, 1},
		{"no key goes local", "", true, true, nil, "local", nil, 0, 1},
		{"cloud remains usable without local", "key", true, false, nil, "cloud", nil, 1, 0},
		{"cloud error without local", "key", true, false, cloudFailed, "", cloudFailed, 1, 0},
		{"neither available", "", false, false, nil, "", errSTTUnavailable, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloudCalls, localCalls := 0, 0
			got, err := sttTranscribeChoice(context.Background(), tt.key, []byte("wav"), tt.online, tt.local,
				func(context.Context, string, []byte) (string, error) {
					cloudCalls++
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
	_, err := sttTranscribeChoice(ctx, "key", nil, true, true,
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

package main

import (
	"strings"
	"testing"
)

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestQueueDownloadArgsProtectOtherSites(t *testing.T) {
	job := downloadJob{URL: "https://vimeo.com/12345", Kind: downloadKindVideo}
	c := &Config{}
	args := videoDownloadArgs("/tmp/part", job, c)
	for _, want := range []string{"--continue", "--no-simulate", "--progress", "--newline", "!is_live"} {
		if !hasArg(args, want) {
			t.Fatalf("video args have no %q: %v", want, args)
		}
	}
	if !hasArg(args, "--user-agent") {
		t.Fatal("another site did not get a browser user agent")
	}
	if args[len(args)-1] != job.URL {
		t.Fatalf("URL is not the last operand: %v", args)
	}
	format := videoDownloadFormat(720, true)
	if !strings.HasSuffix(format, "/b") {
		t.Fatalf("handheld format has no last-resort progressive file: %s", format)
	}

	audioArgs := downloadArgs("/tmp/part", job, c)
	for _, want := range []string{"--continue", "--no-simulate", "!is_live"} {
		if !hasArg(audioArgs, want) {
			t.Fatalf("audio args have no %q: %v", want, audioArgs)
		}
	}
}

func TestOtherSiteDownloadFailuresAreExplained(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"ERROR: Sign in to confirm your age", "needs a login or cookies"},
		{"ERROR: Video is unavailable", "private, unavailable"},
		{"ERROR: unable to download format", "No playable format"},
		{"ERROR: HTTP Error 403: Forbidden", "refused the request"},
	} {
		got := classifyOtherDownloadFailure(tc.text)
		if !strings.Contains(got, tc.want) {
			t.Fatalf("%q => %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestQueueDownloadArgsLeaveYouTubeClientIdentityAlone(t *testing.T) {
	job := downloadJob{URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", Kind: downloadKindVideo}
	args := videoDownloadArgs("/tmp/part", job, &Config{})
	if hasArg(args, "--user-agent") {
		t.Fatal("YouTube was given a browser user agent")
	}
}

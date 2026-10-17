package main

import "testing"

func TestYtDlpAssetName(t *testing.T) {
	cases := []struct {
		goos, goarch, want string
	}{
		{"windows", "amd64", "yt-dlp.exe"},
		{"windows", "arm64", "yt-dlp.exe"},
		{"windows", "386", "yt-dlp_x86.exe"},
		// TrimUI Smart Pro ships no Python; it needs the standalone aarch64 build.
		{"linux", "arm64", "yt-dlp_linux_aarch64"},
		{"linux", "arm", "yt-dlp_linux_armv7l"},
		{"linux", "amd64", "yt-dlp_linux"},
		{"darwin", "arm64", "yt-dlp_macos"},
	}
	for _, tc := range cases {
		got, err := ytDlpAssetName(tc.goos, tc.goarch)
		if err != nil || got != tc.want {
			t.Errorf("ytDlpAssetName(%s/%s) = %q, %v; want %q", tc.goos, tc.goarch, got, err, tc.want)
		}
	}
	if _, err := ytDlpAssetName("plan9", "mips"); err == nil {
		t.Error("unsupported platform must return an error")
	}
}

// A search command is config-supplied, so it must not be able to hand yt-dlp
// an option that runs another program.
func TestRejectYtDlpExecFlags(t *testing.T) {
	allowed := []string{
		"--flat-playlist",
		"--dump-single-json",
		"--default-search", "ytsearch",
		"--playlist-end", "20",
		"ytsearch20:funny cats",
		"--cookies", "/tmp/cookies.txt",
		"--extractor-args", "youtube:api_key=abc=",
	}
	if err := rejectYtDlpExecFlags(allowed); err != nil {
		t.Fatalf("benign search command rejected: %v", err)
	}

	for _, args := range [][]string{
		{"--exec", "rm -rf /"},
		{"--exec-before-download=sh -c id"},
		{"--downloader", "curl"},
		{"--external-downloader=wget"},
		{"--postprocessor-args", "-i"},
		{"--ppa=ffmpeg:-i"},
		{"--update"},
	} {
		if err := rejectYtDlpExecFlags(args); err == nil {
			t.Errorf("execution-capable args %v were accepted", args)
		}
	}
}

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

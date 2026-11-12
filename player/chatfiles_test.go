package main

// Kept from the chat-file tests when the suite was cut to its core: the
// upload name is the one part a remote server sees verbatim.

import (
	"strings"
	"testing"
)

// Upload names keep letters (any script), digits, dots and dashes, turn runs
// of anything else into one underscore, and never carry a path or a line
// break into the part header.
func TestChatUploadName(t *testing.T) {
	cases := map[string]string{
		"my save (1).srm":     "my_save_1.srm",
		"../../etc/passwd":    "passwd",
		`C:\Users\x\game.gba`: "game.gba",
		"Покемон.sav":         "Покемон.sav",
		"a\r\nb.txt":          "a_b.txt",
		"":                    "file",
		"  .bashrc":           "file.bashrc",
		"report.final.v2.log": "report.final.v2.log",
	}
	for in, want := range cases {
		if got := chatUploadName(in); got != want {
			t.Errorf("chatUploadName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A video sent to Juka is read from ffmpeg's own words (vision_video.go): its
// length, its size, whether it has pictures (a song's cover is not a video)
// and sound — and the WAV ffmpeg writes may carry a LIST chunk before "data".
func TestVisionReadsFFmpeg(t *testing.T) {
	phone := "Input #0, mov,mp4,m4a,3gp,3g2,mj2, from 'clip.mp4':\n" +
		"  Duration: 00:01:02.50, start: 0.000000, bitrate: 4532 kb/s\n" +
		"  Stream #0:0[0x1](und): Video: h264 (High) (avc1 / 0x31637661), yuv420p(tv, bt709, progressive), 1920x1080 [SAR 1:1 DAR 16:9], 30 fps (default)\n" +
		"  Stream #0:1[0x2](und): Audio: aac (LC) (mp4a / 0x6134706D), 48000 Hz, stereo, fltp, 128 kb/s (default)\n" +
		"At least one output file must be specified\n"
	song := "Input #0, mp3, from 'song.mp3':\n  Duration: 00:03:21.04, start: 0.025057, bitrate: 320 kb/s\n" +
		"  Stream #0:0: Audio: mp3, 44100 Hz, stereo, fltp, 320 kb/s\n" +
		"  Stream #0:1: Video: mjpeg (Baseline), yuvj420p(pc), 500x500 [SAR 1:1 DAR 1:1], 90k tbn (attached pic)\n"
	live := "Input #0, matroska,webm, from 'live.webm':\n  Duration: N/A, start: 0.000000, bitrate: N/A\n" +
		"  Stream #0:0: Video: vp9 (Profile 0), yuv420p(tv), 1280x720, SAR 1:1 DAR 16:9, 30 fps (default)\n"
	cases := []struct {
		text string
		ok   bool
		want visionProbe
	}{
		{phone, true, visionProbe{duration: 62.5, width: 1920, height: 1080, video: true, audio: true}},
		{song, true, visionProbe{duration: 201.04, audio: true}},
		{live, true, visionProbe{width: 1280, height: 720, video: true}},
		{"clip.mp4: Invalid data found when processing input\n", false, visionProbe{}},
	}
	for i, c := range cases {
		got, ok := visionParseProbe(c.text)
		if ok != c.ok || got.video != c.want.video || got.audio != c.want.audio || got.width != c.want.width ||
			got.height != c.want.height || got.duration < c.want.duration-0.001 || got.duration > c.want.duration+0.001 {
			t.Errorf("case %d: got %+v ok=%v, want %+v ok=%v", i, got, ok, c.want, c.ok)
		}
	}
	wav := func(chunks ...string) []byte { return []byte("RIFF\x00\x00\x00\x00WAVE" + strings.Join(chunks, "")) }
	fmtChunk := "fmt \x10\x00\x00\x00" + strings.Repeat("\x00", 16)
	for _, c := range []struct {
		head []byte
		want int
	}{
		{wav(fmtChunk, "data\x00\x00\x00\x00"), 44},
		{wav(fmtChunk, "LIST\x19\x00\x00\x00"+strings.Repeat("x", 26), "data\x00\x00\x00\x00"), 44 + 34},
		{[]byte("not a wave file at all"), -1},
	} {
		if got := visionWAVData(c.head); got != c.want {
			t.Errorf("visionWAVData(%q) = %d, want %d", c.head, got, c.want)
		}
	}
}

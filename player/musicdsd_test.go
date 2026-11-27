package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDSDFilesReachMusicPlayer(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"First.DSF", "Second.DFF"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !isAudioFile(name) || !filesHasDedicatedViewer(path) {
			t.Errorf("%s is not recognized as playable audio", name)
		}
		if !isAudioEntry(FileEntry{Name: name, Path: path}) ||
			!isAudioEntry(FileEntry{Name: "Album track", Path: path}) {
			t.Errorf("%s is not routed to the music player from Files", name)
		}
		if video, ok := mlibMediaKind(name); !ok || video {
			t.Errorf("%s is not indexed as music", name)
		}
		if got := homeSearchFileKind(name, false); got != "music" {
			t.Errorf("search kind for %s = %q", name, got)
		}
	}
	if files := audioFilesIn(dir); len(files) != 2 {
		t.Errorf("folder playlist has %d DSD tracks, want 2", len(files))
	}
	if !isAudioURL("https://example.test/album/First.DSF?download=1") ||
		!isAudioURL("https://example.test/album/Second.DFF#play") {
		t.Error("remote DSD audio links are not recognized")
	}
	if !isAudioEntry(FileEntry{Name: "Album track", Path: "https://example.test/album/First.DSF?download=1"}) {
		t.Error("remote DSD entry is not routed to the music player")
	}
}

func musicDSDTestSamples() (dsf, dff []byte) {
	const (
		rate     = 2822400
		block    = 4096
		channels = 2
	)
	// The DSF payload has one 4096-byte block per channel. DFF uses the same
	// bytes in its uncompressed DSD chunk. Both are short, valid DSD streams.
	payload := bytes.Repeat([]byte{0x55, 0xaa}, block*channels/2)
	dsf = make([]byte, 28+52+12+len(payload))
	copy(dsf[0:4], "DSD ")
	binary.LittleEndian.PutUint64(dsf[4:12], 28)
	binary.LittleEndian.PutUint64(dsf[12:20], uint64(len(dsf)))
	copy(dsf[28:32], "fmt ")
	binary.LittleEndian.PutUint64(dsf[32:40], 52)
	binary.LittleEndian.PutUint32(dsf[40:44], 1) // format version
	binary.LittleEndian.PutUint32(dsf[48:52], 2) // stereo channel type
	binary.LittleEndian.PutUint32(dsf[52:56], channels)
	binary.LittleEndian.PutUint32(dsf[56:60], rate)
	binary.LittleEndian.PutUint32(dsf[60:64], 1) // least significant bit first
	binary.LittleEndian.PutUint64(dsf[64:72], block*8)
	binary.LittleEndian.PutUint32(dsf[72:76], block)
	copy(dsf[80:84], "data")
	binary.LittleEndian.PutUint64(dsf[84:92], uint64(12+len(payload)))
	copy(dsf[92:], payload)

	chunk := func(id string, body []byte) []byte {
		out := make([]byte, 12+len(body)+len(body)%2)
		copy(out[:4], id)
		binary.BigEndian.PutUint64(out[4:12], uint64(len(body)))
		copy(out[12:], body)
		return out
	}
	fs := make([]byte, 4)
	binary.BigEndian.PutUint32(fs, rate)
	prop := append([]byte("SND "), chunk("FS  ", fs)...)
	prop = append(prop, chunk("CHNL", []byte{0, channels, 'S', 'L', 'F', 'T', 'S', 'R', 'G', 'T'})...)
	prop = append(prop, chunk("CMPR", []byte{'D', 'S', 'D', ' ', 3, 'D', 'S', 'D'})...)
	form := append([]byte("DSD "), chunk("FVER", []byte{1, 5, 0, 0})...)
	form = append(form, chunk("PROP", prop)...)
	form = append(form, chunk("DSD ", payload)...)
	dff = make([]byte, 12+len(form))
	copy(dff[:4], "FRM8")
	binary.BigEndian.PutUint64(dff[4:12], uint64(len(form)))
	copy(dff[12:], form)
	return dsf, dff
}

func TestDSDDecoderProducesPCM(t *testing.T) {
	ffmpeg := ""
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(filepath.Join("required", "ffmpeg.exe")); err == nil {
			ffmpeg, err = filepath.Abs(filepath.Join("required", "ffmpeg.exe"))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if ffmpeg == "" {
		var err error
		ffmpeg, err = exec.LookPath("ffmpeg")
		if err != nil {
			t.Skip("ffmpeg is not installed")
		}
	}
	dsf, dff := musicDSDTestSamples()
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"sample.dsf", dsf},
		{"sample.dff", dff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name)
			if err := os.WriteFile(path, tc.data, 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := hiddenCommand(ffmpeg, musicFFmpegArgs(path, 0, 1, 44100)...)
			pcm, err := cmd.Output()
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					t.Fatalf("ffmpeg could not decode %s: %v: %s", tc.name, err, exit.Stderr)
				}
				t.Fatal(err)
			}
			if len(pcm) == 0 || len(pcm)%4 != 0 {
				t.Errorf("%s decoded to %d PCM bytes", tc.name, len(pcm))
			}
		})
	}
}

// TestUSBDACReadings reads what the kernel and ffprobe say about a USB DAC and
// a lossless track (usbdac.go, usbdac_bitperfect.go): outside data, so odd
// input must not invent a card, a rate or a bit depth.
func TestUSBDACReadings(t *testing.T) {
	cards := parseAsoundCards(" 0 [audiocodec     ]: audiocodec - audiocodec\n" +
		"                      audiocodec\n" +
		" 1 [Dongle         ]: USB-Audio - USB-C to 3.5mm Headphone Jack A\n" +
		"                      Apple Inc. USB-C to 3.5mm Headphone Jack Adapter at usb-xhci-hcd.0-1, high speed\n" +
		"garbage line\n")
	if len(cards) != 2 || cards[1].index != 1 || cards[1].id != "Dongle" || cards[1].driver != "USB-Audio" ||
		cards[1].name != "USB-C to 3.5mm Headphone Jack A" || cards[0].driver != "audiocodec" {
		t.Fatalf("cards = %+v", cards)
	}
	alts := parseUSBStreamPlayback("Apple Inc. USB-C : USB Audio\n\nPlayback:\n  Status: Stop\n  Interface 2\n" +
		"    Altset 1\n    Format: S16_LE\n    Channels: 2\n    Rates: 44100, 48000\n    Bits: 16\n" +
		"    Altset 2\n    Format: S24_3LE\n    Channels: 2\n    Rates: 8000 - 192000 (continuous)\n\n" +
		"Capture:\n    Format: S32_LE\n    Channels: 1\n    Rates: 96000\n")
	if len(alts) != 2 || alts[0].format != "S16_LE" || alts[0].channels != 2 || !alts[0].playsRate(44100) || alts[0].playsRate(96000) ||
		alts[1].format != "S24_3LE" || !alts[1].playsRate(96000) || alts[1].playsRate(384000) {
		t.Fatalf("alts = %+v", alts)
	}
	f := parseMusicTrackFormat("codec_name=flac\nsample_rate=96000\nsample_fmt=s32\nchannels=2\nbits_per_sample=0\nbits_per_raw_sample=24\n")
	if f.codec != "flac" || f.rate != 96000 || f.bits != 24 || f.channels != 2 || f.float || !musicLosslessCodec(f.codec) {
		t.Fatalf("flac = %+v", f)
	}
	f = parseMusicTrackFormat("codec_name=mp3\nsample_rate=44100\nsample_fmt=fltp\nchannels=2\nbits_per_raw_sample=N/A\n")
	if !f.float || f.bits != 0 || musicLosslessCodec(f.codec) {
		t.Fatalf("mp3 = %+v", f)
	}
	if usbDACPCMIs("bluealsa:SRV=org.bluealsa,DEV=00:11:22:33:44:55,PROFILE=a2dp") || !usbDACPCMIs(usbDACPCMFor("Dongle")) {
		t.Fatal("a headset PCM read as a DAC, or the other way")
	}
}

// TestMusicFFmpegArgsStreamTimeout: a radio or podcast URL (or the https
// relay's loopback address for one) gets the video decoders' read timeout and
// reconnect ahead of -i (MED-36); a file on the card gets neither.
func TestMusicFFmpegArgsStreamTimeout(t *testing.T) {
	inputOpts := func(args []string) map[string]string {
		opts := map[string]string{}
		for i := 0; i+1 < len(args) && args[i] != "-i"; i++ {
			if len(args[i]) > 1 && args[i][0] == '-' {
				opts[args[i]] = args[i+1]
			}
		}
		return opts
	}
	for _, src := range []string{"http://radio.example/stream", "https://cdn.example/ep.mp3?t=1", "http://127.0.0.1:4040/r/abc"} {
		opts := inputOpts(musicFFmpegArgs(src, 90, 1, 44100))
		if opts["-rw_timeout"] != ffmpegReadTimeoutArg() || opts["-reconnect"] != "1" ||
			opts["-reconnect_delay_max"] != "4" || opts["-ss"] != "90.000" {
			t.Errorf("%s: input options %v", src, opts)
		}
		if _, ok := opts["-user_agent"]; ok {
			t.Errorf("%s: a podcast or radio link announced a browser: %v", src, opts)
		}
	}
	opts := inputOpts(musicFFmpegArgs("/mnt/SDCARD/Music/a.flac", 0, 1, 44100))
	for _, k := range []string{"-rw_timeout", "-reconnect", "-reconnect_delay_max"} {
		if _, ok := opts[k]; ok {
			t.Errorf("a local file got %s: %v", k, opts)
		}
	}
}


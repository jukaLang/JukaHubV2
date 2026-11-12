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
			cmd := exec.Command(ffmpeg, musicFFmpegArgs(path, 0, 1, 44100)...)
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

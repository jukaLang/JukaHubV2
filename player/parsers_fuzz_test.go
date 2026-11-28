package main

// TEST-8: hostile input for the parsers package main feeds from files, the
// network and /proc. Every target here is also an ordinary test over its seed
// corpus, so a push checks the seeds and the weekly job mutates them.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func FuzzM3UStreams(f *testing.F) {
	f.Add("#EXTM3U\n#EXTINF:-1,Test\nhttp://example.com/a.m3u8\n")
	f.Add("#EXTM3U\n#EXTINF:-1 tvg-id=\"x\" tvg-name=\"N\",N\nhttp://[::1\n")
	f.Fuzz(func(t *testing.T, data string) {
		_ = parseM3UStreams(data, 64)
	})
}

func FuzzM3UChannels(f *testing.F) {
	f.Add("#EXTM3U\n#EXTINF:-1 tvg-id=\"x\",Name\nhttp://example.com/live.m3u8\n")
	f.Add("#EXTM3U\n#EXTGRP:News\n#EXTINF:-1,Name\nudp://@239.0.0.1:1234\n")
	f.Fuzz(func(t *testing.T, data string) {
		_ = parseM3UChannels(data)
		_ = parseM3URemembered(data)
	})
}

func FuzzCueGdiSheets(f *testing.F) {
	f.Add([]byte("FILE \"disc.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n"))
	f.Add([]byte("3\n1 0 4 2352 track01.bin 0\n2 600 0 2352 track02.raw 0\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "sheet")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_ = gameDiagCueMissing(path)
		_ = gameDiagGdiMissing(path)
	})
}

func FuzzPDFText(f *testing.F) {
	f.Add([]byte("%PDF-1.4\n1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n%%EOF\n"))
	f.Add([]byte("%PDF-1.7\n1 0 obj\n<< /Length 5 >>\nstream\nq Q\nendstream\nendobj\n%%EOF\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = pdfExtractTextFromBytes(data, "fuzz.pdf")
		_, _ = pdfvOpenBytes(data)
	})
}

func FuzzEPUBText(f *testing.F) {
	f.Add([]byte("PK\x03\x04"))
	f.Add([]byte("META-INF/container.xml"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = epubParseBytes(data, "fuzz.epub")
	})
}

func FuzzTarMembers(f *testing.F) {
	f.Add([]byte("not a tar"))
	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		archive := filepath.Join(dir, "fuzz.tar")
		stage := filepath.Join(dir, "stage")
		if err := os.MkdirAll(stage, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archive, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = extractTarMembers(context.Background(), archive, stage, map[string]bool{"a": true}, nil)
	})
}

func FuzzXpackManifest(f *testing.F) {
	f.Add([]byte(`{"id":"x","name":"X","version":"1.0"}`))
	f.Add([]byte(`{"files":[{"name":"../../etc/passwd"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = xpackParseManifest(data, "fuzz")
	})
}

func FuzzInputDevices(f *testing.F) {
	f.Add([]byte("I: Bus=0003 Vendor=054c Product=09cc Version=0111\nN: Name=\"Wireless Controller\"\nH: Handlers=event3 js0\n"))
	f.Add([]byte("I: Bus=0000\nN: Name=\"\"\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = bzParseInputDevices(data)
		_, _ = hintParseInputDevices(data)
	})
}

func FuzzXMLConfig(f *testing.F) {
	f.Add([]byte(`<?xml version="1.0"?><JukaHub><Scenes><Scene name="Home"/></Scenes></JukaHub>`))
	f.Add([]byte(`<JukaHub><Variables><Custom key="x" value="y"/></Variables></JukaHub>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "config.xml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = LoadXMLConfig(path)
	})
}

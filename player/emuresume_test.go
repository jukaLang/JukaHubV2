package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEMURangeDecisions(t *testing.T) {
	for _, tc := range []struct {
		size, max, want int64
	}{
		{0, 100, 0}, {-1, 100, 0}, {40, 100, 40}, {140, 100, 0}, {40, 0, 40},
	} {
		if got := emuResumeOffset(tc.size, tc.max); got != tc.want {
			t.Errorf("offset(%d, %d) = %d, want %d", tc.size, tc.max, got, tc.want)
		}
	}
	for _, tc := range []struct {
		status int
		offset int64
		want   emuRangeAction
	}{
		{200, 0, emuRangeRestart}, {206, 10, emuRangeAppend},
		{200, 10, emuRangeRestart}, {416, 10, emuRangeDropRetry},
		{404, 10, emuRangeFail}, {206, 0, emuRangeFail},
	} {
		if got := emuRangeActionFor(tc.status, tc.offset); got != tc.want {
			t.Errorf("action(%d, %d) = %d, want %d", tc.status, tc.offset, got, tc.want)
		}
	}
	for _, tc := range []struct {
		header string
		ok     bool
		total  int64
	}{
		{"bytes 10-19/20", true, 20},
		{"bytes 11-19/20", false, 0}, // wrong start
		{"bytes 10-19/19", false, 0}, // total below end
		{"bytes 10-19", false, 0},
		{"", false, 0},
	} {
		got, ok := emuParseContentRange(tc.header, 10)
		if ok != tc.ok || (ok && got != tc.total) {
			t.Errorf("range %q => (%d, %v), want (%d, %v)", tc.header, got, ok, tc.total, tc.ok)
		}
	}
}

// FuzzEMUContentRange guards the resume path against a hostile or broken
// server: a Content-Range must be parsed without panics or huge allocations.
func FuzzEMUContentRange(f *testing.F) {
	for _, seed := range []string{"bytes 10-19/20", "bytes 0-0/1", "bytes 5-4/9", "bogus", "", "bytes 999999999999999999999-1/2"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, header string) {
		for _, offset := range []int64{0, 1, 10, 1 << 40} {
			_, _ = emuParseContentRange(header, offset)
		}
	})
}

// A cut-off transfer leaves a .part behind; the next download must ask for
// the missing tail and produce the digest of the whole file.
func TestEMUDownloadResumesPart(t *testing.T) {
	body := []byte(strings.Repeat("RetroArch archive bytes ", 400))
	oldValidate := emulatorUpdateValidateURL
	emulatorUpdateValidateURL = func(raw string) error { return nil }
	defer func() { emulatorUpdateValidateURL = oldValidate }()

	var sawRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRange = r.Header.Get("Range")
		start := 0
		if n, err := fmt.Sscanf(sawRange, "bytes=%d-", &start); err == nil && n == 1 && start > 0 && start < len(body) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(body)-1, len(body)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[start:])
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "RetroArch.7z")
	part := dest + ".part"
	if err := os.WriteFile(part, body[:100], 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := emuDownload(context.Background(), srv.Client(), srv.URL, dest, int64(len(body)+1024), nil)
	if err != nil {
		t.Fatal(err)
	}
	if sawRange != "bytes=100-" {
		t.Fatalf("resume asked for %q, want bytes=100-", sawRange)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != string(body) {
		t.Fatalf("resumed file is wrong (len %d, err %v)", len(got), err)
	}
	want := sha256.Sum256(body)
	if sum != hex.EncodeToString(want[:]) {
		t.Fatalf("digest %q is not the whole file's %q", sum, hex.EncodeToString(want[:]))
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("part file left behind: %v", err)
	}
}

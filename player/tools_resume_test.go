package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelperDownloadResumesFromPart(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 1024) // 10 KB
	half := len(body) / 2
	var sawRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRange = r.Header.Get("Range")
		if sawRange == "" {
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[half:])
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "tool.bin")
	if err := os.WriteFile(dest+".part", body[:half], 0o600); err != nil {
		t.Fatal(err)
	}
	var lastDone, lastTotal int64
	err := downloadFileWithProgressCtx(context.Background(), srv.URL, dest, func(done, total int64) {
		lastDone, lastTotal = done, total
	})
	if err != nil {
		t.Fatal(err)
	}
	if sawRange != fmt.Sprintf("bytes=%d-", half) {
		t.Fatalf("Range = %q, want bytes=%d-", sawRange, half)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("resumed file = %d bytes, err %v", len(got), err)
	}
	if lastDone < int64(len(body)) || lastTotal < int64(len(body)) {
		t.Fatalf("progress = %d/%d", lastDone, lastTotal)
	}
	if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
		t.Fatalf("the .part file survived success: %v", err)
	}
}

func TestHelperDownloadRestartsWhenServerIgnoresRange(t *testing.T) {
	body := bytes.Repeat([]byte("abcdefghij"), 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A 200 answer means the staged bytes do not match the server: the
		// helper must start over, not append the whole body to the part.
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "tool.bin")
	if err := os.WriteFile(dest+".part", []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := downloadFileWithProgressCtx(context.Background(), srv.URL, dest, nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, body) || strings.HasPrefix(string(got), "stale") {
		t.Fatalf("file after restart = %q, err %v", string(got[:min(10, len(got))]), err)
	}
}

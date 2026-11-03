package main

// Patch downloads on the handheld run over a slow card and flaky Wi-Fi. These
// tests pin the resume rule: a transport failure leaves the staged bytes, the
// next attempt asks for the rest with a Range request, and a file that does not
// match the expected digest never becomes the final file.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const resumeBody = "the quick brown fox jumps over the lazy dog"

func shaOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// allowLoopbackPatchHost lets a test reach an httptest server through the
// allowlist, and restores the map afterwards.
func allowLoopbackPatchHost(t *testing.T) {
	t.Helper()
	prev, had := allowedPatchHosts["127.0.0.1"]
	allowedPatchHosts["127.0.0.1"] = true
	t.Cleanup(func() {
		if had {
			allowedPatchHosts["127.0.0.1"] = prev
			return
		}
		delete(allowedPatchHosts, "127.0.0.1")
	})
}

func TestPatchDownloadResumesFromThePartial(t *testing.T) {
	allowLoopbackPatchHost(t)
	var sawRange string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRange = r.Header.Get("Range")
		http.ServeContent(w, r, "pkg.zip", timeZero, strings.NewReader(resumeBody))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "pkg.zip")
	part := dest + ".part"
	head := resumeBody[:20]
	if err := os.WriteFile(part, []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := downloadToFile(context.Background(), srv.Client(), srv.URL, dest, shaOf(resumeBody), 1<<20); err != nil {
		t.Fatalf("resumed download failed: %v", err)
	}
	if sawRange != "bytes=20-" {
		t.Fatalf("Range header = %q, want the request to start where the partial ended", sawRange)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != resumeBody {
		t.Fatalf("file = %q, want the partial plus the rest, not a duplicated or truncated body", got)
	}
	if regularFile(part) {
		t.Fatal("the staging file survived a finished download")
	}
}

func TestPatchDownloadRestartsWhenTheServerIgnoresRange(t *testing.T) {
	allowLoopbackPatchHost(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No Range support: always the whole body with 200.
		w.Header().Set("Content-Type", "application/zip")
		io.WriteString(w, resumeBody)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "pkg.zip")
	if err := os.WriteFile(dest+".part", []byte("garbage-head"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := downloadToFile(context.Background(), srv.Client(), srv.URL, dest, shaOf(resumeBody), 1<<20); err != nil {
		t.Fatalf("download failed: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != resumeBody {
		t.Fatalf("file = %q, want the partial replaced by the full body", got)
	}
}

func TestPatchDownloadKeepsPartialOnTransportFailure(t *testing.T) {
	allowLoopbackPatchHost(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Promise more than is written, then drop the connection: the client
		// sees an unexpected EOF mid-body.
		w.Header().Set("Content-Length", "1000")
		io.WriteString(w, "half")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, err := hj.Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "pkg.zip")
	err := downloadToFile(context.Background(), srv.Client(), srv.URL, dest, shaOf(resumeBody), 1<<20)
	if err == nil {
		t.Fatal("a truncated transfer was reported as success")
	}
	if !regularFile(dest + ".part") {
		t.Fatal("the truncated transfer removed its partial file; the next attempt re-downloads from zero")
	}
	if regularFile(dest) {
		t.Fatal("a truncated transfer left a file at the destination")
	}
}

func TestPatchDownloadRemovesPartialOnDigestMismatch(t *testing.T) {
	allowLoopbackPatchHost(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, resumeBody)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "pkg.zip")
	err := downloadToFile(context.Background(), srv.Client(), srv.URL, dest, shaOf("something else"), 1<<20)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("error = %v, want a digest mismatch", err)
	}
	if regularFile(dest+".part") || regularFile(dest) {
		t.Fatal("a bad download was left on the card; it must not become the head of a later attempt")
	}
}

func TestPatchDownloadRejectsOversizedFile(t *testing.T) {
	allowLoopbackPatchHost(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, resumeBody)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "pkg.zip")
	err := downloadToFile(context.Background(), srv.Client(), srv.URL, dest, "", 8)
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("error = %v, want the size limit to refuse the file", err)
	}
	if regularFile(dest) || regularFile(dest+".part") {
		t.Fatal("an oversized download was left on the card")
	}
}

func TestPatchDownloadStillRefusesNonAllowlistedHosts(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, resumeBody)
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if allowedPatchHosts[u.Hostname()] {
		t.Fatal("the loopback host is on the allowlist here, so this test cannot show the refusal")
	}
	dest := filepath.Join(t.TempDir(), "pkg.zip")
	if err := downloadToFile(context.Background(), srv.Client(), srv.URL, dest, "", 1<<20); err == nil {
		t.Fatal("a host outside the allowlist was downloaded from")
	}
}

var timeZero = time.Time{}

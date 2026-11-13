package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTTSFetchPinnedResumesAndVerifies(t *testing.T) {
	data := []byte("a verified voice model")
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			if r.Header.Get("Range") != "" {
				t.Errorf("first request unexpectedly ranged")
			}
			_, _ = w.Write(data[:8])
			return
		}
		if r.Header.Get("Range") != "bytes=8-" {
			t.Errorf("retry Range = %q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", "bytes 8-21/22")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[8:])
	}))
	defer server.Close()
	dest := filepath.Join(t.TempDir(), "model.onnx")
	if err := ttsFetchPinned(context.Background(), server.Client(), server.URL, dest, want, int64(len(data)), nil); err == nil {
		t.Fatal("short first response was accepted")
	}
	if err := ttsFetchPinned(context.Background(), server.Client(), server.URL, dest, want, int64(len(data)), nil); err != nil {
		t.Fatal(err)
	}
	if !ttsFileMatches(dest, int64(len(data)), want) {
		t.Fatal("completed model did not match the pinned bytes")
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if err := ttsFetchPinned(context.Background(), server.Client(), server.URL, dest, want, int64(len(data)), nil); err != nil || requests != 2 {
		t.Fatalf("a verified file was fetched again: requests=%d err=%v", requests, err)
	}
}

func TestTTSFetchPinnedRefusesWrongBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "wrong")
	}))
	defer server.Close()
	dest := filepath.Join(t.TempDir(), "model.onnx")
	sum := sha256.Sum256([]byte("right"))
	err := ttsFetchPinned(context.Background(), server.Client(), server.URL, dest, hex.EncodeToString(sum[:]), 5, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("wrong bytes accepted: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("wrong bytes left a ready model: %v", err)
	}
}

func makeTTSPiperArchive(t *testing.T, files map[string][]byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "piper.tar.gz")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTTSExtractPiperKeepsOnlyRequiredFiles(t *testing.T) {
	archive := makeTTSPiperArchive(t, map[string][]byte{
		"piper/piper":                       []byte("binary"),
		"piper/libespeak-ng.so.1.52.0.1":    []byte("library"),
		"piper/libpiper_phonemize.so.1.2.0": []byte("library"),
		"piper/libonnxruntime.so.1.14.1":    []byte("library"),
		"piper/espeak-ng-data/en_dict":      []byte("dictionary"),
		"piper/espeak-ng-data/phontab":      []byte("table"),
		"piper/espeak-ng-data/phondata":     []byte("table"),
		"piper/espeak-ng-data/phonindex":    []byte("table"),
		"piper/espeak-ng-data/intonations":  []byte("table"),
		"piper/unrelated":                   []byte("no"),
	})
	dest := t.TempDir()
	if err := ttsExtractPiper(context.Background(), archive, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "libespeak-ng.so.1")); err != nil {
		t.Fatal("the loader's soname was not created:", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "unrelated")); !os.IsNotExist(err) {
		t.Fatalf("unrelated executable was extracted: %v", err)
	}
}

func TestTTSExtractPiperRejectsPathTraversal(t *testing.T) {
	archive := makeTTSPiperArchive(t, map[string][]byte{"piper/espeak-ng-data/../../escape": []byte("x")})
	if err := ttsExtractPiper(context.Background(), archive, t.TempDir()); err == nil {
		t.Fatal("archive traversal was accepted")
	}
}

// The voice's settings file is checked by shape, not a pinned digest (a wrong
// digest broke every install): a real-looking file is kept, anything else is
// refused and leaves nothing behind.
func TestTTSFetchVoiceConfigChecksShape(t *testing.T) {
	good := `{"audio":{"sample_rate":22050,"quality":"medium"},"espeak":{"voice":"en-us"},` +
		`"num_symbols":256,"phoneme_id_map":{"_":[0],"^":[1]}}`
	body := good
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	dest := filepath.Join(t.TempDir(), "voice.onnx.json")
	if err := ttsFetchVoiceConfig(context.Background(), server.Client(), server.URL, dest); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`<html>rate limited</html>`, `{"audio":{"sample_rate":22050},"espeak":{"voice":"de"},"num_symbols":2,"phoneme_id_map":{"a":[1]}}`, `{"espeak":{"voice":"en-us"}}`} {
		if err := ttsCheckVoiceConfig([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	body = `{}`
	other := filepath.Join(t.TempDir(), "voice.onnx.json")
	if err := ttsFetchVoiceConfig(context.Background(), server.Client(), server.URL, other); err == nil {
		t.Fatal("an empty settings file was accepted")
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("a refused settings file was kept: %v", err)
	}
	// The good copy from the first try is reused without asking again.
	if err := ttsFetchVoiceConfig(context.Background(), server.Client(), server.URL, dest); err != nil {
		t.Fatal(err)
	}
	wav := []byte("RIFF\x00\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x22\x56\x00\x00\x44\xac\x00\x00\x02\x00\x10\x00data\x04\x00\x00\x00\x01\x00\x02\x00")
	if !ttsWAVComplete(wav) || ttsWAVComplete(wav[:len(wav)-1]) {
		t.Fatal("ttsWAVComplete misjudged the data chunk")
	}
}

// A developer can point this at the pinned upstream archive when checking a
// new release. It exercises the real tar layout without committing the binary.
func TestTTSExtractPinnedPiperArchive(t *testing.T) {
	archive := os.Getenv("JUKA_TEST_PIPER_ARCHIVE")
	if archive == "" {
		t.Skip("set JUKA_TEST_PIPER_ARCHIVE to check the downloaded upstream archive")
	}
	if !ttsFileMatches(archive, ttsPiperArchiveSize, ttsPiperArchiveSHA) {
		t.Fatal("archive differs from the pinned upstream package")
	}
	if err := ttsExtractPiper(context.Background(), archive, t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

// A live answer is spoken in order, each sentence once, with the first chunk
// short and the cap applied, and a stopped wait ends the line.
func TestTTSLiveSourceOrderAndCap(t *testing.T) {
	src := ttsOpenSource()
	ctx := context.Background()
	src.push([]string{"Hi there.", "This is the second sentence."}, false)
	first, ok := src.at(ctx, 0)
	if !ok || first != "Hi there. This is the second sentence." {
		t.Fatalf("first chunk = %q, %v", first, ok)
	}
	got := make(chan string, 1)
	go func() {
		c, _ := src.at(ctx, 1)
		got <- c
	}()
	src.push([]string{"Third.", "Fourth."}, true)
	if c := <-got; c != "Third. Fourth." {
		t.Fatalf("second chunk = %q", c)
	}
	if _, ok := src.at(ctx, 2); ok {
		t.Fatal("a finished line gave a third chunk")
	}
	open := ttsOpenSource()
	stop, cancel := context.WithCancel(ctx)
	cancel()
	if _, ok := open.at(stop, 0); ok {
		t.Fatal("a cancelled wait gave a chunk")
	}
	total := 0
	long := strings.Repeat("word ", 100) + "end."
	out, capped := ttsLiveCap([]string{"Short one.", long, long}, &total)
	if !capped || out[len(out)-1] != ttsRestOnScreen || total > ttsSpokenMax+1 {
		t.Fatalf("cap: %v %v total %d", out, capped, total)
	}
}

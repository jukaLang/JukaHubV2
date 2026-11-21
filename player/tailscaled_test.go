package main

// Tests for the built-in Tailscale's installer (tailscaled.go): the package
// list from pkgs.tailscale.com, the published SHA-256 and the tarball.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTailscaledParseIndex(t *testing.T) {
	data := []byte(`{"TarballsVersion":"1.104.7","Tarballs":{"amd64":"tailscale_1.104.7_amd64.tgz","arm64":"tailscale_1.104.7_arm64.tgz"}}`)
	idx, err := tailscaledParseIndex(data, "arm64")
	if err != nil || idx.Version != "1.104.7" || idx.Tarball != "tailscale_1.104.7_arm64.tgz" {
		t.Fatalf("arm64 = %+v, %v", idx, err)
	}
	if _, err := tailscaledParseIndex(data, "arm"); err == nil {
		t.Fatal("a missing architecture was accepted")
	}
	// Nothing in the answer may steer the download elsewhere.
	for _, bad := range []string{
		`{"TarballsVersion":"1.104.7","Tarballs":{"arm64":"../tailscale_1.104.7_arm64.tgz"}}`,
		`{"TarballsVersion":"1.104.7","Tarballs":{"arm64":"https://evil.example/tailscale_1.104.7_arm64.tgz"}}`,
		`{"TarballsVersion":"1.104/../7","Tarballs":{"arm64":"tailscale_1.104/../7_arm64.tgz"}}`,
		`not json`,
	} {
		if idx, err := tailscaledParseIndex([]byte(bad), "arm64"); err == nil {
			t.Fatalf("accepted %s as %+v", bad, idx)
		}
	}
}

func TestTailscaledCheckSumRefusesMismatch(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tailscale_1.104.7_arm64.tgz")
	if err := os.WriteFile(file, []byte("the tarball"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := hashFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if sum, err := tailscaledCheckSum(file, got+"\n", "tailscale_1.104.7_arm64.tgz"); err != nil || sum != got {
		t.Fatalf("matching hash: %q, %v", sum, err)
	}
	wrong := "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := tailscaledCheckSum(file, wrong+"  tailscale_1.104.7_arm64.tgz\n", "tailscale_1.104.7_arm64.tgz"); err == nil {
		t.Fatal("a mismatched download was accepted")
	}
	if _, err := tailscaledCheckSum(file, "<html>not found</html>", "tailscale_1.104.7_arm64.tgz"); err == nil {
		t.Fatal("an unreadable hash list was accepted")
	}
}

func TestTailscaledUnpackStaysInStage(t *testing.T) {
	write := func(path string, names ...string) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for _, name := range names {
			body := []byte("#!/bin/sh\n")
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
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
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "good.tgz")
	write(good, "tailscale_1.104.7_arm64/tailscale", "tailscale_1.104.7_arm64/tailscaled", "tailscale_1.104.7_arm64/systemd/tailscaled.service")
	stage := filepath.Join(dir, "stage")
	if err := tailscaledUnpack(context.Background(), good, stage); err != nil {
		t.Fatalf("good tarball: %v", err)
	}
	for _, name := range []string{"tailscale", "tailscaled"} {
		if _, err := os.Stat(filepath.Join(stage, name)); err != nil {
			t.Fatalf("%s not unpacked: %v", name, err)
		}
	}

	evil := filepath.Join(dir, "evil.tgz")
	write(evil, "../tailscaled", "tailscale_1.104.7_arm64/tailscale")
	stage2 := filepath.Join(dir, "inner", "stage")
	if err := tailscaledUnpack(context.Background(), evil, stage2); err == nil {
		t.Fatal("a tarball with ../tailscaled was unpacked")
	}
	if _, err := os.Stat(filepath.Join(dir, "inner", "tailscaled")); err == nil {
		t.Fatal("../tailscaled was written outside the stage")
	}
}

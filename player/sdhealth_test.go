package main

// Tests for the SD-card health check: mount-table parsing (the part that decides
// which filesystem the app is really running from) and the findings rules.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseMountsAndUnescape(t *testing.T) {
	data := strings.Join([]string{
		"/dev/root / ext4 rw,relatime 0 0",
		"/dev/mmcblk0p1 /mnt/SDCARD exfat rw 0 0",
		"/dev/mmcblk0p2 /mnt/SDCARD/Emus ext4 rw 0 0",
		"tmpfs /mnt/Card\\040Space tmpfs rw 0 0",
		"garbage",
	}, "\n")
	mounts := parseMounts(data)
	if len(mounts) != 4 {
		t.Fatalf("parsed %d mounts, want 4: %+v", len(mounts), mounts)
	}
	if got := mounts[3].Point; got != "/mnt/Card Space" {
		t.Fatalf("octal escape not decoded: %q", got)
	}

	cases := map[string]string{
		"/mnt/SDCARD/Apps/JukaHub": "exfat",
		"/mnt/SDCARD/Emus/GB/game": "ext4",
		"/mnt/Card Space/file.bin": "tmpfs",
		"/etc/hosts":               "ext4",
	}
	for path, want := range cases {
		if got := fsTypeFor(mounts, path); got != want {
			t.Errorf("fsTypeFor(%q) = %q, want %q", path, got, want)
		}
	}
	// A path that only shares a prefix with a mount point is not inside it.
	if got := fsTypeFor(mounts, "/mnt/SDCARDX/file"); got != "ext4" {
		t.Errorf("prefix-only match leaked: %q", got)
	}
}

func TestSDFindings(t *testing.T) {
	tests := []struct {
		name     string
		fsType   string
		total    int64
		free     int64
		mbps     float64
		probed   bool
		verified bool
		want     []string // substrings that must appear
		wantNone bool
	}{
		{
			name: "healthy exfat", fsType: "exfat", total: 64 << 30, free: 40 << 30,
			mbps: 22, probed: true, verified: true,
			want: nil, wantNone: true,
		},
		{
			name: "fat32 limit", fsType: "vfat", total: 32 << 30, free: 20 << 30,
			mbps: 15, probed: true, verified: true,
			want: []string{"FAT32", "4 GB"},
		},
		{
			name: "low free space", fsType: "exfat", total: 64 << 30, free: 100 << 20,
			mbps: 20, probed: true, verified: true,
			want: []string{"Only 100 MiB free"},
		},
		{
			name: "slow card", fsType: "exfat", total: 64 << 30, free: 40 << 30,
			mbps: 1.2, probed: true, verified: true,
			want: []string{"Slow card"},
		},
		{
			name: "failing card", fsType: "exfat", total: 64 << 30, free: 40 << 30,
			mbps: 0, probed: true, verified: false,
			want: []string{"did not read back correctly"},
		},
		{
			name: "free larger than capacity", fsType: "exfat", total: 1 << 30, free: 2 << 30,
			mbps: 20, probed: true, verified: true,
			want: []string{"more free space than capacity"},
		},
		{
			name: "no probe yet", fsType: "", total: 0, free: -1,
			probed: false, wantNone: true,
		},
	}
	for _, tc := range tests {
		got := sdFindings(tc.fsType, tc.total, tc.free, tc.mbps, tc.probed, tc.verified)
		if tc.wantNone && len(got) != 0 {
			t.Errorf("%s: expected no findings, got %v", tc.name, got)
			continue
		}
		joined := strings.Join(got, "\n")
		for _, want := range tc.want {
			if !strings.Contains(joined, want) {
				t.Errorf("%s: findings missing %q: %v", tc.name, want, got)
			}
		}
	}
}

func TestSDWriteProbeReadsBack(t *testing.T) {
	dir := t.TempDir()
	mbps, verified, err := sdWriteProbe(dir)
	if err != nil {
		t.Fatalf("probe on a temp dir failed: %v", err)
	}
	if !verified {
		t.Fatal("a healthy filesystem must read back identically")
	}
	if mbps <= 0 {
		t.Fatalf("write speed = %v, want a positive measurement", mbps)
	}
	// The probe must clean up after itself.
	if _, err := os.Stat(dir + string(os.PathSeparator) + ".jukahub-sdcheck"); !os.IsNotExist(err) {
		t.Fatal("probe left its scratch file behind")
	}
}

func TestSDHealthLineHasNoPaths(t *testing.T) {
	report := sdHealthReport{
		FSType: "exfat",
		Total:  64 << 30,
		Free:   12 << 30,
	}
	line := sdHealthLine(report)
	for _, want := range []string{"exfat", "64.0 GiB total", "12.0 GiB free"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q is missing %q", line, want)
		}
	}
	if strings.ContainsAny(line, `/\`) {
		t.Errorf("line %q looks like it carries a path", line)
	}
}

func TestSDHealthStatusLabel(t *testing.T) {
	if got := sdStatusLabel(sdHealthReport{Probed: true}); got != "OK" {
		t.Errorf("healthy label = %q", got)
	}
	if got := sdStatusLabel(sdHealthReport{Probed: true, Findings: []string{"a"}}); got != "1 warning" {
		t.Errorf("single warning label = %q", got)
	}
	if got := sdStatusLabel(sdHealthReport{Probed: true, Findings: []string{"a", "b"}}); got != "2 warnings" {
		t.Errorf("plural warning label = %q", got)
	}
	if got := sdStatusLabel(sdHealthReport{}); got != "not measured" {
		t.Errorf("unmeasured label = %q", got)
	}
}

func TestSDHealthCacheExpires(t *testing.T) {
	invalidateSDHealth()
	sdHealthCache.mu.Lock()
	sdHealthCache.report = &sdHealthReport{Probed: true}
	sdHealthCache.at = time.Now()
	sdHealthCache.mu.Unlock()
	if got := sdHealthCached(); !got.Probed {
		t.Fatal("a fresh cached report must be reused")
	}
	sdHealthCache.mu.Lock()
	sdHealthCache.at = time.Now().Add(-2 * sdHealthTTL)
	sdHealthCache.mu.Unlock()
	// Expired: the next call measures for real (temp dir or app dir).
	_ = sdHealthCached()
	invalidateSDHealth()
}

package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseYtDlpVersion(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"2026.10.26", "2026.10.26", true},
		{"2026.10.26\n", "2026.10.26", true},
		{"2026.11.04.232659", "2026.11.04", true},
		{"stable@2026.08.19", "2026.08.19", true},
		{"nightly@2026.11.04.232659 from yt-dlp/yt-dlp-nightly-builds", "2026.11.04", true},
		{"v2026.07.04", "2026.07.04", true},
		{"", "", false},
		{"Unknown", "", false},
		{"2026.13.01", "", false},
		{"1.65.4", "", false},
	}
	for _, c := range cases {
		v, ok := parseYtDlpVersion(c.in)
		if ok != c.ok {
			t.Errorf("parseYtDlpVersion(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && v.String() != c.want {
			t.Errorf("parseYtDlpVersion(%q) = %s, want %s", c.in, v, c.want)
		}
	}
}

func TestYtDlpVersionNewer(t *testing.T) {
	a, _ := parseYtDlpVersion("2026.08.19")
	b, _ := parseYtDlpVersion("2026.10.26")
	if !b.newerThan(a) || a.newerThan(b) || a.newerThan(a) {
		t.Fatal("release dates compare wrongly")
	}
}

func TestYtDlpAgeDue(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	if !ytdlpAgeDue("2026.07.04", time.Time{}, now) {
		t.Error("a July release is due in November")
	}
	if ytdlpAgeDue("2026.11.04", time.Time{}, now) {
		t.Error("a five-day-old release is not due")
	}
	// Unknown version: the binary's install time stands in.
	if !ytdlpAgeDue("", now.Add(-30*24*time.Hour), now) {
		t.Error("a binary written a month ago is due")
	}
	if ytdlpAgeDue("", now.Add(-time.Hour), now) {
		t.Error("a binary written an hour ago is not due")
	}
	if ytdlpAgeDue("", time.Time{}, now) {
		t.Error("nothing known must not count as due")
	}
}

func TestYtDlpUpdateNeeded(t *testing.T) {
	published := time.Date(2026, 10, 26, 22, 25, 0, 0, time.UTC)
	if !ytdlpUpdateNeeded("2026.10.26", published, "2026.07.04", time.Time{}) {
		t.Error("an older install needs the update")
	}
	if ytdlpUpdateNeeded("2026.10.26", published, "2026.10.26", time.Time{}) {
		t.Error("the same release needs no update")
	}
	if ytdlpUpdateNeeded("2026.10.26", published, "2026.11.04.232659", time.Time{}) {
		t.Error("a newer nightly needs no update")
	}
	// Unknown installed version: compare the install time with the release.
	if !ytdlpUpdateNeeded("2026.10.26", published, "", published.Add(-48*time.Hour)) {
		t.Error("a binary written before the release needs it")
	}
	if ytdlpUpdateNeeded("2026.10.26", published, "", published.Add(48*time.Hour)) {
		t.Error("a binary written after the release is that release or newer")
	}
	if ytdlpUpdateNeeded("2026.10.26", published, "", time.Time{}) {
		t.Error("nothing to compare must never mean download")
	}
}

// The release's checksum list is read the way the toolbox reads it.
func TestYtDlpChecksumListMatches(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	list := sum + "  yt-dlp_linux_aarch64\n" + strings.Repeat("cd", 32) + "  yt-dlp.exe\n"
	ok, err := toolboxMatchChecksum(sum, list, "yt-dlp_linux_aarch64", false)
	if err != nil || !ok {
		t.Fatalf("matching hash: ok=%v err=%v", ok, err)
	}
	if _, err := toolboxMatchChecksum(sum, list, "yt-dlp.exe", false); err == nil {
		t.Fatal("a different hash must fail")
	}
}

func TestYtDlpReplaceBinary(t *testing.T) {
	dir := t.TempDir()
	target := dir + string(os.PathSeparator) + "yt-dlp"
	staged := dir + string(os.PathSeparator) + ".yt-dlp-1.part"
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	backup, err := ytdlpReplaceBinary(staged, target)
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new" {
		t.Fatalf("target holds %q (%v), want the new binary", data, err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Error("the staged file is still there")
	}
	// The replaced copy is kept, one generation, and a rollback puts it back.
	if backup != target+ytdlpBackupSuffix {
		t.Fatalf("backup = %q, want %q", backup, target+ytdlpBackupSuffix)
	}
	if old, err := os.ReadFile(backup); err != nil || string(old) != "old" {
		t.Fatalf("backup holds %q (%v), want the old binary", old, err)
	}
	if err := ytdlpRollback(backup, target); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "old" {
		t.Fatalf("after rollback target holds %q (%v), want the old binary", data, err)
	}
	if err := ytdlpRollback("", target); err == nil {
		t.Error("a rollback with no copy kept must fail")
	}
}

// With nothing installed there is nothing to keep: the new copy just lands.
func TestYtDlpReplaceBinaryFirstInstall(t *testing.T) {
	dir := t.TempDir()
	target := dir + string(os.PathSeparator) + "yt-dlp"
	staged := dir + string(os.PathSeparator) + ".yt-dlp-2.part"
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	backup, err := ytdlpReplaceBinary(staged, target)
	if err != nil || backup != "" {
		t.Fatalf("replace = %q, %v; want no backup and no error", backup, err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "new" {
		t.Fatalf("target holds %q (%v)", data, err)
	}
}

// Only a clear failure condemns a release.
func TestClassifyYtDlpProbe(t *testing.T) {
	exitErr := errors.New("exit status 1")
	cases := []struct {
		name     string
		stderr   string
		err      error
		timedOut bool
		want     ytdlpProbeResult
	}{
		{"ran", "", nil, false, ytdlpProbeOK},
		{"timed out on a slow card", "", errors.New("signal: killed"), true, ytdlpProbeInconclusive},
		{"unpack failed", "[PYI-77:ERROR] Failed to extract libpython3.12.so: decompression resulted in return code -1", exitErr, false, ytdlpProbeInconclusive},
		{"c library too old", "./yt-dlp: /lib/libc.so.6: version `GLIBC_2.39' not found (required by ./yt-dlp)", exitErr, false, ytdlpProbeBroken},
		// The bootloader's flavour of the same failure carries the unpack
		// prefix; it must still condemn the release.
		{"python library needs a newer c library", "[PYI-77:ERROR] Failed to load Python shared library '/tmp/_MEI1/libpython3.13.so.1.0': /lib/libc.so.6: version `GLIBC_2.38' not found", exitErr, false, ytdlpProbeBroken},
		{"wrong architecture", "", errors.New("fork/exec ./yt-dlp: exec format error"), false, ytdlpProbeBroken},
		{"not executable", "", errors.New("fork/exec ./yt-dlp: permission denied"), false, ytdlpProbeBroken},
	}
	for _, c := range cases {
		if got := classifyYtDlpProbe(c.stderr, c.err, c.timedOut); got != c.want {
			t.Errorf("%s: classify = %v, want %v", c.name, got, c.want)
		}
	}
}

// Leftover downloads and Windows ".old" copies go; the backup, the binary and a
// download still in flight stay.
func TestYtDlpSweepStale(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name string, age time.Duration) string {
		path := dir + string(os.PathSeparator) + name
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-age)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		return path
	}
	oldPart := write(".yt-dlp-111.part", 3*time.Hour)
	freshPart := write(".yt-dlp-222.part", time.Minute)
	oldCopy := write("yt-dlp.exe.old-123", time.Minute)
	legacyOld := write("yt-dlp.exe.old", time.Minute)
	keepBackup := write("yt-dlp.exe.bak", 3*time.Hour)
	keepBinary := write("yt-dlp.exe", 3*time.Hour)
	ytdlpSweepStale(dir, "yt-dlp.exe", now)
	for _, gone := range []string{oldPart, oldCopy, legacyOld} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", gone)
		}
	}
	for _, kept := range []string{freshPart, keepBackup, keepBinary} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s should have been kept: %v", kept, err)
		}
	}
}

// The install card's "press A" does not belong in an updater toast.
func TestYtDlpToastReason(t *testing.T) {
	if got := ytdlpToastReason(errors.New("SD card is full")); strings.Contains(got, "press A") {
		t.Errorf("toast reason %q still says press A", got)
	}
}

func TestPrependPathEnv(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := prependPathEnv([]string{"HOME=/root", "PATH=/usr/bin" + sep + "/bin"}, "/mnt/SDCARD/tools")
	if got[1] != "PATH=/mnt/SDCARD/tools"+sep+"/usr/bin"+sep+"/bin" || got[0] != "HOME=/root" {
		t.Errorf("prepend: %q", got)
	}
	// Windows spells it Path; the case is kept.
	got = prependPathEnv([]string{"Path=C:\\Windows"}, "D:\\tools")
	if got[0] != "Path=D:\\tools"+sep+"C:\\Windows" {
		t.Errorf("case-insensitive: %q", got)
	}
	// Already first: unchanged, never doubled.
	got = prependPathEnv([]string{"PATH=/tools" + sep + "/bin"}, "/tools")
	if got[0] != "PATH=/tools"+sep+"/bin" {
		t.Errorf("already first: %q", got)
	}
	got = prependPathEnv([]string{"HOME=/root"}, "/tools")
	if len(got) != 2 || got[1] != "PATH=/tools" {
		t.Errorf("no PATH: %q", got)
	}
	if got := prependPathEnv([]string{"PATH=/bin"}, ""); got[0] != "PATH=/bin" {
		t.Errorf("empty dir: %q", got)
	}
}

// android_vr formats are refused by the CDN since 2026-08-17: the first
// attempt must not ask for that client, and android must be asked first.
func TestStreamAttemptsOrder(t *testing.T) {
	attempts := streamAttempts(720)
	if len(attempts) == 0 {
		t.Fatal("no attempts")
	}
	first := strings.Join(attempts[0].args, " ")
	if !strings.Contains(first, "player_client=android") || strings.Contains(first, "android_vr") {
		t.Errorf("first attempt %q", first)
	}
	// No attempt may ask for android_vr: yt-dlp would print its format 18
	// without fetching it, and the refused URL then failed the probe.
	for _, a := range attempts {
		if args := strings.Join(a.args, " "); strings.Contains(args, "android_vr") {
			t.Errorf("attempt %q still asks for android_vr: %q", a.name, args)
		}
	}
	if strings.Contains(ytDlpDownloadClients, "android_vr") || strings.Contains(ytDlpDownloadClients, ";youtube:") {
		t.Errorf("download clients %q", ytDlpDownloadClients)
	}
}

func TestParseYtOEmbed(t *testing.T) {
	title, channel, ok := parseYtOEmbed([]byte(`{"title":"  Never  Gonna Give You Up ","author_name":"Rick Astley","type":"video"}`))
	if !ok || title != "Never Gonna Give You Up" || channel != "Rick Astley" {
		t.Errorf("parse = %q, %q, %v", title, channel, ok)
	}
	if _, _, ok := parseYtOEmbed([]byte(`{"title":""}`)); ok {
		t.Error("an empty title is no title")
	}
	if _, _, ok := parseYtOEmbed([]byte(`Not Found`)); ok {
		t.Error("a non-JSON answer is no title")
	}
}

// A recent entry with no title is named; one that has a title keeps it.
func TestUpdateRecentTitle(t *testing.T) {
	favoritesMutex.Lock()
	saved := favoritesStore.Recent
	favoritesStore.Recent = []FavoriteItem{
		{Type: "video", Data: VideoInfo{URL: "https://www.youtube.com/watch?v=abcdefghijk"}},
		{Type: "video", Data: VideoInfo{URL: "https://www.youtube.com/watch?v=bbbbbbbbbbb", Title: "Kept"}},
	}
	favoritesMutex.Unlock()
	defer func() {
		favoritesMutex.Lock()
		favoritesStore.Recent = saved
		favoritesMutex.Unlock()
	}()
	if !updateRecentTitle("https://www.youtube.com/watch?v=abcdefghijk", "Found", "Channel") {
		t.Fatal("the untitled entry was not named")
	}
	if updateRecentTitle("https://www.youtube.com/watch?v=bbbbbbbbbbb", "Other", "") {
		t.Error("a title was overwritten")
	}
	favoritesMutex.Lock()
	v, _ := favoritesStore.Recent[0].Data.(VideoInfo)
	favoritesMutex.Unlock()
	if v.Title != "Found" || v.Channel != "Channel" {
		t.Errorf("entry = %+v", v)
	}
}

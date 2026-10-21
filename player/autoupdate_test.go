package main

// autoupdate_test.go covers the release-stamp parsing and the staged-file
// planning used by the automatic updater. Everything runs on temporary
// directories; no network and no real installation is touched.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReleaseTimeFromTag(t *testing.T) {
	got, ok := releaseTimeFromTag("JukaHub-20261017120000")
	if !ok {
		t.Fatal("expected the workflow tag to parse")
	}
	want := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("releaseTimeFromTag = %s, want %s", got, want)
	}
	for _, bad := range []string{"", "v0.4.0", "JukaHub-latest", "JukaHub-2026"} {
		if _, ok := releaseTimeFromTag(bad); ok {
			t.Errorf("tag %q must not parse as a release time", bad)
		}
	}
}

func TestReleasePublishedTimeAndLabel(t *testing.T) {
	rel := &githubRelease{TagName: "JukaHub-20260101000000", PublishedAt: "2026-10-17T09:30:00Z"}
	if label := releaseDateLabel(rel); label != "2026-10-17" {
		t.Fatalf("releaseDateLabel = %q", label)
	}
	if rel.PublishedAt != "" && releaseStampISO(rel) != "2026-10-17T09:30:00Z" {
		t.Fatalf("releaseStampISO = %q", releaseStampISO(rel))
	}
	// published_at wins over the tag timestamp.
	if ts, ok := releasePublishedTime(rel); !ok || ts.Hour() != 9 {
		t.Fatalf("releasePublishedTime = %s, %v", ts, ok)
	}
	// Without published_at the tag supplies the date.
	rel2 := &githubRelease{TagName: "JukaHub-20261017120000"}
	if label := releaseDateLabel(rel2); label != "2026-10-17" {
		t.Fatalf("tag-only releaseDateLabel = %q", label)
	}
	// Unknown metadata is never a valid release time.
	if _, ok := releasePublishedTime(&githubRelease{TagName: "nonsense"}); ok {
		t.Error("garbage tag must not yield a release time")
	}
}

func TestInstalledBuildDateAndNewerRelease(t *testing.T) {
	old := buildDate
	buildDate = "2026-10-17"
	defer func() { buildDate = old }()

	if got := installedBuildDate(); got != "2026-10-17" {
		t.Fatalf("installedBuildDate = %q, want the build stamp", got)
	}
	if releaseIsNewerThanInstalled(&githubRelease{TagName: "JukaHub-20261017090000"}) {
		t.Error("a same-day release before the build must not be newer")
	}
	if !releaseIsNewerThanInstalled(&githubRelease{TagName: "JukaHub-20261018120000"}) {
		t.Error("a later release must be newer")
	}
	if releaseIsNewerThanInstalled(&githubRelease{TagName: "not-a-release"}) {
		t.Error("a release without a timestamp must never trigger an update")
	}
}

// A compile stamp with a time is compared at full precision, so a release
// published later on the same day is still offered.
func TestBuildTimeStampedUpdateDecision(t *testing.T) {
	oldTime, oldDate := buildTime, buildDate
	buildTime, buildDate = "2026-10-17T09:00:00Z", "2026-10-17"
	defer func() { buildTime, buildDate = oldTime, oldDate }()

	after := &githubRelease{TagName: "JukaHub-20261017120000", PublishedAt: "2026-10-17T12:00:00Z"}
	if !releaseIsNewerThanInstalled(after) {
		t.Fatal("a release published after the compile time must be newer")
	}
	if got := installedBuildTimeLabel(); got != "2026-10-17 09:00 UTC" {
		t.Fatalf("installedBuildTimeLabel = %q, want the compile time", got)
	}

	buildTime = "2026-10-17T23:00:00Z"
	if releaseIsNewerThanInstalled(after) {
		t.Fatal("a release published before the compile time must not be newer")
	}
}

// The automatic check only advertises a release: it must not download, stage
// or replace anything before the user confirms in the Patch scene.
func TestAvailableUpdateIsAdvertisedWithoutStaging(t *testing.T) {
	patchStateMu.Lock()
	prev := *patchState
	patchStateMu.Unlock()
	t.Cleanup(func() {
		patchStateMu.Lock()
		*patchState = prev
		patchStateMu.Unlock()
	})

	rel := &githubRelease{TagName: "JukaHub-20261018120000", PublishedAt: "2026-10-18T12:00:00Z"}
	markUpdateAvailable(rel)

	if got := availableUpdateTag(); got != rel.TagName {
		t.Fatalf("availableUpdateTag = %q, want %q", got, rel.TagName)
	}
	if got := availableUpdateLabel(); got != "2026-10-18" {
		t.Fatalf("availableUpdateLabel = %q", got)
	}
	patchStateMu.Lock()
	staged := patchState.LastUpdateVer
	patchStateMu.Unlock()
	if staged != "" {
		t.Fatalf("an available release was staged without confirmation (%q)", staged)
	}

	found := false
	for _, a := range patchActions() {
		if a.ID == "install" {
			found = true
		}
	}
	if !found {
		t.Fatal("Patch does not offer an install action for the advertised release")
	}

	clearAvailableUpdate()
	if availableUpdateTag() != "" {
		t.Fatal("clearAvailableUpdate left the tag behind")
	}
	for _, a := range patchActions() {
		if a.ID == "install" {
			t.Fatal("install action still offered after the release was cleared")
		}
	}
}

// The build scripts must stamp the compile time, otherwise a local or release
// build falls back to a date-only stamp and hides same-day releases.
func TestBuildScriptsStampCompileTime(t *testing.T) {
	for _, name := range []string{"build.bat", "release.sh"} {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(data)
		if !strings.Contains(body, "main.buildTime") {
			t.Errorf("%s does not stamp main.buildTime", name)
		}
	}
}

func TestFormatBuildLabel(t *testing.T) {
	if got := formatBuildLabel("v0.4.0", "2026-10-17"); got != "v0.4.0 · 2026-10-17" {
		t.Fatalf("formatBuildLabel = %q", got)
	}
	if got := formatBuildLabel("v0.4.0", ""); got != "v0.4.0" {
		t.Fatalf("formatBuildLabel without date = %q", got)
	}
}

func TestStagedInstallFilesOrdersReleaseStampLast(t *testing.T) {
	root := t.TempDir()
	exe := "JukaHub"
	if runtime.GOOS == "windows" {
		exe = "JukaHub.exe"
	}
	for _, name := range []string{exe, "Inter-Regular.ttf", releaseInfoFileName} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dest := t.TempDir()
	files, err := stagedInstallFiles(root, dest)
	if err != nil {
		t.Fatalf("stagedInstallFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3", len(files))
	}
	if files[len(files)-1].Name != releaseInfoFileName {
		t.Fatalf("release stamp must be copied last, got order %v", files)
	}
	for _, f := range files {
		if filepath.Dir(f.Dest) != dest {
			t.Errorf("destination %q is outside the executable directory", f.Dest)
		}
	}
}

func TestStagedInstallFilesRejectsEmptyTree(t *testing.T) {
	if _, err := stagedInstallFiles(t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("an empty staged tree must be rejected")
	}
}

// TestAppUpdateKeepsInstalledTools is the updater contract for required/: an
// app update may ship a placeholder tool folder, but the tools already on the
// device (yt-dlp, ffmpeg, ...) must survive the swap untouched.
func TestAppUpdateKeepsInstalledTools(t *testing.T) {
	exe := "JukaHub"
	if runtime.GOOS == "windows" {
		exe = "JukaHub.exe"
	}
	staged := t.TempDir()
	install := t.TempDir()

	write := func(dir, rel, body string, mode os.FileMode) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}

	// The release carries its own copies of the tools (a placeholder set that
	// must not replace what the device already downloaded).
	write(staged, exe, "new-binary", 0o755)
	write(staged, releaseInfoFileName, `{"tag":"JukaHub-20261018120000"}`, 0o644)
	write(staged, "required/yt-dlp", "archive-ytdlp", 0o755)
	write(staged, "required/ffmpeg", "archive-ffmpeg", 0o755)

	write(install, exe, "old-binary", 0o755)
	write(install, "required/yt-dlp", "installed-ytdlp", 0o755)
	write(install, "required/ffmpeg", "installed-ffmpeg", 0o755)
	write(install, "required/local-tool", "installed-local", 0o755)

	files, err := stagedInstallFiles(staged, install)
	if err != nil {
		t.Fatalf("stagedInstallFiles: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("planned %d files, want the binary and the release stamp: %v", len(files), files)
	}
	for _, f := range files {
		if isPreservedInstallPath(f.Name) {
			t.Fatalf("update plans to overwrite %q inside %s/", f.Name, preservedInstallDir)
		}
	}

	// Run the real swap helper (minus its relaunch) so the test covers the
	// same copy path the device uses.
	journal := filepath.Join(t.TempDir(), "journal.json")
	if err := os.WriteFile(journal, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "apply-update")
	if runtime.GOOS == "windows" {
		script += ".bat"
	}
	if err := writeSwapScript(script, filepath.Join(install, exe), files, journal, 0); err != nil {
		t.Fatalf("writeSwapScript: %v", err)
	}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	sep := "\n"
	if runtime.GOOS == "windows" {
		sep = "\r\n"
	}
	// Neutralise the relaunch instead of deleting its lines: the POSIX helper
	// wraps it in an if/fi, so dropping lines would leave a broken script.
	// `true` swallows the arguments, and `rem` comments out the .bat command.
	var kept []string
	for _, line := range strings.Split(string(data), sep) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "start ") ||
			trimmed == "sleep 2" || strings.HasPrefix(trimmed, "timeout /t 2") {
			continue
		}
		line = strings.ReplaceAll(line, "nohup ", "true ")
		kept = append(kept, line)
	}
	if err := os.WriteFile(script, []byte(strings.Join(kept, sep)), 0o700); err != nil {
		t.Fatal(err)
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", script)
	} else if sh, lookErr := exec.LookPath("sh"); lookErr == nil {
		cmd = exec.Command(sh, script)
	} else {
		cmd = exec.Command(script)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("swap helper failed: %v\n%s", err, out)
	}

	for rel, want := range map[string]string{
		exe:                   "new-binary",
		"required/yt-dlp":     "installed-ytdlp",
		"required/ffmpeg":     "installed-ffmpeg",
		"required/local-tool": "installed-local",
		releaseInfoFileName:   `{"tag":"JukaHub-20261018120000"}`,
	} {
		got, err := os.ReadFile(filepath.Join(install, filepath.FromSlash(rel)))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", rel, got, err, want)
		}
	}
}

// TestSwapScriptReplacesFiles executes the generated Windows helper against a
// throwaway installation. The relaunch line is removed so the test never
// starts a real JukaHub process.
func TestSwapScriptReplacesFiles(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the generated helper is a .bat on Windows")
	}
	dir := t.TempDir()
	staged := filepath.Join(dir, "staged")
	install := filepath.Join(dir, "install")
	for _, d := range []string{staged, install} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(staged, "JukaHub.exe", "new-binary")
	write(staged, "SDL2.dll", "new-dll")
	write(staged, releaseInfoFileName, `{"tag":"JukaHub-20261017120000"}`)
	write(install, "JukaHub.exe", "old-binary")
	write(install, "SDL2.dll", "old-dll")

	journal := filepath.Join(dir, "journal.json")
	write(dir, "journal.json", "{}")

	files, err := stagedInstallFiles(staged, install)
	if err != nil {
		t.Fatalf("stagedInstallFiles: %v", err)
	}
	script := filepath.Join(dir, "apply-update.bat")
	if err := writeSwapScript(script, filepath.Join(install, "JukaHub.exe"), files, journal, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	// Drop the relaunch line only.
	var kept []string
	for _, line := range strings.Split(string(data), "\r\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "start ") {
			continue
		}
		kept = append(kept, line)
	}
	if err := os.WriteFile(script, []byte(strings.Join(kept, "\r\n")), 0o700); err != nil {
		t.Fatal(err)
	}

	if out, err := exec.Command("cmd", "/c", script).CombinedOutput(); err != nil {
		t.Fatalf("swap helper failed: %v\n%s", err, out)
	}
	for name, want := range map[string]string{
		"JukaHub.exe":       "new-binary",
		"SDL2.dll":          "new-dll",
		releaseInfoFileName: `{"tag":"JukaHub-20261017120000"}`,
	} {
		data, err := os.ReadFile(filepath.Join(install, name))
		if err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Error("journal must be removed after a successful swap")
	}
}

func TestWriteSwapScriptIncludesEveryFile(t *testing.T) {
	dir := t.TempDir()
	files := []stagedInstallFile{
		{Src: filepath.Join(dir, "a.txt"), Name: "a.txt", Dest: filepath.Join(dir, "out", "a.txt")},
		{Src: filepath.Join(dir, "JukaHub"), Name: "JukaHub", Dest: filepath.Join(dir, "out", "JukaHub")},
	}
	script := filepath.Join(dir, "apply-update")
	if err := writeSwapScript(script, filepath.Join(dir, "out", "JukaHub"), files, filepath.Join(dir, "journal.json"), 0); err != nil {
		t.Fatalf("writeSwapScript: %v", err)
	}
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{"a.txt", "JukaHub", "journal.json"} {
		if !strings.Contains(body, want) {
			t.Errorf("swap script does not mention %q", want)
		}
	}
	if runtime.GOOS != "windows" && !strings.HasPrefix(body, "#!/bin/sh") {
		t.Error("POSIX helper must be a shell script")
	}
}

// TestAutoUpdateCheckDue pins the throttle: only a recent, sane, past stamp
// may suppress a check, so a device with a reset clock still checks.
func TestAutoUpdateCheckDue(t *testing.T) {
	now := time.Date(2026, 10, 18, 16, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		last string
		want bool
	}{
		{"missing stamp checks", "", true},
		{"garbled stamp checks", "not-a-time", true},
		{"recent past stamp suppresses", now.Add(-10 * time.Minute).Format(time.RFC3339), false},
		{"stale past stamp checks", now.Add(-2 * time.Hour).Format(time.RFC3339), true},
		{"future stamp (clock rolled back) checks", now.Add(3 * time.Hour).Format(time.RFC3339), true},
		{"pre-sane clock stamp checks", "2001-01-01T00:00:00Z", true},
	}
	for _, tc := range cases {
		if got := autoUpdateCheckDue(tc.last, now); got != tc.want {
			t.Errorf("%s: autoUpdateCheckDue(%q) = %v, want %v", tc.name, tc.last, got, tc.want)
		}
	}
}

// TestRunAutoUpdateAttempts pins the retry: a failed lookup is tried again
// after the delay, a success stops the loop, and the Patch lock is never
// retried because waiting cannot release it.
func TestRunAutoUpdateAttempts(t *testing.T) {
	t.Run("retries then succeeds", func(t *testing.T) {
		calls := 0
		var slept []time.Duration
		used, err := runAutoUpdateAttempts(func() error {
			calls++
			if calls == 1 {
				return errors.New("no route to host")
			}
			return nil
		}, 2, func(d time.Duration) { slept = append(slept, d) }, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if used != 2 || calls != 2 {
			t.Fatalf("used %d attempts, check ran %d times; want 2 and 2", used, calls)
		}
		if len(slept) != 1 || slept[0] != autoUpdateRetryDelay {
			t.Fatalf("slept %v, want one pause of %v", slept, autoUpdateRetryDelay)
		}
	})

	t.Run("gives up after the budget", func(t *testing.T) {
		calls := 0
		used, err := runAutoUpdateAttempts(func() error {
			calls++
			return errors.New("offline")
		}, 3, func(time.Duration) {}, nil)
		if err == nil {
			t.Fatal("expected the last error to be returned")
		}
		if used != 3 || calls != 3 {
			t.Fatalf("used %d attempts, check ran %d times; want 3 and 3", used, calls)
		}
	})

	t.Run("busy lock is not retried", func(t *testing.T) {
		calls := 0
		sleeps := 0
		used, err := runAutoUpdateAttempts(func() error {
			calls++
			return errAutoUpdateInProgress
		}, 3, func(time.Duration) { sleeps++ }, nil)
		if !errors.Is(err, errAutoUpdateInProgress) {
			t.Fatalf("error = %v, want errAutoUpdateInProgress", err)
		}
		if used != 1 || calls != 1 || sleeps != 0 {
			t.Fatalf("used %d, calls %d, sleeps %d; want 1, 1, 0", used, calls, sleeps)
		}
	})
}

// TestReleaseAssetDigest pins the release archive checksum: only a well-formed
// sha256 digest is used, and anything else leaves the download unverified
// instead of rejecting a release the API described differently.
func TestReleaseAssetDigest(t *testing.T) {
	sum := "48ed6fb51883a9aa82c3875a0e87744354bdfd4d8cc6de80004d28d3ded0fa24"
	cases := []struct {
		name, digest, want string
	}{
		{"sha256 is used", "sha256:" + sum, sum},
		{"upper-case hex is normalised", "SHA256:" + strings.ToUpper(sum), sum},
		{"surrounding space is ignored", "  sha256:" + sum + "\n", sum},
		{"missing digest", "", ""},
		{"another algorithm", "sha512:" + sum, ""},
		{"truncated hash", "sha256:abc123", ""},
		{"non-hex hash", "sha256:" + sum[:63] + "z", ""},
		{"bare hash without algorithm", sum, ""},
	}
	for _, tc := range cases {
		if got := releaseAssetDigest(tc.digest); got != tc.want {
			t.Errorf("%s: releaseAssetDigest(%q) = %q, want %q", tc.name, tc.digest, got, tc.want)
		}
	}
}

// TestAnnounceAvailableUpdateRepeatsEveryBoot covers the reported symptom: a
// pending release must be offered again on later boots, through both the toast
// and the notification center that keeps it visible after the toast fades.
func TestAnnounceAvailableUpdateRepeatsEveryBoot(t *testing.T) {
	patchStateMu.Lock()
	prevTag, prevAt := patchState.AvailableUpdateTag, patchState.AvailableUpdateAt
	patchState.AvailableUpdateTag = "JukaHub-20261018154556"
	patchState.AvailableUpdateAt = "2026-10-18T15:45:57Z"
	patchStateMu.Unlock()
	t.Cleanup(func() {
		patchStateMu.Lock()
		patchState.AvailableUpdateTag, patchState.AvailableUpdateAt = prevTag, prevAt
		patchStateMu.Unlock()
	})

	prevHistory := nc.history
	nc.history = nil
	t.Cleanup(func() { nc.history = prevHistory })

	announceAvailableUpdate()
	if len(nc.history) != 1 {
		t.Fatalf("first announcement pushed %d notifications, want 1", len(nc.history))
	}
	msg := nc.history[0].Message
	if !strings.Contains(msg, "2026-10-18") {
		t.Errorf("offer %q lacks the release date", msg)
	}
	if !strings.Contains(msg, "download icon") {
		t.Errorf("offer %q does not tell the user where to install it", msg)
	}

	announceAvailableUpdate()
	// An unread offer for the same release is bumped instead of duplicated, so
	// the bell keeps one entry that still points at the same install.
	if len(nc.history) != 1 {
		t.Fatalf("second boot left %d notifications, want 1 deduped offer", len(nc.history))
	}
	if nc.history[0].Repeat != 2 {
		t.Fatalf("second boot left Repeat=%d, want 2", nc.history[0].Repeat)
	}
}

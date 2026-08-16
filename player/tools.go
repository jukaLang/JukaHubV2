package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ensureRequiredTools downloads yt-dlp and ffplay into the configured
// tools_path (defaults to ./required) if they are not already present.
// It is OS-aware: Windows gets .exe binaries/archives, Linux gets the
// appropriate builds for TrimuiSmartPro (arm64). Failures are logged but
// non-fatal so the app still launches (it can fall back to system PATH).
func ensureRequiredTools(config *Config) {
	requiredDir := "./required"
	if config.Variables.ToolsPath != "" {
		requiredDir = config.Variables.ToolsPath
	}
	if err := os.MkdirAll(requiredDir, 0o755); err != nil {
		log.Printf("[TOOLS] Could not create tools dir %s: %v", requiredDir, err)
		return
	}

	type spec struct {
		archive    bool
		directURL  string
		archiveURL string
	}
	specs := map[string]spec{}

	// yt-dlp: standalone binary for each OS
	ytURL := "https://github.com/yt-dlp/yt-dlp/releases/latest/download/yt-dlp"
	if runtime.GOOS == "windows" {
		ytURL += ".exe"
	}
	specs["yt-dlp"] = spec{directURL: ytURL}

	// ffplay: shipped inside an ffmpeg archive per OS.
	// Windows uses a fully static build so no VC++/UCRT DLLs are needed at runtime.
	if runtime.GOOS == "windows" {
		specs["ffplay"] = spec{archive: true, archiveURL: "STATIC_FFMPEG"}
	} else {
		spec := spec{archive: true, archiveURL: "https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-arm64-static.tar.xz"}
		specs["ffplay"] = spec
	}

	for tool, s := range specs {
		target := filepath.Join(requiredDir, exeNameFor(tool))
		// ffplay ships inside an archive: only consider it "present" if the
		// static-build marker also exists, otherwise re-download (this heals a
		// previously extracted non-static build that failed with 0xc0000135).
		needDownload := false
		if _, err := os.Stat(target); err != nil {
			needDownload = true
		} else if tool == "ffplay" {
			if _, err := os.Stat(filepath.Join(requiredDir, ".ffmpeg-static")); err != nil {
				needDownload = true
			}
		}
		if !needDownload {
			log.Printf("[TOOLS] %s already present at %s", tool, target)
			continue
		}
		log.Printf("[TOOLS] %s not found at %s, attempting download...", tool, target)
		if s.archive {
			if err := downloadToolArchive(tool, s.archiveURL, requiredDir); err != nil {
				log.Printf("[TOOLS] Failed to download %s: %v", tool, err)
				continue
			}
		} else {
			if err := downloadFile(s.directURL, target); err != nil {
				log.Printf("[TOOLS] Failed to download %s: %v", tool, err)
				continue
			}
			if runtime.GOOS != "windows" {
				_ = os.Chmod(target, 0o755)
			}
		}
		if _, err := os.Stat(target); err == nil {
			log.Printf("[TOOLS] %s ready at %s", tool, target)
			if tool == "ffplay" {
				_ = os.WriteFile(filepath.Join(requiredDir, ".ffmpeg-static"), []byte("btbn-static"), 0o644)
			}
		} else {
			log.Printf("[TOOLS] %s still missing after download attempt (will try system PATH)", tool)
		}
	}
}

// exeNameFor returns the platform-specific executable name for a tool.
func exeNameFor(tool string) string {
	if runtime.GOOS == "windows" {
		return tool + ".exe"
	}
	return tool
}

// findMPVPath returns the path to the mpv binary, searching common install
// locations and falling back to system PATH. Returns empty string if not found.
func findMPVPath() string {
	candidates := []string{}
	if runtime.GOOS == "windows" {
		candidates = []string{
			filepath.Join(".", "required", "mpv.exe"),
			`C:\Program Files\mpv\mpv.exe`,
			`C:\Program Files (x86)\mpv\mpv.exe`,
			"mpv.exe",
		}
	} else {
		candidates = []string{
			filepath.Join(".", "required", "mpv"),
			"/usr/bin/mpv",
			"/usr/local/bin/mpv",
			"/bin/mpv",
			"mpv",
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if p, err := exec.LookPath("mpv"); err == nil {
		return p
	}
	return ""
}

// downloadFile downloads url to dest following redirects.
func downloadFile(url, dest string) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %d", url, resp.StatusCode)
	}

	if _, err := io.Copy(out, resp.Body); err != nil {
		return err
	}
	return nil
}

// downloadToolArchive downloads an ffmpeg archive and extracts only the
// ffplay binary (plus its Windows DLLs) into requiredDir.
func downloadToolArchive(tool, url, requiredDir string) error {
	if url == "STATIC_FFMPEG" {
		return downloadStaticFFmpeg(requiredDir)
	}
	tmp, err := os.MkdirTemp("", "jukatool-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	archivePath := filepath.Join(tmp, "archive")
	if err := downloadFile(url, archivePath); err != nil {
		return err
	}

	if runtime.GOOS == "windows" {
		return extractFFmpegZip(archivePath, requiredDir)
	}
	return extractFFmpegTarXz(archivePath, requiredDir)
}

// extractFFmpegZip pulls ffplay.exe and all .dll files from a Windows
// ffmpeg zip build into requiredDir.
func extractFFmpegZip(archivePath, requiredDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()
	found := false
	for _, f := range r.File {
		base := filepath.Base(f.Name)
		lower := strings.ToLower(base)
		if lower == "ffplay.exe" || strings.HasSuffix(lower, ".dll") {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			dest := filepath.Join(requiredDir, base)
			out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
			if err != nil {
				rc.Close()
				return err
			}
			_, err = io.Copy(out, rc)
			out.Close()
			rc.Close()
			if err != nil {
				return err
			}
			log.Printf("[TOOLS] extracted %s", base)
			if lower == "ffplay.exe" {
				found = true
			}
		}
	}
	if !found {
		return fmt.Errorf("ffplay.exe not found in archive")
	}
	return nil
}

// extractFFmpegTarXz extracts a static ffmpeg tar.xz (Linux) and copies the
// ffplay binary into requiredDir.
func extractFFmpegTarXz(archivePath, requiredDir string) error {
	tmpDir := filepath.Dir(archivePath)
	cmd := exec.Command("tar", "-xf", archivePath, "-C", tmpDir)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tar extract failed: %w", err)
	}

	var found string
	filepath.Walk(tmpDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if found == "" && filepath.Base(p) == "ffplay" {
			found = p
		}
		return nil
	})
	if found == "" {
		return fmt.Errorf("ffplay not found in extracted archive")
	}

	data, err := os.ReadFile(found)
	if err != nil {
		return err
	}
	dest := filepath.Join(requiredDir, "ffplay")
	if err := os.WriteFile(dest, data, 0o755); err != nil {
		return err
	}
	log.Printf("[TOOLS] extracted ffplay from archive")
	return nil
}

// downloadStaticFFmpeg fetches a fully static Windows ffmpeg build that
// bundles ffplay.exe with no external (VC++/UCRT) DLL dependencies, then
// extracts the needed executables into requiredDir.
func downloadStaticFFmpeg(requiredDir string) error {
	assetURL, err := latestGitHubReleaseAsset("BtbN/FFmpeg-Builds", func(n string) bool {
		n = strings.ToLower(n)
		// BtbN's non-"shared" win64 builds are fully static (no external DLLs).
		return strings.Contains(n, "win64") &&
			(strings.Contains(n, "lgpl") || strings.Contains(n, "gpl")) &&
			!strings.Contains(n, "shared") &&
			strings.HasSuffix(n, ".zip")
	})
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "jukatool-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archivePath := filepath.Join(tmp, "ffmpeg-static.zip")
	log.Printf("[TOOLS] downloading static ffmpeg build...")
	if err := downloadFile(assetURL, archivePath); err != nil {
		return err
	}
	return extractFilesFromZip(archivePath, requiredDir, []string{"ffplay.exe", "ffmpeg.exe", "ffprobe.exe"})
}

// latestGitHubReleaseAsset resolves the download URL for the first asset of the
// given repo's latest GitHub release for which match(name) returns true.
func latestGitHubReleaseAsset(repo string, match func(string) bool) (string, error) {
	apiURL := "https://api.github.com/repos/" + repo + "/releases/latest"
	resp, err := http.Get(apiURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github api %s: unexpected status %d", apiURL, resp.StatusCode)
	}
	var rel struct {
		Assets []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", err
	}
	for _, a := range rel.Assets {
		if match(a.Name) {
			return a.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("no matching asset found in %s latest release", repo)
}

// extractFilesFromZip extracts any entries whose base name matches one of the
// wanted names (case-insensitive) into requiredDir, regardless of archive path.
func extractFilesFromZip(archivePath, requiredDir string, wants []string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()
	wantSet := make(map[string]bool, len(wants))
	for _, w := range wants {
		wantSet[strings.ToLower(w)] = true
	}
	found := 0
	for _, f := range r.File {
		base := filepath.Base(f.Name)
		if !wantSet[strings.ToLower(base)] {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		dest := filepath.Join(requiredDir, base)
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
		log.Printf("[TOOLS] extracted %s", base)
		found++
	}
	if found == 0 {
		return fmt.Errorf("none of %v found in archive", wants)
	}
	return nil
}

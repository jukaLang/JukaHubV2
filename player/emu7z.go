package main

// emu7z.go — unpacking the .7z archives some Windows emulators only ship as.
//
// RetroArch (the buildbot publishes RetroArch.7z or an installer, nothing
// else), PCSX2, mGBA and RPCS3 release their portable Windows builds as 7-Zip
// archives. Those use LZMA2 with the BCJ/BCJ2 executable filters, which the
// standard library cannot read and which are not worth re-implementing blind,
// so the archive goes to 7-Zip itself: the user's own 7z.exe when 7-Zip is
// installed, otherwise the official standalone 7zr.exe from 7-Zip's GitHub
// releases (ip7z/7zip, an allowlisted host), fetched once into the emulators
// folder and checked against the SHA-256 digest GitHub publishes for it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// emu7zRepo is 7-Zip's official GitHub repository.
	emu7zRepo = "ip7z/7zip"
	// emu7zAsset is the standalone console extractor every release carries.
	emu7zAsset = "7zr.exe"
	// emu7zMaxBytes caps that download; the real file is about 600 KB.
	emu7zMaxBytes = 8 << 20
)

// emu7zInstalled finds a 7-Zip executable that is already on the machine: an
// installed 7-Zip, one on PATH, or the 7zr.exe an earlier install fetched.
func emu7zInstalled() string {
	for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
		if dir := strings.TrimSpace(os.Getenv(env)); dir != "" {
			if candidate := filepath.Join(dir, "7-Zip", "7z.exe"); regularFile(candidate) {
				return candidate
			}
		}
	}
	for _, name := range []string{"7z.exe", "7za.exe", "7zr.exe"} {
		if found, err := exec.LookPath(name); err == nil && found != "" {
			return found
		}
	}
	for _, root := range emuWinExistingRoots() {
		if candidate := filepath.Join(root, ".tools", emu7zAsset); regularFile(candidate) {
			return candidate
		}
	}
	return ""
}

// emu7zTool returns a 7-Zip executable, fetching the official 7zr.exe once
// when the machine has none.
func emu7zTool(ctx context.Context, client *http.Client) (string, error) {
	if found := emu7zInstalled(); found != "" {
		return found, nil
	}
	if !IsWindows() {
		return "", errors.New("7z archives are only unpacked on Windows")
	}
	dir, err := emuWinToolsDir()
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dir, emu7zAsset)
	data, err := fetchUpdateBytes(ctx, client, fmt.Sprintf("%s/repos/%s/releases/latest", standaloneAPIBase, emu7zRepo), patchMaxMetadata)
	if err != nil {
		return "", err
	}
	var rel emuGitHubRelease
	if err := json.Unmarshal(data, &rel); err != nil {
		return "", errors.New("unreadable 7-Zip release metadata")
	}
	for _, asset := range rel.Assets {
		if !strings.EqualFold(strings.TrimSpace(asset.Name), emu7zAsset) {
			continue
		}
		sum, err := emuDownload(ctx, client, asset.BrowserDownloadURL, dest, emu7zMaxBytes, nil)
		if err != nil {
			return "", err
		}
		if err := emuDigestMatches(sum, asset.Digest); err != nil {
			_ = os.Remove(dest)
			return "", err
		}
		return dest, nil
	}
	return "", fmt.Errorf("the 7-Zip release %s has no %s", rel.TagName, emu7zAsset)
}

// emu7zFailure picks the line of 7-Zip's output worth showing: its first ERROR
// line, or the last line it printed.
func emu7zFailure(output string) string {
	last := ""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(strings.ToUpper(line), "ERROR") {
			return line
		}
		last = line
	}
	return last
}

// emu7zExtract unpacks archive into dest with 7-Zip ("x" keeps the folder
// structure; -y answers every prompt; -bd drops the progress meter a hidden
// console cannot show). 7-Zip refuses absolute and parent-relative entry paths
// on its own. Exit code 1 is 7-Zip's "warning" — the files were written — so
// it is accepted; the caller still checks the emulator's executable is there.
func emu7zExtract(ctx context.Context, client *http.Client, archive, dest string) error {
	tool, err := emu7zTool(ctx, client)
	if err != nil {
		return fmt.Errorf("7-Zip is needed to unpack %s: %w", filepath.Base(archive), err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, tool, "x", "-y", "-bd", "-o"+dest, archive)
	emuHideConsole(cmd)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil
	}
	detail := emu7zFailure(string(out))
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("%w (7-Zip: %s)", errBadToolArchive, detail)
}

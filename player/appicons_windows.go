//go:build windows

package main

// appicons_windows.go extracts the icon Windows itself shows for a Start Menu
// shortcut or an .exe and caches it as a PNG next to JukaHub's other caches.
//
// Go cannot read a shortcut's icon (it lives in the shell's icon cache and in
// the target's resources), so the work is handed to PowerShell: one process per
// batch, not per app, writes every missing icon in a couple of seconds. The tile
// shows the app's initial until the picture lands, and nothing here is on the
// render path.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// appIconExtractBatch caps one PowerShell run. A huge Start Menu should not
	// turn into a minute of background work on a handheld.
	appIconExtractBatch = 150
	// appIconExtractTimeout bounds the helper. Extraction of a full batch takes
	// a couple of seconds; this only catches a wedged shell.
	appIconExtractTimeout = 90 * time.Second
	// createNoWindow keeps the helper from flashing a console over the game UI.
	createNoWindow = 0x08000000
)

// appIconScript is the PowerShell helper. It resolves each path through the
// shell's own icon list (SHGetImageList) so icons come out at 48px or 256px
// instead of the 32px ExtractAssociatedIcon offers, and writes them as PNGs.
const appIconScript = `
param([string]$ListPath, [string]$OutDir)
Add-Type -AssemblyName System.Drawing
Add-Type -ReferencedAssemblies System.Drawing -TypeDefinition @"
using System;
using System.Drawing;
using System.Runtime.InteropServices;

public static class JukaHubIcons {
    [StructLayout(LayoutKind.Sequential, CharSet = CharSet.Auto)]
    private struct SHFILEINFO {
        public IntPtr hIcon;
        public int iIcon;
        public uint dwAttributes;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 260)] public string szDisplayName;
        [MarshalAs(UnmanagedType.ByValTStr, SizeConst = 80)] public string szTypeName;
    }

    [ComImport, Guid("46EB5926-582E-4017-9FDF-E8998DAA0950"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
    private interface IImageList {
        [PreserveSig] int Add(IntPtr hbmImage, IntPtr hbmMask, ref int pi);
        [PreserveSig] int ReplaceIcon(int i, IntPtr hicon, ref int pi);
        [PreserveSig] int SetOverlayImage(int iImage, int iOverlay);
        [PreserveSig] int Replace(int i, IntPtr hbmImage, IntPtr hbmMask);
        [PreserveSig] int AddMasked(IntPtr hbmImage, int crMask, ref int pi);
        [PreserveSig] int Draw(IntPtr pimldp);
        [PreserveSig] int Remove(int i);
        [PreserveSig] int GetIcon(int i, int flags, ref IntPtr picon);
    }

    [DllImport("shell32.dll", CharSet = CharSet.Auto)]
    private static extern IntPtr SHGetFileInfo(string pszPath, uint dwFileAttributes, ref SHFILEINFO psfi, uint cbFileInfo, uint uFlags);

    [DllImport("shell32.dll")]
    private static extern int SHGetImageList(int iImageList, ref Guid riid, out IImageList ppv);

    [DllImport("user32.dll")]
    private static extern bool DestroyIcon(IntPtr hIcon);

    public static bool Save(string path, string outFile) {
        SHFILEINFO info = new SHFILEINFO();
        const uint SHGFI_SYSICONINDEX = 0x4000;
        const uint FILE_ATTRIBUTE_NORMAL = 0x80;
        IntPtr res = SHGetFileInfo(path, FILE_ATTRIBUTE_NORMAL, ref info, (uint)Marshal.SizeOf(info), SHGFI_SYSICONINDEX);
        if (res == IntPtr.Zero || info.iIcon < 0) { return false; }

        Guid iid = new Guid("46EB5926-582E-4017-9FDF-E8998DAA0950");
        IImageList list;
        // SHIL_EXTRALARGE (48px), then SHIL_JUMBO (256px), then SHIL_LARGE (32px).
        int[] sizes = new int[] { 2, 4, 0 };
        foreach (int size in sizes) {
            if (SHGetImageList(size, ref iid, out list) != 0 || list == null) { continue; }
            IntPtr hIcon = IntPtr.Zero;
            if (list.GetIcon(info.iIcon, 1, ref hIcon) != 0 || hIcon == IntPtr.Zero) { continue; }
            try {
                using (Icon icon = Icon.FromHandle(hIcon)) {
                    using (Bitmap bmp = icon.ToBitmap()) {
                        if (bmp.Width < 24 || bmp.Height < 24) { continue; }
                        bmp.Save(outFile, System.Drawing.Imaging.ImageFormat.Png);
                        return true;
                    }
                }
            } finally {
                DestroyIcon(hIcon);
            }
        }
        return false;
    }
}
"@

$targets = Get-Content -LiteralPath $ListPath -Raw | ConvertFrom-Json
$ok = 0
foreach ($t in $targets) {
    $out = Join-Path $OutDir ($t.key + ".png")
    try {
        if ([JukaHubIcons]::Save($t.path, $out)) { $ok++ }
    } catch { }
}
Write-Output ("extracted " + $ok + "/" + $targets.Count)
`

var appIconExtractState struct {
	mu   sync.Mutex
	busy bool
	// tried remembers programs this session already asked about, so a Refresh
	// press does not start another helper for icons that cannot be extracted.
	tried map[string]bool
}

// defaultAppIconCacheDir is where extracted icons live: the per-user cache
// directory, falling back to the temp directory on a machine without a profile.
func defaultAppIconCacheDir() string {
	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "JukaHub", "appicons")
}

// appIconCacheDir is a variable so a test can point the cache at a temp folder.
var appIconCacheDir = defaultAppIconCacheDir

// appIconCacheFile is the cached PNG for one program. The key is a hash of the
// lower-cased path, so the file name is stable across runs and safe on disk.
func appIconCacheFile(programPath string) string {
	key := strings.ToLower(filepath.Clean(strings.TrimSpace(programPath)))
	if key == "" {
		return ""
	}
	sum := sha1.Sum([]byte(key))
	return filepath.Join(appIconCacheDir(), hex.EncodeToString(sum[:])+".png")
}

// extractedAppIcon returns the cached icon for a program when one exists.
func extractedAppIcon(programPath string) string {
	return extractedIconExists(appIconCacheFile(programPath))
}

// requestAppIconExtraction starts the background helper for the programs that
// still have no icon. It is safe to call on every scan: it returns immediately
// and only the first call for a given program does any work.
func requestAppIconExtraction(programs []string) {
	state := &appIconExtractState
	state.mu.Lock()
	if state.tried == nil {
		state.tried = map[string]bool{}
	}
	if state.busy {
		state.mu.Unlock()
		return
	}
	var missing []string
	for _, p := range programs {
		clean := strings.ToLower(filepath.Clean(strings.TrimSpace(p)))
		if clean == "" || state.tried[clean] {
			continue
		}
		switch strings.ToLower(filepath.Ext(clean)) {
		case ".lnk", ".exe", ".url":
		default:
			// A .bat/.sh launcher has no icon of its own to extract.
			state.tried[clean] = true
			continue
		}
		if extractedAppIcon(p) != "" {
			state.tried[clean] = true
			continue
		}
		state.tried[clean] = true
		missing = append(missing, p)
		if len(missing) >= appIconExtractBatch {
			break
		}
	}
	if len(missing) == 0 {
		state.mu.Unlock()
		return
	}
	state.busy = true
	state.mu.Unlock()

	go func() {
		defer func() {
			state.mu.Lock()
			state.busy = false
			state.mu.Unlock()
		}()
		extractAppIcons(missing)
	}()
}

// extractAppIcons runs the PowerShell helper once for a batch of programs.
func extractAppIcons(programs []string) {
	dir := appIconCacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[apps] cannot create the icon cache: %v", err)
		return
	}
	scriptPath := filepath.Join(dir, "extract-icons.ps1")
	if err := os.WriteFile(scriptPath, []byte(appIconScript), 0o644); err != nil {
		log.Printf("[apps] cannot write the icon helper: %v", err)
		return
	}
	type iconRequest struct {
		Key  string `json:"key"`
		Path string `json:"path"`
	}
	requests := make([]iconRequest, 0, len(programs))
	for _, p := range programs {
		cached := appIconCacheFile(p)
		if cached == "" {
			continue
		}
		requests = append(requests, iconRequest{
			Key:  strings.TrimSuffix(filepath.Base(cached), ".png"),
			Path: p,
		})
	}
	if len(requests) == 0 {
		return
	}
	listPath := filepath.Join(dir, "icons.json")
	data, err := json.Marshal(requests)
	if err != nil {
		return
	}
	if err := os.WriteFile(listPath, data, 0o644); err != nil {
		log.Printf("[apps] cannot write the icon list: %v", err)
		return
	}
	defer os.Remove(listPath)

	ctx, cancel := context.WithTimeout(context.Background(), appIconExtractTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", scriptPath, "-ListPath", listPath, "-OutDir", dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("[apps] icon extraction failed: %v (%s)", err, strings.TrimSpace(string(out)))
		return
	}
	log.Printf("[apps] icons ready: %s", strings.TrimSpace(string(out)))
	// The render path caches misses; forget them so the new pictures show up
	// on the next frame instead of on the next scan.
	dropAppIconPathCache()
}

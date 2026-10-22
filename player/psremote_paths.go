package main

// psremote_paths.go — where the native client lives on the SD card.
//
// The helpers ship inside JukaHub's own app folder (Apps/JukaHub/bin), so the
// client no longer depends on the TrimUI Chiaki-ng port being installed. The
// SD-root lookup, the Brick Pro runtime choice and the handoff environment are
// the only pieces this file keeps from the old integration: everything else it
// used was the Python app's installer.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// psRemoteSDRoot is overridable in tests. On a device the SD card is mounted at
// /mnt/SDCARD; the executable-relative fallback covers installs moved elsewhere.
var psRemoteSDRoot = "/mnt/SDCARD"

// psRemoteSDRootDir returns the SD-card root. Explicit configuration wins, then
// the real TrimUI mount, then the Apps/JukaHub layout next to the executable.
// An empty string means the platform cannot host the Linux ARM64 helpers.
func psRemoteSDRootDir() string {
	if root := strings.TrimSpace(os.Getenv("JUKAHUB_SD_ROOT")); root != "" {
		return root
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return ""
	}
	if st, err := os.Stat(psRemoteSDRoot); err == nil && st.IsDir() {
		return psRemoteSDRoot
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return psRemoteSDRoot
	}
	appDir := filepath.Dir(exe)
	if strings.EqualFold(filepath.Base(appDir), "JukaHub") {
		appsDir := filepath.Dir(appDir)
		if strings.EqualFold(filepath.Base(appsDir), "Apps") {
			return filepath.Dir(appsDir)
		}
	}
	return psRemoteSDRoot
}

// psRemoteSupported reports whether the native helpers can run at all.
func psRemoteSupported() bool {
	return runtime.GOOS == "linux" && runtime.GOARCH == "arm64"
}

// psRemoteUsesBrickRuntime reports whether the helpers need their bundled
// brick-stock runtime. The upstream port keys this off the device-tree model,
// not the marketing name, so do the same here.
func psRemoteUsesBrickRuntime() bool {
	model := strings.ToLower(readDeviceModelFile("/proc/device-tree/model"))
	if strings.Contains(model, "sun50iw10") {
		return true
	}
	return DetectDevice() == DeviceTrimuiBrickPro
}

// psShellQuote wraps a value so the generated handoff script treats it literally.
func psShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// psLaunchEnv is the environment the helpers get when JukaHub hands the display
// over. The Brick Pro is the one target whose native helpers need the bundled
// FFmpeg/OpenSSL runtime; the Smart Pro S uses the firmware's newer libraries.
func psLaunchEnv(root string, base []string) []string {
	env := append([]string{}, base...)
	libs := []string{
		filepath.Join(root, "libs"),
		root,
		"/usr/trimui/lib",
		"/usr/lib64",
		"/usr/lib",
		"/lib",
	}
	if psRemoteUsesBrickRuntime() {
		libs = append([]string{filepath.Join(root, "libs", "brick-stock")}, libs...)
	}
	if existing := strings.TrimSpace(os.Getenv("LD_LIBRARY_PATH")); existing != "" {
		libs = append(libs, existing)
	}
	sdRoot := filepath.Dir(filepath.Dir(root))
	env = append(env,
		"HOME="+root,
		"SDCARD_PATH="+sdRoot,
		"LD_LIBRARY_PATH="+strings.Join(libs, ":"),
		"PATH="+filepath.Join(root, "bin")+":"+filepath.Join(sdRoot, "System", "bin")+":"+os.Getenv("PATH"),
		"OPENSSL_CONF=/dev/null",
	)
	return env
}

// psHandoffEnv keeps only the variables the helper needs. The whole process
// environment is never written to disk: it can hold tokens.
func psHandoffEnv(root string) []string {
	allowed := map[string]bool{
		"HOME": true, "SDCARD_PATH": true, "LD_LIBRARY_PATH": true,
		"PATH": true, "OPENSSL_CONF": true,
	}
	var out []string
	for _, kv := range psLaunchEnv(root, nil) {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if allowed[name] {
			out = append(out, kv)
		}
	}
	return out
}

// psBinDir is where the release puts chiaki-stream and chiaki-regist.
func psBinDir() string {
	if sd := psRemoteSDRootDir(); sd != "" {
		return filepath.Join(sd, "Apps", "JukaHub", "bin")
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		return filepath.Join(filepath.Dir(exe), "bin")
	}
	return ""
}

// psRuntimeInstallHint is the one-line explanation the panel shows when the
// helpers are missing, so a user knows what to install instead of guessing.
func psRuntimeInstallHint() string {
	dir := psBinDir()
	if dir == "" {
		return "install the JukaHub-ng-chiaki build into Apps/JukaHub"
	}
	return "missing " + filepath.Join(dir, "chiaki-stream")
}

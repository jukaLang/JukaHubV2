package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Platform abstracts OS-specific behavior so the rest of the app never
// calls runtime.GOOS directly. Two implementations are provided:
//   - tsp: Trimui Smart Pro (Linux/ARM64)
//   - win: Windows (x64)
type Platform interface {
	// Name returns a short platform identifier for logs/config.
	Name() string

	// ExecutableDir returns the directory containing the running binary.
	ExecutableDir() (string, error)

	// DataDir returns the preferred writable directory for user data.
	// On Windows this uses SDL_GetPrefPath when available, otherwise %APPDATA%.
	// On TSP it returns the app directory next to the binary.
	DataDir() (string, error)

	// ConfigPath returns the full path to the main config file.
	ConfigPath() string

	// UserConfigPath returns the full path to the mutable user config.
	UserConfigPath() string

	// ToolsDir returns the directory where external tools (yt-dlp, mpv, etc.)
	// are expected to live.
	ToolsDir() string

	// RequiredDir returns the directory for bundled/extracted dependencies.
	RequiredDir() string

	// IsHandheld returns true for small-screen controller-first devices.
	IsHandheld() bool

	// FullscreenDefault returns whether the platform prefers fullscreen.
	FullscreenDefault() bool

	// ResizableDefault returns whether the platform prefers a resizable window.
	ResizableDefault() bool

	// LookPath returns the full path to an executable if it exists on PATH,
	// or an empty string if not found.
	LookPath(exe string) (string, error)

	// OpenURL opens a URL in the system browser (no-op on TSP).
	OpenURL(url string) error

	// OpenFile opens a file with the system default application (no-op on TSP).
	OpenFile(path string) error

	// Stat wraps os.Stat with platform-specific path handling.
	Stat(name string) (os.FileInfo, error)

	// ReadFile wraps os.ReadFile with platform-specific path handling.
	ReadFile(name string) ([]byte, error)

	// WriteFile wraps os.WriteFile with platform-specific path handling.
	WriteFile(name string, data []byte, perm os.FileMode) error

	// MkdirAll wraps os.MkdirAll with platform-specific path handling.
	MkdirAll(path string, perm os.FileMode) error

	// CommandContext creates an exec.Cmd with platform-specific env handling.
	CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd
}

var platform Platform

// InitPlatform selects the platform implementation based on runtime.GOOS.
// It must be called once during startup before any platform methods are used.
func InitPlatform() {
	if runtime.GOOS == "windows" {
		platform = &windowsPlatform{}
	} else {
		platform = &tspPlatform{}
	}
}

// P returns the active platform implementation.
func P() Platform {
	if platform == nil {
		InitPlatform()
	}
	return platform
}

// --- TSP implementation ---

type tspPlatform struct{}

func (tspPlatform) Name() string { return "tsp" }

func (tspPlatform) ExecutableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func (tspPlatform) DataDir() (string, error) {
	exeDir, err := tspPlatform{}.ExecutableDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(exeDir, "data")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func (tspPlatform) ConfigPath() string      { return "jukaconfig.json" }
func (tspPlatform) UserConfigPath() string  { return "jukauser.json" }
func (tspPlatform) ToolsDir() string        { return filepath.Join(".", "required") }
func (tspPlatform) RequiredDir() string     { return filepath.Join(".", "required") }
func (tspPlatform) IsHandheld() bool        { return true }
func (tspPlatform) FullscreenDefault() bool { return true }
func (tspPlatform) ResizableDefault() bool  { return false }

func (tspPlatform) LookPath(exe string) (string, error) {
	// TSP tools are expected in ./required/ relative to the binary.
	exeDir, err := tspPlatform{}.ExecutableDir()
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(exeDir, "required", exe)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	// Fallback to PATH lookup.
	return exec.LookPath(exe)
}

func (tspPlatform) OpenURL(url string) error   { return nil }
func (tspPlatform) OpenFile(path string) error { return nil }

func (tspPlatform) Stat(name string) (os.FileInfo, error) { return os.Stat(name) }
func (tspPlatform) ReadFile(name string) ([]byte, error)  { return os.ReadFile(name) }
func (tspPlatform) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}
func (tspPlatform) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

func (tspPlatform) CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// TSP tools in ./required/ may need the directory on PATH.
	if dir := filepath.Join(".", "required"); dir != "" {
		cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return cmd
}

// --- Windows implementation ---

type windowsPlatform struct{}

func (windowsPlatform) Name() string { return "windows" }

func (windowsPlatform) ExecutableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

func (windowsPlatform) DataDir() (string, error) {
	// Prefer SDL preference path if available, otherwise %APPDATA%.
	appName := "JukaHub"
	if sdlPrefPath := sdlGetPrefPath("uk.co.jukahub", appName); sdlPrefPath != "" {
		if err := os.MkdirAll(sdlPrefPath, 0755); err != nil {
			return "", err
		}
		return sdlPrefPath, nil
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		appData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	dir := filepath.Join(appData, appName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func (windowsPlatform) ConfigPath() string      { return filepath.Join(".", "jukaconfig.json") }
func (windowsPlatform) UserConfigPath() string  { return filepath.Join(".", "jukauser.json") }
func (windowsPlatform) ToolsDir() string        { return filepath.Join(".", "required") }
func (windowsPlatform) RequiredDir() string     { return filepath.Join(".", "required") }
func (windowsPlatform) IsHandheld() bool        { return false }
func (windowsPlatform) FullscreenDefault() bool { return false }
func (windowsPlatform) ResizableDefault() bool  { return true }

func (windowsPlatform) LookPath(exe string) (string, error) {
	// On Windows, tools may be in ./required/ or on PATH.
	exeDir, err := windowsPlatform{}.ExecutableDir()
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(exeDir, "required", exe)
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return exec.LookPath(exe)
}

func (windowsPlatform) OpenURL(url string) error {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func (windowsPlatform) OpenFile(path string) error {
	return exec.Command("cmd", "/c", "start", "", path).Start()
}

func (windowsPlatform) Stat(name string) (os.FileInfo, error) { return os.Stat(name) }
func (windowsPlatform) ReadFile(name string) ([]byte, error)  { return os.ReadFile(name) }
func (windowsPlatform) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}
func (windowsPlatform) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

func (windowsPlatform) CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	// Merge env from current process.
	cmd.Env = os.Environ()
	return cmd
}

// sdlGetPrefPath returns the SDL preference path if available, or "".
// This uses cgo to call SDL_GetPrefPath. It is intentionally guarded so
// the rest of the app never depends on SDL being initialized.
func sdlGetPrefPath(org, app string) string {
	// Stub: on this build the function always returns "" and the app falls
	// back to %APPDATA%. The real implementation can be wired when cgo is
	// available and the app is ready to use SDL preference paths.
	return ""
}

// --- Convenience helpers ---

// IsTSP returns true when running on the Trimui Smart Pro platform.
func IsTSP() bool { return P().Name() == "tsp" }

// IsWindows returns true when running on Windows.
func IsWindows() bool { return P().Name() == "windows" }

// MustExecutableDir returns the executable directory or panics.
func MustExecutableDir() string {
	dir, err := P().ExecutableDir()
	if err != nil {
		panic(fmt.Sprintf("platform: cannot determine executable dir: %v", err))
	}
	return dir
}

// MustDataDir returns the data directory or panics.
func MustDataDir() string {
	dir, err := P().DataDir()
	if err != nil {
		panic(fmt.Sprintf("platform: cannot determine data dir: %v", err))
	}
	return dir
}

// ReplaceRuntimeGOOSCalls is a no-op migration helper. New code should call
// P() or the convenience helpers above instead of runtime.GOOS directly.
// This function exists so grep can find all remaining runtime.GOOS calls.
func ReplaceRuntimeGOOSCalls() {
	_ = runtime.GOOS
	_ = filepath.Join
	_ = os.Stat
	_ = os.Open
	_ = exec.Command
	_ = strings.Contains
	_ = fmt.Sprintf
}

package main

// Dolphin asks RetroArch's EGL proc-address callback for core OpenGL ES entry
// points. The Smart Pro's EGL 1.4 returns NULL for those names even though its
// libGLESv2 exports them. This small ARM64 preload keeps EGL's answer when it
// has one and fills only missing gl* names from the firmware GLES library. It
// is installed under JukaHub's private data and given only to direct Dolphin
// core launches; the original RetroArch and firmware libraries stay in place.

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

//go:embed dolphin_egl/eglproc_arm64.bin
var dolphinEGLShim []byte

const (
	dolphinEGLFolder = "dolphin-egl"
	dolphinEGLFile   = "libjukadolphin-eglproc.so"
	// Verified on the Smart Pro: a plain firmware EGL query could not find
	// glGetIntegerv; this exact image in LD_PRELOAD could.
	dolphinEGLSHA256 = "bd8d42c9e32b8aabe52b21e66db00c0fcf32e7ca7f9ffc08819e64d303952edf"
)

// dolphinEGLInstallAt places the verified image under base, replacing only a
// missing or altered copy. AtomicWrite leaves a running RetroArch's mapped
// image intact if a later JukaHub carries a different build.
func dolphinEGLInstallAt(base string) (string, error) {
	if !filepath.IsAbs(base) {
		return "", errors.New("Dolphin graphics helper has no private data folder")
	}
	sum := sha256.Sum256(dolphinEGLShim)
	if hex.EncodeToString(sum[:]) != dolphinEGLSHA256 {
		return "", errors.New("embedded Dolphin graphics helper is damaged")
	}
	path := filepath.Join(base, dolphinEGLFolder, dolphinEGLFile)
	if info, err := os.Lstat(path); err == nil {
		if info.Mode().IsRegular() && info.Size() == int64(len(dolphinEGLShim)) {
			if current, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(current, dolphinEGLShim) {
				return path, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := AtomicWrite(path, dolphinEGLShim, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func dolphinEGLInstall() (string, error) {
	base, err := privateGlibcBase()
	if err != nil {
		return "", err
	}
	return dolphinEGLInstallAt(base)
}

// dolphinEGLPreloadEnv puts the shim before any existing preload, including
// the in-game indicators, and leaves every other environment entry alone.
func dolphinEGLPreloadEnv(env []string, shim string) []string {
	if env == nil {
		env = os.Environ()
	}
	old, _ := childEnvLookup(env, "LD_PRELOAD")
	for _, entry := range strings.FieldsFunc(old, func(r rune) bool { return r == ':' || unicode.IsSpace(r) }) {
		if entry == shim {
			return env
		}
	}
	preload := shim
	if old = strings.TrimSpace(old); old != "" {
		preload += ":" + old
	}
	return append(childEnvWithout(env, "LD_PRELOAD"), "LD_PRELOAD="+preload)
}

// dolphinEGLPrepareLaunch runs after the child has the firmware environment
// and the optional in-game indicator preload, but before raGlibcApply wraps
// RetroArch in its private C runtime. That wrapper changes cmd.Path/Args, not
// cmd.Env, so the EGL shim reaches the final RetroArch process either way.
func dolphinEGLPrepareLaunch(launcher string, cmd *exec.Cmd) error {
	if cmd == nil || dolphinSoC() == "" || !dolphinIsCore(launcher) {
		return nil
	}
	shim, err := dolphinEGLInstall()
	if err != nil {
		return fmt.Errorf("Dolphin graphics helper: %w", err)
	}
	cmd.Env = dolphinEGLPreloadEnv(cmd.Env, shim)
	logInfo("library", "GameCube: Dolphin's EGL graphics helper is in %s for this RetroArch launch", shim)
	return nil
}

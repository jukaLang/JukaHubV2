package main

// wine_keyboard.go installs the small Wine-native keyboard beside JukaHub's
// runtime, then plans a Windows Apps launch through it. The keyboard is a
// separate Wine program so it can stay visible while JukaHub's own screen is
// parked for Weston. Game profiles and the built-in Wine Test stay direct.

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

//go:embed winekeyboard/keyboard.exe.gz
var wineKeyboardGz []byte

const (
	wineKeyboardMaxImage        = 4 << 20
	wineKeyboardMaxCommandUnits = 4095 // keyboard.c has a 4096-WCHAR buffer.
	wineKeyboardTargetEnv       = "JUKAHUB_VK_TARGET"
	wineKeyboardCommandEnv      = "JUKAHUB_VK_CMDLINE"
)

type wineProgramSpec struct {
	Exe  string
	Args []string
	Env  map[string]string
}

func wineKeyboardExePath() string {
	return filepath.Join(windowsRuntimeDir(), "helpers", "keyboard.exe")
}

// wineKeyboardImage unpacks the embedded PE with a strict size bound.
func wineKeyboardImage() ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(wineKeyboardGz))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	image, err := io.ReadAll(io.LimitReader(zr, wineKeyboardMaxImage+1))
	if err != nil {
		return nil, err
	}
	if len(image) < 1024 || len(image) > wineKeyboardMaxImage || !bytes.HasPrefix(image, []byte("MZ")) {
		return nil, errors.New("the embedded Virtual Keyboard is damaged")
	}
	return image, nil
}

// wineKeyboardEnsure writes the PE only when it changed. The helper is outside
// the scanned Windows Apps tree, so it never appears as another user app.
func wineKeyboardEnsure() (string, error) {
	image, err := wineKeyboardImage()
	if err != nil {
		return "", err
	}
	path := wineKeyboardExePath()
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() && info.Size() == int64(len(image)) {
		if current, readErr := os.ReadFile(path); readErr == nil && bytes.Equal(current, image) {
			return path, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := AtomicWrite(path, image, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// wineKeyboardTargetPath maps a scanned Linux executable to Wine's Z: drive.
// Keeping the target in a dedicated environment variable avoids shell parsing
// of spaces, quotes, and other characters in the app's path.
func wineKeyboardTargetPath(unixPath string) (string, error) {
	if !strings.HasPrefix(unixPath, "/") || strings.ContainsAny(unixPath, "\x00\\") {
		return "", fmt.Errorf("Virtual Keyboard needs an absolute Linux app path: %q", unixPath)
	}
	clean := path.Clean(unixPath)
	if clean == "/" {
		return "", errors.New("Virtual Keyboard needs a Windows executable path")
	}
	return "Z:" + strings.ReplaceAll(clean, "/", `\`), nil
}

// wineKeyboardQuoteArg follows CreateProcessW/CommandLineToArgvW's backslash
// and quote rules. All arguments are quoted, including empty strings.
func wineKeyboardQuoteArg(arg string) string {
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for _, r := range arg {
		if r == '\\' {
			backslashes++
			continue
		}
		count := backslashes
		if r == '"' {
			count = 2*backslashes + 1
		}
		for i := 0; i < count; i++ {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
		backslashes = 0
	}
	for i := 0; i < 2*backslashes; i++ {
		b.WriteByte('\\')
	}
	b.WriteByte('"')
	return b.String()
}

// wineKeyboardWrap keeps the original app's arguments and environment in the
// child launch. It does not change the caller's slices or map.
func wineKeyboardWrap(l wineLaunch, helper string) (wineProgramSpec, error) {
	if helper == "" {
		return wineProgramSpec{Exe: l.Exe, Args: l.Args, Env: l.Env}, nil
	}
	target, err := wineKeyboardTargetPath(l.Exe)
	if err != nil {
		return wineProgramSpec{}, err
	}
	words := make([]string, 0, len(l.Args)+1)
	words = append(words, wineKeyboardQuoteArg(target))
	for _, arg := range l.Args {
		if strings.ContainsRune(arg, '\x00') {
			return wineProgramSpec{}, errors.New("Virtual Keyboard cannot pass an argument containing NUL")
		}
		words = append(words, wineKeyboardQuoteArg(arg))
	}
	command := strings.Join(words, " ")
	if len(utf16.Encode([]rune(command))) > wineKeyboardMaxCommandUnits {
		return wineProgramSpec{}, errors.New("the Windows app command line is too long for the Virtual Keyboard")
	}
	env := make(map[string]string, len(l.Env)+2)
	for key, value := range l.Env {
		if !strings.EqualFold(key, wineKeyboardTargetEnv) && !strings.EqualFold(key, wineKeyboardCommandEnv) {
			env[key] = value
		}
	}
	env[wineKeyboardTargetEnv] = target
	env[wineKeyboardCommandEnv] = command
	return wineProgramSpec{Exe: helper, Env: env}, nil
}

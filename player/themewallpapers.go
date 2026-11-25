package main

// themewallpapers.go — the wallpapers JukaHub ships for its themes (Juka,
// 2026-11-25: "Generate a few wallpapers for people to use to customize (apply
// to certain themes)").
//
// Eight pictures drawn by branding/mkwallpapers.py from the palettes of the
// theme families they belong to (themeWallpaperPairs), embedded from
// builtin_wallpapers/ and copied — the first time the wallpaper picker opens,
// or a theme needs one — into the data folder's wallpapers folder, where the
// picker lists them beside the user's own pictures. They are 16:9; on a screen
// of another shape (the Brick's 4:3) the copy is the middle of the picture at
// the screen's shape, because the backdrop stretches a picture to the screen.
//
// A theme that has one brings it along: picking Synthwave puts Neon Horizon
// under the glass. The rule (themeWallpaperFollow) runs whenever a theme is
// applied — by hand, by the time-of-day schedule, by Battery Saver, by an
// experience pack (whose own wallpaper choice comes after and wins):
//
//   - a wallpaper the user chose in the picker, or None chosen there, is never
//     replaced (wallpaperSavedKey holds it, and it is not the one a theme put
//     there);
//   - otherwise the theme's wallpaper goes on, and a theme without one takes
//     a wallpaper a theme put there away again;
//   - Settings › Theme & look › Theme wallpapers (Custom themeWallpapers, on by
//     default) turns the rule off, taking a theme's wallpaper away; turning it
//     back on forgets a "None" picked earlier and applies the theme's.
//
// themeWallpaperAuto (bookkeeping) remembers the wallpaper a theme put there,
// which is how "the theme's" is told from "the user's".

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"strings"
)

//go:embed builtin_wallpapers/*.jpg
var builtinWallpapers embed.FS

const (
	// themeWallpapersKey is the Theme wallpapers switch.
	themeWallpapersKey = "themeWallpapers"
	// themeWallpaperAutoKey is the wallpaper a theme put on (bookkeeping).
	themeWallpaperAutoKey = "themeWallpaperAuto"
	// themeWallpaperMarker records the copies JukaHub made, by file: the build
	// they came from and the screen shape they were cut for.
	themeWallpaperMarker = ".jukahub-wallpapers.json"
)

func init() {
	// Bookkeeping, not a setting: carried to another device it would name a
	// file that is not there (cfgdoc.go).
	cfgdocRuntimeKeys[themeWallpaperAutoKey] = true
}

// themeWallpaper is one shipped picture: its file under builtin_wallpapers/
// (without .jpg) and its name, which is also the copy's file name.
type themeWallpaper struct {
	id   string
	name string
}

var themeWallpaperList = []themeWallpaper{
	{"neon-horizon", "Neon Horizon"},
	{"midnight-aurora", "Midnight Aurora"},
	{"deep-ocean", "Deep Ocean"},
	{"misty-forest", "Misty Forest"},
	{"ember-glow", "Ember Glow"},
	{"paper-garden", "Paper Garden"},
	{"sakura-dusk", "Sakura Dusk"},
	{"arctic-light", "Arctic Light"},
}

// themeWallpaperPairs names, for a colour preset, the picture made for it.
// Presets left out keep the glass backdrop: the defaults (hub, dark, light's
// grey cousins), OLED's true black, the high-contrast and colour-blind
// palettes, the terminal looks and the console styles, which have looks of
// their own.
var themeWallpaperPairs = map[string]string{
	"synthwave": "neon-horizon", "vaporwave": "neon-horizon", "cyberpunk": "neon-horizon", "arcade": "neon-horizon",
	"midnight": "midnight-aurora", "aurora": "midnight-aurora", "nord": "midnight-aurora", "dracula": "midnight-aurora",
	"ocean": "deep-ocean", "oceanglass": "deep-ocean", "cobalt": "deep-ocean", "blue": "deep-ocean",
	"forest": "misty-forest", "green": "misty-forest", "gruvbox": "misty-forest",
	"ember": "ember-glow", "sunset": "ember-glow", "amber": "ember-glow", "ruby": "ember-glow", "red": "ember-glow",
	"paper": "paper-garden", "eink": "paper-garden", "sand": "paper-garden", "solarized-light": "paper-garden", "moss": "paper-garden",
	"sakura": "sakura-dusk", "cute": "sakura-dusk", "girly": "sakura-dusk", "lavender": "sakura-dusk", "orchid": "sakura-dusk",
	"arctic": "arctic-light", "light": "arctic-light",
}

// themeWallpaperFor is the picture made for preset: a colour preset's own, or
// a total conversion's through the palette it is built on. Pure.
func themeWallpaperFor(preset string) (themeWallpaper, bool) {
	preset = strings.TrimSpace(preset)
	id, ok := themeWallpaperPairs[preset]
	if !ok {
		for _, spec := range themeConversionSpecs {
			if spec.key == preset {
				id, ok = themeWallpaperPairs[spec.palette]
				break
			}
		}
	}
	if !ok {
		return themeWallpaper{}, false
	}
	for _, w := range themeWallpaperList {
		if w.id == id {
			return w, true
		}
	}
	return themeWallpaper{}, false
}

// themeWallpaperDir is the data folder's wallpapers folder (the picker's own
// start folder on a handheld).
func themeWallpaperDir() string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "wallpapers")
	}
	return filepath.Join("data", "wallpapers")
}

func themeWallpaperPath(w themeWallpaper) string {
	return filepath.Join(themeWallpaperDir(), w.name+".jpg")
}

// themeWallpaperRecord is one copy in the marker.
type themeWallpaperRecord struct {
	Build string `json:"build"`
	Shape string `json:"shape"`
}

type themeWallpaperMarkerFile struct {
	Files map[string]themeWallpaperRecord `json:"files"`
}

func themeWallpaperReadMarker(dir string) themeWallpaperMarkerFile {
	var m themeWallpaperMarkerFile
	if data, err := os.ReadFile(filepath.Join(dir, themeWallpaperMarker)); err == nil {
		_ = json.Unmarshal(stripUTF8BOM(data), &m)
	}
	if m.Files == nil {
		m.Files = map[string]themeWallpaperRecord{}
	}
	return m
}

// themeWallpaperShape is the shape the copies are cut for: "16:9" when the
// screen is 16:9 (within a few per cent), else the screen's size.
func themeWallpaperShape(sw, sh int32) string {
	if sw <= 0 || sh <= 0 || math.Abs(float64(sw)/float64(sh)-16.0/9.0) < 0.05 {
		return "16:9"
	}
	return fmt.Sprintf("%dx%d", sw, sh)
}

// themeWallpaperBytes is w's picture for a screen of sw x sh: as drawn on a
// 16:9 screen, else its middle at the screen's shape. Pure.
func themeWallpaperBytes(w themeWallpaper, sw, sh int32) ([]byte, string, error) {
	data, err := builtinWallpapers.ReadFile("builtin_wallpapers/" + w.id + ".jpg")
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(data)
	build := hex.EncodeToString(sum[:8])
	if themeWallpaperShape(sw, sh) == "16:9" {
		return data, build, nil
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return data, build, nil
	}
	b := img.Bounds()
	want := float64(sw) / float64(sh)
	rect := b
	if want < float64(b.Dx())/float64(b.Dy()) {
		nw := int(float64(b.Dy())*want + 0.5)
		x0 := b.Min.X + (b.Dx()-nw)/2
		rect = image.Rect(x0, b.Min.Y, x0+nw, b.Max.Y)
	} else {
		nh := int(float64(b.Dx())/want + 0.5)
		y0 := b.Min.Y + (b.Dy()-nh)/2
		rect = image.Rect(b.Min.X, y0, b.Max.X, y0+nh)
	}
	sub, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return data, build, nil
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, sub.SubImage(rect), &jpeg.Options{Quality: 90}); err != nil {
		return data, build, nil
	}
	return buf.Bytes(), build, nil
}

// themeWallpaperEnsure makes sure the copies are in the wallpapers folder:
// all of them (want empty), or the one want names. A copy JukaHub made from
// an older build, or for another screen shape, is made again; a file of the
// same name that JukaHub did not make is left as it is; and a copy the user
// deleted comes back only when it is asked for by name (a theme needs it).
// It returns the path of want's copy. Main thread (file work only).
func themeWallpaperEnsure(want string) (string, error) {
	dir := themeWallpaperDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	marker := themeWallpaperReadMarker(dir)
	shape := themeWallpaperShape(screenWidth, screenHeight)
	changed := false
	wantPath := ""
	var firstErr error
	for _, w := range themeWallpaperList {
		if want != "" && w.id != want {
			continue
		}
		file := w.name + ".jpg"
		path := filepath.Join(dir, file)
		rec, placed := marker.Files[file]
		exists := regularFile(path)
		switch {
		case exists && !placed:
			// Someone's own file by that name: used, never overwritten.
			if want != "" {
				wantPath = path
			}
			continue
		case !exists && placed && want == "":
			// Deleted by the user after JukaHub put it there.
			continue
		}
		data, build, err := themeWallpaperBytes(w, screenWidth, screenHeight)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if exists && rec.Build == build && rec.Shape == shape {
			if want != "" {
				wantPath = path
			}
			continue
		}
		if err := AtomicWrite(path, data, 0o644); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		marker.Files[file] = themeWallpaperRecord{Build: build, Shape: shape}
		changed = true
		if want != "" {
			wantPath = path
		}
	}
	if changed {
		if data, err := json.MarshalIndent(marker, "", "  "); err == nil {
			_ = AtomicWrite(filepath.Join(dir, themeWallpaperMarker), append(data, '\n'), 0o644)
		}
		logInfo("wallpaper", "theme wallpapers copied to %s (%s)", dir, shape)
	}
	if want != "" && wantPath == "" {
		if firstErr == nil {
			firstErr = errors.New("no wallpaper called " + want)
		}
		return "", firstErr
	}
	return wantPath, firstErr
}

// themeWallpapersOn reads the Theme wallpapers switch (on by default).
func themeWallpapersOn(config *Config) bool {
	return config != nil && customBool(config.Variables.Custom, themeWallpapersKey, true)
}

// sameWallpaperPath compares two wallpaper values as files.
func sameWallpaperPath(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(filepath.Clean(resolvePath(a)), filepath.Clean(resolvePath(b)))
}

// themeWallpaperFollow applies the rule in the header for preset ("" is no
// preset, the default palette) and returns the name of a wallpaper it put on,
// "" when it put none on. Main thread.
func themeWallpaperFollow(config *Config, preset string) string {
	if config == nil {
		return ""
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	custom := config.Variables.Custom
	auto, _ := custom[themeWallpaperAutoKey].(string)
	saved, _ := custom[wallpaperSavedKey].(string)
	cur := config.Variables.BackgroundImage
	ours := sameWallpaperPath(cur, auto)
	if !ours && strings.TrimSpace(saved) != "" && !sameWallpaperPath(saved, auto) {
		// The user chose this wallpaper — or None — in the picker.
		return ""
	}
	w, ok := themeWallpaperFor(preset)
	if !themeWallpapersOn(config) {
		ok = false
	}
	if !ok {
		if ours {
			themeWallpaperClear(config)
		}
		return ""
	}
	path, err := themeWallpaperEnsure(w.id)
	if err != nil || path == "" {
		logWarn("wallpaper", "theme wallpaper %s not available: %v", w.name, err)
		return ""
	}
	if ours && sameWallpaperPath(cur, path) {
		return ""
	}
	themeWallpaperSet(config, path)
	return w.name
}

// themeWallpaperSet puts path on as a theme's wallpaper and remembers it, so
// it comes back after a restart (wallpaperRestoreSaved) and is known as the
// theme's. Main thread.
func themeWallpaperSet(config *Config, path string) {
	custom := config.Variables.Custom
	config.Variables.BackgroundImage = path
	// The theme owns the wallpaper now, not an experience pack picked before.
	delete(custom, xpackWallpaperKey)
	custom[themeWallpaperAutoKey] = path
	// Saves the user config too (syncVariableOverrides), the auto key with it.
	setStoreString(config, wallpaperSavedKey, path)
	wallpaperRestoreRemember(config)
	xpackDropDynamicWallpaper()
}

// themeWallpaperClear takes a theme's wallpaper away: the glass backdrop is
// back, and nothing records a pick, so the next theme may bring its own.
func themeWallpaperClear(config *Config) {
	custom := config.Variables.Custom
	config.Variables.BackgroundImage = xpackDefaultWallpaper
	delete(custom, themeWallpaperAutoKey)
	setStoreString(config, wallpaperSavedKey, "")
	wallpaperRestoreRemember(config)
	xpackDropDynamicWallpaper()
}

// themeWallpapersRow is Settings › Theme & look › Theme wallpapers.
func themeWallpapersRow() setRow {
	return setRow{
		ID: themeWallpapersKey, Label: "Theme wallpapers", Kind: setKindToggle,
		Detail: setStatic("A theme with a wallpaper made for it brings it along, unless you picked a wallpaper (or None) yourself"),
		Get:    themeWallpapersOn,
		Set: func(c *Config, on bool) {
			if c == nil {
				return
			}
			setStoreBool(c, themeWallpapersKey, on)
			if saved, _ := c.Variables.Custom[wallpaperSavedKey].(string); on && strings.EqualFold(strings.TrimSpace(saved), wallpaperNoneValue) {
				// Back on: a None picked earlier stops blocking the themes.
				setStoreString(c, wallpaperSavedKey, "")
			}
			name := themeWallpaperFollow(c, themePresetName(c))
			switch {
			case on && name != "":
				setToast("Theme wallpapers on · " + name)
			default:
				setToast("Theme wallpapers: " + setOnOff(on))
			}
		},
	}
}

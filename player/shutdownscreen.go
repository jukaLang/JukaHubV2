package main

// shutdownscreen.go — the power-off screen.
//
// Settings → Theme & look → Power-off screen: Glass is the teardown as it has
// always been (shutdown.go), ending on black. Picture ends it on a full-screen
// picture instead, held about a second so the panel keeps it as the device
// powers off: "See you soon" with the JukaHub wordmark and your name, a card
// with the clock and the battery, or your own PNG or JPEG from the card's
// Themes/Shutdown folder.
//
// Only a real power-off or restart (Home's power card, devpower.go) shows it;
// quitting to the stock menu ends on black as before. Nothing here can hold
// the shutdown up: a picture of your own is decoded on a worker from the
// moment the teardown starts, under a deadline, and if it is not ready (or
// cannot be read) when the teardown ends, the built-in picture is drawn
// instead. The hold itself fits inside the teardown's budget, under the exit
// watchdog.

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

const (
	// shutdownScreenKey is the style: Glass or Picture.
	shutdownScreenKey = "shutdownScreen"
	// shutdownPictureKey is the picture: a built-in or a file's name in
	// <SD>/Themes/Shutdown.
	shutdownPictureKey = "shutdownPicture"

	shutdownScreenGlass   = "glass"
	shutdownScreenPicture = "picture"

	shutdownPictureFarewell = "builtin:farewell"
	shutdownPictureBattery  = "builtin:battery"

	// shutdownScreenHoldTime is how long the picture stays up before the
	// power goes; shutdownScreenFade its fade in from black.
	shutdownScreenHoldTime = 1200 * time.Millisecond
	shutdownScreenFade     = 160 * time.Millisecond
	// shutdownScreenDecodeBudget bounds the decode of a picture of your own.
	shutdownScreenDecodeBudget = 2500 * time.Millisecond
	// shutdownPictureMaxBytes bounds that picture's file.
	shutdownPictureMaxBytes = 8 << 20
	// shutdownPictureMaxFiles bounds the folder's listing.
	shutdownPictureMaxFiles = 64
)

// shutdownScreenState is one teardown's picture, prepared when it starts.
var shutdownScreenState struct {
	mu      sync.Mutex
	active  bool
	picture string
	pic     *image.NRGBA
	done    bool
	name    string
	title   string
	clock   string
	battery int
	cancel  context.CancelFunc
}

// shutdownPictureList is the Themes/Shutdown listing the picker shows.
var shutdownPictureList struct {
	mu       sync.Mutex
	files    []string
	at       time.Time
	scanning bool
}

// shutdownPictureDir is <SD>/Themes/Shutdown.
func shutdownPictureDir() string {
	return filepath.Join(saveManagerSDRoot(), "Themes", "Shutdown")
}

// shutdownScreenMode is the stored style, Glass unless Picture was chosen.
func shutdownScreenMode(c *Config) string {
	if c == nil || c.Variables.Custom == nil {
		return shutdownScreenGlass
	}
	if v, _ := c.Variables.Custom[shutdownScreenKey].(string); strings.EqualFold(strings.TrimSpace(v), shutdownScreenPicture) {
		return shutdownScreenPicture
	}
	return shutdownScreenGlass
}

// shutdownPictureValue is the stored picture: a built-in, or a bare file name
// (nothing that leaves Themes/Shutdown); "See you soon" otherwise.
func shutdownPictureValue(c *Config) string {
	if c == nil || c.Variables.Custom == nil {
		return shutdownPictureFarewell
	}
	v, _ := c.Variables.Custom[shutdownPictureKey].(string)
	v = strings.TrimSpace(v)
	switch {
	case v == shutdownPictureBattery:
		return v
	case v == "" || strings.HasPrefix(v, "builtin:"):
		return shutdownPictureFarewell
	case strings.ContainsAny(v, `/\`) || strings.HasPrefix(v, ".") || !lookImageExtOK(v):
		return shutdownPictureFarewell
	}
	return v
}

// shutdownPicturePath is a picture value's file, "" for a built-in.
func shutdownPicturePath(value string) string {
	if strings.HasPrefix(value, "builtin:") || value == "" {
		return ""
	}
	return filepath.Join(shutdownPictureDir(), value)
}

// shutdownScreenPrepare starts this teardown's picture (ShowShutdown calls it
// first): nothing for Glass, for a plain exit or a signal; otherwise the
// words it shows are taken now and a picture of your own starts decoding.
func shutdownScreenPrepare(config *Config) {
	s := &shutdownScreenState
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.active, s.pic, s.done = false, nil, false
	if config == nil || shutdownScreenMode(config) != shutdownScreenPicture ||
		devPowerAction(devPowerPending.Load()) == devPowerNone || exitRequestedBySignal.Load() {
		return
	}
	s.active = true
	s.picture = shutdownPictureValue(config)
	s.name = bootGreetName(config)
	s.title = devPowerShutdownTitle()
	s.clock = clockDigits(config, time.Now())
	s.battery = -1
	path := shutdownPicturePath(s.picture)
	w, h := int(screenWidth), int(screenHeight)
	ctx, cancel := context.WithTimeout(context.Background(), shutdownScreenDecodeBudget)
	s.cancel = cancel
	go func() {
		defer cancel()
		battery := batteryCalCorrect(batteryPercentIn(batterySysfsDir()))
		var pic *image.NRGBA
		if path != "" {
			var err error
			if pic, err = lookDecodeFit(ctx, path, w, h, shutdownPictureMaxBytes); err != nil {
				logWarn("shutdown", "power-off picture %s: %v", filepath.Base(path), err)
				pic = nil
			}
		}
		s.mu.Lock()
		s.battery, s.pic, s.done = battery, pic, true
		s.mu.Unlock()
	}()
}

// shutdownScreenHold ends the teardown on the picture and holds it, and
// reports whether it did; false leaves the teardown's own black frame
// (ShowShutdown). It never waits for the decode: a picture that is not ready
// gives way to the built-in one.
func shutdownScreenHold(renderer *sdl.Renderer, config *Config, deadline time.Time) bool {
	s := &shutdownScreenState
	s.mu.Lock()
	active, picture, pic := s.active, s.picture, s.pic
	name, title, clock, battery := s.name, s.title, s.clock, s.battery
	s.active, s.pic = false, nil
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.mu.Unlock()
	if !active || renderer == nil || config == nil || gameScreenSuspended() || !time.Now().Before(deadline) {
		return false
	}
	var tex *sdl.Texture
	if pic != nil {
		tex = brandLogoUpload(renderer, pic)
	}
	if tex != nil {
		defer tex.Destroy()
	}
	draw := func() {
		_ = renderer.SetClipRect(nil)
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		renderer.SetDrawColor(0, 0, 0, 255)
		_ = renderer.Clear()
		switch {
		case tex != nil:
			_, _, tw, th, err := tex.Query()
			if err == nil {
				dst := sdl.Rect{X: (screenWidth - tw) / 2, Y: (screenHeight - th) / 2, W: tw, H: th}
				_ = tex.SetAlphaMod(255)
				_ = renderer.Copy(tex, nil, &dst)
			}
		case picture == shutdownPictureBattery:
			shutdownDrawBatteryCard(renderer, config, title, clock, battery)
		default:
			shutdownDrawSeeYou(renderer, config, name)
		}
	}
	fade := shutdownScreenFade
	if effectiveReducedMotion(config) {
		fade = 0
	}
	start := time.Now()
	for {
		t := 1.0
		if fade > 0 {
			t = float64(time.Since(start)) / float64(fade)
		}
		draw()
		if t < 1 {
			renderer.SetDrawColor(0, 0, 0, uint8(255*(1-t)))
			veil := sdl.Rect{X: 0, Y: 0, W: screenWidth, H: screenHeight}
			_ = renderer.FillRect(&veil)
		}
		renderer.Present()
		if t >= 1 || !time.Now().Before(deadline) {
			break
		}
		for ev := sdl.PollEvent(); ev != nil; ev = sdl.PollEvent() {
		}
		sdl.Delay(16)
	}
	holdEnd := time.Now().Add(shutdownScreenHoldTime)
	if holdEnd.After(deadline) {
		holdEnd = deadline
	}
	for time.Now().Before(holdEnd) {
		for ev := sdl.PollEvent(); ev != nil; ev = sdl.PollEvent() {
		}
		sdl.Delay(20)
	}
	return true
}

// shutdownDrawSeeYou is the built-in "See you soon": the wordmark over the
// line, with a short accent rule between them.
func shutdownDrawSeeYou(renderer *sdl.Renderer, config *Config, name string) {
	word := stageWordFont(config)
	line := FontForRole(config, TypeHeading)
	if word == nil || line == nil {
		return
	}
	msg := uiT("See you soon")
	if name != "" {
		msg += ", " + name
	}
	wordH, lineH := int32(word.Height()), int32(line.Height())
	gap := lineH
	y := (screenHeight - wordH - gap - lineH) / 2
	cx := screenWidth / 2
	drawTextRaw(renderer, word, "JukaHub", cx, y, sdl.Color{R: 255, G: 255, B: 255, A: 255}, textAlignCenter)
	accent := glassAccent()
	renderer.SetDrawColor(accent.R, accent.G, accent.B, 255)
	rule := sdl.Rect{X: cx - 24, Y: y + wordH + gap/2 - 2, W: 48, H: 4}
	_ = renderer.FillRect(&rule)
	drawTextRaw(renderer, line, msg, cx, y+wordH+gap, sdl.Color{R: 200, G: 204, B: 212, A: 255}, textAlignCenter)
}

// shutdownDrawBatteryCard is the built-in clock-and-battery card: the time,
// the battery's charge as a bar and a number, and what is happening.
func shutdownDrawBatteryCard(renderer *sdl.Renderer, config *Config, title, clock string, battery int) {
	big := getFontSized(config, "big", 72)
	small := FontForRole(config, TypeBody)
	if big == nil || small == nil {
		return
	}
	bigH, smallH := int32(big.Height()), int32(small.Height())
	cardW := min(screenWidth*2/3, int32(560))
	cardH := bigH + smallH*3 + 64
	card := sdl.Rect{X: (screenWidth - cardW) / 2, Y: (screenHeight - cardH) / 2, W: cardW, H: cardH}
	fillRoundedRect(renderer, card.X, card.Y, card.W, card.H, 28, sdl.Color{R: 22, G: 24, B: 30, A: 255})
	strokeRoundedRect(renderer, card.X, card.Y, card.W, card.H, 28, 1, sdl.Color{R: 255, G: 255, B: 255, A: 40})
	cx := card.X + card.W/2
	y := card.Y + 20
	drawTextRaw(renderer, big, clock, cx, y, sdl.Color{R: 255, G: 255, B: 255, A: 255}, textAlignCenter)
	y += bigH + 12
	if battery >= 0 {
		// The battery: an outline with its tip, filled to the charge.
		bw, bh := int32(120), smallH
		bx := cx - (bw+8+int32(len("100%"))*smallH/2)/2
		strokeRoundedRect(renderer, bx, y, bw, bh, 6, 2, sdl.Color{R: 255, G: 255, B: 255, A: 200})
		fillRoundedRect(renderer, bx+bw, y+bh/3, 4, bh/3, 1, sdl.Color{R: 255, G: 255, B: 255, A: 200})
		ink := glassAccent()
		if battery <= 15 {
			ink = sdl.Color{R: 239, G: 68, B: 68, A: 255}
		}
		if fill := (bw - 8) * int32(min(battery, 100)) / 100; fill > 0 {
			fillRoundedRect(renderer, bx+4, y+4, fill, bh-8, 3, WithAlpha(ink, 255))
		}
		drawTextRaw(renderer, small, itoa(min(battery, 100))+"%", bx+bw+14, y, sdl.Color{R: 230, G: 232, B: 238, A: 255}, textAlignLeft)
		y += bh + 14
	}
	drawText(renderer, small, title, cx, y, sdl.Color{R: 170, G: 176, B: 188, A: 255}, textAlignCenter)
}

// shutdownPictureFiles lists Themes/Shutdown (PNG and JPEG), refreshing the
// listing in the background when it is older than ten seconds (the picker
// asks every frame).
func shutdownPictureFiles() []string {
	l := &shutdownPictureList
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.scanning && (l.at.IsZero() || time.Since(l.at) > 10*time.Second) {
		l.scanning = true
		go func() {
			var files []string
			if items, err := os.ReadDir(shutdownPictureDir()); err == nil {
				for _, it := range items {
					name := it.Name()
					if it.IsDir() || strings.HasPrefix(name, ".") || !lookImageExtOK(name) {
						continue
					}
					files = append(files, name)
					if len(files) >= shutdownPictureMaxFiles {
						break
					}
				}
			}
			sort.Strings(files)
			l.mu.Lock()
			l.files, l.at, l.scanning = files, time.Now(), false
			l.mu.Unlock()
		}()
	}
	return l.files
}

// shutdownPictureLabel is a picture value as the picker names it.
func shutdownPictureLabel(value string) string {
	switch value {
	case shutdownPictureFarewell:
		return "See you soon"
	case shutdownPictureBattery:
		return "Clock and battery"
	}
	return strings.TrimSuffix(value, filepath.Ext(value))
}

// shutdownPictureOptions are the built-ins, then the folder's pictures.
func shutdownPictureOptions(c *Config) []setOption {
	files := shutdownPictureFiles()
	opts := make([]setOption, 0, len(files)+3)
	opts = append(opts,
		setOption{Value: shutdownPictureFarewell, Label: "See you soon", Note: "The JukaHub wordmark and a goodbye by name"},
		setOption{Value: shutdownPictureBattery, Label: "Clock and battery", Note: "The time and the charge left"})
	cur := shutdownPictureValue(c)
	listed := strings.HasPrefix(cur, "builtin:")
	for _, f := range files {
		opts = append(opts, setOption{Value: f, Label: shutdownPictureLabel(f), Note: "From Themes/Shutdown on the card"})
		if f == cur {
			listed = true
		}
	}
	if !listed {
		opts = append(opts, setOption{Value: cur, Label: shutdownPictureLabel(cur), Note: "Not found in Themes/Shutdown; See you soon is shown instead"})
	}
	return opts
}

// shutdownScreenRow is Settings → Theme & look → Power-off screen.
func shutdownScreenRow() setRow {
	opts := func(*Config) []setOption {
		return []setOption{
			{Value: shutdownScreenGlass, Label: "Glass", Note: "The shutdown card dissolves to black"},
			{Value: shutdownScreenPicture, Label: "Picture", Note: "Ends on a picture that stays as the power goes"},
		}
	}
	return setRow{
		ID: shutdownScreenKey, Label: "Power-off screen", Kind: setKindChoice,
		Detail:  setStatic("What the screen shows as the device turns off or restarts: the glass shutdown card fading to black, or a picture held as the power goes (choose it below). Quitting to the stock menu always ends on black."),
		Options: opts,
		Current: func(c *Config) int {
			if shutdownScreenMode(c) == shutdownScreenPicture {
				return 1
			}
			return 0
		},
		Value: func(c *Config) string {
			if shutdownScreenMode(c) == shutdownScreenPicture {
				return "Picture"
			}
			return "Glass"
		},
		Choose: func(c *Config, value string) {
			if value != shutdownScreenPicture {
				value = shutdownScreenGlass
			}
			setStoreString(c, shutdownScreenKey, value)
			saveUserConfigDebounced(userConfigFromVariables(c))
			if value == shutdownScreenPicture {
				setToast("Power-off screen: Picture")
			} else {
				setToast("Power-off screen: Glass")
			}
		},
	}
}

// shutdownPictureRow is Settings → Theme & look → Power-off picture, shown
// while Power-off screen is Picture.
func shutdownPictureRow() setRow {
	return setRow{
		ID: shutdownPictureKey, Label: "Power-off picture", Kind: setKindChoice,
		Detail:     setStatic("See you soon, the clock and battery, or a PNG or JPEG of your own: copy it to Themes/Shutdown on the card (screen-sized pictures look best; up to 4096×4096 and 8 MB)."),
		Visible:    func(c *Config) bool { return shutdownScreenMode(c) == shutdownScreenPicture },
		Options:    shutdownPictureOptions,
		Sheet:      shutdownPictureOptions,
		SheetValue: shutdownPictureValue,
		SheetTitle: "Power-off picture",
		Current: func(c *Config) int {
			cur := shutdownPictureValue(c)
			for i, opt := range shutdownPictureOptions(c) {
				if opt.Value == cur {
					return i
				}
			}
			return 0
		},
		Value: func(c *Config) string { return shutdownPictureLabel(shutdownPictureValue(c)) },
		Choose: func(c *Config, value string) {
			setStoreString(c, shutdownPictureKey, value)
			saveUserConfigDebounced(userConfigFromVariables(c))
			setToast("Power-off picture: " + shutdownPictureLabel(shutdownPictureValue(c)))
		},
	}
}

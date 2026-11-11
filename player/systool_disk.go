package main

// systool_disk.go — the Storage screen (config scene "DiskSpace") and the
// volume reader it shares with the Hardware screen and the legacy disk chart.
//
// The old reader ran `df -k /` — a process per reading, and on Windows, which
// has no df, a chart that read "Reading storage" for ever. Sizes now come from
// statfs on the handheld and Linux and from GetDiskFreeSpaceEx (through
// gopsutil) on Windows. The screen reads every volume as it opens, the one
// JukaHub lives on first, and re-reads every five seconds while it is up.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/veandco/go-sdl2/sdl"
)

// sysToolDiskRefresh is how often the open Storage screen re-reads the
// volumes: statfs is a cheap call, and five seconds is quick enough to watch a
// download fill the card.
const sysToolDiskRefresh = 5 * time.Second

// sysToolVolume is one filesystem the screen shows.
type sysToolVolume struct {
	Name  string
	Path  string
	Total uint64
	Free  uint64
	App   bool // JukaHub is installed on it
}

func (v sysToolVolume) used() uint64 {
	if v.Free > v.Total {
		return 0
	}
	return v.Total - v.Free
}

func (v sysToolVolume) frac() float64 {
	if v.Total == 0 {
		return 0
	}
	return float64(v.used()) / float64(v.Total)
}

// sysToolVolumeUsage reports the size and the free space of the filesystem
// holding path.
func sysToolVolumeUsage(path string) (total, free uint64, err error) {
	if runtime.GOOS == "windows" {
		return sysToolGopsutilUsage(path)
	}
	t, f, err := diskUsage(path)
	if err != nil {
		return 0, 0, err
	}
	if t <= 0 || f < 0 {
		return 0, 0, errors.New("the filesystem reported no size")
	}
	return uint64(t), uint64(f), nil
}

// sysToolGopsutilUsage asks gopsutil, which can panic on unusual systems; a
// panic is a missing reading, never a crash.
func sysToolGopsutilUsage(path string) (total, free uint64, err error) {
	defer func() {
		if r := recover(); r != nil {
			total, free, err = 0, 0, fmt.Errorf("disk usage: %v", r)
		}
	}()
	u, err := disk.Usage(path)
	if err != nil {
		return 0, 0, err
	}
	if u == nil || u.Total == 0 {
		return 0, 0, errors.New("the drive reported no size")
	}
	return u.Total, u.Free, nil
}

// sysToolAppVolumePath is where JukaHub lives: the volume the benchmark
// measures and the Hardware screen reports.
func sysToolAppVolumePath() string {
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	if runtime.GOOS == "windows" {
		return `C:\`
	}
	return "/"
}

// sysToolVolumes lists the volumes worth showing on this system.
func sysToolVolumes() []sysToolVolume {
	appDir := sysToolAppVolumePath()
	if runtime.GOOS == "windows" {
		return sysToolWindowsVolumes(appDir)
	}
	return sysToolUnixVolumes(appDir)
}

// sysToolWindowsVolumes lists the drives that have a size (an empty card
// reader has none).
func sysToolWindowsVolumes(appDir string) (out []sysToolVolume) {
	defer func() {
		if r := recover(); r != nil {
			out = nil
		}
	}()
	parts, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	upperApp := strings.ToUpper(appDir)
	for _, p := range parts {
		mount := strings.TrimRight(p.Mountpoint, `\`)
		key := strings.ToUpper(mount)
		if mount == "" || seen[key] {
			continue
		}
		seen[key] = true
		total, free, err := sysToolVolumeUsage(mount + `\`)
		if err != nil || total == 0 {
			continue
		}
		out = append(out, sysToolVolume{
			Name:  "Drive " + key,
			Path:  mount + `\`,
			Total: total,
			Free:  free,
			App:   upperApp != "" && strings.HasPrefix(upperApp, key),
		})
		if len(out) >= 8 {
			break
		}
	}
	return out
}

// sysToolUnixVolumes lists the handheld's SD card, its internal storage and
// the system partition — or a desktop's system, home and JukaHub folders —
// skipping the ones that turn out to be the same filesystem.
func sysToolUnixVolumes(appDir string) []sysToolVolume {
	type candidate struct{ name, path string }
	var cands []candidate
	if sd := strings.TrimSpace(trimuiSDRootFn()); sd != "" || IsTrimuiDevice() {
		if sd == "" {
			sd = "/mnt/SDCARD"
		}
		cands = append(cands, candidate{"SD card", sd}, candidate{"Internal storage", "/mnt/UDISK"}, candidate{"System", "/"})
	} else {
		home, _ := os.UserHomeDir()
		cands = append(cands, candidate{"System", "/"}, candidate{"Home", home}, candidate{"JukaHub folder", appDir})
	}
	var out []sysToolVolume
	seen := map[[2]uint64]bool{}
	bestApp, bestLen := -1, -1
	for _, c := range cands {
		if c.path == "" {
			continue
		}
		if _, err := os.Stat(c.path); err != nil {
			continue
		}
		total, free, err := sysToolVolumeUsage(c.path)
		if err != nil || total == 0 {
			continue
		}
		key := [2]uint64{total, free}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, sysToolVolume{Name: c.name, Path: c.path, Total: total, Free: free})
		prefix := strings.TrimRight(c.path, "/") + "/"
		if appDir != "" && (appDir == c.path || strings.HasPrefix(appDir, prefix)) && len(c.path) > bestLen {
			bestApp, bestLen = len(out)-1, len(c.path)
		}
	}
	if bestApp >= 0 {
		out[bestApp].App = true
	}
	return out
}

// sysToolDiskRow is a volume with its words, formatted when it is read.
type sysToolDiskRow struct {
	vol  sysToolVolume
	pct  string
	line string
}

type sysToolDiskData struct {
	loaded bool
	rows   []sysToolDiskRow
	at     time.Time
}

type sysToolDiskState struct {
	mu      sync.Mutex
	version uint64
	d       sysToolDiskData
}

var sysToolDisk sysToolDiskState

// sysToolDiskRead reads the volumes and publishes them, the app's first.
func sysToolDiskRead() {
	vols := sysToolVolumes()
	rows := make([]sysToolDiskRow, 0, len(vols))
	for _, v := range vols {
		row := sysToolDiskRow{
			vol:  v,
			pct:  fmt.Sprintf("%d%%", int(v.frac()*100+0.5)),
			line: formatBytes(v.Free) + " free of " + formatBytes(v.Total),
		}
		if v.App {
			rows = append([]sysToolDiskRow{row}, rows...)
		} else {
			rows = append(rows, row)
		}
	}
	sysToolDisk.mu.Lock()
	sysToolDisk.d = sysToolDiskData{loaded: true, rows: rows, at: time.Now()}
	sysToolDisk.version++
	sysToolDisk.mu.Unlock()
}

// sysToolDiskEnter reads the volumes now and every few seconds while the
// screen is up.
func sysToolDiskEnter() {
	runSceneTask(func(ctx context.Context) {
		counted := false
		for {
			sysToolDiskRead()
			if !counted { // the read as the screen opens; the 5 s refreshes are not counted
				counted = true
				trophyEvent(tkDiskScan)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(sysToolDiskRefresh):
			}
		}
	})
}

// sysToolDiskAgain is X: read again right away.
func sysToolDiskAgain() {
	PlayActivateSound()
	runSceneTask(func(ctx context.Context) {
		if ctx.Err() == nil {
			sysToolDiskRead()
			trophyEvent(tkDiskScan)
		}
	})
}

var sysToolDiskView struct {
	version uint64
	built   bool
	d       sysToolDiskData
	status  string
	runRect sdl.Rect
}

func sysToolDiskSync() {
	sysToolDisk.mu.Lock()
	if sysToolDiskView.built && sysToolDiskView.version == sysToolDisk.version {
		sysToolDisk.mu.Unlock()
		return
	}
	sysToolDiskView.version = sysToolDisk.version
	sysToolDiskView.d = sysToolDisk.d
	sysToolDisk.mu.Unlock()
	sysToolDiskView.built = true
	sysToolDiskView.status = ""
	if sysToolDiskView.d.loaded {
		sysToolDiskView.status = "Updated " + sysToolDiskView.d.at.Format("15:04:05")
	}
}

// sysToolDiskFooterHints is the Storage screen's footer row.
func sysToolDiskFooterHints() []FooterHint {
	return []FooterHint{{Button: "B", Label: "Back"}, {Button: "X", Label: "Refresh"}}
}

// sysToolDiskDonut draws a usage ring with the percentage in its middle.
func sysToolDiskDonut(renderer *sdl.Renderer, config *Config, cx, cy, radius int32, frac float64, pct string) {
	if radius < 8 {
		return
	}
	inner := radius * 70 / 100
	drawDiskPieSector(renderer, cx, cy, radius, inner, 1, glassMix(ColorBackground, glassInk(), 0.14))
	drawDiskPieSector(renderer, cx, cy, radius, inner, frac, sysToolHeatColor(frac))
	if font := diskPiePercentFont(config, pct, inner); font != nil {
		w, h := measureText(font, pct)
		drawText(renderer, font, pct, cx-w/2, cy-h/2, ColorTextPrimary(), textAlignLeft)
	}
}

func renderSysToolDisk(renderer *sdl.Renderer, config *Config) {
	sysToolDiskSync()
	v := &sysToolDiskView
	area := sysToolDrawPage(renderer, config, "DiskSpace", sysToolPageSpec{
		Title:       "Storage",
		Subtitle:    "How full each drive is, the one JukaHub uses first",
		Icon:        "disk",
		Status:      v.status,
		StatusColor: ColorTextTertiary(),
	})
	f, ok := sysToolLoadFonts(config)
	if !ok {
		sysToolFinishPage(renderer, config)
		return
	}
	v.runRect = sdl.Rect{}
	actionH := int32(f.body.Height()) + 22
	body := sdl.Rect{X: area.X, Y: area.Y, W: area.W, H: area.H - actionH - 16}
	rows := v.d.rows
	switch {
	case !v.d.loaded:
		phase := float64(time.Now().UnixMilli()%2000) / 1000
		UILoadingSkeleton(renderer, sdl.Rect{X: body.X, Y: body.Y, W: body.W * 44 / 100, H: body.H}, phase)
		UILoadingSkeleton(renderer, sdl.Rect{X: body.X + body.W*44/100 + 16, Y: body.Y, W: body.W - body.W*44/100 - 16, H: 96}, phase+0.2)
	case len(rows) == 0:
		UIEmptyState(renderer, config, body, "@disk", "No storage information", "This system did not report its drives.")
	default:
		gap := int32(16)
		heroW := body.W * 44 / 100
		if len(rows) == 1 {
			heroW = body.W
		}
		hero := sdl.Rect{X: body.X, Y: body.Y, W: heroW, H: body.H}
		sysToolCard(renderer, hero, false)
		primary := rows[0]
		textBlock := int32(f.value.Height()) + int32(f.body.Height()) + int32(f.label.Height()) + 40
		radius := (hero.H - textBlock - 48) / 2
		if maxR := (hero.W - 48) / 2; radius > maxR {
			radius = maxR
		}
		if radius > 120 {
			radius = 120
		}
		cx := hero.X + hero.W/2
		cy := hero.Y + 24 + radius
		sysToolDiskDonut(renderer, config, cx, cy, radius, primary.vol.frac(), primary.pct)
		ty := cy + radius + 18
		name := ellipsize(f.value, primary.vol.Name, hero.W-40)
		nw, nh := measureText(f.value, name)
		drawText(renderer, f.value, name, cx-nw/2, ty, ColorTextPrimary(), textAlignLeft)
		ty += nh
		lw, lh := measureText(f.body, primary.line)
		drawText(renderer, f.body, primary.line, cx-lw/2, ty, ColorTextSecondary(), textAlignLeft)
		ty += lh + 8
		if primary.vol.App {
			chip := "JukaHub is installed here"
			cw, ch := measureText(f.label, chip)
			fillRoundedRect(renderer, cx-cw/2-12, ty, cw+24, ch+8, (ch+8)/2, WithAlpha(glassAccent(), 40))
			drawText(renderer, f.label, chip, cx-cw/2, ty+4, ColorTextPrimary(), textAlignLeft)
		}

		// The other drives, one card each.
		x := hero.X + hero.W + gap
		w := body.X + body.W - x
		rowH := int32(96)
		y := body.Y
		for _, r := range rows[1:] {
			if y+rowH > body.Y+body.H {
				break
			}
			card := sdl.Rect{X: x, Y: y, W: w, H: rowH}
			sysToolCard(renderer, card, false)
			ring := int32(34)
			sysToolDiskDonut(renderer, config, card.X+18+ring, card.Y+rowH/2, ring, r.vol.frac(), r.pct)
			tx := card.X + 18 + 2*ring + 18
			tw := card.X + card.W - 18 - tx
			drawText(renderer, f.body, ellipsize(f.body, r.vol.Name, tw), tx, card.Y+16, ColorTextPrimary(), textAlignLeft)
			drawText(renderer, f.label, ellipsize(f.label, r.line, tw), tx, card.Y+16+int32(f.body.Height())+2, ColorTextSecondary(), textAlignLeft)
			UIProgressBar(renderer, sdl.Rect{X: tx, Y: card.Y + rowH - 22, W: tw, H: 6}, r.vol.frac(), glassA(18), sysToolHeatColor(r.vol.frac()))
			y += rowH + 12
		}
	}
	ay := area.Y + area.H - actionH
	v.runRect = sysToolAction(renderer, f, area.X, ay, "X", "Refresh", false)
	sysToolFinishPage(renderer, config)
}

func sysToolDiskPad(e *sdl.ControllerButtonEvent, config *Config) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	case sdl.CONTROLLER_BUTTON_X, sdl.CONTROLLER_BUTTON_Y:
		sysToolDiskAgain()
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		return focusChromeBar()
	default:
		return false
	}
	return true
}

func sysToolDiskKey(e *sdl.KeyboardEvent, config *Config) bool {
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		goBackScene(config)
	case sdl.K_x, sdl.K_y, sdl.K_F5, sdl.K_r:
		sysToolDiskAgain()
	case sdl.K_UP:
		return focusChromeBar()
	default:
		return false
	}
	return true
}

func sysToolDiskClick(mx, my int32) bool {
	if sysToolDiskView.runRect.W > 0 && pointInRect(mx, my, sysToolDiskView.runRect) {
		sysToolDiskAgain()
		return true
	}
	return false
}

// sysToolDiskScene is the registered module for the "DiskSpace" scene.
type sysToolDiskScene struct{ sceneBase }

func (sysToolDiskScene) render(r *sdl.Renderer, c *Config) bool { renderSysToolDisk(r, c); return true }

func (sysToolDiskScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	return sysToolDiskKey(e, c)
}

func (sysToolDiskScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return sysToolDiskPad(e, c)
}

func init() { registerScene(sysToolDiskScene{sceneBase{name: "DiskSpace"}}) }

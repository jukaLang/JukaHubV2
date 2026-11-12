package main

// datetime.go — Settings → Date & time: the zone JukaHub runs in, how its
// clock is set, and how it writes dates.
//
// Who owns the time zone depends on the machine. On Windows, macOS and a Linux
// desktop with its own settings app, the operating system does: the row shows
// the zone and opens the system's page. On the handheld (and any Linux without
// such an app) nothing else sets it, so JukaHub does, with its own picker
// (tzpicker.go). A chosen zone takes effect at once in this process
// (time.Local) and is written to "jukatimezone" next to the binary, which
// launch.sh exports as TZ on the next start — so the games and emulators
// JukaHub starts get it from then on. The process environment is not changed
// at run time: libc's setenv races with SDL's and libc's own getenv on other
// threads (musl frees the replaced value), and a crash in a clock setting is
// not worth a few minutes of the new zone in a game. JukaHub never writes
// /etc: the TrimUI root filesystem may be read-only, a firmware update would
// replace it, and the card is the one place that is ours.
//
// The clock itself is set from the network (ntpsync.go) where the process may
// (Linux as root); elsewhere the row reports how far off the system clock is.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Who owns the zone ─────────────────────────────────────────────────────

// tzOwner says what sets the time zone on this machine.
type tzOwner int

const (
	tzOwnerJukaHub tzOwner = iota // the handheld, or a Linux without a settings app: JukaHub's picker
	tzOwnerWindows                // Windows Settings → Time & language
	tzOwnerMac                    // System Settings → General → Date & Time
	tzOwnerDesktop                // a Linux desktop's own settings app
)

var (
	dtDesktopOnce sync.Once
	dtDesktopApp  string
)

// linuxDesktopSettingsApp is the settings app of a real Linux desktop, or ""
// on the handheld and on any Linux without one (no graphical session, or no
// GNOME settings). Looked up once: it is a walk of PATH.
func linuxDesktopSettingsApp() string {
	dtDesktopOnce.Do(func() {
		if runtime.GOOS != "linux" || IsTrimuiDevice() {
			return
		}
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return
		}
		if p, err := exec.LookPath("gnome-control-center"); err == nil && p != "" {
			dtDesktopApp = "gnome-control-center"
		}
	})
	return dtDesktopApp
}

// dateTimeZoneOwner is who sets the zone here.
func dateTimeZoneOwner() tzOwner {
	switch runtime.GOOS {
	case "windows":
		return tzOwnerWindows
	case "darwin":
		return tzOwnerMac
	}
	if linuxDesktopSettingsApp() != "" {
		return tzOwnerDesktop
	}
	return tzOwnerJukaHub
}

// dateTimeOwnerName names the system that owns the zone and the clock.
func dateTimeOwnerName(o tzOwner) string {
	switch o {
	case tzOwnerWindows:
		return "Windows"
	case tzOwnerMac:
		return "macOS"
	case tzOwnerDesktop:
		return "your desktop"
	}
	return "JukaHub"
}

// ── The zone in force ─────────────────────────────────────────────────────

var (
	dtSystemZoneOnce sync.Once
	dtSystemZone     string
)

// dtReadSystemZone names the zone /etc/localtime points at ("Europe/Berlin"),
// or "" when it cannot be told (a copied file, no file, Windows).
func dtReadSystemZone() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			return target[i+len("zoneinfo/"):]
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if name := strings.TrimSpace(string(b)); tzValidName(name) {
			return name
		}
	}
	return ""
}

// dateTimeZoneName is the zone in force, by its IANA name when there is one.
// time.Local is named after the zone JukaHub applied or TZ named; a system
// default is "Local", which /etc/localtime may still name.
func dateTimeZoneName() string {
	name := time.Local.String()
	if name != "" && name != "Local" {
		return name
	}
	dtSystemZoneOnce.Do(func() { dtSystemZone = dtReadSystemZone() })
	if dtSystemZone != "" {
		return dtSystemZone
	}
	if abbr, _ := time.Now().Zone(); abbr != "" {
		return abbr
	}
	return "Local"
}

// utcOffsetLabel writes t's offset from UTC: "UTC", "UTC+1", "UTC-3",
// "UTC+5:30".
func utcOffsetLabel(t time.Time) string {
	_, off := t.Zone()
	if off == 0 {
		return "UTC"
	}
	sign := "+"
	if off < 0 {
		sign = "-"
		off = -off
	}
	h, m := off/3600, (off%3600)/60
	if m == 0 {
		return "UTC" + sign + strconv.Itoa(h)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, h, m)
}

// dateTimeZoneLabel is the zone row's value: "Europe/Berlin · UTC+1".
func dateTimeZoneLabel(now time.Time) string {
	return dateTimeZoneName() + " · " + utcOffsetLabel(now)
}

// tzValidName accepts what an IANA zone name can look like ("UTC",
// "America/Argentina/Buenos_Aires", "Etc/GMT+5") and nothing that could
// reach outside the zone directory or into a shell: no leading slash, no "..",
// only letters, digits and _ - + /. launch.sh applies the same rule before it
// exports the file's contents.
func tzValidName(name string) bool {
	if name == "" || len(name) > 64 || name[0] == '/' || strings.Contains(name, "..") || strings.HasSuffix(name, "/") {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '+' || c == '/':
		default:
			return false
		}
	}
	return true
}

// dateTimeZoneFile is where the chosen zone is kept: next to the binary on the
// card, where launch.sh reads it (APP_DIR/jukatimezone).
func dateTimeZoneFile() string {
	if dir, err := P().ExecutableDir(); err == nil && dir != "" {
		return filepath.Join(dir, "jukatimezone")
	}
	return "jukatimezone"
}

// dateTimeLoadZone reads the saved zone, or "" when none was chosen.
func dateTimeLoadZone() string {
	b, err := os.ReadFile(dateTimeZoneFile())
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(string(b))
	if !tzValidName(name) {
		return ""
	}
	return name
}

// errZoneOwnedBySystem is the answer to a zone change where the operating
// system owns the zone.
var errZoneOwnedBySystem = errors.New("the time zone is set by the operating system here")

// dateTimeSetLocal makes loc the zone of this process: every time.Now() after
// this is in it. Main thread only (the picker, and "Set automatically" through
// tbxPost), so the main loop never sees the zone change halfway through a
// frame. time.Local is a package variable the standard library reads without
// a lock; goroutines (logging, network work) may still read it while it is
// replaced. That residual race is accepted: the store is a single pointer
// word, a reader gets the old zone or the new one, and the race detector is
// the only thing that would notice (no test exercises it).
func dateTimeSetLocal(loc *time.Location) {
	now := time.Now()
	_, before := now.Zone()
	_, after := now.In(loc).Zone()
	time.Local = loc
	// The local wall clock moved by the change of UTC offset, the instants
	// did not: alarms and the playtime day re-anchor (clockjump.go).
	clockJumpNotify(0, time.Duration(after-before)*time.Second)
}

// dateTimeApplyZone makes name the zone JukaHub runs in, now and from the next
// start. It is the one path the picker and "Set automatically" use, on the
// main thread. The file is written off the calling thread (a card write can
// stall for a while).
func dateTimeApplyZone(name string) error {
	name = strings.TrimSpace(name)
	if dateTimeZoneOwner() != tzOwnerJukaHub {
		return errZoneOwnedBySystem
	}
	if !tzValidName(name) {
		return fmt.Errorf("%q is not a time zone name", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return fmt.Errorf("%s is not in the time zone data", name)
	}
	dateTimeSetLocal(loc)
	logInfo("clock", "time zone set to %s (%s)", name, utcOffsetLabel(time.Now()))
	dtZoneSave.mu.Lock()
	dtZoneSave.want = name
	dtZoneSave.mu.Unlock()
	go dateTimeSaveZone()
	return nil
}

// dtZoneSave serialises the zone file's writes: want is the latest choice,
// and each writer stores whatever is latest when its turn comes, so two quick
// choices can never leave the older one on the card.
var dtZoneSave struct {
	mu    sync.Mutex // guards want
	write sync.Mutex // one writer at a time
	want  string
}

// dateTimeSaveZone writes the latest chosen zone (off the render thread).
func dateTimeSaveZone() {
	dtZoneSave.write.Lock()
	defer dtZoneSave.write.Unlock()
	dtZoneSave.mu.Lock()
	name := dtZoneSave.want
	dtZoneSave.mu.Unlock()
	if err := AtomicWrite(dateTimeZoneFile(), []byte(name+"\n"), 0o644); err != nil {
		logWarn("clock", "time zone %s applies until JukaHub closes: %v", name, err)
	}
}

// dateTimeInit runs once at startup, before the first frame: it brings back
// the zone chosen last time (launch.sh normally exported it already; an older
// launcher or a Linux desktop did not) and the network-time switch. Only
// time.Local is set, never TZ (see the top of the file): programs started
// from here keep the environment's zone until a launcher exports it.
func dateTimeInit(config *Config) {
	netTimeSetEnabled(netTimeEnabledIn(config))
	clockRecordLoad()
	if dateTimeZoneOwner() != tzOwnerJukaHub {
		return
	}
	name := dateTimeLoadZone()
	if name == "" || name == time.Local.String() {
		return
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		logWarn("clock", "saved time zone %s cannot be loaded: %v", name, err)
		return
	}
	time.Local = loc
	logInfo("clock", "time zone %s restored", name)
}

// dateTimeTZName is the zone a TZ value (or a Location's name) stands for, in
// the form the picker uses: a leading ":" (POSIX's implementation-defined
// form) and a path's ".../zoneinfo/" prefix are dropped, so ":Europe/Berlin"
// and "/usr/share/zoneinfo/Europe/Berlin" both read "Europe/Berlin", and
// "/etc/localtime" is the system's zone, "Local" — as Go itself reads TZ.
func dateTimeTZName(tz string) string {
	tz = strings.TrimPrefix(strings.TrimSpace(tz), ":")
	if tz == "/etc/localtime" {
		return "Local"
	}
	if i := strings.LastIndex(tz, "/zoneinfo/"); i >= 0 {
		tz = tz[i+len("/zoneinfo/"):]
	}
	return tz
}

// ── Opening the zone page ─────────────────────────────────────────────────

// dateTimeOpenZone is the Time zone row's press, and the first-run tour's: the
// picker where JukaHub owns the zone, the system's page where it does not.
func dateTimeOpenZone(config *Config) {
	if dateTimeZoneOwner() == tzOwnerJukaHub {
		tzPickerOpen(config)
		return
	}
	if err := openSystemSettingsPage("timezone"); err != nil {
		ShowToast(err.Error(), ToastKindInfo)
	}
}

// dateTimeOpenSettings opens Settings on Date & time, with the zone picker up
// when pickZone is set. Home search and the first-run tour use it.
func dateTimeOpenSettings(config *Config, pickZone bool) {
	if config == nil {
		return
	}
	section := setSectionIndex("datetime")
	if section < 0 {
		return
	}
	if setIsHubScene(setHubSceneName(config)) {
		// Already in Settings: switch sections in place.
		setHubSelectSection(config, section, false)
		setHubEnterPane(false)
		if pickZone {
			dateTimeOpenZone(config)
		}
		return
	}
	if sceneIndexExact(config, "Settings") < 0 {
		// A setup without the Settings scene: nothing to open, and a picker
		// marked for "the next visit" would surface on some later one.
		setToast("That screen is not part of this setup")
		return
	}
	setHub.lastSection = section
	setHub.deepLink = true
	if pickZone && dateTimeZoneOwner() == tzOwnerJukaHub {
		// The hub starts its visit on the next frame; the picker survives that
		// first entry (tzPickerHubEnter).
		tzPickerOpen(config)
		tzPicker.pending = true
	}
	setOpenScene("Settings")(config)
	if pickZone && dateTimeZoneOwner() != tzOwnerJukaHub {
		dateTimeOpenZone(config)
	}
}

// dateTimeSettingsOpen is Home search's "Date & time" entry.
func dateTimeSettingsOpen(config *Config) { dateTimeOpenSettings(config, false) }

// ── Clock record ("Clock last set") ───────────────────────────────────────

// clockRecord is the last time JukaHub set the clock, kept in jukaclock.json
// so the row survives a restart.
type clockRecord struct {
	At       time.Time `json:"at"`
	How      string    `json:"how"`
	OffsetMS int64     `json:"offset_ms"`
}

// clockCheck is the last comparison with network time, set or not.
type clockCheck struct {
	At     time.Time
	Offset time.Duration
	Source string
	Err    string
	Set    bool
}

var clockState struct {
	mu     sync.Mutex
	record clockRecord
	check  clockCheck
}

const clockRecordFile = "jukaclock.json"

// clockRecordLoad reads the last record at startup.
func clockRecordLoad() {
	var rec clockRecord
	if _, err := readJSONWithBackup(clockRecordFile, &rec); err != nil {
		return
	}
	clockState.mu.Lock()
	clockState.record = rec
	clockState.mu.Unlock()
}

// clockRecordSave remembers a set clock. Called off the render thread.
func clockRecordSave(rec clockRecord) {
	clockState.mu.Lock()
	clockState.record = rec
	clockState.mu.Unlock()
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	if err := AtomicWrite(clockRecordFile, data, 0o644); err != nil {
		logWarn("clock", "clock record not saved: %v", err)
	}
}

// clockNoteCheck records the latest comparison for Settings.
func clockNoteCheck(c clockCheck) {
	clockState.mu.Lock()
	clockState.check = c
	clockState.mu.Unlock()
}

// clockSnapshot is what the rows read.
func clockSnapshot() (clockRecord, clockCheck) {
	clockState.mu.Lock()
	defer clockState.mu.Unlock()
	return clockState.record, clockState.check
}

// durationWords writes a clock offset for people: "0.4 s", "12 s", "3 min",
// "2 h 13 min", "41 days".
func durationWords(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < 10*time.Second:
		return strconv.FormatFloat(d.Seconds(), 'f', 1, 64) + " s"
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + " s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min"
	case d < 48*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) - 60*h
		if m == 0 {
			return strconv.Itoa(h) + " h"
		}
		return strconv.Itoa(h) + " h " + strconv.Itoa(m) + " min"
	}
	return strconv.Itoa(int(d.Hours()/24)) + " days"
}

// clockOffsetWords says which way a clock was off: "2 h 13 min behind".
func clockOffsetWords(offset time.Duration) string {
	// offset is network time minus the device clock: positive means the
	// device was behind.
	if offset >= 0 {
		return durationWords(offset) + " behind"
	}
	return durationWords(offset) + " ahead"
}

// clockLastSetValue is the "Clock last set" read-out.
func clockLastSetValue(c *Config) string {
	rec, _ := clockSnapshot()
	if rec.At.IsZero() {
		return "Not by JukaHub yet"
	}
	return formatDateTimeNear(c, rec.At.In(time.Local), time.Now()) + " (" + setOr(rec.How, "network time") + ")"
}

// clockLastSetDetail is the line under it: the last check, or why there is
// none.
func clockLastSetDetail(c *Config) string {
	rec, check := clockSnapshot()
	if !check.At.IsZero() {
		ago := durationWords(time.Since(check.At))
		switch {
		case check.Err != "":
			return "Last check " + ago + " ago failed: " + check.Err
		case check.Set:
			return "Set " + ago + " ago from " + check.Source + "; it was " + clockOffsetWords(check.Offset)
		case check.Offset < clockSetThreshold && check.Offset > -clockSetThreshold:
			return "Checked " + ago + " ago: within " + durationWords(check.Offset) + " of " + check.Source
		}
		return "Checked " + ago + " ago: " + clockOffsetWords(check.Offset) + " " + check.Source
	}
	if !rec.At.IsZero() && rec.OffsetMS != 0 {
		return "It was " + clockOffsetWords(time.Duration(rec.OffsetMS)*time.Millisecond) + " then"
	}
	if !clockSetSupported() {
		// JukaHub asks no time server by itself here.
		return "Network tools → Clock check compares it with internet time"
	}
	if !netTimeEnabledIn(c) {
		return "Not set automatically: Set time automatically is off"
	}
	return "Set from internet time when Wi-Fi connects"
}

// ── Settings rows ─────────────────────────────────────────────────────────

// netTimeKey is the Custom switch behind "Set time automatically".
const netTimeKey = "netTimeSync"

// netTimeEnabledIn reads the switch: on unless the user turned it off.
func netTimeEnabledIn(c *Config) bool {
	if c != nil && c.Variables.Custom != nil {
		if v, ok := customValue(c.Variables.Custom, netTimeKey); ok {
			return coerceBool(v)
		}
	}
	return true
}

// dateFormatOptions are the date orders, each shown on today's date.
func dateFormatOptions(*Config) []setOption {
	now := time.Now()
	auto := dateLayoutsFor(dateOrderForCountry(weatherCountryCode())).Medium
	return []setOption{
		{Value: dateOrderAuto, Label: "Auto · " + now.Format(auto)},
		{Value: dateOrderDMY, Label: now.Format(dateLayoutsDMY.Medium)},
		{Value: dateOrderMDY, Label: now.Format(dateLayoutsMDY.Medium)},
		{Value: dateOrderYMD, Label: now.Format(dateLayoutsYMD.Medium)},
		{Value: dateOrderDots, Label: now.Format(dateLayoutsDots.Medium)},
	}
}

// dateFormatCurrent is the stored choice's stop (Auto for anything else).
func dateFormatCurrent(c *Config) int {
	stored := ""
	if c != nil && c.Variables.Custom != nil {
		if v, ok := c.Variables.Custom[dateFormatKey].(string); ok && dateOrderValid(v) {
			stored = v
		}
	}
	for i, o := range dateFormatOptions(c) {
		if o.Value == stored {
			return i
		}
	}
	return 0
}

// dateTimeSettingsRows are the Date & time section (setmodel.go). The
// 12-hour switch moved here from Display & theme with its id, key and
// behaviour unchanged.
func dateTimeSettingsRows() []setRow {
	return []setRow{
		{
			ID: "netTime", Label: "Set time automatically", Kind: setKindToggle,
			Detail: func(c *Config) string {
				if !netTimeEnabledIn(c) {
					return "Off: the clock keeps whatever time the device has"
				}
				return "From internet time servers when Wi-Fi connects, then hourly"
			},
			Visible: func(*Config) bool { return clockSetSupported() },
			Get:     netTimeEnabledIn,
			Set: func(c *Config, on bool) {
				setStoreBool(c, netTimeKey, on)
				netTimeSetEnabled(on)
				if on {
					netTimeSyncNow()
				}
				setToast("Set time automatically: " + setOnOff(on))
			},
		},
		{
			ID: "clockSource", Label: "Clock", Kind: setKindInfo,
			Detail:  clockLastSetDetail,
			Visible: func(*Config) bool { return !clockSetSupported() },
			Value: func(*Config) string {
				if o := dateTimeZoneOwner(); o != tzOwnerJukaHub {
					return "Set by " + dateTimeOwnerName(o)
				}
				// JukaHub picks the zone here but may not set the clock (not
				// the handheld, or not root).
				return "Set by the system"
			},
		},
		{
			ID: "timezone", Label: "Time zone", Kind: setKindAction,
			Detail: func(*Config) string {
				switch o := dateTimeZoneOwner(); o {
				case tzOwnerJukaHub:
					return "JukaHub and the games it starts use this zone"
				default:
					return "Set by " + dateTimeOwnerName(o) + hintSentence(" · %s opens its settings", "A", "Open")
				}
			},
			Value: func(*Config) string { return dateTimeZoneLabel(time.Now()) },
			Open:  dateTimeOpenZone,
		},
		{
			ID: "clock12", Label: "12-hour clock", Kind: setKindToggle,
			Detail: setStatic("Show times as 3:30 PM instead of 15:30"),
			Get:    func(c *Config) bool { return c.Variables.Clock12 },
			Set: func(c *Config, on bool) {
				setStoreBool(c, "clock12", on)
				setToast("12-hour clock: " + setOnOff(on))
			},
		},
		{
			ID: "dateFormat", Label: "Date format", Kind: setKindChoice,
			Detail:  setStatic("How dates read on the clock, the idle screen and in lists"),
			Options: dateFormatOptions,
			Current: dateFormatCurrent,
			Choose: func(c *Config, value string) {
				setStoreString(c, dateFormatKey, value)
				setToast("Date format: " + formatDate(c, time.Now()))
			},
		},
		{
			ID: "clockLastSet", Label: "Clock last set", Kind: setKindInfo,
			Detail:  clockLastSetDetail,
			Visible: func(*Config) bool { return clockSetSupported() },
			Value:   clockLastSetValue,
		},
	}
}

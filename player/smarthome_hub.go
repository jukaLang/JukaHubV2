package main

// smarthome_hub.go — one list of every smart-home device JukaHub can show or
// switch: TP-Link Kasa and Tapo (smarthome_tplink.go), Google's Cast
// speakers, displays and TVs (smarthome_cast.go) and Home Assistant's
// entities (smart_home.go). The Smart Home tab (hometabs_smarthome.go), the
// Smart Home panel (smart_home_ui.go) and the phone's /smarthome page
// (companion_smarthome.go) all read it through shubSnapshot.
//
// Nothing runs at boot. The first time the tab or the panel is shown
// (shubKick) the devices kept from last time come back from smarthome.json and
// one look round the network starts: TP-Link's broadcast and Google Cast's
// mDNS browse, three seconds each, side by side. While the tab or the panel is
// on screen the devices are read again every few seconds; once neither has
// asked for a while nothing more is sent. A press changes the device on
// screen at once and sends the command off the render thread; a refusal puts
// the old state back and says why.
//
// smarthome.json (0600) keeps the devices found, the hidden ones and the
// TP-Link ID (the password encrypted with the device key, like every other
// stored password). Home Assistant keeps its ha_url / ha_token keys in the
// config, which the panel has always read.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// What a device is (shubDevice.Kind).
const (
	shubKindPlug       = "plug"
	shubKindOutlet     = "outlet"
	shubKindSwitch     = "switch"
	shubKindDimmer     = "dimmer"
	shubKindBulb       = "bulb"
	shubKindLightStrip = "lightstrip"
	shubKindSensor     = "sensor"
	shubKindHub        = "hub"
	shubKindSpeaker    = "speaker"
	shubKindScreen     = "screen"
	shubKindGroup      = "group"
	shubKindFan        = "fan"
	shubKindCover      = "cover"
)

// Where a device comes from (shubDevice.Source).
const (
	shubSourceTPLink = "tplink"
	shubSourceGoogle = "google"
	shubSourceHA     = "ha"
)

// What a device needs before it can be used (shubDevice.Need).
const (
	shubNeedNothing = iota
	shubNeedSignIn
	shubNeedTPAP
	shubNeedOffline
	shubNeedUnsupported
)

const (
	shubPollEvery = 6 * time.Second
	shubScanEvery = 15 * time.Minute
	shubScanWait  = 3 * time.Second
	// shubPendingFor: after a press, reads that were already on their way do
	// not put the old state back.
	shubPendingFor = 4 * time.Second
	shubWorkers    = 4
	shubFileName   = "smarthome.json"
	// shubPassKeyName names the password for the encryption log.
	shubPassKeyName = "TP-Link password"
)

// shubDevice is one tile: a device, a socket of a strip, a sensor of a hub, a
// speaker, a Home Assistant entity.
type shubDevice struct {
	ID       string
	Source   string
	Kind     string
	Name     string
	Model    string
	Parent   string
	On       bool
	CanOn    bool
	Level    int // brightness or volume, 0–100; -1 unknown
	CanLevel bool
	CanTemp  bool
	Muted    bool
	App      string
	Title    string
	Player   string // Cast: PLAYING, PAUSED, BUFFERING, IDLE
	Sensor   string
	Need     int
	Online   bool

	base     string // the TP-Link device a row belongs to
	tp       tplinkState
	cast     gcastFound
	haDomain string
	pending  time.Time
}

// shubSavedRow is a TP-Link row as smarthome.json keeps it, so the tab shows
// the device by name before its first answer.
type shubSavedRow struct {
	ID       string `json:"id"`
	Base     string `json:"base"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Parent   string `json:"parent,omitempty"`
	Child    string `json:"child,omitempty"`
	DeviceID string `json:"deviceId,omitempty"`
}

// shubFile is smarthome.json.
type shubFile struct {
	V      int            `json:"v"`
	User   string         `json:"tplinkUser,omitempty"`
	Pass   string         `json:"tplinkPassword,omitempty"`
	TPLink []tplinkFound  `json:"tplink,omitempty"`
	Rows   []shubSavedRow `json:"rows,omitempty"`
	Google []gcastFound   `json:"google,omitempty"`
	Hidden []string       `json:"hidden,omitempty"`
}

var shub struct {
	mu      sync.Mutex
	loaded  bool
	devices map[string]*shubDevice
	tp      map[string]*tplinkDevice
	gc      map[string]gcastFound
	hidden  map[string]bool

	creds   tplinkCreds
	passEnc string
	ha      smartHomeSettings
	haOn    bool
	haErr   string

	scanning bool
	scanned  bool
	scanAt   time.Time
	polling  bool
	pollAt   time.Time
	dirty    bool
}

// shubSaveMu keeps one write of smarthome.json at a time, in order.
var shubSaveMu sync.Mutex

// shubPath is a test seam.
var shubPath = func() string {
	dir, err := P().DataDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, shubFileName)
}

// shubLoadLocked reads smarthome.json the first time the hub is used.
func shubLoadLocked() {
	if shub.loaded {
		return
	}
	shub.loaded = true
	shub.devices = map[string]*shubDevice{}
	shub.tp = map[string]*tplinkDevice{}
	shub.gc = map[string]gcastFound{}
	shub.hidden = map[string]bool{}
	p := shubPath()
	if p == "" {
		return
	}
	var f shubFile
	if _, err := readJSONWithBackup(p, &f); err != nil {
		if !os.IsNotExist(err) {
			logWarn("smarthome", "ignoring %s: %v", shubFileName, err)
		}
		return
	}
	shub.creds.User = strings.TrimSpace(f.User)
	shub.passEnc = f.Pass
	if f.Pass != "" {
		if pass, err := DecryptAPIToken(f.Pass, shubPassKeyName); err == nil {
			shub.creds.Pass = pass
		}
	}
	for _, t := range f.TPLink {
		if t.ID == "" || t.Host == "" {
			continue
		}
		shub.tp[t.ID] = &tplinkDevice{found: t}
	}
	for _, r := range f.Rows {
		if r.ID == "" || shub.tp[r.Base] == nil {
			continue
		}
		shub.devices[r.ID] = &shubDevice{
			ID: r.ID, Source: shubSourceTPLink, Kind: r.Kind, Name: r.Name, Parent: r.Parent, Level: -1, base: r.Base,
			CanOn: shubKindSwitchable(r.Kind),
			tp:    tplinkState{Child: r.Child, DeviceID: r.DeviceID, Name: r.Name, Kind: r.Kind, Parent: r.Parent, Level: -1},
		}
	}
	for _, g := range f.Google {
		if g.ID == "" || g.Host == "" {
			continue
		}
		shub.gc[g.ID] = g
		shub.devices[g.ID] = &shubDevice{ID: g.ID, Source: shubSourceGoogle, Kind: g.Kind, Name: g.Name, Model: g.Model, Level: -1, cast: g}
	}
	for _, id := range f.Hidden {
		shub.hidden[id] = true
	}
}

// shubKindSwitchable reports a kind that switches on and off.
func shubKindSwitchable(kind string) bool {
	switch kind {
	case shubKindPlug, shubKindOutlet, shubKindSwitch, shubKindDimmer, shubKindBulb, shubKindLightStrip, shubKindFan, shubKindCover:
		return true
	}
	return false
}

// shubFileLocked is what smarthome.json should hold now.
func shubFileLocked() shubFile {
	f := shubFile{V: 1, User: shub.creds.User, Pass: shub.passEnc}
	for _, d := range shub.tp {
		f.TPLink = append(f.TPLink, d.found)
	}
	for _, g := range shub.gc {
		f.Google = append(f.Google, g)
	}
	for _, d := range shub.devices {
		if d.Source == shubSourceTPLink && d.base != "" {
			f.Rows = append(f.Rows, shubSavedRow{
				ID: d.ID, Base: d.base, Name: d.Name, Kind: d.Kind, Parent: d.Parent, Child: d.tp.Child, DeviceID: d.tp.DeviceID,
			})
		}
	}
	for id := range shub.hidden {
		f.Hidden = append(f.Hidden, id)
	}
	sort.Slice(f.TPLink, func(i, j int) bool { return f.TPLink[i].ID < f.TPLink[j].ID })
	sort.Slice(f.Google, func(i, j int) bool { return f.Google[i].ID < f.Google[j].ID })
	sort.Slice(f.Rows, func(i, j int) bool { return f.Rows[i].ID < f.Rows[j].ID })
	sort.Strings(f.Hidden)
	return f
}

// shubSave writes smarthome.json when something it keeps changed. Not under
// shub.mu: the copy is made under it, the write after.
func shubSave(force bool) {
	shub.mu.Lock()
	if !shub.loaded || (!shub.dirty && !force) {
		shub.mu.Unlock()
		return
	}
	shub.dirty = false
	f := shubFileLocked()
	shub.mu.Unlock()
	p := shubPath()
	if p == "" {
		return
	}
	data, err := jsonMarshalIndent(f)
	if err != nil {
		return
	}
	shubSaveMu.Lock()
	defer shubSaveMu.Unlock()
	if err := AtomicWrite(p, data, 0o600); err != nil {
		logWarn("smarthome", "could not save %s: %v", shubFileName, err)
	}
}

// shubSyncConfig copies the Home Assistant settings the main thread can read
// into the hub. Main thread.
func shubSyncConfig(c *Config) {
	if c == nil {
		return
	}
	ha := currentSmartHomeSettings(c)
	on := ha.hasHA() && ha.HAToken != ""
	shub.mu.Lock()
	shub.ha = ha
	if shub.haOn && !on {
		for id, d := range shub.devices {
			if d.Source == shubSourceHA {
				delete(shub.devices, id)
			}
		}
		shub.haErr = ""
	}
	shub.haOn = on
	shub.mu.Unlock()
}

// smartHomeSettingsForget drops the panel's cached settings, after the keys
// behind them were written in place (the cache only notices a new config).
func smartHomeSettingsForget() {
	smartHomeSettingsCache.mu.Lock()
	smartHomeSettingsCache.conf = nil
	smartHomeSettingsCache.mu.Unlock()
}

// shubKick is the tab's or the panel's "I am on screen": the first call loads
// the kept devices and looks round the network, later ones read the devices
// again when they are due. Main thread; cheap enough to call every frame.
func shubKick(c *Config) {
	shubSyncConfig(c)
	now := time.Now()
	shub.mu.Lock()
	shubLoadLocked()
	scan := !shub.scanning && (!shub.scanned || now.Sub(shub.scanAt) > shubScanEvery)
	poll := !scan && !shub.scanning && !shub.polling && now.Sub(shub.pollAt) >= shubPollEvery
	if scan {
		shub.scanning, shub.scanAt = true, now
	}
	if poll {
		shub.polling, shub.pollAt = true, now
	}
	shub.mu.Unlock()
	if scan {
		go shubScan()
	}
	if poll {
		go shubPoll()
	}
}

// shubScanNow looks round the network now (Find devices); false when a look
// is already under way.
func shubScanNow() bool {
	shub.mu.Lock()
	shubLoadLocked()
	if shub.scanning {
		shub.mu.Unlock()
		return false
	}
	shub.scanning, shub.scanAt = true, time.Now()
	shub.mu.Unlock()
	homeTabsTouch()
	go shubScan()
	return true
}

// shubPollSoon lets the next kick read the devices at once (after a press, a
// new sign-in).
func shubPollSoon() {
	shub.mu.Lock()
	shub.pollAt = time.Time{}
	shub.mu.Unlock()
}

// shubScan finds devices, merges them in and reads them all.
func shubScan() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var tps []tplinkFound
	var gcs []gcastFound
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		tps = tplinkDiscover(ctx, shubScanWait)
	}()
	go func() {
		defer wg.Done()
		gcs = gcastDiscover(ctx, shubScanWait)
	}()
	wg.Wait()

	shub.mu.Lock()
	for _, f := range tps {
		old := shub.tp[f.ID]
		if old != nil && old.found.Host == f.Host && old.found.Port == f.Port && old.found.Transport == f.Transport {
			continue // the open session stays
		}
		fresh := f.sysinfo
		f.sysinfo = nil
		shub.tp[f.ID] = &tplinkDevice{found: f, fresh: fresh}
		shub.dirty = true
	}
	for _, g := range gcs {
		if shub.gc[g.ID] != g {
			shub.gc[g.ID] = g
			shub.dirty = true
		}
	}
	shub.scanning, shub.scanned = false, true
	shub.polling, shub.pollAt = true, time.Now()
	shub.mu.Unlock()
	logInfo("smarthome", "look round the network: %d TP-Link, %d Google Cast", len(tps), len(gcs))
	shubPollWork()
}

// shubPoll reads every device once.
func shubPoll() { shubPollWork() }

// shubPollWork reads every known device, a few at a time, and applies what
// each answered.
func shubPollWork() {
	defer func() {
		shub.mu.Lock()
		shub.polling, shub.pollAt = false, time.Now()
		shub.mu.Unlock()
		shubSave(false)
		homeTabsTouch()
	}()
	shub.mu.Lock()
	creds, ha, haOn := shub.creds, shub.ha, shub.haOn
	tps := make([]*tplinkDevice, 0, len(shub.tp))
	for _, d := range shub.tp {
		tps = append(tps, d)
	}
	gcs := make([]gcastFound, 0, len(shub.gc))
	for _, g := range shub.gc {
		gcs = append(gcs, g)
	}
	shub.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sem := make(chan struct{}, shubWorkers)
	var wg sync.WaitGroup
	for _, d := range tps {
		wg.Add(1)
		go func(d *tplinkDevice) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows, err := d.state(ctx, creds)
			shubApplyTPLink(d, rows, err)
		}(d)
	}
	for _, g := range gcs {
		wg.Add(1)
		go func(g gcastFound) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st, err := gcastReadStatus(ctx, g)
			shubApplyCast(g, st, err)
		}(g)
	}
	if haOn {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ents, err := fetchHAEntities(ctx, ha)
			shubApplyHA(ents, err)
		}()
	}
	wg.Wait()
}

// shubNeedOf is what an error asks of the user.
func shubNeedOf(err error) int {
	switch {
	case errors.Is(err, errTPLinkAuth):
		return shubNeedSignIn
	case errors.Is(err, errTPLinkTPAP):
		return shubNeedTPAP
	case errors.Is(err, errTPLinkKind):
		return shubNeedUnsupported
	}
	return shubNeedOffline
}

// shubApplyTPLink puts one TP-Link device's answer into its rows.
func shubApplyTPLink(d *tplinkDevice, rows []tplinkState, err error) {
	base := d.found.ID
	now := time.Now()
	shub.mu.Lock()
	defer shub.mu.Unlock()
	if shub.tp[base] != d {
		return // a newer look round replaced it meanwhile
	}
	if err != nil {
		need, n := shubNeedOf(err), 0
		for _, dev := range shub.devices {
			if dev.base == base {
				dev.Need, dev.Online = need, false
				n++
			}
		}
		if n == 0 {
			name := d.found.Name
			if name == "" {
				name = tplinkModelName(d.found.Type, d.found.Model)
			}
			shub.devices[base] = &shubDevice{
				ID: base, Source: shubSourceTPLink, Kind: shubKindPlug, Name: name, Model: d.found.Model, Level: -1,
				Need: need, base: base, tp: tplinkState{Name: name, Kind: shubKindPlug, Level: -1},
			}
			shub.dirty = true
		}
		return
	}
	seen := make(map[string]bool, len(rows))
	for _, r := range rows {
		id := base
		if r.Child != "" {
			id = base + "/" + strings.ToLower(r.Child)
		}
		seen[id] = true
		dev := shub.devices[id]
		if dev == nil {
			dev = &shubDevice{ID: id, Source: shubSourceTPLink, base: base}
			shub.devices[id] = dev
			shub.dirty = true
		}
		if dev.Name != r.Name || dev.Kind != r.Kind || dev.Parent != r.Parent {
			shub.dirty = true
		}
		dev.Kind, dev.Name, dev.Model, dev.Parent = r.Kind, r.Name, r.Model, r.Parent
		dev.CanOn, dev.CanLevel, dev.CanTemp, dev.Sensor = r.CanOn, r.CanLevel, r.CanTemp, r.Sensor
		if now.After(dev.pending) {
			dev.On, dev.Level = r.On, r.Level
		}
		dev.Need, dev.Online, dev.tp = shubNeedNothing, true, r
	}
	for id, dev := range shub.devices {
		if dev.base == base && !seen[id] {
			delete(shub.devices, id)
			shub.dirty = true
		}
	}
}

// shubApplyCast puts one Cast receiver's status into its tile.
func shubApplyCast(g gcastFound, st gcastStatus, err error) {
	now := time.Now()
	shub.mu.Lock()
	defer shub.mu.Unlock()
	if shub.gc[g.ID] != g {
		return
	}
	dev := shub.devices[g.ID]
	if dev == nil {
		dev = &shubDevice{ID: g.ID, Source: shubSourceGoogle, Level: -1}
		shub.devices[g.ID] = dev
	}
	dev.Kind, dev.Name, dev.Model, dev.cast, dev.CanLevel = g.Kind, g.Name, g.Model, g, true
	if err != nil {
		dev.Need, dev.Online = shubNeedOffline, false
		return
	}
	dev.Need, dev.Online = shubNeedNothing, true
	if now.After(dev.pending) {
		dev.Level, dev.Muted, dev.Player = st.Volume, st.Muted, st.Player
		dev.App, dev.Title = "", ""
		if !st.Idle {
			dev.App, dev.Title = st.App, st.Title
		}
	}
}

// shubHAKind is a Home Assistant domain's kind.
func shubHAKind(domain string) string {
	switch domain {
	case "light":
		return shubKindBulb
	case "fan":
		return shubKindFan
	case "cover":
		return shubKindCover
	}
	return shubKindSwitch
}

// shubApplyHA puts Home Assistant's entities in; a failed read keeps the last
// list (marked offline) and says why.
func shubApplyHA(ents []haEntity, err error) {
	now := time.Now()
	shub.mu.Lock()
	defer shub.mu.Unlock()
	if !shub.haOn {
		return
	}
	if err != nil {
		shub.haErr = err.Error()
		for _, dev := range shub.devices {
			if dev.Source == shubSourceHA {
				dev.Online, dev.Need = false, shubNeedOffline
			}
		}
		return
	}
	shub.haErr = ""
	seen := make(map[string]bool, len(ents))
	for _, e := range ents {
		id := "ha:" + e.EntityID
		seen[id] = true
		dev := shub.devices[id]
		if dev == nil {
			dev = &shubDevice{ID: id, Source: shubSourceHA, Level: -1}
			shub.devices[id] = dev
		}
		dev.Kind, dev.Name, dev.haDomain = shubHAKind(e.Domain), e.Name, e.Domain
		dev.CanOn, dev.CanLevel = true, e.Domain == "light"
		if now.After(dev.pending) {
			dev.On = haEntityOn(e.State)
		}
		dev.Online, dev.Need = !strings.EqualFold(e.State, "unavailable"), shubNeedNothing
		if !dev.Online {
			dev.Need = shubNeedOffline
		}
	}
	for id, dev := range shub.devices {
		if dev.Source == shubSourceHA && !seen[id] {
			delete(shub.devices, id)
		}
	}
}

// ── What the screens read ────────────────────────────────────────────────

// shubView is a consistent copy for one frame or one page.
type shubView struct {
	Devices  []shubDevice
	Hidden   []shubDevice
	Scanning bool
	Scanned  bool
	ScanAt   time.Time
	SignedIn bool
	User     string
	HAOn     bool
	HAURL    string
	HAErr    string
	Google   int
	TPLink   int
}

// shubSnapshot copies the hub for the screens.
func shubSnapshot() shubView {
	shub.mu.Lock()
	defer shub.mu.Unlock()
	shubLoadLocked()
	v := shubView{
		Scanning: shub.scanning, Scanned: shub.scanned, ScanAt: shub.scanAt, SignedIn: shub.creds.User != "",
		User: shub.creds.User, HAOn: shub.haOn, HAURL: shub.ha.HAURL, HAErr: shub.haErr, Google: len(shub.gc), TPLink: len(shub.tp),
	}
	for _, d := range shub.devices {
		if shub.hidden[d.ID] {
			v.Hidden = append(v.Hidden, *d)
		} else {
			v.Devices = append(v.Devices, *d)
		}
	}
	shubSortDevices(v.Devices)
	shubSortDevices(v.Hidden)
	return v
}

// shubSortDevices orders by name, then id, so tiles never swap places between
// two reads.
func shubSortDevices(list []shubDevice) {
	sort.Slice(list, func(i, j int) bool {
		a, b := strings.ToLower(list[i].Name), strings.ToLower(list[j].Name)
		if a != b {
			return a < b
		}
		return list[i].ID < list[j].ID
	})
}

// shubDeviceByID is one device's copy.
func shubDeviceByID(id string) (shubDevice, bool) {
	shub.mu.Lock()
	defer shub.mu.Unlock()
	shubLoadLocked()
	d, ok := shub.devices[id]
	if !ok {
		return shubDevice{}, false
	}
	return *d, true
}

// ── Commands ─────────────────────────────────────────────────────────────

// shubCommand changes a device on screen at once (change, under the lock),
// sends the command off the render thread (send), and puts the old state back
// with a toast when the device refuses.
func shubCommand(id string, change func(d *shubDevice), send func(ctx context.Context, d shubDevice, creds tplinkCreds, ha smartHomeSettings, tpd *tplinkDevice) error) {
	shub.mu.Lock()
	shubLoadLocked()
	dev, ok := shub.devices[id]
	if !ok {
		shub.mu.Unlock()
		return
	}
	before := *dev
	change(dev)
	dev.pending = time.Now().Add(shubPendingFor)
	cp, creds, ha, tpd := *dev, shub.creds, shub.ha, shub.tp[dev.base]
	shub.mu.Unlock()
	homeTabsTouch()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		err := send(ctx, cp, creds, ha, tpd)
		shub.mu.Lock()
		if d := shub.devices[id]; d != nil {
			if err != nil {
				d.On, d.Level, d.Muted, d.Player, d.App, d.Title = before.On, before.Level, before.Muted, before.Player, before.App, before.Title
				d.pending = time.Time{}
			} else {
				d.pending = time.Now().Add(1500 * time.Millisecond)
			}
		}
		shub.pollAt = time.Time{}
		shub.mu.Unlock()
		homeTabsTouch()
		if err != nil {
			ShowToast(cp.Name+": "+shubErrText(err), ToastKindWarning)
			return
		}
		trophyEvent(tkSmartHome)
	}()
}

// shubErrText is an error as the toast says it.
func shubErrText(err error) string {
	switch shubNeedOf(err) {
	case shubNeedSignIn:
		return "sign in with your TP-Link ID (Smart Home › Add devices › TP-Link)"
	case shubNeedTPAP:
		return errTPLinkTPAP.Error()
	}
	return err.Error()
}

// errShubNoDevice is the error of a row whose device went away.
var errShubNoDevice = errors.New("the device is gone — Find devices looks again")

// shubToggle switches a device on or off.
func shubToggle(id string) {
	var want bool
	shubCommand(id, func(d *shubDevice) {
		want = !d.On
		d.On = want
	}, func(ctx context.Context, d shubDevice, creds tplinkCreds, ha smartHomeSettings, tpd *tplinkDevice) error {
		switch d.Source {
		case shubSourceTPLink:
			if tpd == nil {
				return errShubNoDevice
			}
			return tpd.setOn(ctx, creds, d.tp, want)
		case shubSourceHA:
			return shubHAToggle(ctx, ha, d, want)
		case shubSourceGoogle:
			act := "pause"
			if want {
				act = "play"
			}
			return gcastDo(ctx, d.cast, act, 0)
		}
		return errShubNoDevice
	})
}

// shubSetLevel sets a light's brightness or a speaker's volume (0–100).
func shubSetLevel(id string, level int) {
	level = max(0, min(100, level))
	shubCommand(id, func(d *shubDevice) {
		d.Level = level
		if d.Source != shubSourceGoogle && level > 0 {
			d.On = true
		}
	}, func(ctx context.Context, d shubDevice, creds tplinkCreds, ha smartHomeSettings, tpd *tplinkDevice) error {
		switch d.Source {
		case shubSourceTPLink:
			if tpd == nil {
				return errShubNoDevice
			}
			return tpd.setLevel(ctx, creds, d.tp, level)
		case shubSourceGoogle:
			return gcastDo(ctx, d.cast, "volume", float64(level)/100)
		case shubSourceHA:
			return shubHACall(ctx, ha, "light", "turn_on", map[string]interface{}{"entity_id": strings.TrimPrefix(d.ID, "ha:"), "brightness_pct": level})
		}
		return errShubNoDevice
	})
}

// shubStepLevel moves brightness or volume by step points. A Home Assistant
// light whose level JukaHub does not know steps by Home Assistant's own
// brightness_step_pct.
func shubStepLevel(id string, step int) {
	d, ok := shubDeviceByID(id)
	if !ok {
		return
	}
	if d.Source == shubSourceGoogle && d.Level < 0 {
		// A guess could jump a quiet speaker to half volume.
		shubPollSoon()
		ShowToast("Reading "+d.Name+"'s volume first — try again in a moment", ToastKindInfo)
		return
	}
	if d.Source == shubSourceHA && d.Level < 0 {
		shubCommand(id, func(d *shubDevice) { d.On = true }, func(ctx context.Context, d shubDevice, _ tplinkCreds, ha smartHomeSettings, _ *tplinkDevice) error {
			return shubHACall(ctx, ha, "light", "turn_on", map[string]interface{}{"entity_id": strings.TrimPrefix(d.ID, "ha:"), "brightness_step_pct": step})
		})
		return
	}
	cur := d.Level
	if cur < 0 {
		cur = 50
	}
	next := cur + step
	if d.Source != shubSourceGoogle {
		next = max(1, next)
	}
	shubSetLevel(id, next)
}

// shubSetTemp sets a white light's colour temperature (TP-Link).
func shubSetTemp(id string, kelvin int) {
	shubCommand(id, func(d *shubDevice) { d.On = true }, func(ctx context.Context, d shubDevice, creds tplinkCreds, _ smartHomeSettings, tpd *tplinkDevice) error {
		if tpd == nil {
			return errShubNoDevice
		}
		return tpd.setTemp(ctx, creds, d.tp, kelvin)
	})
}

// shubCastDo runs a Cast command: "play", "pause", "stop", "mute", "unmute".
func shubCastDo(id, act string) {
	shubCommand(id, func(d *shubDevice) {
		switch act {
		case "play":
			d.Player = "PLAYING"
		case "pause":
			d.Player = "PAUSED"
		case "stop":
			d.Player, d.App, d.Title = "", "", ""
		case "mute", "unmute":
			d.Muted = act == "mute"
		}
	}, func(ctx context.Context, d shubDevice, _ tplinkCreds, _ smartHomeSettings, _ *tplinkDevice) error {
		return gcastDo(ctx, d.cast, act, 0)
	})
}

// shubSetHidden hides a device from the tab and the panel, or brings it back.
func shubSetHidden(id string, hidden bool) {
	shub.mu.Lock()
	shubLoadLocked()
	if hidden {
		shub.hidden[id] = true
	} else {
		delete(shub.hidden, id)
	}
	shub.dirty = true
	shub.mu.Unlock()
	homeTabsTouch()
	go shubSave(false)
}

// ── Home Assistant calls ─────────────────────────────────────────────────

// shubHAToggle toggles an entity the way the panel does (and mirrors the new
// state to MQTT when a broker is set).
func shubHAToggle(ctx context.Context, s smartHomeSettings, d shubDevice, on bool) error {
	entity := strings.TrimPrefix(d.ID, "ha:")
	if err := callHAService(ctx, s, entity); err != nil {
		return err
	}
	if s.hasMQTT() {
		payload := "OFF"
		switch {
		case d.haDomain == "cover" && on:
			payload = "OPEN"
		case d.haDomain == "cover":
			payload = "CLOSED"
		case on:
			payload = "ON"
		}
		if err := mqttPublishTo(s, mqttTopicFor(s.MQTTPFX, entity), payload); err != nil {
			logInfo("smarthome", "MQTT mirror failed (%v)", err)
		}
	}
	return nil
}

// shubHACall posts one Home Assistant service call, with the token, the way
// callHAService posts a toggle.
func shubHACall(ctx context.Context, s smartHomeSettings, domain, service string, body map[string]interface{}) error {
	if s.HAToken == "" {
		return errors.New("Home Assistant is not connected")
	}
	base, err := smartHomeEndpoint(s.HAURL)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/services/"+domain+"/"+service, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.HAToken)
	req.Header.Set("Content-Type", "application/json")
	client := smartHomeHTTP
	if !s.HAVerify {
		client = smartHomeInsecureHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("Home Assistant unreachable")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized:
		return errors.New("Home Assistant rejected the token")
	}
	return errors.New("Home Assistant answered " + strconv.Itoa(resp.StatusCode))
}

// ── The TP-Link ID ───────────────────────────────────────────────────────

// shubCleanUser checks a TP-Link ID typed or pasted (outside data).
func shubCleanUser(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" || len(u) > 254 || !strings.Contains(u, "@") || strings.ContainsAny(u, " \t\r\n") {
		return "", errors.New("a TP-Link ID is the e-mail address of your Tapo or Kasa account")
	}
	return u, nil
}

// shubSignIn checks a TP-Link ID with a device that needs one (when one is
// known) and keeps it. Any goroutine; it talks to the network.
func shubSignIn(ctx context.Context, user, pass string) (string, error) {
	user, err := shubCleanUser(user)
	if err != nil {
		return "", err
	}
	if pass == "" || len(pass) > 256 {
		return "", errors.New("type the password of your TP-Link ID")
	}
	creds := tplinkCreds{User: user, Pass: pass}
	// The check: a fresh connection (no shared session) to a device that
	// takes an account, offered this account and nothing else.
	shub.mu.Lock()
	shubLoadLocked()
	var probe *tplinkFound
	for _, d := range shub.tp {
		if d.found.Transport != tplinkKLAP && d.found.Transport != tplinkAES {
			continue
		}
		f := d.found
		f.sysinfo = nil
		needs := false
		for _, dev := range shub.devices {
			if dev.base == f.ID && dev.Need == shubNeedSignIn {
				needs = true
			}
		}
		if probe == nil || needs {
			probe = &f
		}
		if needs {
			break
		}
	}
	shub.mu.Unlock()
	note := "Saved. JukaHub uses it as soon as it finds a Tapo or newer Kasa device."
	if probe != nil {
		check := &tplinkDevice{found: *probe, onlyUser: true}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := check.state(cctx, creds)
		cancel()
		switch {
		case err == nil:
			note = "Signed in — " + tplinkModelName(probe.Type, probe.Model) + " accepted your TP-Link ID."
		case errors.Is(err, errTPLinkAuth):
			return "", errors.New("your " + tplinkModelName(probe.Type, probe.Model) + " did not accept that TP-Link ID or password — check them in the Tapo or Kasa app")
		case errors.Is(err, errTPLinkTPAP):
		default:
			note = "Saved. The device used to check it did not answer, so it was not checked yet."
		}
	}
	enc, err := EncryptAPIToken(pass, shubPassKeyName)
	if err != nil || enc == "" {
		return "", errors.New("the handheld could not store the password")
	}
	shub.mu.Lock()
	shub.creds, shub.passEnc, shub.dirty = creds, enc, true
	shub.pollAt = time.Time{}
	shub.mu.Unlock()
	shubSave(true)
	homeTabsTouch()
	return note, nil
}

// shubSignOut forgets the TP-Link ID.
func shubSignOut() {
	shub.mu.Lock()
	shubLoadLocked()
	shub.creds, shub.passEnc, shub.dirty = tplinkCreds{}, "", true
	shub.pollAt = time.Time{}
	shub.mu.Unlock()
	go shubSave(true)
	homeTabsTouch()
}

// shubMaskUser shows an e-mail address with most of its name hidden.
func shubMaskUser(u string) string {
	name, domain, ok := strings.Cut(u, "@")
	if !ok || name == "" {
		return u
	}
	r := []rune(name)
	if len(r) <= 2 {
		return string(r[:1]) + "…@" + domain
	}
	return string(r[:2]) + "…@" + domain
}

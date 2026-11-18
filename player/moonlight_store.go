package main

// moonlight_store.go — the PCs JukaHub streams from with Moonlight.
//
// A GameStream host (Vibepollo, Apollo, Sunshine or GeForce Experience) is
// known by the uniqueid its serverinfo reports, and that id is the key of
// DataDir/moonlight-hosts.json. Beside the addresses and ports the file keeps
// the PC's certificate, pinned at pairing (moonlight_pair.go): every HTTPS
// request afterwards accepts that certificate and no other. The stream
// settings live per PC, because a desktop at home and a laptop over a VPN want
// different ones.
//
// The file is versioned like psremote.json, written atomically under one
// mutex (a refresh, a pairing and the panel may all save at once), and a
// damaged file reads as its .bak or, failing that, as no PCs at all.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	mlHostsFileVer      = 1
	mlHostsMax          = 32
	mlHTTPPortDefault   = 47989
	mlHTTPSPortDefault  = 47984
	mlDefaultVolume     = 80
	mlHostsFileName     = "moonlight-hosts.json"
	mlDefaultScreenW    = 1280
	mlDefaultScreenH    = 720
	mlCustomMinWidth    = 256
	mlCustomMaxWidth    = 3840
	mlCustomMinHeight   = 144
	mlCustomMaxHeight   = 2160
	mlCustomMinFPS      = 10
	mlCustomMaxFPS      = 120
	mlCustomMinBitrate  = 500
	mlCustomMaxBitrate  = 150000
	mlCodecDefault      = "h264"
	mlCodecHEVC         = "h265"
	mlCodecAutoSetting  = "auto"
	mlPresetBalanced    = "balanced"
	mlPresetSteady30    = "steady30"
	mlPresetLight       = "light"
	mlPresetSharp       = "sharp"
	mlPresetCustom      = "custom"
	mlPresetDefaultName = mlPresetBalanced
)

// mlStreamSettings is how one PC is streamed. Preset is "balanced" (the
// default), "steady30", "light", "sharp" or "custom"; Width, Height, FPS and
// BitrateKbps only count for custom. Codec is "h264" (the default: the
// cheapest to decode in software), "h265" or "auto". VirtualDisplay nil means
// on: /launch asks Apollo / Vibepollo for a virtual screen of exactly the
// stream's size. QuitOnExit sends /cancel after every stream. Volume 0 is the
// default, 80.
type mlStreamSettings struct {
	Preset         string `json:"preset,omitempty"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
	FPS            int    `json:"fps,omitempty"`
	BitrateKbps    int    `json:"bitrate_kbps,omitempty"`
	Codec          string `json:"codec,omitempty"`
	VirtualDisplay *bool  `json:"virtual_display,omitempty"`
	PlayAudioOnPC  bool   `json:"play_audio_on_pc,omitempty"`
	QuitOnExit     bool   `json:"quit_on_exit,omitempty"`
	NoRumble       bool   `json:"no_rumble,omitempty"`
	Volume         int    `json:"volume,omitempty"`
}

// mlPresetNames lists the presets in the order the panel shows them.
func mlPresetNames() []string {
	return []string{mlPresetBalanced, mlPresetSteady30, mlPresetLight, mlPresetSharp, mlPresetCustom}
}

// mlPresetLabel is a preset as the panel prints it; an unknown name reads as
// the default.
func mlPresetLabel(preset string) string {
	switch mlNormalizePreset(preset) {
	case mlPresetSteady30:
		return "Steady 30"
	case mlPresetLight:
		return "Light"
	case mlPresetSharp:
		return "Sharp"
	case mlPresetCustom:
		return "Custom"
	}
	return "Balanced"
}

// mlNormalizePreset reads a stored preset name; anything unknown is balanced.
func mlNormalizePreset(preset string) string {
	p := strings.ToLower(strings.TrimSpace(preset))
	for _, name := range mlPresetNames() {
		if p == name {
			return p
		}
	}
	return mlPresetDefaultName
}

// Resolve is the stream mode the settings ask for on a screen of screenW x
// screenH (the handheld's panel): every preset but Light streams at the
// panel's own size, so the picture is never scaled on the handheld. Sizes are
// even, as the encoders want them. A PC whose preset was never chosen ("")
// streams at what the handheld check measured (mlRecommendedPreset), else
// Balanced; a chosen preset always wins.
func (s mlStreamSettings) Resolve(screenW, screenH int) (w, h, fps, kbps int) {
	if screenW <= 0 || screenH <= 0 {
		screenW, screenH = mlDefaultScreenW, mlDefaultScreenH
	}
	w, h = screenW&^1, screenH&^1
	preset := mlNormalizePreset(s.Preset)
	if strings.TrimSpace(s.Preset) == "" {
		if rec := mlRecommendedPreset(screenW, screenH); rec != "" {
			preset = rec
		}
	}
	switch preset {
	case mlPresetSteady30:
		return w, h, 30, 8000
	case mlPresetLight:
		return (screenW * 3 / 4) &^ 1, (screenH * 3 / 4) &^ 1, 60, 6000
	case mlPresetSharp:
		return w, h, 60, 15000
	case mlPresetCustom:
		cw, ch, cfps, ckbps := s.Width, s.Height, s.FPS, s.BitrateKbps
		if cw <= 0 || ch <= 0 {
			cw, ch = w, h
		}
		if cfps <= 0 {
			cfps = 60
		}
		if ckbps <= 0 {
			ckbps = 10000
		}
		cw = mlClamp(cw, mlCustomMinWidth, mlCustomMaxWidth) &^ 1
		ch = mlClamp(ch, mlCustomMinHeight, mlCustomMaxHeight) &^ 1
		return cw, ch, mlClamp(cfps, mlCustomMinFPS, mlCustomMaxFPS), mlClamp(ckbps, mlCustomMinBitrate, mlCustomMaxBitrate)
	}
	return w, h, 60, 10000
}

// UseVirtualDisplay reports whether /launch asks for a virtual screen (on
// unless switched off).
func (s mlStreamSettings) UseVirtualDisplay() bool {
	return s.VirtualDisplay == nil || *s.VirtualDisplay
}

// mlCodec is the codec the session file asks for: h264 unless the setting
// says h265 or auto.
func (s mlStreamSettings) mlCodec() string {
	switch strings.ToLower(strings.TrimSpace(s.Codec)) {
	case "h265", "h.265", "hevc", "x265":
		return mlCodecHEVC
	case "auto":
		return mlCodecAutoSetting
	}
	return mlCodecDefault
}

// mlVolume is the stream's volume, 1-100 (0 is the default, 80).
func (s mlStreamSettings) mlVolume() int {
	if s.Volume <= 0 {
		return mlDefaultVolume
	}
	return mlClamp(s.Volume, 1, 100)
}

func mlClamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// mlHost is one PC. UUID (serverinfo uniqueid) is the store's key. LocalAddr
// is the LAN address that last answered or was discovered; ManualAddr is one
// the user typed (a VPN or internet address, or a host name) and is tried
// first; ExternalAddr is the PC's own idea of its internet address. Addresses
// are kept without a port: HTTPPort and HTTPSPort (0 = 47989 / 47984) say
// where. ServerCert is the PEM pinned at pairing; ServerKind is a display
// guess ("Vibepollo / Apollo", "Sunshine", "GeForce Experience").
type mlHost struct {
	UUID                  string           `json:"uuid"`
	Name                  string           `json:"name"`
	LocalAddr             string           `json:"local_addr,omitempty"`
	ManualAddr            string           `json:"manual_addr,omitempty"`
	ExternalAddr          string           `json:"external_addr,omitempty"`
	HTTPPort              int              `json:"http_port,omitempty"`
	HTTPSPort             int              `json:"https_port,omitempty"`
	MAC                   string           `json:"mac,omitempty"`
	ServerCert            string           `json:"server_cert,omitempty"`
	Paired                bool             `json:"paired,omitempty"`
	ServerKind            string           `json:"server_kind,omitempty"`
	VirtualDisplayCapable bool             `json:"vd_capable,omitempty"`
	LastAppID             int              `json:"last_app_id,omitempty"`
	LastAppName           string           `json:"last_app_name,omitempty"`
	LastSeen              time.Time        `json:"last_seen,omitempty"`
	Settings              mlStreamSettings `json:"settings"`
}

// Label is what the panel prints for the PC: its name, else an address.
func (h mlHost) Label() string {
	for _, s := range []string{h.Name, h.ManualAddr, h.LocalAddr, h.ExternalAddr} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return "PC"
}

// mlHTTPPortOf and mlHTTPSPortOf are the host's ports with the defaults filled in.
func mlHTTPPortOf(h mlHost) int {
	if h.HTTPPort > 0 && h.HTTPPort < 65536 {
		return h.HTTPPort
	}
	return mlHTTPPortDefault
}

func mlHTTPSPortOf(h mlHost) int {
	if h.HTTPSPort > 0 && h.HTTPSPort < 65536 {
		return h.HTTPSPort
	}
	return mlHTTPSPortDefault
}

// mlAddresses lists the host's addresses in the order they are tried:
// typed, LAN, internet; each once.
func mlAddresses(h mlHost) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range []string{h.ManualAddr, h.LocalAddr, h.ExternalAddr} {
		a = mlBareHost(a)
		if a == "" || seen[strings.ToLower(a)] {
			continue
		}
		seen[strings.ToLower(a)] = true
		out = append(out, a)
	}
	return out
}

// ── The file ─────────────────────────────────────────────────────────────

type mlHostsFile struct {
	Version int      `json:"version"`
	Hosts   []mlHost `json:"hosts"`
}

// mlHostsPathOverride replaces the store's path in tests.
var mlHostsPathOverride string

func mlHostsPath() string {
	if p := strings.TrimSpace(mlHostsPathOverride); p != "" {
		return p
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, mlHostsFileName)
	}
	return mlHostsFileName
}

// mlHostsMu serialises every load-change-save of moonlight-hosts.json, so a
// pairing saved by one goroutine is never written over by another's older
// copy.
var mlHostsMu sync.Mutex

var errMLNoHostID = errors.New("a PC without an id cannot be saved")

// mlReadHostsLocked reads the store (caller holds mlHostsMu). Entries without
// an id are dropped, a repeated id keeps its first entry, and a pairing
// without its certificate is not one.
func mlReadHostsLocked() []mlHost {
	var file mlHostsFile
	if _, err := readJSONWithBackup(mlHostsPath(), &file); err != nil {
		return nil
	}
	seen := map[string]bool{}
	hosts := make([]mlHost, 0, len(file.Hosts))
	for _, h := range file.Hosts {
		h.UUID = strings.TrimSpace(h.UUID)
		if h.UUID == "" || seen[strings.ToLower(h.UUID)] {
			continue
		}
		seen[strings.ToLower(h.UUID)] = true
		if strings.TrimSpace(h.ServerCert) == "" {
			h.Paired = false
		}
		hosts = append(hosts, h)
	}
	return hosts
}

// mlWriteHostsLocked saves the store (caller holds mlHostsMu): 0600, through
// AtomicWrite, at most mlHostsMax PCs (the paired and most recently seen kept).
func mlWriteHostsLocked(hosts []mlHost) error {
	if len(hosts) > mlHostsMax {
		sort.SliceStable(hosts, func(i, j int) bool {
			if hosts[i].Paired != hosts[j].Paired {
				return hosts[i].Paired
			}
			return hosts[i].LastSeen.After(hosts[j].LastSeen)
		})
		hosts = hosts[:mlHostsMax]
	}
	data, err := json.MarshalIndent(mlHostsFile{Version: mlHostsFileVer, Hosts: hosts}, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(mlHostsPath(), append(data, '\n'), 0o600)
}

// mlSortHosts orders the list for the panel: paired first, then by name.
func mlSortHosts(hosts []mlHost) {
	sort.SliceStable(hosts, func(i, j int) bool {
		a, b := hosts[i], hosts[j]
		if a.Paired != b.Paired {
			return a.Paired
		}
		la, lb := strings.ToLower(a.Label()), strings.ToLower(b.Label())
		if la != lb {
			return la < lb
		}
		return a.UUID < b.UUID
	})
}

// mlLoadHosts returns the stored PCs, paired first, then by name.
func mlLoadHosts() []mlHost {
	mlHostsMu.Lock()
	hosts := mlReadHostsLocked()
	mlHostsMu.Unlock()
	mlSortHosts(hosts)
	return hosts
}

// mlStoredHost returns the stored PC with this id.
func mlStoredHost(uuid string) (mlHost, bool) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return mlHost{}, false
	}
	for _, h := range mlLoadHosts() {
		if strings.EqualFold(h.UUID, uuid) {
			return h, true
		}
	}
	return mlHost{}, false
}

// mlSaveHost upserts the PC by its id. The pairing of a PC already stored
// (ServerCert, Paired) is kept as it is on disk: only mlPair, mlUnpair and
// mlRefreshHost change it, so a copy the panel loaded before a pairing cannot
// undo it.
func mlSaveHost(h mlHost) error {
	h.UUID = strings.TrimSpace(h.UUID)
	if h.UUID == "" {
		return errMLNoHostID
	}
	mlHostsMu.Lock()
	defer mlHostsMu.Unlock()
	hosts := mlReadHostsLocked()
	for i, old := range hosts {
		if strings.EqualFold(old.UUID, h.UUID) {
			h.UUID = old.UUID
			h.ServerCert, h.Paired = old.ServerCert, old.Paired
			hosts[i] = h
			return mlWriteHostsLocked(hosts)
		}
	}
	if strings.TrimSpace(h.ServerCert) == "" {
		h.Paired = false
	}
	hosts = append(hosts, h)
	return mlWriteHostsLocked(hosts)
}

// mlUpdateHost applies change to the stored PC with this id and saves it. It
// reports whether that PC is stored; nothing is written when it is not.
func mlUpdateHost(uuid string, change func(*mlHost)) (mlHost, bool, error) {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return mlHost{}, false, nil
	}
	mlHostsMu.Lock()
	defer mlHostsMu.Unlock()
	hosts := mlReadHostsLocked()
	for i := range hosts {
		if strings.EqualFold(hosts[i].UUID, uuid) {
			change(&hosts[i])
			if strings.TrimSpace(hosts[i].ServerCert) == "" {
				hosts[i].Paired = false
			}
			// Copied first: the write may sort hosts in place (more than
			// mlHostsMax), and a return's operands have no fixed order.
			h := hosts[i]
			return h, true, mlWriteHostsLocked(hosts)
		}
	}
	return mlHost{}, false, nil
}

// mlPutPairedHost stores a freshly paired PC: its pairing and what the PC
// said about itself over the stored entry (whose settings stay), or the whole
// host when it is new.
func mlPutPairedHost(h mlHost) (mlHost, error) {
	mlHostsMu.Lock()
	defer mlHostsMu.Unlock()
	hosts := mlReadHostsLocked()
	for i, old := range hosts {
		if strings.EqualFold(old.UUID, h.UUID) {
			merged := old
			mlMergeSeen(&merged, h)
			merged.ServerCert, merged.Paired = h.ServerCert, h.Paired
			hosts[i] = merged
			return merged, mlWriteHostsLocked(hosts)
		}
	}
	hosts = append(hosts, h)
	return h, mlWriteHostsLocked(hosts)
}

// mlMergeSeen copies what a PC says about itself (name, addresses, ports,
// MAC, kind, last seen) from src into dst; empty values keep dst's.
func mlMergeSeen(dst *mlHost, src mlHost) {
	if s := strings.TrimSpace(src.Name); s != "" {
		dst.Name = s
	}
	if s := mlBareHost(src.LocalAddr); s != "" {
		dst.LocalAddr = s
	}
	if s := mlBareHost(src.ManualAddr); s != "" {
		dst.ManualAddr = s
	}
	if s := mlBareHost(src.ExternalAddr); s != "" {
		dst.ExternalAddr = s
	}
	if src.HTTPPort > 0 {
		dst.HTTPPort = src.HTTPPort
	}
	if src.HTTPSPort > 0 {
		dst.HTTPSPort = src.HTTPSPort
	}
	if s := strings.TrimSpace(src.MAC); s != "" {
		dst.MAC = s
	}
	if s := strings.TrimSpace(src.ServerKind); s != "" {
		dst.ServerKind = s
		dst.VirtualDisplayCapable = src.VirtualDisplayCapable
	}
	if !src.LastSeen.IsZero() {
		dst.LastSeen = src.LastSeen
	}
}

// mlRemoveHost forgets the PC and its cached box art. It forgets the pairing
// here only: nothing is sent to the PC (mlUnpair does that).
func mlRemoveHost(uuid string) error {
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return errMLNoHostID
	}
	mlHostsMu.Lock()
	hosts := mlReadHostsLocked()
	kept := hosts[:0]
	found := false
	for _, h := range hosts {
		if strings.EqualFold(h.UUID, uuid) {
			found = true
			continue
		}
		kept = append(kept, h)
	}
	var err error
	if found {
		err = mlWriteHostsLocked(kept)
	}
	mlHostsMu.Unlock()
	mlForgetBoxArt(uuid)
	return err
}

// mlForgetBoxArt deletes the cached box art of one PC.
func mlForgetBoxArt(uuid string) {
	dir := mlBoxArtDir()
	prefix := mlFileSafe(uuid) + "-"
	if dir == "" || prefix == "-" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// mlFileSafe keeps the letters, digits and dashes of an id for a file name.
func mlFileSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	return b.String()
}

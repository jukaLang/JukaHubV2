package main

// moonlight_ui.go — PC streaming (Moonlight): the panel behind the header's
// "PC" pill, Start → Games → PC streaming (Moonlight), the Games tab and Home
// search.
//
// JukaHub's own GameStream client (moonlight_*.go) plays games from a PC that
// runs Vibepollo, Apollo or Sunshine. The panel has four pages:
//
//   - PCs: every PC the store knows and the ones a network search finds, with
//     whether each answers, is paired or is playing something. A opens one
//     (its games once paired, the PIN otherwise), X wakes it, Y opens its
//     settings; rows add a PC by address and search again.
//   - Pair: a 4-digit PIN to type on the PC's web page (PIN tab), with a QR
//     code of that page for the phone. The exchange waits on a goroutine
//     until the PIN is typed there; B cancels it.
//   - Games: the PC's apps as a grid of box art. A plays (or resumes the one
//     running), X quits the running game on the PC, L1/R1 step to the next
//     paired PC.
//   - Settings: quality, codec, the virtual display Apollo and Vibepollo make
//     at exactly this screen's size, sound, rumble, volume, a typed address
//     and Unpair.
//
// Every network call runs on a goroutine and comes back through tbxPost; a
// generation number per page drops an answer for a page that was left. The
// drawing is in moonlight_ui_draw.go.

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/veandco/go-sdl2/sdl"
)

const (
	// moonlightEntryName is the Start menu row (Games), the Games tab tile
	// and the Home search result.
	moonlightEntryName = "PC streaming (Moonlight)"
	// pcPillLabel is the pill's name in the header's Remote Play group.
	pcPillLabel = "PC"
	// mlDesktopClientURL is where Moonlight PC comes from: Nonary's build adds
	// VRR and pairs best with Vibepollo (his Apollo fork).
	mlDesktopClientURL = "https://github.com/Nonary/moonlight-qt/releases"
	// mlPollEvery is how often the PC list looks at its PCs again.
	mlPollEvery = 15 * time.Second
	// mlWakeFor is how long a woken PC is looked for.
	mlWakeFor = 40 * time.Second
	// mlPairTimeout bounds the wait for the PIN to be typed on the PC.
	mlPairTimeout = 3 * time.Minute
)

// mlAddrVKTarget aims the on-screen keyboard at a PC address: one case per
// keyboard hook (vkCurrentText, vkSetText, vkTitle, vkPlaceholder,
// vkActionLabel, vkSubmit, vkCloseKeyboard, vkStripModeNow). Submit-only:
// nothing is asked of the network or saved until Done.
const mlAddrVKTarget vkTargetKind = 173

type mlPanelPage int

const (
	mlPageHosts mlPanelPage = iota
	mlPagePair
	mlPageApps
	mlPageSettings
)

// mlHostView is one PC on the list: the stored host and what the last look
// at it found.
type mlHostView struct {
	host    mlHost
	info    mlServerInfo
	addr    string // the address that answered last
	online  bool
	checked bool // a look came back, either way
	busy    bool // a look is running
	err     string
}

type mlRowKind int

const (
	mlRowHost mlRowKind = iota
	mlRowAdd
	mlRowSearch
	mlRowLast
	mlRowEmpty
	// mlRowCheck runs the handheld check (moonlight_selftest.go).
	mlRowCheck
)

// mlRow is one row of the PC list.
type mlRow struct {
	kind   mlRowKind
	host   int // index into mlPanel.hosts for mlRowHost
	title  string
	detail string
	status string
}

// mlSetRow is one row of a PC's settings page.
type mlSetRow int

const (
	mlSetQuality mlSetRow = iota
	mlSetWidth
	mlSetHeight
	mlSetFPS
	mlSetBitrate
	mlSetCodec
	mlSetVirtual
	mlSetPCAudio
	mlSetQuitOnExit
	mlSetRumble
	mlSetVolume
	mlSetAddress
	mlSetUnpair
)

type mlConfirmKind int

const (
	mlConfirmNone mlConfirmKind = iota
	mlConfirmQuit
	mlConfirmUnpair
)

type mlNoticeKind int

const (
	mlNoticeNone mlNoticeKind = iota
	// mlNoticeHelper: the handheld's streaming helper is not installed.
	mlNoticeHelper
	// mlNoticeDesktop: no Moonlight PC on this computer.
	mlNoticeDesktop
)

// mlVKMode says what the address keyboard is for.
type mlVKMode int

const (
	mlVKAdd mlVKMode = iota
	mlVKEdit
)

type mlPanelState struct {
	open   bool
	page   mlPanelPage
	gen    int
	cursor int
	scroll int

	// ctx lives as long as the panel: closing it (stop) cancels every look,
	// search, list read and launch still running for it.
	ctx  context.Context
	stop context.CancelFunc

	hosts       []mlHostView
	discovering bool
	probing     int // typed or found addresses being asked
	pollAt      time.Time

	// The PC whose Wake-on-LAN packets went out, until when it is looked for
	// and when the next look goes.
	wakeUUID  string
	wakeUntil time.Time
	wakeNext  time.Time

	// uuid is the PC the pair, games and settings pages are about.
	uuid string

	pairGen    int
	pairPIN    string
	pairBusy   bool
	pairErr    string
	pairCancel context.CancelFunc
	pairLink   string
	pairQR     [][]bool // the symbol, drawn on the page
	pairQRFull [][]bool // with its quiet zone, for the full-screen card

	appsGen    int
	apps       []mlApp
	appsBusy   bool
	appsLoaded bool
	appsErr    string
	appCursor  int
	appScroll  int // first grid row on screen
	appCols    int
	artPath    map[int]string // "" = the PC has no art for that app
	artCancel  context.CancelFunc
	infoAt     time.Time // last look at the PC from the games page
	quitting   bool

	// starting is the "Starting … on …" card while a launch runs; B cancels
	// it through startCancel, and startGen drops the answer it still sends.
	// startCheck: the card is for the handheld check, not a game.
	starting    string
	startGen    int
	startCancel context.CancelFunc
	startCheck  bool
	confirm     mlConfirmKind
	notice      mlNoticeKind
	helperBusy  bool
	helperMsg   string

	setCursor int
	setScroll int
	setBack   mlPanelPage

	// What this machine streams with, read when the panel opens.
	desktopName string
	helperReady bool
	lastNote    string
	lastKind    ToastKind
}

var mlPanel mlPanelState

// mlGenSeq numbers every panel opening and every page load, so an answer for
// a page or a panel that is gone is never applied.
var mlGenSeq int

type mlHitKind int

const (
	mlHitRow mlHitKind = iota
	mlHitTile
	mlHitQR
	mlHitPrimary   // A button of the confirm or notice card
	mlHitSecondary // B button of the confirm or notice card
)

type mlHit struct {
	kind  mlHitKind
	index int
	rect  sdl.Rect
}

var (
	mlPanelRect   sdl.Rect
	mlHits        []mlHit
	mlVisibleRows int
)

// mlVKBuffer holds the typed address; read on Done only.
var (
	mlVKBuffer string
	mlVKFor    mlVKMode
)

// ── The header pill ──────────────────────────────────────────────────────

// pcTileRect is this frame's PC pill; cleared when it is not drawn, so a
// stale rect cannot swallow clicks.
var pcTileRect sdl.Rect

// mlPillCache keeps the pill's dot from reading the store on the render
// thread: a goroutine reads it at most every 5 s, and gen drops an answer
// read before a pairing or an unpair changed it.
var mlPillCache struct {
	at   time.Time
	on   bool
	busy bool
	gen  int
}

// pcPillSupported reports whether the PC pill is drawn. Every build can pair
// a PC and list its games; Play needs the handheld helper or Moonlight PC,
// which the panel offers when missing.
func pcPillSupported() bool { return true }

// pcTileOn is the pill's lit dot: a paired PC exists.
func pcTileOn() bool {
	if !mlPillCache.busy && (mlPillCache.at.IsZero() || time.Since(mlPillCache.at) > 5*time.Second) {
		mlPillCache.busy = true
		gen := mlPillCache.gen
		go func() {
			on := false
			for _, h := range mlLoadHosts() {
				if h.Paired {
					on = true
					break
				}
			}
			tbxPost(func() {
				mlPillCache.busy = false
				if gen == mlPillCache.gen {
					mlPillCache.on, mlPillCache.at = on, time.Now()
				}
			})
		}()
	}
	return mlPillCache.on
}

// mlPillInvalidate makes the dot look again (a pairing or an unpair).
func mlPillInvalidate() {
	mlPillCache.at = time.Time{}
	mlPillCache.gen++
}

// ── Saving, off the main loop ────────────────────────────────────────────

// mlStorePatch is what the panel changed on one PC and has not written to
// moonlight-hosts.json yet. A later change to a field replaces the earlier
// one, so the latest state wins and a run of D-pad presses is one write.
type mlStorePatch struct {
	label    string
	add      *mlHost // a PC new to the store: saved whole first
	settings *mlStreamSettings
	addrSet  bool // manual and port hold the typed address and its port
	manual   string
	port     int
	local    string // the LAN address a search found it at
}

// mlStore is the one ordered save worker: q is drained by a single
// goroutine started once, pending holds each PC's latest patch until its
// turn. The panel's own copy is changed at once; only the disk waits.
var mlStore struct {
	once    sync.Once
	q       chan func()
	mu      sync.Mutex
	pending map[string]*mlStorePatch
}

// mlStoreErrAt keeps a failing SD card from toasting on every change.
var mlStoreErrAt time.Time

func mlStoreStart() {
	mlStore.once.Do(func() {
		mlStore.q = make(chan func(), 64)
		mlStore.pending = map[string]*mlStorePatch{}
		go func() {
			for fn := range mlStore.q {
				fn()
			}
		}()
	})
}

// mlStoreQueue merges edit into the PC's pending patch and queues its write
// when none is waiting. Main thread.
func mlStoreQueue(uuid, label string, edit func(p *mlStorePatch)) {
	if strings.TrimSpace(uuid) == "" {
		return
	}
	mlStoreStart()
	mlStore.mu.Lock()
	p, waiting := mlStore.pending[uuid]
	if !waiting {
		p = &mlStorePatch{}
		mlStore.pending[uuid] = p
	}
	p.label = label
	edit(p)
	mlStore.mu.Unlock()
	if !waiting {
		mlStore.q <- func() { mlStoreFlush(uuid) }
	}
}

// mlStoreFlush writes one PC's patch (worker goroutine).
func mlStoreFlush(uuid string) {
	mlStore.mu.Lock()
	p := mlStore.pending[uuid]
	delete(mlStore.pending, uuid)
	mlStore.mu.Unlock()
	if p == nil {
		return
	}
	err := p.apply(uuid)
	if err == nil {
		return
	}
	logWarn("moonlight", "saving %s failed: %v", p.label, err)
	label := p.label
	tbxPost(func() {
		if !mlStoreErrAt.IsZero() && time.Since(mlStoreErrAt) < time.Minute {
			return
		}
		mlStoreErrAt = time.Now()
		PlayErrorSound()
		ShowToast("Could not save the changes to "+label+" — "+firstLine(err.Error()), ToastKindError)
	})
}

// apply writes the patch. A PC that is not stored (forgotten meanwhile) is
// not re-created by a settings or address change: mlUpdateHost skips it.
func (p *mlStorePatch) apply(uuid string) error {
	if p.add != nil {
		if err := mlSaveHost(*p.add); err != nil {
			return err
		}
	}
	if p.settings == nil && !p.addrSet && p.local == "" {
		return nil
	}
	_, _, err := mlUpdateHost(uuid, p.over)
	return err
}

// over applies the patch's fields to h.
func (p *mlStorePatch) over(h *mlHost) {
	if p.settings != nil {
		h.Settings = *p.settings
	}
	if p.addrSet {
		h.ManualAddr, h.HTTPPort = p.manual, p.port
	}
	if p.local != "" {
		h.LocalAddr = p.local
	}
}

// mlStoreOverlay lays the writes still queued over hosts read from the
// disk, so a panel reopened at once shows its latest changes.
func mlStoreOverlay(hosts []mlHost) []mlHost {
	mlStore.mu.Lock()
	defer mlStore.mu.Unlock()
	listed := map[string]bool{}
	for i := range hosts {
		listed[hosts[i].UUID] = true
		if p := mlStore.pending[hosts[i].UUID]; p != nil {
			p.over(&hosts[i])
		}
	}
	for uuid, p := range mlStore.pending {
		if p.add != nil && !listed[uuid] {
			h := *p.add
			p.over(&h)
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// mlStoreSettings queues v's settings for the disk.
func mlStoreSettings(v *mlHostView) {
	s := v.host.Settings
	mlStoreQueue(v.host.UUID, v.host.Label(), func(p *mlStorePatch) { p.settings = &s })
}

// mlStoreAddr queues v's typed address and HTTP port for the disk.
func mlStoreAddr(v *mlHostView) {
	manual, port := v.host.ManualAddr, v.host.HTTPPort
	mlStoreQueue(v.host.UUID, v.host.Label(), func(p *mlStorePatch) {
		p.addrSet, p.manual, p.port = true, manual, port
	})
}

// mlStoreForget forgets a PC (and its box art) behind every write queued
// before it, so none of those brings it back. Blocks: call it off the main
// loop.
func mlStoreForget(uuid string) error {
	mlStoreStart()
	done := make(chan error, 1)
	mlStore.q <- func() { done <- mlRemoveHost(uuid) }
	return <-done
}

// pcTileActivate is A on the pill (or a click).
func pcTileActivate() {
	if gameScreenSuspended() {
		return
	}
	openMoonlightPanel()
}

// ── Open and close ───────────────────────────────────────────────────────

// openMoonlightPanel opens the PC list and looks for PCs.
func openMoonlightPanel() {
	// The other overlay panels close, as openSteamPanel closes them: the pad
	// and keys reach the Steam and PlayStation panels before this one.
	closeSteamPanel()
	closeChiakiPanel()
	qs.open = false
	toolsPanel.open = false
	emuPanel.open = false
	gpPanel.open = false
	raPanel.open = false
	portsPanel = portsPanelState{}
	winAppsPanel = windowsAppsPanelState{}
	mlPanelStop()
	mlGenSeq++
	mlPanel = mlPanelState{open: true, gen: mlGenSeq}
	mlPanel.ctx, mlPanel.stop = context.WithCancel(context.Background())
	mlResetHits()
	for _, h := range mlStoreOverlay(mlLoadHosts()) {
		mlPanel.hosts = append(mlPanel.hosts, mlHostView{host: h})
	}
	mlReadClients()
	mlPanel.lastNote, mlPanel.lastKind = mlLastRunNote()
	PlayActivateSound()
	mlRefreshAll(true)
}

// mlReadClients notes what Play streams with on this machine.
func mlReadClients() {
	mlPanel.desktopName, mlPanel.helperReady = "", false
	if mlOnHandheld() {
		mlPanel.helperReady = mlHelperPath() != ""
		return
	}
	if name, _, _, ok := mlDesktopClient(); ok {
		mlPanel.desktopName = name
		if mlPanel.desktopName == "" {
			mlPanel.desktopName = "Moonlight"
		}
	}
}

func mlResetHits() {
	mlPanelRect = sdl.Rect{}
	mlHits = nil
	mlVisibleRows = 0
}

// mlPanelCtx is the panel's lifetime, for the goroutines it starts. Read it
// on the main thread, before the go statement.
func mlPanelCtx() context.Context {
	if mlPanel.ctx == nil {
		return context.Background()
	}
	return mlPanel.ctx
}

// mlPanelStop cancels what runs for the panel and frees its textures.
func mlPanelStop() {
	if mlPanel.pairCancel != nil {
		mlPanel.pairCancel()
		mlPanel.pairCancel = nil
	}
	if mlPanel.startCancel != nil {
		mlPanel.startCancel()
		mlPanel.startCancel = nil
	}
	if mlPanel.stop != nil {
		mlPanel.stop()
		mlPanel.stop = nil
	}
	mlArtStop()
	mlArtFree()
	if virtualKeyboardActive && vkTarget == mlAddrVKTarget {
		vkCloseKeyboard()
	}
}

func closeMoonlightPanel() {
	if !mlPanel.open {
		return
	}
	mlPanelStop()
	mlGenSeq++
	mlPanel = mlPanelState{}
	mlResetHits()
	mlPillInvalidate()
	PlayBackSound()
}

// ── Looking at PCs ───────────────────────────────────────────────────────

func mlHostIndex(uuid string) int {
	if uuid == "" {
		return -1
	}
	for i := range mlPanel.hosts {
		if mlPanel.hosts[i].host.UUID == uuid {
			return i
		}
	}
	return -1
}

// mlCurrentHost is the PC the pair, games and settings pages are about.
func mlCurrentHost() (*mlHostView, bool) {
	i := mlHostIndex(mlPanel.uuid)
	if i < 0 {
		return nil, false
	}
	return &mlPanel.hosts[i], true
}

// mlHostOnly is addr without a port or IPv6 brackets.
func mlHostOnly(addr string) string {
	addr = strings.TrimSpace(addr)
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]")
}

// mlHostAddr is where to reach v: the address that answered last, else the
// typed one, the LAN one, the internet one.
func mlHostAddr(v mlHostView) string {
	for _, a := range []string{v.addr, v.host.ManualAddr, v.host.LocalAddr, v.host.ExternalAddr} {
		if strings.TrimSpace(a) != "" {
			return a
		}
	}
	return ""
}

// mlHostIndexByAddr finds a listed PC by an address a search found.
func mlHostIndexByAddr(addr string) int {
	want := strings.ToLower(mlHostOnly(addr))
	if want == "" {
		return -1
	}
	for i, v := range mlPanel.hosts {
		for _, a := range []string{v.addr, v.host.ManualAddr, v.host.LocalAddr, v.host.ExternalAddr} {
			if a != "" && strings.ToLower(mlHostOnly(a)) == want {
				return i
			}
		}
	}
	return -1
}

// mlRefreshAll looks at every listed PC again, and searches the network.
func mlRefreshAll(discover bool) {
	for i := range mlPanel.hosts {
		mlRefreshOne(mlPanel.hosts[i].host.UUID)
	}
	if discover {
		mlStartDiscovery()
	}
	mlPanel.pollAt = time.Now()
}

// mlRefreshOne asks one PC for its state in the background.
func mlRefreshOne(uuid string) {
	i := mlHostIndex(uuid)
	if i < 0 || mlPanel.hosts[i].busy {
		return
	}
	mlPanel.hosts[i].busy = true
	h := mlPanel.hosts[i].host
	gen := mlPanel.gen
	parent := mlPanelCtx()
	go func() {
		ctx, cancel := context.WithTimeout(parent, 20*time.Second)
		nh, info, addr, err := mlRefreshHost(ctx, h)
		cancel()
		tbxPost(func() { mlApplyRefresh(gen, h.UUID, nh, info, addr, err) })
	}()
}

// mlApplyRefresh takes a look's answer on the main loop.
func mlApplyRefresh(gen int, uuid string, nh mlHost, info mlServerInfo, addr string, err error) {
	if !mlPanel.open || mlPanel.gen != gen {
		return
	}
	i := mlHostIndex(uuid)
	if i < 0 {
		return
	}
	mlPanel.hosts[i].busy = false
	if err != nil {
		mlNoteOffline(i, err)
		return
	}
	mlMergeRefresh(i, nh, info, addr)
}

// mlNoteOffline marks a PC that did not answer. err is kept only when it
// says more than "not answering" (another PC there, a refusal), for the row.
func mlNoteOffline(i int, err error) {
	v := &mlPanel.hosts[i]
	v.checked = true
	v.online = false
	v.info.CurrentGame = 0
	v.err = ""
	if err != nil && !mlNoAnswer(err) && !errors.Is(err, context.Canceled) {
		v.err = firstLine(err.Error())
	}
}

// mlNoAnswer reports whether err only says the PC did not answer in time.
func mlNoAnswer(err error) bool {
	return errors.Is(err, errMLHostNotFound) || errors.Is(err, context.DeadlineExceeded)
}

// mlWhy is err as one line; "did not answer" reads the same everywhere.
func mlWhy(err error) string {
	if mlNoAnswer(err) {
		return firstLine(errMLHostNotFound.Error())
	}
	return firstLine(err.Error())
}

// mlMergeRefresh folds a PC's answer into the list. The settings, the typed
// address and its port stay the panel's (a change made while the look ran
// is kept); mlRefreshHost saved what the PC says about itself.
func mlMergeRefresh(i int, nh mlHost, info mlServerInfo, addr string) {
	v := &mlPanel.hosts[i]
	v.checked = true
	if nh.UUID != "" && nh.UUID != v.host.UUID {
		// Another PC answers at this address now.
		v.online = false
		v.err = "another PC answers at this address"
		return
	}
	old := v.host
	nh.UUID = old.UUID
	nh.Settings = old.Settings
	if nh.ManualAddr != old.ManualAddr || nh.HTTPPort != old.HTTPPort {
		// A look that started before an address edit wrote the old one back
		// to the disk: the panel's (the latest) goes there again.
		mlStoreAddr(v)
	}
	nh.ManualAddr, nh.HTTPPort = old.ManualAddr, old.HTTPPort
	// What one answer does not carry is kept: the pinned certificate above
	// all, which a save without it would lose.
	if nh.ServerCert == "" {
		nh.ServerCert = old.ServerCert
	}
	if nh.MAC == "" {
		nh.MAC = old.MAC
	}
	if nh.LocalAddr == "" {
		nh.LocalAddr = old.LocalAddr
	}
	if nh.ExternalAddr == "" {
		nh.ExternalAddr = old.ExternalAddr
	}
	if nh.LastAppID == 0 {
		nh.LastAppID, nh.LastAppName = old.LastAppID, old.LastAppName
	}
	v.host = nh
	v.info = info
	if addr != "" {
		v.addr = addr
	}
	v.online = true
	v.err = ""
	if nh.Paired != old.Paired {
		mlPillInvalidate()
	}
	if mlPanel.wakeUUID == nh.UUID {
		mlPanel.wakeUUID = ""
		PlaySuccessSound()
		ShowToast(nh.Label()+" is awake", ToastKindSuccess)
	}
}

// mlStartDiscovery searches the network for GameStream PCs (mDNS).
func mlStartDiscovery() {
	if mlPanel.discovering {
		return
	}
	mlPanel.discovering = true
	gen := mlPanel.gen
	parent := mlPanelCtx()
	go func() {
		ctx, cancel := context.WithTimeout(parent, 6*time.Second)
		found := mlDiscover(ctx, 3*time.Second)
		cancel()
		tbxPost(func() { mlApplyDiscovery(gen, found) })
	}()
}

// mlApplyDiscovery asks every PC the search found that is not on the list
// yet who it is; a known PC is looked at by its own refresh.
func mlApplyDiscovery(gen int, found []mlDiscovered) {
	if !mlPanel.open || mlPanel.gen != gen {
		return
	}
	mlPanel.discovering = false
	asked := map[string]bool{}
	for _, d := range found {
		addr := strings.TrimSpace(d.Addr)
		if addr == "" || asked[strings.ToLower(addr)] || mlHostIndexByAddr(addr) >= 0 {
			continue
		}
		asked[strings.ToLower(addr)] = true
		if d.Port > 0 && d.Port != mlHTTPPortDefault {
			addr = net.JoinHostPort(mlHostOnly(addr), strconv.Itoa(d.Port))
		}
		mlProbe(addr, false)
	}
}

// mlProbe asks an address (typed, or found by the search) for its PC.
func mlProbe(addr string, typed bool) {
	mlPanel.probing++
	gen := mlPanel.gen
	parent := mlPanelCtx()
	go func() {
		ctx, cancel := context.WithTimeout(parent, 8*time.Second)
		h, info, err := mlProbeAddress(ctx, addr)
		cancel()
		tbxPost(func() { mlApplyProbe(gen, addr, typed, h, info, err) })
	}()
}

// mlApplyProbe puts a PC that answered on the list (and in the store, as Not
// paired), or moves a listed PC to the address it answered at.
func mlApplyProbe(gen int, addr string, typed bool, h mlHost, info mlServerInfo, err error) {
	if !mlPanel.open || mlPanel.gen != gen {
		return
	}
	if mlPanel.probing > 0 {
		mlPanel.probing--
	}
	if err != nil || h.UUID == "" {
		if typed {
			PlayErrorSound()
			switch {
			case err == nil:
				ShowToast("Could not add "+addr+" — it did not say who it is", ToastKindError)
			case mlNoAnswer(err):
				ShowToast("No PC answered at "+addr+" · check that it is on and on this network", ToastKindError)
			default:
				ShowToast("Could not add "+addr+" — "+firstLine(err.Error()), ToastKindError)
			}
		}
		return
	}
	// mlProbeAddress put the address where it belongs (a LAN one in
	// LocalAddr, a VPN, internet or name one in ManualAddr, a port in
	// HTTPPort); calls take it bare.
	bare := mlHostOnly(addr)
	if i := mlHostIndex(h.UUID); i >= 0 {
		v := &mlPanel.hosts[i]
		if h.ManualAddr != "" {
			v.host.ManualAddr = h.ManualAddr
		}
		if h.LocalAddr != "" {
			v.host.LocalAddr = h.LocalAddr
		}
		if h.HTTPPort > 0 {
			v.host.HTTPPort = h.HTTPPort
		}
		v.addr = bare
		mlStoreAddr(v)
		if local := v.host.LocalAddr; local != "" {
			mlStoreQueue(v.host.UUID, v.host.Label(), func(p *mlStorePatch) { p.local = local })
		}
		if typed {
			mlPanel.cursor = i
			ShowToast(v.host.Label()+" is already on the list · address saved", ToastKindInfo)
		}
		mlRefreshOne(v.host.UUID)
		return
	}
	add := h
	mlStoreQueue(h.UUID, h.Label(), func(p *mlStorePatch) { p.add = &add })
	// A PC found in the background lands above the rows under the PCs: the
	// cursor stays on the PC (or row) it was on.
	onUUID, onKind := mlCursorOn()
	mlPanel.hosts = append(mlPanel.hosts, mlHostView{host: h, info: info, addr: bare, online: true, checked: true})
	mlCursorTo(onUUID, onKind)
	if typed {
		mlPanel.cursor = len(mlPanel.hosts) - 1
		PlaySuccessSound()
		ShowToast("Found "+h.Label()+" · "+hintSentence("press %s to pair", "A", "Pair"), ToastKindSuccess)
	}
}

// mlWakeFocused sends Wake-on-LAN to the focused PC and watches for it.
func mlWakeFocused() {
	v, ok := mlFocusedHost()
	if !ok {
		return
	}
	if v.online {
		ShowToast(v.host.Label()+" is already awake", ToastKindInfo)
		return
	}
	if v.host.MAC == "" {
		ShowToast(v.host.Label()+" cannot be woken yet · JukaHub learns how once it has seen the PC on", ToastKindInfo)
		return
	}
	PlayActivateSound()
	h := v.host
	gen := mlPanel.gen
	go func() {
		err := mlWake(h)
		tbxPost(func() { mlApplyWake(gen, h, err) })
	}()
}

func mlApplyWake(gen int, h mlHost, err error) {
	if err != nil {
		PlayErrorSound()
		ShowToast("Could not wake "+h.Label()+" — "+firstLine(err.Error()), ToastKindError)
		return
	}
	if !mlPanel.open || mlPanel.gen != gen {
		return
	}
	now := time.Now()
	mlPanel.wakeUUID = h.UUID
	mlPanel.wakeUntil = now.Add(mlWakeFor)
	mlPanel.wakeNext = now.Add(3 * time.Second)
	ShowToast("Waking "+h.Label()+"…", ToastKindInfo)
}

// mlPanelTick runs every frame the panel is up: the wake watch and the
// periodic looks. A few time compares when nothing is due.
func mlPanelTick() {
	if !mlPanel.open {
		return
	}
	now := time.Now()
	if mlPanel.wakeUUID != "" {
		switch {
		case now.After(mlPanel.wakeUntil):
			label := "The PC"
			if i := mlHostIndex(mlPanel.wakeUUID); i >= 0 {
				label = mlPanel.hosts[i].host.Label()
			}
			mlPanel.wakeUUID = ""
			ShowToast(label+" did not wake up · Wake-on-LAN must be on in its BIOS and network card settings", ToastKindWarning)
		case now.After(mlPanel.wakeNext):
			mlPanel.wakeNext = now.Add(4 * time.Second)
			mlRefreshOne(mlPanel.wakeUUID)
		}
	}
	switch mlPanel.page {
	case mlPageHosts:
		if now.Sub(mlPanel.pollAt) > mlPollEvery {
			mlRefreshAll(false)
		}
	case mlPageApps:
		if mlPanel.appsLoaded && !mlPanel.appsBusy && now.Sub(mlPanel.infoAt) > 10*time.Second {
			mlPanel.infoAt = now
			mlRefreshOne(mlPanel.uuid)
		}
	}
}

// ── The PC list ──────────────────────────────────────────────────────────

// mlHostStatus is a PC's right-hand status and its detail line.
func mlHostStatus(v mlHostView) (status, detail string) {
	var parts []string
	if v.host.ServerKind != "" {
		parts = append(parts, v.host.ServerKind)
	}
	if a := mlHostAddr(v); a != "" {
		parts = append(parts, a)
	}
	switch {
	case mlPanel.wakeUUID != "" && mlPanel.wakeUUID == v.host.UUID:
		status = "Waking…"
		parts = append(parts, "waiting for it to start")
	case !v.checked && v.busy:
		status = "…"
		parts = append(parts, "checking")
	case v.online && v.host.Paired && v.info.CurrentGame != 0:
		status = "Playing"
		parts = append(parts, "playing "+mlRunningName(v))
	case v.online && v.host.Paired:
		status = "Paired"
		parts = append(parts, "online")
	case v.online:
		status = "Not paired"
		parts = append(parts, hintSentence("online · press %s to pair", "A", "Pair"))
	case v.checked && v.err != "":
		// It answered and refused, or another PC answers there.
		status = "Unreachable"
		parts = append(parts, v.err)
	case v.checked && v.host.MAC != "":
		status = "Asleep"
		parts = append(parts, hintSentence("not answering · press %s to wake it", "X", "Wake"))
	case v.checked:
		status = "Offline"
		parts = append(parts, "not answering · turn it on (Wake works once JukaHub has seen it on)")
	}
	return status, strings.Join(parts, " · ")
}

// mlRunningName names the game a PC is playing.
func mlRunningName(v mlHostView) string {
	id := v.info.CurrentGame
	if v.host.UUID == mlPanel.uuid {
		for _, a := range mlPanel.apps {
			if a.ID == id {
				return a.Title
			}
		}
	}
	if v.host.LastAppID == id && v.host.LastAppName != "" {
		return v.host.LastAppName
	}
	return "a game"
}

// mlHostRows is the PC list page.
func mlHostRows() []mlRow {
	rows := make([]mlRow, 0, len(mlPanel.hosts)+4)
	for i, v := range mlPanel.hosts {
		status, detail := mlHostStatus(v)
		rows = append(rows, mlRow{kind: mlRowHost, host: i, title: v.host.Label(), detail: detail, status: status})
	}
	if len(mlPanel.hosts) == 0 {
		title, detail := "No PC yet", "Install Vibepollo, Apollo or Sunshine on your PC · it shows up here on the same network"
		if mlPanel.discovering || mlPanel.probing > 0 {
			title, detail = "Looking for PCs…", "Vibepollo, Apollo and Sunshine answer on this network"
		}
		rows = append(rows, mlRow{kind: mlRowEmpty, title: title, detail: detail})
	}
	searchDetail, searchStatus := "Looks for Vibepollo, Apollo and Sunshine PCs on this network", "Search"
	if mlPanel.discovering || mlPanel.probing > 0 {
		searchDetail, searchStatus = "Searching…", "…"
	}
	rows = append(rows,
		mlRow{kind: mlRowAdd, title: "Add a PC by address", detail: "For a PC the search cannot see: another network, a VPN, a host name", status: "Add"},
		mlRow{kind: mlRowSearch, title: "Search the network again", detail: searchDetail, status: searchStatus},
	)
	if mlOnHandheld() {
		note, ok := mlSelfTestNote()
		status := "Check"
		if mlSelfTestChecked() {
			status = "Problem"
			if ok {
				status = "Checked"
			}
		}
		rows = append(rows, mlRow{kind: mlRowCheck, title: "Check this handheld", detail: note, status: status})
	}
	if mlPanel.lastNote != "" {
		rows = append(rows, mlRow{kind: mlRowLast, title: "Last stream", detail: mlPanel.lastNote, status: "Info"})
	}
	return rows
}

// mlFocusedHost is the PC under the cursor on the PC list.
func mlFocusedHost() (mlHostView, bool) {
	rows := mlHostRows()
	if mlPanel.page != mlPageHosts || mlPanel.cursor < 0 || mlPanel.cursor >= len(rows) {
		return mlHostView{}, false
	}
	r := rows[mlPanel.cursor]
	if r.kind != mlRowHost || r.host < 0 || r.host >= len(mlPanel.hosts) {
		return mlHostView{}, false
	}
	return mlPanel.hosts[r.host], true
}

// mlCursorOn is what the PC list's cursor is on: a PC's id, or the kind of
// the row under it.
func mlCursorOn() (string, mlRowKind) {
	rows := mlHostRows()
	if mlPanel.cursor < 0 || mlPanel.cursor >= len(rows) {
		return "", mlRowEmpty
	}
	r := rows[mlPanel.cursor]
	if r.kind == mlRowHost && r.host >= 0 && r.host < len(mlPanel.hosts) {
		return mlPanel.hosts[r.host].host.UUID, r.kind
	}
	return "", r.kind
}

// mlCursorTo puts the cursor back on that PC or row after the list changed;
// a row that is gone (the "No PC yet" one) leaves the cursor where it is.
func mlCursorTo(uuid string, kind mlRowKind) {
	for k, r := range mlHostRows() {
		if r.kind != kind {
			continue
		}
		if kind == mlRowHost && mlPanel.hosts[r.host].host.UUID != uuid {
			continue
		}
		mlPanel.cursor = k
		return
	}
}

func mlHostsMove(delta int) {
	n := len(mlHostRows())
	if n == 0 {
		return
	}
	mlPanel.cursor = (mlPanel.cursor + delta + n) % n
	PlayNavSound()
}

// mlHostsActivate is A on the PC list.
func mlHostsActivate() {
	rows := mlHostRows()
	if mlPanel.cursor < 0 || mlPanel.cursor >= len(rows) {
		return
	}
	r := rows[mlPanel.cursor]
	switch r.kind {
	case mlRowHost:
		v := mlPanel.hosts[r.host]
		if v.host.Paired {
			PlayActivateSound()
			mlOpenApps(v.host.UUID)
			return
		}
		if v.checked && !v.online {
			PlayErrorSound()
			switch {
			case v.err != "":
				ShowToast(v.host.Label()+" · "+v.err, ToastKindWarning)
			case v.host.MAC != "":
				ShowToast(v.host.Label()+" is not answering · turn it on (or "+hintKeyFor("X", "Wake")+" to wake it), then pair", ToastKindWarning)
			default:
				ShowToast(v.host.Label()+" is not answering · turn it on, then pair", ToastKindWarning)
			}
			return
		}
		PlayActivateSound()
		mlOpenPair(v.host.UUID)
	case mlRowAdd:
		mlOpenAddressKeyboard(mlVKAdd, "")
	case mlRowSearch:
		PlayActivateSound()
		mlRefreshAll(true)
	case mlRowLast:
		ShowToast(mlPanel.lastNote, mlPanel.lastKind)
	case mlRowEmpty:
		PlayActivateSound()
		mlRefreshAll(true)
	case mlRowCheck:
		mlStartCheck()
	}
}

// ── Pairing ──────────────────────────────────────────────────────────────

// mlWebPINURL is the PC's web page with the PIN tab, as the phone opens it.
func mlWebPINURL(h mlHost, addr string) string {
	host := mlHostOnly(addr)
	if host == "" {
		return ""
	}
	port := h.HTTPPort
	if port <= 0 {
		port = mlHTTPPortDefault
	}
	return "https://" + net.JoinHostPort(host, strconv.Itoa(port+1)) + "/pin"
}

func mlOpenPair(uuid string) {
	mlArtStop()
	mlPanel.uuid = uuid
	mlPanel.page = mlPagePair
	mlPanel.confirm = mlConfirmNone
	mlStartPair()
}

// mlStartPair makes a new PIN and waits for it on the PC.
func mlStartPair() {
	mlCancelPair()
	v, ok := mlCurrentHost()
	if !ok {
		mlPanel.page = mlPageHosts
		return
	}
	mlGenSeq++
	mlPanel.pairGen = mlGenSeq
	gen := mlPanel.pairGen
	addr := mlHostAddr(*v)
	pin := mlNewPIN()
	mlPanel.pairPIN = pin
	mlPanel.pairErr = ""
	mlPanel.pairBusy = true
	mlPanel.pairLink = mlWebPINURL(v.host, addr)
	mlPanel.pairQR, mlPanel.pairQRFull = nil, nil
	if mlPanel.pairLink != "" {
		if code, err := qrcode.New(mlPanel.pairLink, qrcode.Medium); err == nil {
			mlPanel.pairQRFull = code.Bitmap()
			mlPanel.pairQR = qrCoreModules(mlPanel.pairQRFull)
		}
	}
	ctx, cancel := context.WithTimeout(mlPanelCtx(), mlPairTimeout)
	mlPanel.pairCancel = cancel
	h := v.host
	go func() {
		nh, err := mlPair(ctx, h, addr, pin)
		cancel()
		tbxPost(func() { mlApplyPair(gen, h.UUID, nh, err) })
	}()
}

// mlCancelPair stops a pairing that waits; its answer is dropped.
func mlCancelPair() {
	if mlPanel.pairCancel != nil {
		mlPanel.pairCancel()
		mlPanel.pairCancel = nil
	}
	mlPanel.pairBusy = false
	mlGenSeq++
	mlPanel.pairGen = mlGenSeq
}

func mlApplyPair(gen int, uuid string, nh mlHost, err error) {
	if !mlPanel.open || mlPanel.pairGen != gen || mlPanel.page != mlPagePair {
		return
	}
	mlPanel.pairBusy = false
	mlPanel.pairCancel = nil
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return
		case errors.Is(err, context.DeadlineExceeded):
			mlPanel.pairErr = "No PIN was typed on the PC in time"
		default:
			mlPanel.pairErr = firstLine(err.Error())
		}
		PlayErrorSound()
		return
	}
	i := mlHostIndex(uuid)
	if i < 0 {
		mlPanel.page = mlPageHosts
		return
	}
	v := &mlPanel.hosts[i]
	if nh.UUID == "" {
		nh.UUID = uuid
	}
	nh.Paired = true
	v.host = nh
	v.online, v.checked = true, true
	mlPillInvalidate()
	PlaySuccessSound()
	ShowToast("Paired with "+nh.Label(), ToastKindSuccess)
	mlOpenApps(uuid)
}

// mlPairActivate is A on the pairing page: a new PIN after a failure, else
// the code full screen for the phone's camera.
func mlPairActivate() {
	if mlPanel.pairErr != "" || !mlPanel.pairBusy {
		PlayActivateSound()
		mlStartPair()
		return
	}
	if len(mlPanel.pairQRFull) > 0 {
		qrZoomOpenCardDetail(mlPanel.pairQRFull, "Open the PIN page on your phone", mlPanel.pairLink+" · PIN "+mlPanel.pairPIN)
	}
}

// ── Games ────────────────────────────────────────────────────────────────

// mlOpenApps shows a paired PC's games and reads them.
func mlOpenApps(uuid string) {
	if mlPanel.uuid != uuid {
		mlArtFree()
	}
	mlCancelPair()
	mlArtStop()
	mlPanel.uuid = uuid
	mlPanel.page = mlPageApps
	mlPanel.confirm = mlConfirmNone
	mlPanel.apps = nil
	mlPanel.appsLoaded = false
	mlPanel.appsErr = ""
	mlPanel.appCursor, mlPanel.appScroll = 0, 0
	mlPanel.artPath = nil
	mlLoadApps()
}

// mlLoadApps asks the PC how it is (for Resume and the Running badge) and
// for its apps, then fetches their box art.
func mlLoadApps() {
	v, ok := mlCurrentHost()
	if !ok {
		mlPanel.page = mlPageHosts
		return
	}
	mlArtStop()
	mlGenSeq++
	mlPanel.appsGen = mlGenSeq
	gen := mlPanel.appsGen
	mlPanel.appsBusy = true
	mlPanel.appsErr = ""
	h := v.host
	parent := mlPanelCtx()
	go func() {
		ctx, cancel := context.WithTimeout(parent, 40*time.Second)
		defer cancel()
		nh, info, addr, rerr := mlRefreshHost(ctx, h)
		var apps []mlApp
		err := rerr
		if err == nil && nh.Paired {
			apps, err = mlAppList(ctx, nh, addr)
		}
		tbxPost(func() { mlApplyApps(gen, h.UUID, nh, info, addr, rerr, apps, err) })
	}()
}

func mlApplyApps(gen int, uuid string, nh mlHost, info mlServerInfo, addr string, rerr error, apps []mlApp, err error) {
	if !mlPanel.open || mlPanel.appsGen != gen {
		return
	}
	mlPanel.appsBusy = false
	mlPanel.infoAt = time.Now()
	i := mlHostIndex(uuid)
	if i < 0 {
		mlPanel.page = mlPageHosts
		return
	}
	if rerr != nil {
		mlNoteOffline(i, rerr)
		mlPanel.appsErr = mlHostLabel(i) + ": " + mlWhy(rerr)
		return
	}
	mlMergeRefresh(i, nh, info, addr)
	v := mlPanel.hosts[i]
	if !v.host.Paired {
		// The PC forgot this handheld (removed in its web page): pair again,
		// but only from its games page (never from Settings or the PC list).
		if mlPanel.page == mlPageApps && mlPanel.uuid == uuid {
			ShowToast(v.host.Label()+" asks to pair again", ToastKindWarning)
			mlOpenPair(uuid)
		}
		return
	}
	if err != nil {
		mlPanel.appsErr = firstLine(err.Error())
		return
	}
	mlPanel.apps = apps
	mlPanel.appsLoaded = true
	// Start on the game that runs, else the one played last.
	want := v.info.CurrentGame
	if want == 0 {
		want = v.host.LastAppID
	}
	for j, a := range apps {
		if a.ID == want {
			mlPanel.appCursor = j
			break
		}
	}
	mlStartArt(gen, v.host, mlHostAddr(v), apps)
}

func mlHostLabel(i int) string {
	if i < 0 || i >= len(mlPanel.hosts) {
		return "The PC"
	}
	return mlPanel.hosts[i].host.Label()
}

// mlStartArt fetches the apps' box art, three at a time.
func mlStartArt(gen int, h mlHost, addr string, apps []mlApp) {
	mlArtStop()
	if len(apps) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(mlPanelCtx())
	mlPanel.artCancel = cancel
	ids := make(chan int, len(apps))
	for _, a := range apps {
		ids <- a.ID
	}
	close(ids)
	for w := 0; w < 3; w++ {
		go func() {
			for id := range ids {
				if ctx.Err() != nil {
					return
				}
				one, done := context.WithTimeout(ctx, 15*time.Second)
				path, err := mlBoxArtPath(one, h, addr, id)
				done()
				if err != nil {
					path = ""
				}
				if ctx.Err() != nil {
					return
				}
				tbxPost(func() { mlApplyArt(gen, id, path) })
			}
		}()
	}
}

// mlArtStop cancels the art fetches still running.
func mlArtStop() {
	if mlPanel.artCancel != nil {
		mlPanel.artCancel()
		mlPanel.artCancel = nil
	}
}

func mlApplyArt(gen, id int, path string) {
	if !mlPanel.open || mlPanel.appsGen != gen {
		return
	}
	if mlPanel.artPath == nil {
		mlPanel.artPath = map[int]string{}
	}
	mlPanel.artPath[id] = path
}

// mlFocusedApp is the app under the cursor.
func mlFocusedApp() (mlApp, bool) {
	if mlPanel.appCursor < 0 || mlPanel.appCursor >= len(mlPanel.apps) {
		return mlApp{}, false
	}
	return mlPanel.apps[mlPanel.appCursor], true
}

// mlAppsMove walks the grid: ±1 along a row, ±columns between rows.
func mlAppsMove(dx, dy int) {
	n := len(mlPanel.apps)
	if n == 0 {
		return
	}
	cols := max(1, mlPanel.appCols)
	next := mlPanel.appCursor
	switch {
	case dx != 0:
		next = (next + dx + n) % n
	case dy != 0:
		next += dy * cols
		if next < 0 || next >= n {
			return
		}
	}
	if next != mlPanel.appCursor {
		mlPanel.appCursor = next
		PlayNavSound()
	}
}

// mlPairedIndexes are the paired PCs' places in the list.
func mlPairedIndexes() []int {
	var out []int
	for i, v := range mlPanel.hosts {
		if v.host.Paired {
			out = append(out, i)
		}
	}
	return out
}

// mlCycleHost is L1/R1 on the games page: the previous or next paired PC.
func mlCycleHost(delta int) {
	paired := mlPairedIndexes()
	if len(paired) < 2 || mlPanel.starting != "" {
		return
	}
	at := 0
	for k, i := range paired {
		if mlPanel.hosts[i].host.UUID == mlPanel.uuid {
			at = k
			break
		}
	}
	next := paired[(at+delta+len(paired))%len(paired)]
	PlayToggleSound()
	mlOpenApps(mlPanel.hosts[next].host.UUID)
}

// mlPlayFocused is A on a game: play it, or resume it when it runs.
func mlPlayFocused() {
	app, ok := mlFocusedApp()
	v, vok := mlCurrentHost()
	if !ok || !vok || mlPanel.starting != "" {
		return
	}
	if mlPanel.helperBusy {
		ShowToast("The streaming helper is still installing · press "+hintKeyFor("A", "Play")+" again once it is done", ToastKindInfo)
		return
	}
	if !mlStreamSupported() {
		mlShowNoClient()
		return
	}
	verb := "Starting "
	if v.info.CurrentGame != 0 && v.info.CurrentGame == app.ID {
		verb = "Resuming "
	}
	mlPanel.starting = verb + app.Title + " on " + v.host.Label() + "…"
	mlPanel.startCheck = false
	PlayActivateSound()
	h, info, addr := v.host, v.info, mlHostAddr(*v)
	gen := mlPanel.gen
	mlGenSeq++
	mlPanel.startGen = mlGenSeq
	sgen := mlPanel.startGen
	// B and closing the panel cancel the launch until the stream takes over.
	ctx, cancel := context.WithCancel(mlPanelCtx())
	mlPanel.startCancel = cancel
	go func() {
		err := mlStartStreamCtx(ctx, h, info, app, addr)
		cancel()
		tbxPost(func() { mlApplyStart(gen, sgen, h, app, err) })
	}()
}

// mlApplyStart takes a launch's answer. On a handheld the stream has the
// screen now: JukaHub steps aside and comes back when it ends, whether or
// not the panel is still up (or B let the card go after the hand-off).
func mlApplyStart(gen, sgen int, h mlHost, app mlApp, err error) {
	here := mlPanel.open && mlPanel.gen == gen
	mine := here && mlPanel.startGen == sgen && mlPanel.starting != ""
	if mine {
		mlPanel.starting = ""
		mlPanel.startCancel = nil
	}
	if err != nil {
		if !mine || errors.Is(err, context.Canceled) {
			// B or closing the panel let it go: the games page is back as it was.
			return
		}
		switch {
		case errors.Is(err, errMLHelperMissing):
			mlShowNoClient()
			return
		case errors.Is(err, errMLOtherAppRunning):
			// Offer to quit the other game; its name comes with a fresh look.
			PlayErrorSound()
			mlPanel.confirm = mlConfirmQuit
			mlRefreshOne(h.UUID)
			return
		case errors.Is(err, errMLNotPaired), errors.Is(err, errMLCertMismatch):
			PlayErrorSound()
			ShowToast(h.Label()+" asks to pair again", ToastKindWarning)
			mlOpenPair(h.UUID)
			return
		}
		PlayErrorSound()
		ShowToast("Could not start "+app.Title+" — "+mlWhy(err), ToastKindError)
		return
	}
	if mlOnHandheld() {
		if here {
			closeMoonlightPanel()
		}
		ShowToast(app.Title+" is starting on "+h.Label()+" · JukaHub closes while it streams", ToastKindInfo)
		requestAppExit()
		return
	}
	// Moonlight PC may still pair or connect on its own: no "streaming" yet.
	PlayActivateSound()
	client := mlPanel.desktopName
	if client == "" {
		client = "Moonlight PC"
	}
	ShowToast("Opening "+client+" for "+app.Title+"…", ToastKindInfo)
	if !here {
		return
	}
	// mlStartStream saved the last game; the panel's copy follows.
	if i := mlHostIndex(h.UUID); i >= 0 {
		v := &mlPanel.hosts[i]
		v.host.LastAppID, v.host.LastAppName = app.ID, app.Title
		v.info.CurrentGame = app.ID
	}
	mlPanel.infoAt = time.Now()
}

// mlStartCheck is A on "Check this handheld": moonlight-stream's self-test
// takes the screen for about 20 s (moonlight_selftest.go) and JukaHub comes
// back with the result. A missing helper gets the same install card as Play.
func mlStartCheck() {
	if mlPanel.starting != "" {
		return
	}
	if mlPanel.helperBusy {
		ShowToast("The streaming helper is still installing · press "+hintKeyFor("A", "Check")+" again once it is done", ToastKindInfo)
		return
	}
	if mlHelperPath() == "" {
		mlShowNoClient()
		return
	}
	mlPanel.starting = "Checking this handheld…"
	mlPanel.startCheck = true
	PlayActivateSound()
	gen := mlPanel.gen
	mlGenSeq++
	mlPanel.startGen = mlGenSeq
	sgen := mlPanel.startGen
	ctx, cancel := context.WithCancel(mlPanelCtx())
	mlPanel.startCancel = cancel
	go func() {
		err := mlStartSelfTestCtx(ctx)
		cancel()
		tbxPost(func() { mlApplyCheck(gen, sgen, err) })
	}()
}

// mlApplyCheck takes the check's start: on success the helper has the
// screen and JukaHub steps aside.
func mlApplyCheck(gen, sgen int, err error) {
	here := mlPanel.open && mlPanel.gen == gen
	mine := here && mlPanel.startGen == sgen && mlPanel.starting != ""
	if mine {
		mlPanel.starting = ""
		mlPanel.startCancel = nil
	}
	if err != nil {
		if !mine || errors.Is(err, context.Canceled) {
			return
		}
		if errors.Is(err, errMLHelperMissing) {
			mlShowNoClient()
			return
		}
		PlayErrorSound()
		ShowToast("Could not start the check — "+mlWhy(err), ToastKindError)
		return
	}
	if here {
		closeMoonlightPanel()
	}
	ShowToast("Checking this handheld · JukaHub comes back in about 20 s", ToastKindInfo)
	requestAppExit()
}

// mlShowNoClient says what Play needs here: the handheld's helper, or
// Moonlight PC on a computer.
func mlShowNoClient() {
	PlayErrorSound()
	mlPanel.helperMsg = ""
	if mlOnHandheld() {
		mlPanel.notice = mlNoticeHelper
		return
	}
	mlPanel.notice = mlNoticeDesktop
}

// mlNoticeActivate is A on the notice card.
func mlNoticeActivate() {
	switch mlPanel.notice {
	case mlNoticeHelper:
		if mlPanel.helperBusy {
			return
		}
		PlayActivateSound()
		mlPanel.helperBusy = true
		mlPanel.helperMsg = ""
		gen := mlPanel.gen
		mlEnsureHelper(func(ok bool, msg string) {
			if !mlPanel.open || mlPanel.gen != gen {
				if !ok && msg != "" {
					ShowToast(msg, ToastKindError)
				}
				return
			}
			mlPanel.helperBusy = false
			mlReadClients()
			if ok {
				mlPanel.notice = mlNoticeNone
				PlaySuccessSound()
				next := hintSentence("press %s to play", "A", "Play")
				if mlPanel.page == mlPageHosts {
					// The install card came from Check this handheld.
					next = hintSentence("press %s to check this handheld", "A", "Check")
				}
				ShowToast("Streaming helper installed · "+next, ToastKindSuccess)
				return
			}
			PlayErrorSound()
			mlPanel.helperMsg = msg
			if mlPanel.helperMsg == "" {
				mlPanel.helperMsg = "The install did not finish"
			}
		})
	case mlNoticeDesktop:
		if err := openExternalURL(mlDesktopClientURL); err != nil {
			PlayErrorSound()
			ShowToast("Could not open the web browser — "+firstLine(err.Error()), ToastKindError)
			return
		}
		PlayActivateSound()
		mlPanel.notice = mlNoticeNone
		ShowToast("Install Moonlight PC, then press "+hintKeyFor("A", "Play")+" again", ToastKindInfo)
	}
}

// mlAskQuit is X on the games page: quit the running game on the PC.
func mlAskQuit() {
	v, ok := mlCurrentHost()
	if !ok || mlPanel.starting != "" {
		return
	}
	if v.info.CurrentGame == 0 {
		ShowToast("Nothing is running on "+v.host.Label(), ToastKindInfo)
		return
	}
	if mlPanel.quitting {
		return
	}
	PlayActivateSound()
	mlPanel.confirm = mlConfirmQuit
}

func mlDoQuit() {
	mlPanel.confirm = mlConfirmNone
	v, ok := mlCurrentHost()
	if !ok {
		return
	}
	mlPanel.quitting = true
	PlayActivateSound()
	h, addr := v.host, mlHostAddr(*v)
	name := mlRunningName(*v)
	gen := mlPanel.gen
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		err := mlCancelApp(ctx, h, addr)
		cancel()
		tbxPost(func() { mlApplyQuit(gen, h, name, err) })
	}()
}

func mlApplyQuit(gen int, h mlHost, name string, err error) {
	if !mlPanel.open || mlPanel.gen != gen {
		return
	}
	mlPanel.quitting = false
	if err != nil {
		PlayErrorSound()
		ShowToast("Could not quit "+name+" — "+firstLine(err.Error()), ToastKindError)
		return
	}
	if i := mlHostIndex(h.UUID); i >= 0 {
		mlPanel.hosts[i].info.CurrentGame = 0
	}
	PlaySuccessSound()
	ShowToast("Closed "+name+" on "+h.Label(), ToastKindSuccess)
	mlRefreshOne(h.UUID)
}

// ── Settings ─────────────────────────────────────────────────────────────

// mlOpenSettings shows one PC's settings; B goes back to from.
func mlOpenSettings(uuid string, from mlPanelPage) {
	if mlHostIndex(uuid) < 0 {
		return
	}
	if from != mlPageApps {
		mlArtStop()
	}
	mlPanel.uuid = uuid
	mlPanel.setBack = from
	mlPanel.page = mlPageSettings
	mlPanel.setCursor, mlPanel.setScroll = 0, 0
	mlPanel.confirm = mlConfirmNone
	PlayActivateSound()
}

func mlSettingsRows() []mlSetRow {
	v, ok := mlCurrentHost()
	if !ok {
		return nil
	}
	rows := []mlSetRow{mlSetQuality}
	if mlNormalizePreset(v.host.Settings.Preset) == mlPresetCustom {
		rows = append(rows, mlSetWidth, mlSetHeight, mlSetFPS, mlSetBitrate)
	}
	return append(rows, mlSetCodec, mlSetVirtual, mlSetPCAudio, mlSetQuitOnExit, mlSetRumble, mlSetVolume, mlSetAddress, mlSetUnpair)
}

func mlSettingsMove(delta int) {
	n := len(mlSettingsRows())
	if n == 0 {
		return
	}
	mlPanel.setCursor = (mlPanel.setCursor + delta + n) % n
	PlayNavSound()
}

// mlStreamSize is the stream mode the settings ask for on this screen.
func mlStreamSize(s mlStreamSettings) (w, h, fps, kbps int) {
	return s.Resolve(int(screenWidth), int(screenHeight))
}

// mlPresetIndex is s's preset in mlPresetNames.
func mlPresetIndex(names []string, preset string) int {
	if preset == "" {
		preset = "balanced"
	}
	for i, n := range names {
		if n == preset {
			return i
		}
	}
	return 0
}

// mlStepList moves cur to the neighbouring value of list (ascending).
func mlStepList(cur int, list []int, delta int) int {
	at := 0
	for i, v := range list {
		if v <= cur {
			at = i
		}
	}
	if cur < list[0] && delta > 0 {
		return list[0]
	}
	at = min(max(at+delta, 0), len(list)-1)
	return list[at]
}

var (
	mlWidths   = []int{640, 720, 800, 854, 960, 1024, 1152, 1280, 1366, 1440, 1600, 1920, 2560, 3840}
	mlHeights  = []int{360, 400, 480, 540, 576, 600, 720, 768, 800, 900, 1080, 1200, 1440, 1600, 2160}
	mlFPSList  = []int{30, 40, 45, 50, 60, 72, 90, 120}
	mlBitrates = []int{2000, 3000, 4000, 5000, 6000, 8000, 10000, 12000, 15000, 20000, 25000, 30000, 40000, 50000, 80000, 100000, 150000}
	mlCodecs   = []string{"h264", "h265", "auto"}
)

// mlCodecLabel names a codec setting.
func mlCodecLabel(c string) string {
	switch c {
	case "h265":
		return "H.265"
	case "auto":
		return "Auto"
	}
	return "H.264"
}

// mlSettingAdjust is Left/Right (and A, forward) on a settings row.
func mlSettingAdjust(row mlSetRow, delta int) bool {
	v, ok := mlCurrentHost()
	if !ok {
		return false
	}
	s := &v.host.Settings
	switch row {
	case mlSetQuality:
		// Auto ("": what the handheld check picked) comes before the presets,
		// so a PC can go back to it.
		names := append([]string{""}, mlPresetNames()...)
		cur := 0
		if strings.TrimSpace(s.Preset) != "" {
			cur = mlPresetIndex(names, mlNormalizePreset(s.Preset))
		}
		next := names[(cur+delta+len(names))%len(names)]
		if next == mlPresetCustom && mlNormalizePreset(s.Preset) != mlPresetCustom {
			// Custom starts from what the preset it replaces streamed.
			w, h, fps, kbps := mlStreamSize(*s)
			if s.Width <= 0 {
				s.Width = w
			}
			if s.Height <= 0 {
				s.Height = h
			}
			if s.FPS <= 0 {
				s.FPS = fps
			}
			if s.BitrateKbps <= 0 {
				s.BitrateKbps = kbps
			}
		}
		s.Preset = next
	case mlSetWidth:
		w, _, _, _ := mlStreamSize(*s)
		s.Width = mlStepList(w, mlWidths, delta)
	case mlSetHeight:
		_, h, _, _ := mlStreamSize(*s)
		s.Height = mlStepList(h, mlHeights, delta)
	case mlSetFPS:
		_, _, fps, _ := mlStreamSize(*s)
		s.FPS = mlStepList(fps, mlFPSList, delta)
	case mlSetBitrate:
		_, _, _, kbps := mlStreamSize(*s)
		s.BitrateKbps = mlStepList(kbps, mlBitrates, delta)
	case mlSetCodec:
		at := 0
		for i, c := range mlCodecs {
			if c == s.mlCodec() {
				at = i
			}
		}
		s.Codec = mlCodecs[(at+delta+len(mlCodecs))%len(mlCodecs)]
	case mlSetVirtual:
		on := !s.UseVirtualDisplay()
		s.VirtualDisplay = &on
	case mlSetPCAudio:
		s.PlayAudioOnPC = !s.PlayAudioOnPC
	case mlSetQuitOnExit:
		s.QuitOnExit = !s.QuitOnExit
	case mlSetRumble:
		s.NoRumble = !s.NoRumble
	case mlSetVolume:
		vol := s.Volume
		if vol <= 0 {
			vol = 80
		}
		// 0 means "default" in the store, so the floor is 5.
		s.Volume = min(max(vol+delta*5, 5), 100)
	default:
		return false
	}
	mlStoreSettings(v)
	PlayToggleSound()
	return true
}

// mlSettingsActivate is A on a settings row.
func mlSettingsActivate() {
	rows := mlSettingsRows()
	if mlPanel.setCursor < 0 || mlPanel.setCursor >= len(rows) {
		return
	}
	switch row := rows[mlPanel.setCursor]; row {
	case mlSetAddress:
		v, ok := mlCurrentHost()
		if !ok {
			return
		}
		seed := v.host.ManualAddr
		if seed != "" && v.host.HTTPPort > 0 && v.host.HTTPPort != mlHTTPPortDefault {
			seed = net.JoinHostPort(seed, strconv.Itoa(v.host.HTTPPort))
		}
		mlOpenAddressKeyboard(mlVKEdit, seed)
	case mlSetUnpair:
		PlayActivateSound()
		mlPanel.confirm = mlConfirmUnpair
	default:
		mlSettingAdjust(row, 1)
	}
}

// mlDoUnpair tells the PC to forget this handheld (when it answers) and
// forgets the PC here.
func mlDoUnpair() {
	mlPanel.confirm = mlConfirmNone
	v, ok := mlCurrentHost()
	if !ok {
		return
	}
	PlayActivateSound()
	h, addr, online := v.host, mlHostAddr(*v), v.online && v.host.Paired
	gen := mlPanel.gen
	// Confirmed by the user: it finishes even if the panel closes.
	go func() {
		var err error
		if online {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err = mlUnpair(ctx, h, addr)
			cancel()
		}
		// Forgetting reads the art folder and deletes files: here, not on
		// the main loop, and after every save queued for this PC.
		rerr := mlStoreForget(h.UUID)
		tbxPost(func() { mlApplyUnpair(gen, h, online, err, rerr) })
	}()
}

func mlApplyUnpair(gen int, h mlHost, asked bool, err, rerr error) {
	if rerr != nil {
		PlayErrorSound()
		ShowToast("Could not forget "+h.Label()+" — "+firstLine(rerr.Error()), ToastKindError)
		return
	}
	mlPillInvalidate()
	switch {
	case asked && err == nil:
		ShowToast("Unpaired "+h.Label()+" and forgot it", ToastKindSuccess)
	case !h.Paired:
		ShowToast("Forgot "+h.Label()+" · a search may find it again", ToastKindInfo)
	default:
		ShowToast("Forgot "+h.Label()+" · remove this handheld from its web page's client list too", ToastKindInfo)
	}
	if !mlPanel.open || mlPanel.gen != gen {
		return
	}
	if i := mlHostIndex(h.UUID); i >= 0 {
		mlPanel.hosts = append(mlPanel.hosts[:i], mlPanel.hosts[i+1:]...)
	}
	if mlPanel.uuid == h.UUID {
		mlArtStop()
		mlArtFree()
		mlPanel.uuid = ""
		mlPanel.apps = nil
		mlPanel.page = mlPageHosts
		mlPanel.cursor = 0
	}
}

// ── The address keyboard (mlAddrVKTarget) ────────────────────────────────

func mlOpenAddressKeyboard(mode mlVKMode, seed string) {
	mlVKFor = mode
	mlVKBuffer = seed
	chatInputActive = false
	unitInputActive = false
	vkTarget = mlAddrVKTarget
	vkLayer = vkLayerLetters
	vkLayoutSig = ""
	keyboardPosY, keyboardPosX = 0, 0
	virtualKeyboardActive = true
	vkAnimStart = sdl.GetTicks64()
	sdl.StartTextInput()
	vkEnsureLayout()
	PlayActivateSound()
}

func mlVKText() string        { return mlVKBuffer }
func mlVKSetText(text string) { mlVKBuffer = text }
func mlVKClear()              { mlVKBuffer = "" }
func mlVKTitle() string       { return "PC ADDRESS" }
func mlVKPlaceholder() string { return "192.168.1.20, my-pc.local or 10.0.0.5:47989" }

func mlVKActionLabel() string {
	if mlVKFor == mlVKAdd {
		return "Add"
	}
	return "Save"
}

// mlCleanAddr takes what a person may paste: a link to the web page, a
// trailing slash.
func mlCleanAddr(s string) string {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(low, p) {
			s = s[len(p):]
			break
		}
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}

// mlValidAddr accepts an IP address, a host name, either with a port.
func mlValidAddr(s string) bool {
	if s == "" || len(s) > 260 || strings.ContainsAny(s, " \t\\@") {
		return false
	}
	host := s
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, perr := strconv.Atoi(p)
		if perr != nil || n <= 0 || n > 65535 {
			return false
		}
		host = h
	}
	if net.ParseIP(host) != nil {
		return true
	}
	return psPanelValidHost(host)
}

// mlVKSubmit takes the typed address on Done.
func mlVKSubmit(text string) {
	if !mlPanel.open {
		return
	}
	text = mlCleanAddr(text)
	switch mlVKFor {
	case mlVKAdd:
		if text == "" {
			return
		}
		if !mlValidAddr(text) {
			PlayErrorSound()
			ShowToast("Type the PC's address, like 192.168.1.20 or my-pc.local", ToastKindWarning)
			mlOpenAddressKeyboard(mlVKAdd, text)
			return
		}
		ShowToast("Asking "+text+"…", ToastKindInfo)
		mlProbe(text, true)
	case mlVKEdit:
		v, ok := mlCurrentHost()
		if !ok {
			return
		}
		if text != "" && !mlValidAddr(text) {
			PlayErrorSound()
			ShowToast("Type the PC's address, like 192.168.1.20 or my-pc.local", ToastKindWarning)
			mlOpenAddressKeyboard(mlVKEdit, text)
			return
		}
		// The store keeps the address bare and the port apart (HTTPPort).
		host, port := text, 0
		if hh, p, err := net.SplitHostPort(text); err == nil {
			host = hh
			port, _ = strconv.Atoi(p)
		}
		host = mlHostOnly(host)
		if host == v.host.ManualAddr && (port == 0 || port == v.host.HTTPPort) {
			return
		}
		if host == "" {
			// The typed port went with the typed address.
			v.host.HTTPPort = 0
		}
		v.host.ManualAddr = host
		if port > 0 {
			v.host.HTTPPort = port
		}
		v.addr = ""
		mlStoreAddr(v)
		if text == "" {
			ShowToast("Address cleared · JukaHub finds "+v.host.Label()+" on the network", ToastKindInfo)
		} else {
			ShowToast("Address saved · "+v.host.Label()+" is tried at "+text+" first", ToastKindSuccess)
		}
		mlRefreshOne(v.host.UUID)
	}
}

// ── Back, input ──────────────────────────────────────────────────────────

// mlPanelBack steps back one level: a card, the pairing, the games or the
// settings, and then closes the panel.
func mlPanelBack() {
	switch {
	case mlPanel.starting != "":
		// Cancel the launch; its answer is let go and the games page is back.
		if mlPanel.startCancel != nil {
			mlPanel.startCancel()
			mlPanel.startCancel = nil
		}
		mlGenSeq++
		mlPanel.startGen = mlGenSeq
		mlPanel.starting = ""
		PlayBackSound()
		// The PC may have started the game already: the Running badge follows.
		mlRefreshOne(mlPanel.uuid)
	case mlPanel.notice != mlNoticeNone:
		if mlPanel.helperBusy {
			// The install goes on; its answer still lands here.
			ShowToast("The helper keeps installing in the background", ToastKindInfo)
		}
		mlPanel.notice = mlNoticeNone
		PlayBackSound()
	case mlPanel.confirm != mlConfirmNone:
		mlPanel.confirm = mlConfirmNone
		PlayBackSound()
	case mlPanel.page == mlPagePair:
		mlCancelPair()
		mlPanel.page = mlPageHosts
		PlayBackSound()
	case mlPanel.page == mlPageSettings:
		mlPanel.page = mlPanel.setBack
		if mlPanel.page == mlPageApps && !mlPanel.appsLoaded && !mlPanel.appsBusy {
			mlLoadApps()
		}
		PlayBackSound()
	case mlPanel.page == mlPageApps:
		mlArtStop()
		// A games answer still on its way is dropped: it must not start art
		// or a pairing on the PC list.
		mlGenSeq++
		mlPanel.appsGen = mlGenSeq
		mlPanel.appsBusy = false
		mlPanel.page = mlPageHosts
		for k, r := range mlHostRows() {
			if r.kind == mlRowHost && mlPanel.hosts[r.host].host.UUID == mlPanel.uuid {
				mlPanel.cursor = k
				break
			}
		}
		PlayBackSound()
	default:
		closeMoonlightPanel()
	}
}

// mlPanelActivate is A wherever the panel is.
func mlPanelActivate() {
	switch {
	case mlPanel.starting != "":
	case mlPanel.notice != mlNoticeNone:
		mlNoticeActivate()
	case mlPanel.confirm == mlConfirmQuit:
		mlDoQuit()
	case mlPanel.confirm == mlConfirmUnpair:
		mlDoUnpair()
	case mlPanel.page == mlPagePair:
		mlPairActivate()
	case mlPanel.page == mlPageApps:
		// A reloads only an empty page; after a failed reload the games
		// shown still play.
		if len(mlPanel.apps) == 0 && mlPanel.appsErr != "" && !mlPanel.appsBusy {
			PlayActivateSound()
			mlLoadApps()
			return
		}
		mlPlayFocused()
	case mlPanel.page == mlPageSettings:
		mlSettingsActivate()
	default:
		mlHostsActivate()
	}
}

// mlPanelModal reports whether a card or a launch owns the keys.
func mlPanelModal() bool {
	return mlPanel.starting != "" || mlPanel.notice != mlNoticeNone || mlPanel.confirm != mlConfirmNone
}

// mlPanelDir is the D-pad and the arrow keys.
func mlPanelDir(dx, dy int) {
	if mlPanelModal() {
		return
	}
	switch mlPanel.page {
	case mlPageHosts:
		if dy != 0 {
			mlHostsMove(dy)
		}
	case mlPageApps:
		mlAppsMove(dx, dy)
	case mlPageSettings:
		if dy != 0 {
			mlSettingsMove(dy)
			return
		}
		rows := mlSettingsRows()
		if dx != 0 && mlPanel.setCursor >= 0 && mlPanel.setCursor < len(rows) {
			mlSettingAdjust(rows[mlPanel.setCursor], dx)
		}
	}
}

// mlPanelX is X: wake the focused PC, or quit the running game.
func mlPanelX() {
	if mlPanelModal() {
		return
	}
	switch mlPanel.page {
	case mlPageHosts:
		mlWakeFocused()
	case mlPageApps:
		mlAskQuit()
	}
}

// mlPanelY is Y: the focused (or shown) PC's settings.
func mlPanelY() {
	if mlPanelModal() {
		return
	}
	switch mlPanel.page {
	case mlPageHosts:
		if v, ok := mlFocusedHost(); ok {
			mlOpenSettings(v.host.UUID, mlPageHosts)
		}
	case mlPageApps:
		mlOpenSettings(mlPanel.uuid, mlPageApps)
	case mlPageSettings:
		mlPanelBack()
	}
}

// mlPanelRefresh is Start / F5: search again, or read the games again.
func mlPanelRefresh() {
	if mlPanelModal() {
		return
	}
	switch mlPanel.page {
	case mlPageHosts:
		PlayNavSound()
		mlRefreshAll(true)
		ShowToast("Looking for your PCs…", ToastKindInfo)
	case mlPageApps:
		if !mlPanel.appsBusy {
			PlayNavSound()
			mlLoadApps()
		}
	}
}

func ctrlMoonlightPanel(e *sdl.ControllerButtonEvent) bool {
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		mlPanelDir(0, -1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		mlPanelDir(0, 1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT:
		mlPanelDir(-1, 0)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT:
		mlPanelDir(1, 0)
	case sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		if mlPanel.page == mlPageApps && !mlPanelModal() {
			mlCycleHost(-1)
		}
	case sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		if mlPanel.page == mlPageApps && !mlPanelModal() {
			mlCycleHost(1)
		}
	case sdl.CONTROLLER_BUTTON_A:
		mlPanelActivate()
	case sdl.CONTROLLER_BUTTON_X:
		mlPanelX()
	case sdl.CONTROLLER_BUTTON_Y:
		mlPanelY()
	case sdl.CONTROLLER_BUTTON_START:
		mlPanelRefresh()
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		mlPanelBack()
	}
	return true
}

func handleMoonlightKey(e *sdl.KeyboardEvent) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !mlPanel.open {
		return false
	}
	// Space on "Add a PC by address" opens the keyboard: its space must not
	// be typed into the field it opened (vkopenertext.go).
	wasOpen := virtualKeyboardActive
	defer vkDropOpenerText(e, wasOpen)
	switch e.Keysym.Sym {
	case sdl.K_UP:
		mlPanelDir(0, -1)
	case sdl.K_DOWN:
		mlPanelDir(0, 1)
	case sdl.K_LEFT:
		mlPanelDir(-1, 0)
	case sdl.K_RIGHT:
		mlPanelDir(1, 0)
	case sdl.K_TAB:
		if mlPanel.page == mlPageApps && !mlPanelModal() {
			if e.Keysym.Mod&sdl.KMOD_SHIFT != 0 {
				mlCycleHost(-1)
			} else {
				mlCycleHost(1)
			}
		}
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		mlPanelActivate()
	case sdl.K_x:
		mlPanelX()
	case sdl.K_y:
		mlPanelY()
	case sdl.K_r, sdl.K_F5:
		mlPanelRefresh()
	case sdl.K_ESCAPE, sdl.K_b, sdl.K_BACKSPACE:
		mlPanelBack()
	}
	return true
}

func handleMoonlightMouse(mx, my int32) bool {
	if !mlPanel.open {
		return false
	}
	// Topmost first: mlHits is in drawing order, so a card's buttons (drawn
	// last) win over the row or tile under them.
	for i := len(mlHits) - 1; i >= 0; i-- {
		hit := mlHits[i]
		if !pointInRect(mx, my, hit.rect) {
			continue
		}
		switch hit.kind {
		case mlHitPrimary:
			mlPanelActivate()
		case mlHitSecondary:
			mlPanelBack()
		case mlHitQR:
			if mlPanelModal() {
				return true
			}
			mlPairActivate()
		case mlHitTile:
			if mlPanelModal() {
				return true
			}
			if hit.index != mlPanel.appCursor {
				// A game starts on the second tap: the first one picks it.
				mlPanel.appCursor = hit.index
				PlayNavSound()
				return true
			}
			mlPlayFocused()
		case mlHitRow:
			if mlPanelModal() {
				return true
			}
			switch mlPanel.page {
			case mlPageSettings:
				mlPanel.setCursor = hit.index
				mlSettingsActivate()
			case mlPageHosts:
				mlPanel.cursor = hit.index
				mlHostsActivate()
			}
		}
		return true
	}
	if mlPanelRect.W > 0 && pointInRect(mx, my, mlPanelRect) {
		return true
	}
	mlPanelBack()
	return true
}

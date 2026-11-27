package main

// moonlight_stream.go — starting a Moonlight stream.
//
// On a TrimUI handheld the stream is JukaHub's own native helper,
// bin/moonlight-stream (player/native/moonlight-stream.c: moonlight-common-c,
// FFmpeg, SDL2), started the way PS Remote Play starts chiaki-stream: JukaHub
// asks the PC to launch (or resume) the game with a fresh remote-input key,
// writes a 0600 session file (key=value, documented in native/README.md),
// leaves a "started" note for the next start (moonlight_lastrun.go), hands
// the screen over through the same hand-off script (psStartHandoffWith with
// Moonlight's psHandoffSpec) and exits; launch.sh brings it back when the
// stream ends.
//
// On a computer there is no helper: Play starts an installed Moonlight PC
// (stock, or Nonary's VRR build, which installs over it) with its own
// `stream <host> <app>` command line, and Moonlight PC launches the game
// itself, so JukaHub sends no /launch then. Moonlight PC has an identity of
// its own, though: its `stream` command works only for a PC that was paired
// in Moonlight PC once (JukaHub's pairing does not carry over), and says so
// otherwise.
//
// The helper ships in the Remote Play package of each release
// (JukaHub-RemotePlay-linux-arm64.zip); mlEnsureHelper installs it through
// the same installer as chiaki-stream (psremote_helpers_install.go).

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mlHelperName       = "moonlight-stream"
	mlHelperEnv        = "JUKAHUB_MOONLIGHT_STREAM"
	mlLastRunFileName  = "moonlight-last.txt"
	mlLastSessionName  = "moonlight-last-session.txt"
	mlLogName          = "moonlight.log"
	mlFallbackLogPath  = "/tmp/moonlight.log"
	mlLogBanner        = "JukaHub Moonlight"
	mlStreamNameMax    = 40
	mlAppNameMax       = 60
	mlFlatpakID        = "com.moonlight_stream.Moonlight"
	mlHelperNoneRetry  = 30 * time.Minute
	mlSupportedRecheck = 2 * time.Second
)

var errMLHelperMissing = errors.New("Moonlight is not installed: a handheld needs its streaming helper (the Remote Play download), a computer needs Moonlight PC")

// errMLHelperWontRun ends an install that was for moonlight-stream alone when
// this firmware refuses it (psInstallHelpersFor). It is no refusal of Remote
// Play: the PS helpers are not touched, and mlSetHelperInstallNote says why.
var errMLHelperWontRun = errors.New("moonlight-stream won't run on this firmware")

// ── Where it is ──────────────────────────────────────────────────────────

// mlOnHandheld reports whether this is the TrimUI (linux/arm64) build, which
// streams with the native helper.
func mlOnHandheld() bool {
	return psRemoteSupported()
}

// mlHelperPath is bin/moonlight-stream, looked for where psRemoteEngine looks
// for chiaki-stream (psHelperDirs), or JUKAHUB_MOONLIGHT_STREAM; "" when it
// is missing.
func mlHelperPath() string {
	if p := strings.TrimSpace(os.Getenv(mlHelperEnv)); p != "" && regularFile(p) {
		return p
	}
	for _, root := range psHelperDirs() {
		if p := filepath.Join(root, mlHelperName); regularFile(p) {
			return p
		}
	}
	return ""
}

// mlHelperMissingHere reports whether this is a handheld without the helper:
// an install of the Remote Play package would bring it.
func mlHelperMissingHere() bool {
	return psHelperSupportedFn() && mlHelperPath() == ""
}

// mlDesktopClient finds Moonlight PC: name for messages, the program and the
// arguments that come before `stream <host> <app>`. Never on a handheld.
func mlDesktopClient() (name, path string, args []string, ok bool) {
	if mlOnHandheld() {
		return "", "", nil, false
	}
	switch runtime.GOOS {
	case "windows":
		// Nonary's VRR installer replaces stock Moonlight at the same path.
		var cands []string
		for _, env := range []string{"ProgramFiles", "ProgramW6432", "ProgramFiles(x86)"} {
			if base := strings.TrimSpace(os.Getenv(env)); base != "" {
				cands = append(cands, filepath.Join(base, "Moonlight Game Streaming", "Moonlight.exe"))
			}
		}
		if base := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); base != "" {
			cands = append(cands, filepath.Join(base, "Programs", "Moonlight Game Streaming", "Moonlight.exe"))
		}
		for _, c := range cands {
			if regularFile(c) {
				return "Moonlight", c, nil, true
			}
		}
	case "darwin":
		cands := []string{"/Applications/Moonlight.app/Contents/MacOS/Moonlight"}
		if home, err := os.UserHomeDir(); err == nil {
			cands = append(cands, filepath.Join(home, "Applications", "Moonlight.app", "Contents", "MacOS", "Moonlight"))
		}
		for _, c := range cands {
			if regularFile(c) {
				return "Moonlight", c, nil, true
			}
		}
	default:
		for _, bin := range []string{"moonlight", "moonlight-qt"} {
			if p, err := exec.LookPath(bin); err == nil {
				return "Moonlight", p, nil, true
			}
		}
		if fp, err := exec.LookPath("flatpak"); err == nil {
			dirs := []string{filepath.Join("/var/lib/flatpak/app", mlFlatpakID)}
			if home, err := os.UserHomeDir(); err == nil {
				dirs = append(dirs, filepath.Join(home, ".local", "share", "flatpak", "app", mlFlatpakID))
			}
			for _, d := range dirs {
				if st, err := os.Stat(d); err == nil && st.IsDir() {
					return "Moonlight (Flatpak)", fp, []string{"run", mlFlatpakID}, true
				}
			}
		}
	}
	return "", "", nil, false
}

// mlSupportedSeen keeps mlStreamSupported off the card between frames.
var mlSupportedSeen struct {
	mu sync.Mutex
	at time.Time
	ok bool
}

// mlStreamSupported reports whether Play can stream here: the helper on a
// handheld, Moonlight PC on a computer. Looked up at most every two seconds,
// so a panel may ask every frame. On a handheld without the helper,
// mlEnsureHelper installs it.
func mlStreamSupported() bool {
	mlSupportedSeen.mu.Lock()
	if !mlSupportedSeen.at.IsZero() && time.Since(mlSupportedSeen.at) < mlSupportedRecheck {
		ok := mlSupportedSeen.ok
		mlSupportedSeen.mu.Unlock()
		return ok
	}
	mlSupportedSeen.mu.Unlock()
	// The helper only on the handheld build: a desktop Linux machine with a
	// bin/moonlight-stream (or JUKAHUB_MOONLIGHT_STREAM) set streams through
	// Moonlight PC, never through the handheld's screen hand-off.
	ok := mlOnHandheld() && mlHelperPath() != ""
	if !ok {
		_, _, _, ok = mlDesktopClient()
	}
	mlSupportedSeen.mu.Lock()
	mlSupportedSeen.at, mlSupportedSeen.ok = time.Now(), ok
	mlSupportedSeen.mu.Unlock()
	return ok
}

func mlForgetSupported() {
	mlSupportedSeen.mu.Lock()
	mlSupportedSeen.at = time.Time{}
	mlSupportedSeen.mu.Unlock()
}

// ── Paths ────────────────────────────────────────────────────────────────

// mlDataPath is name in DataDir (the temp folder without one).
func mlDataPath(name string) string {
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, name)
	}
	return filepath.Join(os.TempDir(), name)
}

// mlLastRunPathOverride replaces mlLastRunPath in tests.
var mlLastRunPathOverride string

// mlLastRunPath is the helper's status file: beside moonlight-hosts.json, so
// it survives JukaHub's restart after the stream.
func mlLastRunPath() string {
	if p := strings.TrimSpace(mlLastRunPathOverride); p != "" {
		return p
	}
	return mlDataPath(mlLastRunFileName)
}

// mlLastSessionPath is JukaHub's own note of which PC and game the last
// stream was (the helper's report names only the address).
func mlLastSessionPath() string {
	return mlDataPath(mlLastSessionName)
}

// mlLogPath is moonlight.log beside psremote.log (the app folder on the card).
func mlLogPath() string {
	return filepath.Join(filepath.Dir(psLogPath()), mlLogName)
}

// mlHandoffSpec is Moonlight's screen hand-off (psremote.go), tuned: the
// performance CPU governor and no Wi-Fi power saving while the helper runs.
func mlHandoffSpec() psHandoffSpec {
	return psHandoffSpec{
		Helper:      mlHelperName,
		Script:      filepath.Join(os.TempDir(), "jukahub-moonlight-handoff.sh"),
		Log:         mlLogPath(),
		FallbackLog: mlFallbackLogPath,
		Status:      mlLastRunPath(),
		Banner:      mlLogBanner,
		Tune:        true,
	}
}

// ── The stream ───────────────────────────────────────────────────────────

// mlScreenSize is the panel the stream is sized for: the window JukaHub runs
// in, else the handheld's native mode.
func mlScreenSize() (int, int) {
	w, h := int(screenWidth), int(screenHeight)
	if w > 0 && h > 0 {
		return w, h
	}
	switch DetectDevice() {
	case DeviceTrimuiBrick, DeviceTrimuiBrickPro, DeviceTrimuiHammer:
		return 1024, 768
	}
	return mlDefaultScreenW, mlDefaultScreenH
}

// mlTruncate keeps at most max runes, with "..." (ASCII: the helper's font
// has no ellipsis) when it cut.
func mlTruncate(s string, max int) string {
	s = psSessionValue(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max-3])) + "..."
}

// mlStreamIsRemote reports whether host is reached over the internet or a VPN
// rather than the LAN: the helper then uses smaller packets and encrypts
// everything (moonlight-common-c's STREAM_CFG_REMOTE). A host name is looked
// up for at most a second, within ctx.
func mlStreamIsRemote(ctx context.Context, host string) bool {
	if mlOnLocalSubnet(host) {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".local") {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return true
	}
	for _, ip := range ips {
		if mlOnLocalSubnet(ip.IP.String()) {
			return false
		}
	}
	return true
}

// mlSession is everything the session file says.
type mlSession struct {
	host         string
	appVersion   string
	gfeVersion   string
	rtspURL      string
	codecSupport int
	keys         mlLaunchKeys
	width        int
	height       int
	fps          int
	bitrate      int
	codec        string
	remote       bool
	name         string
	appName      string
	volume       int
	rumble       bool
	hints        bool
	dpadStick    bool
	motor        psStreamMotor
	statusFile   string
}

// mlSessionFile renders the helper's session file (native/README.md). Every
// value goes through psSessionValue, so nothing that came off the network can
// start a line of its own.
func mlSessionFile(s mlSession) string {
	var b strings.Builder
	line := func(key, value string) {
		value = psSessionValue(value)
		if value == "" {
			return
		}
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(value)
		b.WriteByte('\n')
	}
	line("host", s.host)
	line("app_version", s.appVersion)
	line("ri_key", fmt.Sprintf("%x", s.keys.key))
	line("ri_key_id", strconv.FormatInt(int64(s.keys.id), 10))
	line("width", strconv.Itoa(s.width))
	line("height", strconv.Itoa(s.height))
	line("fps", strconv.Itoa(s.fps))
	line("bitrate", strconv.Itoa(s.bitrate))
	line("gfe_version", s.gfeVersion)
	line("rtsp_url", s.rtspURL)
	line("codec_support", strconv.Itoa(s.codecSupport))
	line("codec", s.codec)
	line("remote", psBit(s.remote))
	line("name", s.name)
	line("app_name", s.appName)
	line("volume", strconv.Itoa(s.volume))
	line("rumble", psBit(s.rumble))
	line("hints", psBit(s.hints))
	line("dpad_stick", psBit(s.dpadStick))
	if s.motor.GPIO != "" {
		line("motor_gpio", s.motor.GPIO)
		if s.motor.Level != "" && s.motor.LevelMax > s.motor.LevelMin {
			line("motor_level", s.motor.Level)
			line("motor_level_min", strconv.Itoa(s.motor.LevelMin))
			line("motor_level_max", strconv.Itoa(s.motor.LevelMax))
		}
	}
	line("status_file", s.statusFile)
	line("verbose", "0")
	return b.String()
}

// mlWriteSessionFile writes the session file through a 0600 temp file in
// tmpfs (psSessionDir): the key never survives a reboot.
func mlWriteSessionFile(content string) (string, error) {
	f, err := os.CreateTemp(psSessionDir(), "jukahub-ml-*.conf")
	if err != nil {
		return "", fmt.Errorf("create session file: %w", err)
	}
	path := f.Name()
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.WriteString(content); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// mlStartStream starts the game on the PC and streams it. On a handheld it
// launches it (or resumes it, when the PC's current game is this one) with a
// fresh remote-input key, writes the session file, hands the screen to
// moonlight-stream and asks JukaHub to exit (launch.sh brings it back after
// the stream); on a computer it starts Moonlight PC with `stream <host>
// <app>`. It blocks up to ~50 s (Apollo may make a virtual display first):
// call it from a goroutine. errMLOtherAppRunning: another game runs on the PC
// (mlCancelApp quits it); errMLHelperMissing: nothing here can stream.
func mlStartStream(h mlHost, info mlServerInfo, app mlApp, addr string) error {
	return mlStartStreamCtx(context.Background(), h, info, app, addr)
}

// mlStartStreamCtx is mlStartStream that ctx can call off: every network step
// (the serverinfo check, /launch or /resume, the host name lookup) runs
// within ctx as well as within its own time limit, and a ctx that ends before
// the hand-off returns ctx.Err() with nothing handed over (a game the PC has
// already launched keeps running there). Once the hand-off has started
// (requestAppExit called) ctx no longer applies. Blocks: call it from a
// goroutine.
func mlStartStreamCtx(ctx context.Context, h mlHost, info mlServerInfo, app mlApp, addr string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	host := mlAddrFor(h, addr)
	if host == "" {
		return errMLHostNotFound
	}
	// The helper and its screen hand-off are the handheld's only (the env
	// override included); a computer streams through Moonlight PC.
	if helper := mlHelperPath(); helper != "" && mlOnHandheld() {
		return mlStartHelperStream(ctx, h, info, app, host, helper)
	}
	if mlOnHandheld() {
		return errMLHelperMissing
	}
	return mlStartDesktopStream(h, app, host)
}

// mlStartHelperStream is mlStartStreamCtx on a handheld.
func mlStartHelperStream(ctx context.Context, h mlHost, info mlServerInfo, app mlApp, host, helper string) error {
	if !h.Paired {
		return errMLNotPaired
	}
	engine := psEngine{Stream: helper, Root: filepath.Dir(helper)}
	// A helper this firmware's loader would refuse is reported now, while
	// JukaHub is on screen and before the PC starts a game for nothing
	// (psremote_preflight.go). The execute bits go on first.
	psEnsureExecutable(helper)
	if problem := psStreamHelperCheckWait(engine); problem != "" {
		return errors.New(problem)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// The network steps' own limit, as before; ctx (the caller's) can end
	// them sooner, and is what a cancel is told by.
	netCtx, cancel := context.WithTimeout(ctx, mlLaunchTimeout+2*mlServerInfoTimeout)
	defer cancel()
	// The game running on the PC decides launch or resume, and the panel's
	// answer may be minutes old: ask again (a LAN PC answers in milliseconds).
	fresh, err := mlServerInfoHTTPS(netCtx, h, host)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	switch {
	case err == nil:
		info = fresh
	case errors.Is(err, errMLCertMismatch):
		return err
	case mlForgotPairing(err):
		return errMLNotPaired
	case strings.TrimSpace(info.AppVersion) == "" || info.UUID == "":
		return err
	}
	if info.CurrentGame != 0 && info.CurrentGame != app.ID {
		return errMLOtherAppRunning
	}
	resume := info.CurrentGame != 0 && info.CurrentGame == app.ID
	sw, sh := mlScreenSize()
	width, height, fps, kbps := h.Settings.Resolve(sw, sh)
	keys, err := mlNewLaunchKeys()
	if err != nil {
		return err
	}
	defer func() {
		// The key has done its job once the file holds it.
		for i := range keys.key {
			keys.key[i] = 0
		}
	}()
	rtspURL, err := mlLaunchApp(netCtx, h, host, mlLaunchParams{
		appID:     app.ID,
		width:     width,
		height:    height,
		fps:       fps,
		keys:      keys,
		audioOnPC: h.Settings.PlayAudioOnPC,
		// Sent whenever the setting is on, not only when serverinfo carried
		// VirtualDisplayCapable: that element is Apollo's, and a fork that
		// leaves it out still honours the request; Sunshine ignores the key.
		virtualDisplay: h.Settings.UseVirtualDisplay(),
	}, resume)
	if cerr := ctx.Err(); cerr != nil {
		if err == nil {
			logInfo("moonlight", "called off after %s started %q; it keeps running there", h.Label(), app.Title)
		}
		return cerr
	}
	if err != nil {
		return err
	}
	verb := "launched"
	if resume {
		verb = "resumed"
	}
	logInfo("moonlight", "%s %q on %s at %dx%d, %d fps, %d kbps", verb, app.Title, h.Label(), width, height, fps, kbps)

	device := DetectDevice()
	session := mlSessionFile(mlSession{
		host:         host,
		appVersion:   info.AppVersion,
		gfeVersion:   info.GfeVersion,
		rtspURL:      rtspURL,
		codecSupport: info.CodecSupport,
		keys:         keys,
		width:        width,
		height:       height,
		fps:          fps,
		bitrate:      kbps,
		codec:        h.Settings.mlCodec(),
		remote:       mlStreamIsRemote(ctx, host),
		name:         mlTruncate(h.Label(), mlStreamNameMax),
		appName:      mlTruncate(app.Title, mlAppNameMax),
		volume:       h.Settings.mlVolume(),
		rumble:       !h.Settings.NoRumble,
		hints:        true,
		dpadStick:    psDpadStickDefault(device),
		motor:        psStreamMotorLookup(),
		statusFile:   mlLastRunPath(),
	})
	path, err := mlWriteSessionFile(session)
	if err != nil {
		return err
	}
	// The last moment a cancel is taken: from here the screen is handed over.
	if cerr := ctx.Err(); cerr != nil {
		_ = os.Remove(path)
		logInfo("moonlight", "called off after %s started %q; it keeps running there", h.Label(), app.Title)
		return cerr
	}
	// The hand-off runs after JukaHub exits; a log that cannot be written is
	// noted here (the script falls back to mlFallbackLogPath and says so).
	if f, err := os.OpenFile(mlLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err != nil {
		logWarn("moonlight", "cannot write %s (%v); the stream logs to %s", mlLogPath(), err, mlFallbackLogPath)
	} else {
		_ = f.Close()
	}
	status := mlLastRunPath()
	mlMarkStreamStarted(status, host)
	mlSaveLastSession(mlLastSession{uuid: h.UUID, addr: host, appID: app.ID, appName: app.Title, fps: fps})
	mlRememberLastApp(h.UUID, app)
	if err := psStartHandoffWith(mlHandoffSpec(), engine, path, "Moonlight "+mlTruncate(h.Label(), mlStreamNameMax)); err != nil {
		_ = os.Remove(path)
		_ = os.Remove(status)
		_ = os.Remove(mlLastSessionPath())
		return err
	}
	requestAppExit()
	return nil
}

// mlRememberLastApp keeps the game just started on the PC's entry.
func mlRememberLastApp(uuid string, app mlApp) {
	if _, _, err := mlUpdateHost(uuid, func(s *mlHost) {
		s.LastAppID, s.LastAppName = app.ID, app.Title
		s.LastSeen = time.Now()
	}); err != nil {
		logWarn("moonlight", "saving the last game failed: %v", err)
	}
}

// mlStartDesktopStream is mlStartStream on a computer: Moonlight PC streams.
func mlStartDesktopStream(h mlHost, app mlApp, host string) error {
	name, path, args, ok := mlDesktopClient()
	if !ok {
		return errMLHelperMissing
	}
	target := host
	if port := mlHTTPPortOf(h); port != mlHTTPPortDefault {
		target = net.JoinHostPort(host, strconv.Itoa(port))
	} else if strings.Contains(host, ":") {
		target = "[" + host + "]"
	}
	argv := append(append([]string(nil), args...), "stream", target, app.Title)
	cmd := hiddenCommand(path, argv...)
	cmd.Dir = filepath.Dir(path)
	configureChiakiDetached(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	go func() { _ = cmd.Wait() }()
	logInfo("moonlight", "%s streams %q from %s (%s)", name, app.Title, h.Label(), target)
	mlRememberLastApp(h.UUID, app)
	return nil
}

// ── The helper install ───────────────────────────────────────────────────

// mlHelperInstall is why the last install of the Remote Play package left
// moonlight-stream out ("" when it put it in or had no reason to), and when a
// package without it was last seen: a press within mlHelperNoneRetry of that
// is answered without another download.
var mlHelperInstall struct {
	mu     sync.Mutex
	note   string
	noneAt time.Time
}

// mlSetHelperInstallNote is the installer's word on moonlight-stream
// (psremote_helpers_install.go); none says the package did not carry it.
func mlSetHelperInstallNote(note string, none bool) {
	mlHelperInstall.mu.Lock()
	mlHelperInstall.note = note
	if none {
		mlHelperInstall.noneAt = time.Now()
	} else {
		mlHelperInstall.noneAt = time.Time{}
	}
	mlHelperInstall.mu.Unlock()
	mlForgetSupported()
}

func mlHelperInstallState() (string, time.Time) {
	mlHelperInstall.mu.Lock()
	defer mlHelperInstall.mu.Unlock()
	return mlHelperInstall.note, mlHelperInstall.noneAt
}

// mlEnsureHelper makes sure moonlight-stream is installed, starting the
// Remote Play package's install (or joining the one that runs) when it is
// missing, and calls done on the main loop: ok with "" when the helper was
// already there, ok with a sentence when it was just installed, or !ok with
// the reason. Returns at once.
func mlEnsureHelper(done func(ok bool, msg string)) {
	report := func(ok bool, msg string) {
		mlForgetSupported()
		if done != nil {
			tbxPost(func() { done(ok, msg) })
		}
	}
	if mlHelperPath() != "" {
		report(true, "")
		return
	}
	if !psHelperSupportedFn() {
		report(false, "Moonlight streams through Moonlight PC on a computer: install it from moonlight-stream.org")
		return
	}
	if note, noneAt := mlHelperInstallState(); note != "" && !noneAt.IsZero() && time.Since(noneAt) < mlHelperNoneRetry && !psHelpersRunning() {
		report(false, note)
		return
	}
	if !psHelpersRunning() && !IsOnline() {
		report(false, "Moonlight needs a one-time download, and "+psHelperNoInternetText())
		return
	}
	// A remembered refusal of the PS helpers does not hold this press back
	// (psHelpersStartFor), and is never its answer: moonlight-stream is
	// judged on its own.
	if !psHelpersStartFor(psStartPress, true) {
		if mlHelperPath() != "" {
			report(true, "")
			return
		}
		report(false, "Could not start the Moonlight download — try again in a moment")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), psHelperTimeout+time.Minute)
		defer cancel()
		err := psHelpersWait(ctx)
		psHelpersForgetPresence()
		if mlHelperPath() != "" {
			report(true, "Moonlight is installed")
			return
		}
		note, _ := mlHelperInstallState()
		switch {
		case errors.Is(err, errMLHelperWontRun) && note != "":
			// A Moonlight-only install this firmware refused: the note says
			// it in full.
			report(false, note)
		case err != nil && !errors.Is(err, errPSHelpersMissing) && !errors.Is(err, errPSHelpersWontRun):
			report(false, "Could not install Moonlight — "+psHelpersFailText(err))
		case note != "":
			// Moonlight's own word, also when the PS helpers were refused.
			report(false, note)
		default:
			report(false, "Moonlight did not install — try again")
		}
	}()
}


package main

// usbstorage.go — "USB storage mode" (Settings → Network, under USB file
// transfer): the SD card becomes a plain USB drive on a computer, the way a
// card reader shows it — any program, any file manager, full USB speed, a
// Mac without OpenMTP — and JukaHub comes back when the computer ejects it.
// USB file transfer (usbmtp.go) stays the everyday way, because it works
// while JukaHub runs.
//
// The catch is that JukaHub runs from that card. A computer writing to a
// file system the handheld still has mounted damages it, so the card must be
// unmounted first, and nothing may be using it then — not JukaHub, not
// launch.sh (a shell reads its script from the card as it goes), not the
// shell the stock menu ran launch.sh from (its working folder is JukaHub's).
// So the lending happens with JukaHub gone, by a shell script in RAM:
//
//  1. Here: the row's checks (usbstorage_linux.go: the USB port can be a
//     device, the kernel has a mass-storage function, the card is a
//     partition of its own, nothing else holds it, a swap file on it can be
//     moved into memory), a sheet that says what happens, then a full-screen
//     card. Once it is on the panel its picture is kept in /tmp (the boot
//     frame's raw framebuffer page, bootframe_linux.go), the helper
//     (usbstorage_helper.sh, built into the binary so it always matches it)
//     and the request are written there, and JukaHub quits the normal way:
//     games closed, everything saved, USB file transfer and controller mode
//     give the port back (their Cleanup hooks).
//  2. launch.sh sees the request after the clean exit, pauses the firmware's
//     launcher loop above it (so the stock menu stays closed), starts the
//     helper detached and ends without handing the screen back.
//  3. The helper (its header has the details) puts the picture back on the
//     panel, waits for the shells above launch.sh to finish, refuses when
//     anything else still uses the card, unmounts it, lends it out through a
//     USB gadget of its own, waits for the eject (or a button, the cable, the
//     time limit), mounts the card back as it was and starts launch.sh anew,
//     whose hand-back at the very end lets the firmware's loop run again.
//  4. The next start reads how it went (usbStorageResult) and says so.
//
// Only where launch.sh says it knows the protocol (JUKAHUB_USB_STORAGE=1,
// exported by the running launcher, so a newer launch.sh copied under an
// older running one never makes the app expect it), on the Smart Pro, the
// Brick and the Hammer. Not on the Smart Pro S or the Brick Pro: their newer
// firmware answers the card's device closing with its own "card removed"
// script (a lazy unmount and a recursive delete of the mount point), which a
// mount back would race. Not on the JukaHub OS image (its
// own launcher), not under NextUI (its menu runs from the card).

import (
	_ "embed"
	"os"
	"runtime"
	"strings"
	"sync"
)

const (
	// usbStorageEnv is launch.sh's word that it hands the card over.
	usbStorageEnv = "JUKAHUB_USB_STORAGE"
	// The files in RAM (/tmp is empty at every boot) that JukaHub, launch.sh
	// and the helper pass on. The request is written last, by a rename:
	// launch.sh acts on it only after a clean exit.
	usbStorageRequestPath = "/tmp/jukahub-usbstorage.req"
	usbStorageHelperPath  = "/tmp/jukahub-usbstorage-helper.sh"
	usbStorageResultPath  = "/tmp/jukahub-usbstorage.result"
	usbStoragePicturePath = "/tmp/jukahub-usbstorage.fb"
	// usbStorageHelperToken is the helper's second line; launch.sh runs no
	// file without it.
	usbStorageHelperToken = "jukahub-usbstorage-helper: 1"
)

//go:embed usbstorage_helper.sh
var usbStorageHelperScript []byte

// usbStoragePlan is what the checks found: the card and why it cannot be lent
// out, if it cannot.
type usbStoragePlan struct {
	mount   string // the card's mount point
	dev     string // its block device
	fsType  string
	size    string // "59.5 GB", for the sheet
	problem string // "" when it can go
}

// usbStorage is the mode's state on the main thread. leaving is set once the
// player chose Start: the full-screen card is drawn from then until JukaHub
// quits (or the save fails); frames counts its frames, and the one that
// reaches usbStorageCardFrames starts the save, once.
var usbStorage struct {
	checking bool
	leaving  bool
	frames   int
	plan     usbStoragePlan
}

func init() {
	// How the last lending went, after the first frame (a toast needs one).
	deferStartupTask("usb-storage-result", usbStorageShowResult)
}

// usbStorageModelOK: the handhelds whose firmware leaves an unmounted card
// alone (see the header).
func usbStorageModelOK() bool {
	switch DetectDevice() {
	case DeviceTrimuiSmartPro, DeviceTrimuiBrick, DeviceTrimuiHammer:
		return true
	}
	return false
}

// usbStorageSupported decides whether the row shows at all. None of it
// changes while JukaHub runs, and the row asks every frame.
var usbStorageSupported = sync.OnceValue(func() bool {
	return runtime.GOOS == "linux" && usbStorageModelOK() && !jukaosNativeActive() && launchedByDeviceLauncher()
})

// usbStorageUnavailable is why the row is greyed out, "" when it is not.
func usbStorageUnavailable() string {
	switch {
	case strings.EqualFold(os.Getenv("JUKAHUB_HOST"), "nextui") || os.Getenv("SHARED_USERDATA_PATH") != "":
		return "Not under NextUI, whose menu runs from the SD card — use USB file transfer"
	case os.Getenv(usbStorageEnv) != "1":
		return "Restart JukaHub to use this (its launcher was updated)"
	case usbPadHolds.Load():
		return "Controller mode is using the USB port — stop it first"
	}
	return ""
}

// usbStorageDetail is the line under the row.
func usbStorageDetail(*Config) string {
	if why := usbStorageUnavailable(); why != "" {
		return why
	}
	if usbStorage.checking {
		return "Checking the SD card…"
	}
	return "The SD card as a USB drive on a computer; JukaHub closes until you eject it there"
}

// usbStorageSettingRow is the action in Settings → Network.
func usbStorageSettingRow() setRow {
	return setRow{
		ID: "usbStorageMode", Label: "USB storage mode", Kind: setKindAction,
		Visible:  func(*Config) bool { return usbStorageSupported() },
		Disabled: func(*Config) bool { return usbStorageUnavailable() != "" },
		Detail:   usbStorageDetail,
		Open:     usbStorageOpen,
	}
}

// usbStorageOpen runs the checks on a worker (they read every process's
// files) and shows the answer. Main thread.
func usbStorageOpen(config *Config) {
	if usbStorage.checking || usbStorage.leaving {
		return
	}
	if why := usbStorageUnavailable(); why != "" {
		ShowToast(why, ToastKindWarning)
		return
	}
	usbStorage.checking = true
	go func() {
		plan := usbStorageCheck()
		tbxPost(func() {
			usbStorage.checking = false
			usbStorageShowPlan(appConfig, plan)
		})
	}()
}

// usbStorageBegin puts the full-screen card up; usbStorageRender takes it
// from there. Main thread.
func usbStorageBegin(plan usbStoragePlan) {
	if usbStorage.leaving || exitRequested.Load() {
		return
	}
	usbStorage.plan = plan
	usbStorage.leaving = true
	usbStorage.frames = 0
	logInfo("usbstorage", "lending %s (%s) to a computer: JukaHub quits for the helper", plan.dev, plan.fsType)
}

// usbStorageSave writes what launch.sh and the helper need, the request
// last, then asks JukaHub to quit. A worker: the picture is a few megabytes.
// A picture that cannot be kept only leaves the panel as JukaHub left it.
func usbStorageSave(plan usbStoragePlan) {
	picture := ""
	if pix, info, err := bootFrameGrab(); err != nil {
		logWarn("usbstorage", "the USB storage card was not kept for the helper: %v", err)
	} else if err := os.WriteFile(usbStoragePicturePath+".info", []byte(info), 0o600); err != nil {
		logWarn("usbstorage", "picture not written: %v", err)
	} else if err := os.WriteFile(usbStoragePicturePath, pix, 0o600); err != nil {
		logWarn("usbstorage", "picture not written: %v", err)
	} else {
		picture = usbStoragePicturePath
	}
	fail := func(err error) {
		logWarn("usbstorage", "USB storage mode not started: %v", err)
		tbxPost(func() {
			usbStorage.leaving = false
			ShowToast("USB storage mode could not start: "+firstLine(err.Error()), ToastKindError)
		})
	}
	_ = os.Remove(usbStorageHelperPath)
	if err := os.WriteFile(usbStorageHelperPath, usbStorageHelperScript, 0o700); err != nil {
		fail(err)
		return
	}
	var b strings.Builder
	b.WriteString("# jukahub-usbstorage-request: 1 (player/usbstorage.go; read by the helper, never run)\n")
	for _, kv := range [][2]string{
		{"mount", plan.mount},
		{"dev", plan.dev},
		{"name", usbMtpDeviceName()},
		{"serial", usbMtpSerial()},
		{"picture", picture},
	} {
		b.WriteString(kv[0] + "=" + strings.Map(usbStorageLineRune, kv[1]) + "\n")
	}
	tmp := usbStorageRequestPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		fail(err)
		return
	}
	if err := os.Rename(tmp, usbStorageRequestPath); err != nil {
		_ = os.Remove(tmp)
		fail(err)
		return
	}
	tbxPost(requestAppExit)
}

// usbStorageLineRune keeps a request value on its line.
func usbStorageLineRune(r rune) rune {
	if r == '\n' || r == '\r' || r == 0 {
		return -1
	}
	return r
}

// usbStorageShowResult says how the last lending went, once, and forgets it:
// "ok <how>" or "failed <reason>" from the helper (or launch.sh), then notes
// such as the file system check's answer. Main thread, after the first frame.
func usbStorageShowResult() {
	if runtime.GOOS != "linux" {
		return
	}
	// The picture (a few MB of RAM) and the helper go even when no helper ran
	// (launch.sh dropped the request after an exit that was not clean): none
	// runs while JukaHub does.
	_ = os.Remove(usbStoragePicturePath)
	_ = os.Remove(usbStoragePicturePath + ".info")
	_ = os.Remove(usbStorageHelperPath)
	data, err := os.ReadFile(usbStorageResultPath)
	if err != nil {
		return
	}
	_ = os.Remove(usbStorageResultPath)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	status, rest, _ := strings.Cut(strings.TrimSpace(lines[0]), " ")
	repaired, checkFailed := false, false
	for _, l := range lines[1:] {
		l = strings.TrimSpace(l)
		if l == "fsck repaired" {
			repaired = true
		}
		if strings.HasPrefix(l, "fsck failed") {
			checkFailed = true
		}
	}
	logInfo("usbstorage", "last lending: %s", strings.Join(lines, " | "))
	if status != "ok" {
		msg := strings.TrimSpace(rest)
		if msg == "" {
			msg = "the helper did not say why"
		}
		// Every "failed" comes before the card was lent out: a card that
		// will not mount afterwards restarts the handheld, and /tmp with it.
		ShowToast("USB storage mode did not start: "+msg, ToastKindError)
		return
	}
	text := "The SD card is back from the computer"
	switch strings.TrimSpace(rest) {
	case "button", "unplugged", "timeout":
		text = "The SD card is back. Next time, eject it on the computer first"
	case "idle":
		text = "No computer used the SD card in 10 minutes, so JukaHub came back"
	case "stopped":
		text = "USB storage mode was ended by the power button; the SD card is back"
	}
	kind := ToastKindSuccess
	if strings.TrimSpace(rest) == "mounted" {
		// The helper saw the card mounted on the handheld while the computer
		// had it (an automount) and ended the lending at once.
		text = "The handheld opened the SD card while the computer had it, so it was taken back. Check the files you copied"
		kind = ToastKindWarning
	}
	switch {
	case checkFailed:
		text += " · the card check found problems it could not fix"
		kind = ToastKindWarning
	case repaired:
		text += " · the card check repaired it"
	}
	ShowToast(text, kind)
}

package main

// sched.go — the Scheduled tasks screen (config scene "Cron", titled
// "Scheduled tasks").
//
// It lists what the system runs on its own, in two tabs. Scheduled: Task
// Scheduler on Windows (schtasks, read through its CSV and XML exports, never
// cron), and on the handheld and Linux every crontab the app can read (the
// user's, /etc/crontab, /etc/cron.d, BusyBox's /etc/crontabs and the spool),
// the periodic run-parts folders and systemd timers. Startup: on Windows the
// registry's Run / RunOnce keys and the Startup folders; on the handheld the
// SD card's System/starts boot scripts, init.d services and rc.local; on a
// Linux desktop the autostart entries. JukaHub's own "Start at login" item is
// listed first everywhere and can be switched from here.
//
// The lists load by themselves when the screen opens, as scene tasks (leaving
// stops them), and every schedule is said in words — "Daily at 03:00", "At
// logon" — with the next run and the status. A details sheet shows the rest.
// Only items that belong to the user and can be switched back exactly are
// toggleable: JukaHub's launcher, the user's Windows startup apps (through
// Task Manager's StartupApproved flags) and the user's desktop autostart
// entries.

import (
	"context"
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

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// schedSceneName is the config scene the Scheduled tasks screen lives in. The
// name stays "Cron" so existing configs and menus keep working; the screen's
// title is "Scheduled tasks".
const schedSceneName = "Cron"

const (
	// schedToolTimeout bounds one helper run (schtasks, crontab, systemctl).
	schedToolTimeout = 20 * time.Second
	// schedFileMaxBytes bounds one crontab or script read.
	schedFileMaxBytes = 256 << 10
	// schedDirMax bounds one folder listing (a cron.d with thousands of files
	// is not a schedule anyone reads on a handheld).
	schedDirMax = 200
)

// schedOpen opens the Scheduled tasks screen (cross-area contract: the Tools
// hub and the Toolbox call it).
func schedOpen(config *Config) {
	idx := sceneIndexExact(config, schedSceneName)
	if idx < 0 {
		showToast("Scheduled tasks are not available", ToastWarn())
		return
	}
	if idx == currentSceneIndex {
		return
	}
	changeSceneTo(config, idx)
}

// ── Data ─────────────────────────────────────────────────────────────────

// schedDataState is what the loaders publish. The render thread copies it
// into schedUI when version changes.
type schedDataState struct {
	mu      sync.Mutex
	gen     uint64
	version uint64
	loading [2]bool
	loaded  [2]bool
	items   [2][]schedItem
	errText [2]string
	updated time.Time
}

var schedData schedDataState

// schedEnter starts a fresh load whenever the screen opens. What was listed
// last time stays on screen while it runs, marked as updating.
func schedEnter(config *Config) {
	_ = config
	schedUI.sheet = false
	schedUI.ensureVisible = true
	schedData.mu.Lock()
	schedData.gen++
	gen := schedData.gen
	schedData.loading = [2]bool{true, true}
	schedData.version++
	schedData.mu.Unlock()
	started := runSceneTask(func(ctx context.Context) {
		now := time.Now()
		// Startup items first: they are local reads and arrive at once,
		// while the Task Scheduler export can take a second or two.
		startup := schedLoadStartup(ctx)
		schedPublish(ctx, gen, schedTabStartup, startup, "")
		tasks, errText := schedLoadTasks(ctx, now)
		schedPublish(ctx, gen, schedTabTasks, tasks, errText)
	})
	if !started {
		schedData.mu.Lock()
		schedData.loading = [2]bool{}
		schedData.version++
		schedData.mu.Unlock()
	}
}

// schedPublish hands one tab's list to the screen, unless the screen was left
// or a newer load replaced this one.
func schedPublish(ctx context.Context, gen uint64, tab schedTab, items []schedItem, errText string) {
	if ctx.Err() != nil {
		return
	}
	schedData.mu.Lock()
	defer schedData.mu.Unlock()
	if gen != schedData.gen {
		return
	}
	schedData.items[tab] = items
	schedData.errText[tab] = errText
	schedData.loading[tab] = false
	schedData.loaded[tab] = true
	schedData.updated = time.Now()
	schedData.version++
}

// schedRunTool runs a helper with a timeout, no console window and bounded
// output, and turns a failure into its first line of explanation.
func schedRunTool(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, schedToolTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	schedHideWindow(cmd)
	cmd.WaitDelay = 2 * time.Second
	var out, errOut taskOutputBuffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(schedDecodeText(errOut.Bytes()))
		if msg == "" {
			msg = strings.TrimSpace(schedDecodeText(out.Bytes()))
		}
		if msg != "" {
			return out.Bytes(), fmt.Errorf("%s: %w", firstLine(msg), err)
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// schedReadFile reads a small text file, or reports false.
func schedReadFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, schedFileMaxBytes)
	n, _ := f.Read(buf)
	if n <= 0 {
		return "", false
	}
	return schedDecodeText(buf[:n]), true
}

// schedDirFile is one regular file in a listed folder.
type schedDirFile struct {
	name, path string
}

// schedListDir lists a folder's regular files (links followed), bounded and
// without the placeholder files packages leave there.
func schedListDir(dir string) []schedDirFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []schedDirFile
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.EqualFold(name, "README") || strings.HasSuffix(name, "~") ||
			strings.HasSuffix(name, ".dpkg-old") || strings.HasSuffix(name, ".dpkg-dist") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		out = append(out, schedDirFile{name: name, path: path})
		if len(out) >= schedDirMax {
			break
		}
	}
	return out
}

// schedFriendlyError says why a list could not be read without the syscall.
func schedFriendlyError(err error, what string) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, exec.ErrNotFound) {
		return what + " isn't available on this system."
	}
	return "Couldn't read " + what + ": " + firstLine(err.Error())
}

// schedLoadTasks reads the Scheduled tab for this platform.
func schedLoadTasks(ctx context.Context, now time.Time) ([]schedItem, string) {
	if runtime.GOOS == "windows" {
		return schedLoadWindowsTasks(ctx)
	}
	return schedLoadUnixTasks(ctx, now), ""
}

// schedLoadWindowsTasks reads Task Scheduler twice at once: the CSV export for
// status, next and last run (in the user's language), and the XML export for
// the schedule itself (in the scheduler's own vocabulary, so "Daily at 03:00"
// can be said on any Windows). Either one alone still gives a usable list.
func schedLoadWindowsTasks(ctx context.Context) ([]schedItem, string) {
	var csvOut, xmlOut []byte
	var csvErr, xmlErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		csvOut, csvErr = schedRunTool(ctx, "schtasks", "/query", "/fo", "CSV", "/v")
	}()
	go func() {
		defer wg.Done()
		xmlOut, xmlErr = schedRunTool(ctx, "schtasks", "/query", "/xml")
	}()
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ""
	}
	var rows []schedTaskRow
	if csvErr == nil {
		rows, _ = schedParseTasksCSV(csvOut)
	}
	var tasks []schedXMLTask
	if xmlErr == nil {
		tasks, _ = schedParseTasksXML(xmlOut)
	}
	if len(rows) == 0 && len(tasks) == 0 {
		if csvErr != nil {
			return nil, schedFriendlyError(csvErr, "Task Scheduler")
		}
		return nil, ""
	}
	items := schedTaskItems(rows, tasks)
	schedSortItems(items)
	return items, ""
}

// schedLoadUnixTasks reads every crontab the app can see, the periodic
// run-parts folders and systemd timers. A job that appears twice (the user's
// crontab is also /etc/crontabs/<user> on BusyBox) is listed once.
func schedLoadUnixTasks(ctx context.Context, now time.Time) []schedItem {
	var out []schedItem
	seen := map[string]bool{}
	add := func(items []schedItem) {
		for _, it := range items {
			key := it.Schedule + "\x00" + it.Command + "\x00" + it.User
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, it)
		}
	}
	me := termUserName()
	if termHasProgram("crontab") {
		if data, err := schedRunTool(ctx, "crontab", "-l"); err == nil {
			add(schedParseCrontab(schedDecodeText(data), "crontab -l", "Your crontab", me, false, now))
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	if text, ok := schedReadFile("/etc/crontab"); ok {
		add(schedParseCrontab(text, "/etc/crontab", "System crontab", "", true, now))
	}
	for _, f := range schedListDir("/etc/cron.d") {
		if text, ok := schedReadFile(f.path); ok {
			add(schedParseCrontab(text, f.path, "System crontab", "", true, now))
		}
	}
	for _, dir := range []string{"/etc/crontabs", "/var/spool/cron/crontabs", "/var/spool/cron"} {
		for _, f := range schedListDir(dir) {
			text, ok := schedReadFile(f.path)
			if !ok {
				continue
			}
			group := "Other users' crontabs"
			if f.name == me {
				group = "Your crontab"
			}
			add(schedParseCrontab(text, f.path, group, f.name, false, now))
		}
	}
	for _, p := range []struct{ dir, when string }{
		{"/etc/cron.hourly", "Every hour"},
		{"/etc/cron.daily", "Daily"},
		{"/etc/cron.weekly", "Weekly"},
		{"/etc/cron.monthly", "Monthly"},
	} {
		for _, f := range schedListDir(p.dir) {
			add([]schedItem{{
				Group:    "Periodic scripts",
				Name:     f.name,
				Schedule: p.when + " (run-parts)",
				Status:   "Enabled",
				Enabled:  true,
				Command:  f.path,
				Source:   p.dir,
				Icon:     "schedule",
			}})
		}
	}
	if ctx.Err() == nil && termHasProgram("systemctl") {
		if data, err := schedRunTool(ctx, "systemctl", "list-timers", "--all", "--no-pager", "--no-legend"); err == nil {
			add(schedParseTimers(schedDecodeText(data)))
		}
	}
	schedSortItems(out)
	return out
}

// schedLoadStartup reads the Startup tab for this platform, JukaHub first.
func schedLoadStartup(ctx context.Context) []schedItem {
	var out []schedItem
	if it, ok := schedJukaHubItem(); ok {
		out = append(out, it)
	}
	if runtime.GOOS == "windows" {
		out = append(out, schedStartupFolderItems()...)
		out = append(out, schedRegistryStartupItems()...)
	} else if ctx.Err() == nil {
		out = append(out, schedLoadUnixStartup()...)
	}
	schedSortItems(out)
	return out
}

// schedJukaHubItem is JukaHub's own launcher, listed whether it is installed or
// not, so start-at-login can be switched on right here.
func schedJukaHubItem() (schedItem, bool) {
	if !autostartSupported() {
		return schedItem{}, false
	}
	path, err := autoStartLauncherPath()
	if err != nil {
		return schedItem{}, false
	}
	on := autoStartInstalled()
	it := schedItem{
		Group:    "JukaHub",
		Name:     "Start JukaHub automatically",
		Schedule: "At login",
		Enabled:  on,
		Status:   "Off",
		Command:  path,
		Source:   filepath.Dir(path),
		Icon:     "juka",
		Comment:  "The same switch as Settings → Smart Widgets → Start JukaHub at login.",
		Toggle:   schedToggleJukaHub,
	}
	if autostartIsTrimui() {
		it.Schedule = "When the device boots"
	}
	if on {
		it.Status = "On"
	}
	return it, true
}

// schedLoadUnixStartup lists what starts with the handheld or the Linux
// session: the SD card's boot scripts (TrimUI and CrossMix run every file in
// System/starts), init.d services, rc.local, desktop autostart entries and
// systemd's enabled services.
func schedLoadUnixStartup() []schedItem {
	var out []schedItem

	own := ""
	if p, err := autoStartLauncherPath(); err == nil {
		own = filepath.Clean(p)
	}
	root := strings.TrimSpace(trimuiSDRootFn())
	if root == "" && IsTrimuiDevice() {
		root = "/mnt/SDCARD"
	}
	if root != "" {
		dir := filepath.Join(root, "System", "starts")
		for _, f := range schedListDir(dir) {
			if filepath.Clean(f.path) == own {
				continue
			}
			out = append(out, schedItem{
				Group:    "SD card boot scripts",
				Name:     f.name,
				Schedule: "When the device boots",
				Status:   "Runs at boot",
				Enabled:  true,
				Command:  f.path,
				Comment:  schedScriptComment(f.path),
				Source:   dir,
				Icon:     "power",
			})
		}
	}

	enabled, haveRC := schedRCEnabled()
	for _, f := range schedListDir("/etc/init.d") {
		switch f.name {
		case "rcS", "rcK", "rc", "functions", "skeleton", "halt", "reboot", "single":
			continue
		}
		on := enabled[f.name]
		if !haveRC {
			// BusyBox's rcS runs the S?? scripts of init.d itself.
			on = len(f.name) > 2 && f.name[0] == 'S' && f.name[1] >= '0' && f.name[1] <= '9'
		}
		it := schedItem{
			Group:    "System services",
			Name:     f.name,
			Schedule: "When the device boots",
			Status:   "Starts at boot",
			Enabled:  on,
			Command:  f.path,
			Comment:  schedScriptComment(f.path),
			Source:   "/etc/init.d",
			Icon:     "server",
		}
		if !on {
			it.Schedule = "When started by hand"
			it.Status = "Installed"
		}
		out = append(out, it)
	}

	if text, ok := schedReadFile("/etc/rc.local"); ok {
		var cmds []string
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || line == "exit 0" {
				continue
			}
			cmds = append(cmds, line)
		}
		if len(cmds) > 0 {
			out = append(out, schedItem{
				Group:    "System services",
				Name:     "rc.local",
				Schedule: "When the device boots",
				Status:   "Runs at boot",
				Enabled:  true,
				Command:  strings.Join(cmds, "; "),
				Source:   "/etc/rc.local",
				Icon:     "power",
			})
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".config", "autostart")
		for _, f := range schedListDir(dir) {
			if !strings.HasSuffix(f.name, ".desktop") || filepath.Clean(f.path) == own {
				continue
			}
			text, ok := schedReadFile(f.path)
			if !ok {
				continue
			}
			out = append(out, schedDesktopItem(text, f, dir, "Your autostart apps", true))
		}
	}
	for _, f := range schedListDir("/etc/xdg/autostart") {
		if !strings.HasSuffix(f.name, ".desktop") {
			continue
		}
		if text, ok := schedReadFile(f.path); ok {
			out = append(out, schedDesktopItem(text, f, "/etc/xdg/autostart", "Autostart apps for all users", false))
		}
	}

	for _, dir := range []string{"/etc/systemd/system/multi-user.target.wants", "/etc/systemd/system/graphical.target.wants"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for i, e := range entries {
			if i >= schedDirMax {
				break
			}
			out = append(out, schedItem{
				Group:    "systemd services",
				Name:     strings.TrimSuffix(e.Name(), ".service"),
				Schedule: "When the system starts",
				Status:   "Enabled",
				Enabled:  true,
				Command:  filepath.Join(dir, e.Name()),
				Source:   dir,
				Icon:     "server",
			})
		}
	}
	return out
}

// schedDesktopItem turns one autostart .desktop file into a row.
func schedDesktopItem(text string, f schedDirFile, dir, group string, user bool) schedItem {
	e := schedParseDesktopEntry(text)
	name := e.Name
	if name == "" {
		name = strings.TrimSuffix(f.name, ".desktop")
	}
	it := schedItem{
		Group:    group,
		Name:     name,
		Schedule: "At login",
		Status:   "Enabled",
		Enabled:  !e.Hidden,
		Command:  e.Exec,
		Comment:  e.Comment,
		Source:   f.path,
		Icon:     "startup",
	}
	if e.Hidden {
		it.Status = "Disabled"
	}
	if user {
		it.Toggle = schedToggleXDG
		it.ToggleKey = f.path
	}
	return it
}

// schedScriptComment is a script's first comment line (after the #! line),
// which usually says what it is for.
func schedScriptComment(path string) string {
	text, ok := schedReadFile(path)
	if !ok {
		return ""
	}
	for i, line := range strings.Split(text, "\n") {
		if i > 12 {
			break
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#!") || !strings.HasPrefix(line, "#") {
			continue
		}
		c := strings.TrimSpace(strings.TrimLeft(line, "#"))
		if c != "" && !strings.HasPrefix(c, "-") && !strings.HasPrefix(c, "=") {
			return c
		}
	}
	return ""
}

// schedRCEnabled maps init.d script names to "linked as S?? in a run level",
// the way OpenWrt-style (/etc/rc.d) and SysV (/etc/rc?.d) systems enable a
// service at boot. haveRC is false when there are no run-level folders at all.
func schedRCEnabled() (map[string]bool, bool) {
	enabled := map[string]bool{}
	haveRC := false
	for _, dir := range []string{"/etc/rc.d", "/etc/rcS.d", "/etc/rc2.d", "/etc/rc3.d", "/etc/rc4.d", "/etc/rc5.d"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		haveRC = true
		for _, e := range entries {
			name := e.Name()
			if len(name) < 4 || name[0] != 'S' {
				continue
			}
			// S10network → network
			rest := name[1:]
			for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
				rest = rest[1:]
			}
			if rest != "" {
				enabled[rest] = true
			}
		}
	}
	return enabled, haveRC
}

// ── Toggling ─────────────────────────────────────────────────────────────

// schedApplyToggle switches one item on or off.
func schedApplyToggle(config *Config, it schedItem, on bool) error {
	switch it.Toggle {
	case schedToggleJukaHub:
		if err := setAutoStart(on); err != nil {
			return err
		}
		if config != nil && config.Variables.Custom != nil {
			// The same setting the Smart Widgets switch writes, so the two can
			// never disagree and the next start does not undo this.
			dropCustomMirror(config, "autoStart")
			config.Variables.Custom["autoStart"] = strconv.FormatBool(on)
			syncVariableOverrides(config)
		}
		return nil
	case schedToggleApproved:
		return schedSetApproved(it.ToggleKey, it.ToggleName, on)
	case schedToggleXDG:
		data, err := os.ReadFile(it.ToggleKey)
		if err != nil {
			return err
		}
		return AtomicWrite(it.ToggleKey, []byte(schedSetDesktopHidden(string(data), !on)), 0o644)
	}
	return errors.New("this item is managed by the system")
}

// schedToggleSelected is X: switch the highlighted item, when it is the user's
// to switch, and update the row in place.
func schedToggleSelected(config *Config) {
	it, ok := schedSelected()
	if !ok {
		return
	}
	if it.Toggle == schedToggleNone {
		showToast("Only your own startup apps can be switched here", ToastInfo())
		return
	}
	on := !it.Enabled
	if err := schedApplyToggle(config, it, on); err != nil {
		PlayBackSound()
		showToast("Couldn't change "+it.Name+": "+firstLine(err.Error()), ToastWarn())
		return
	}
	PlayActivateSound()
	schedMarkToggled(it, on)
	switch {
	case it.Toggle == schedToggleJukaHub && on:
		showToast("JukaHub will start automatically", ToastSuccess())
	case it.Toggle == schedToggleJukaHub:
		showToast("JukaHub will not start automatically", ToastInfo())
	case on:
		showToast(it.Name+" will start again", ToastSuccess())
	default:
		showToast(it.Name+" will not start any more", ToastInfo())
	}
}

// schedMarkToggled updates the switched item in the published list and in the
// open details sheet.
func schedMarkToggled(it schedItem, on bool) {
	status := "Disabled"
	if on {
		status = "Enabled"
	}
	if it.Toggle == schedToggleJukaHub {
		status = "Off"
		if on {
			status = "On"
		}
	}
	schedData.mu.Lock()
	for tab := range schedData.items {
		for i := range schedData.items[tab] {
			cur := &schedData.items[tab][i]
			if cur.Toggle == it.Toggle && cur.ToggleKey == it.ToggleKey && cur.ToggleName == it.ToggleName &&
				cur.Name == it.Name && cur.Command == it.Command {
				cur.Enabled = on
				cur.Status = status
			}
		}
	}
	schedData.version++
	schedData.mu.Unlock()
	if schedUI.sheet {
		schedUI.sheetItem.Enabled = on
		schedUI.sheetItem.Status = status
		schedUI.sheetLines = nil
	}
}

// ── Screen state ─────────────────────────────────────────────────────────

// schedViewRow is one row of the drawn list: a group heading, an item, or the
// note about hidden system tasks.
type schedViewRow struct {
	header string
	item   int // index into the tab's items; -1 for a heading or the note
	note   string
}

// schedRowHit maps a drawn row back to its item.
type schedRowHit struct {
	rect    sdl.Rect
	ordinal int
}

// schedSheetLine is one wrapped line of the details sheet.
type schedSheetLine struct {
	label string // set on a row's first line only
	text  string
}

type schedUIState struct {
	tab        schedTab
	cursor     [2]int   // item ordinal within the visible list
	scroll     [2]int32 // list scroll in pixels
	showSystem bool
	// ensureVisible asks the next frame to scroll the cursor into view (set
	// by the pad and keys, not by the wheel).
	ensureVisible bool

	sheet     bool
	sheetItem schedItem
	// sheetLines is the details sheet laid out for a card sheetLinesW wide in
	// sheetLinesFont, with its labels sheetLabelW wide; nil means lay it out.
	sheetLines     []schedSheetLine
	sheetLinesW    int32
	sheetLinesFont *ttf.Font
	sheetLabelW    int32

	// The published data as of the last frame, and the rows built from it.
	viewVersion uint64
	viewSystem  bool
	viewBuilt   bool
	items       [2][]schedItem
	rows        [2][]schedViewRow
	itemRows    [2][]int
	hidden      [2]int
	loading     [2]bool
	loaded      [2]bool
	errText     [2]string
	updated     time.Time

	tabRects   [2]sdl.Rect
	rowHits    []schedRowHit
	sheetRect  sdl.Rect
	toggleRect sdl.Rect
	closeRect  sdl.Rect

	statusLabel string
	statusAt    time.Time
	tabLabels   [2]string
	tabCounts   [2]int
}

var schedUI schedUIState

// schedSyncView copies newly published data and rebuilds the rows. A steady
// frame does nothing here.
func schedSyncView() {
	schedData.mu.Lock()
	if schedUI.viewBuilt && schedUI.viewVersion == schedData.version && schedUI.viewSystem == schedUI.showSystem {
		schedData.mu.Unlock()
		return
	}
	schedUI.viewVersion = schedData.version
	schedUI.items = schedData.items
	schedUI.loading = schedData.loading
	schedUI.loaded = schedData.loaded
	schedUI.errText = schedData.errText
	schedUI.updated = schedData.updated
	schedData.mu.Unlock()
	schedUI.viewSystem = schedUI.showSystem
	schedUI.viewBuilt = true
	schedUI.tabLabels = [2]string{}
	for tab := range schedUI.rows {
		schedUI.rows[tab], schedUI.itemRows[tab], schedUI.hidden[tab] = schedBuildRows(schedUI.items[tab], schedUI.showSystem)
		if n := len(schedUI.itemRows[tab]); schedUI.cursor[tab] >= n {
			schedUI.cursor[tab] = n - 1
		}
		if schedUI.cursor[tab] < 0 {
			schedUI.cursor[tab] = 0
		}
	}
	schedUI.statusLabel = ""
}

// schedBuildRows lays a tab's items out as headed groups. System tasks are
// left out unless asked for, and counted for the note at the end.
func schedBuildRows(items []schedItem, showSystem bool) (rows []schedViewRow, itemRows []int, hidden int) {
	counts := map[string]int{}
	for _, it := range items {
		if it.System && !showSystem {
			hidden++
			continue
		}
		counts[it.Group]++
	}
	group := "\x00"
	for i, it := range items {
		if it.System && !showSystem {
			continue
		}
		if it.Group != group {
			group = it.Group
			rows = append(rows, schedViewRow{header: fmt.Sprintf("%s · %d", group, counts[group]), item: -1})
		}
		itemRows = append(itemRows, len(rows))
		rows = append(rows, schedViewRow{item: i})
	}
	if hidden > 0 {
		label := "1 Windows system task hidden · Y shows it"
		if hidden != 1 {
			label = fmt.Sprintf("%d Windows system tasks hidden · Y shows them", hidden)
		}
		rows = append(rows, schedViewRow{item: -1, note: label})
	}
	return rows, itemRows, hidden
}

// schedSelected is the highlighted item of the current tab.
func schedSelected() (schedItem, bool) {
	tab := schedUI.tab
	ord := schedUI.cursor[tab]
	if ord < 0 || ord >= len(schedUI.itemRows[tab]) {
		return schedItem{}, false
	}
	row := schedUI.rows[tab][schedUI.itemRows[tab][ord]]
	if row.item < 0 || row.item >= len(schedUI.items[tab]) {
		return schedItem{}, false
	}
	return schedUI.items[tab][row.item], true
}

// schedMove moves the highlight; it reports false when there is nothing above
// to move to (so Up can go on to the top bar).
func schedMove(delta int) bool {
	tab := schedUI.tab
	n := len(schedUI.itemRows[tab])
	if n == 0 {
		return delta > 0
	}
	next := schedUI.cursor[tab] + delta
	if next < 0 {
		if schedUI.cursor[tab] == 0 {
			return false
		}
		next = 0
	}
	if next > n-1 {
		next = n - 1
	}
	if next != schedUI.cursor[tab] {
		schedUI.cursor[tab] = next
		PlayNavSound()
	}
	schedUI.ensureVisible = true
	return true
}

// schedSwitchTab shows the other list.
func schedSwitchTab(tab schedTab) {
	if schedUI.tab == tab {
		return
	}
	schedUI.tab = tab
	schedUI.ensureVisible = true
	PlayNavSound()
}

// schedOpenSheet shows the details of the highlighted item.
func schedOpenSheet() {
	it, ok := schedSelected()
	if !ok {
		return
	}
	schedUI.sheet = true
	schedUI.sheetItem = it
	schedUI.sheetLines = nil
	PlayActivateSound()
}

func schedCloseSheet() {
	schedUI.sheet = false
	schedUI.sheetLines = nil
	PlayBackSound()
}

// schedToggleSystem shows or hides Windows' own maintenance tasks.
func schedToggleSystem() {
	if schedUI.hidden[schedUI.tab] == 0 && !schedUI.showSystem {
		return
	}
	schedUI.showSystem = !schedUI.showSystem
	schedUI.ensureVisible = true
	PlayNavSound()
}

// schedFooterHints is the footer row: what the pad does on the list, or in the
// details sheet.
func schedFooterHints() []FooterHint {
	it, ok := schedSelected()
	if schedUI.sheet {
		it, ok = schedUI.sheetItem, true
	}
	toggle := FooterHint{}
	if ok && it.Toggle != schedToggleNone {
		toggle = FooterHint{Button: "X", Label: "Turn off"}
		if !it.Enabled {
			toggle = FooterHint{Button: "X", Label: "Turn on"}
		}
	}
	if schedUI.sheet {
		hints := []FooterHint{{Button: "B", Label: "Close"}}
		if toggle.Button != "" {
			hints = append(hints, toggle)
		}
		return hints
	}
	hints := []FooterHint{
		{Button: "D-Pad", Label: "Choose"},
		{Button: "A", Label: "Details"},
		{Button: "B", Label: "Back"},
		{Button: "L1/R1", Label: "Tab"},
	}
	if toggle.Button != "" {
		hints = append(hints, toggle)
	}
	if schedUI.hidden[schedUI.tab] > 0 {
		hints = append(hints, FooterHint{Button: "Y", Label: "System tasks"})
	} else if schedUI.showSystem {
		hints = append(hints, FooterHint{Button: "Y", Label: "Hide system"})
	}
	return hints
}

// ── Input ────────────────────────────────────────────────────────────────

func schedHandlePad(e *sdl.ControllerButtonEvent, config *Config) bool {
	if schedUI.sheet {
		switch e.Button {
		case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK, sdl.CONTROLLER_BUTTON_A:
			schedCloseSheet()
		case sdl.CONTROLLER_BUTTON_X:
			schedToggleSelectedSheet(config)
		}
		return true
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		goBackScene(config)
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		if !schedMove(-1) {
			return focusChromeBar()
		}
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		schedMove(1)
	case sdl.CONTROLLER_BUTTON_DPAD_LEFT, sdl.CONTROLLER_BUTTON_LEFTSHOULDER:
		schedSwitchTab(schedTabTasks)
	case sdl.CONTROLLER_BUTTON_DPAD_RIGHT, sdl.CONTROLLER_BUTTON_RIGHTSHOULDER:
		schedSwitchTab(schedTabStartup)
	case sdl.CONTROLLER_BUTTON_A:
		schedOpenSheet()
	case sdl.CONTROLLER_BUTTON_X:
		schedToggleSelected(config)
	case sdl.CONTROLLER_BUTTON_Y:
		schedToggleSystem()
	default:
		return false
	}
	return true
}

// schedToggleSelectedSheet is X inside the details sheet.
func schedToggleSelectedSheet(config *Config) {
	if schedUI.sheetItem.Toggle == schedToggleNone {
		return
	}
	it := schedUI.sheetItem
	on := !it.Enabled
	if err := schedApplyToggle(config, it, on); err != nil {
		PlayBackSound()
		showToast("Couldn't change "+it.Name+": "+firstLine(err.Error()), ToastWarn())
		return
	}
	PlayActivateSound()
	schedMarkToggled(it, on)
}

func schedHandleKey(e *sdl.KeyboardEvent, config *Config) bool {
	if schedUI.sheet {
		switch e.Keysym.Sym {
		case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b, sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
			schedCloseSheet()
		case sdl.K_x:
			schedToggleSelectedSheet(config)
		}
		return true
	}
	switch e.Keysym.Sym {
	case sdl.K_ESCAPE, sdl.K_BACKSPACE, sdl.K_b:
		goBackScene(config)
	case sdl.K_UP:
		if !schedMove(-1) {
			return focusChromeBar()
		}
	case sdl.K_DOWN:
		schedMove(1)
	case sdl.K_PAGEUP:
		schedMove(-8)
	case sdl.K_PAGEDOWN:
		schedMove(8)
	case sdl.K_HOME:
		schedMove(-len(schedUI.itemRows[schedUI.tab]))
	case sdl.K_END:
		schedMove(len(schedUI.itemRows[schedUI.tab]))
	case sdl.K_LEFT:
		schedSwitchTab(schedTabTasks)
	case sdl.K_RIGHT:
		schedSwitchTab(schedTabStartup)
	case sdl.K_TAB:
		if schedUI.tab == schedTabTasks {
			schedSwitchTab(schedTabStartup)
		} else {
			schedSwitchTab(schedTabTasks)
		}
	case sdl.K_RETURN, sdl.K_KP_ENTER, sdl.K_SPACE:
		schedOpenSheet()
	case sdl.K_x:
		schedToggleSelected(config)
	case sdl.K_y:
		schedToggleSystem()
	default:
		return false
	}
	return true
}

// schedHandleClick: a tab switches, a row opens its details, and in the sheet
// the buttons act and a click outside closes it.
func schedHandleClick(mx, my int32, config *Config) bool {
	if schedUI.sheet {
		switch {
		case schedUI.toggleRect.W > 0 && pointInRect(mx, my, schedUI.toggleRect):
			schedToggleSelectedSheet(config)
		case schedUI.closeRect.W > 0 && pointInRect(mx, my, schedUI.closeRect):
			schedCloseSheet()
		case !pointInRect(mx, my, schedUI.sheetRect):
			schedCloseSheet()
		}
		return true
	}
	for tab, r := range schedUI.tabRects {
		if r.W > 0 && pointInRect(mx, my, r) {
			schedSwitchTab(schedTab(tab))
			return true
		}
	}
	for _, hit := range schedUI.rowHits {
		if pointInRect(mx, my, hit.rect) {
			schedUI.cursor[schedUI.tab] = hit.ordinal
			schedOpenSheet()
			return true
		}
	}
	return false
}

// schedHandleWheel scrolls the list.
func schedHandleWheel(dy int32) bool {
	if schedUI.sheet || dy == 0 {
		return schedUI.sheet
	}
	schedUI.scroll[schedUI.tab] -= dy * 48
	return true
}

// ── Drawing ──────────────────────────────────────────────────────────────

// schedSubtitle says what the screen lists on this system.
func schedSubtitle() string {
	switch {
	case runtime.GOOS == "windows":
		return "Task Scheduler and the apps that start with Windows"
	case IsTrimuiDevice():
		return "Cron jobs and the scripts that run when the device boots"
	}
	return "Cron jobs, timers and the apps that start with your session"
}

// schedStatusText is the header's read-out: loading, or when it was read.
func schedStatusText() (string, sdl.Color) {
	if schedUI.loading[0] || schedUI.loading[1] {
		if schedUI.loaded[0] || schedUI.loaded[1] {
			return "Updating…", glassAccent()
		}
		return "Loading…", glassAccent()
	}
	if schedUI.updated.IsZero() {
		return "", ColorTextTertiary()
	}
	if schedUI.statusLabel == "" || !schedUI.statusAt.Equal(schedUI.updated) {
		schedUI.statusAt = schedUI.updated
		schedUI.statusLabel = "Updated " + schedUI.updated.Format("15:04")
	}
	return schedUI.statusLabel, ColorTextTertiary()
}

func renderSchedScreen(renderer *sdl.Renderer, config *Config) {
	drawAppBackdrop(renderer, config, screenWidth, screenHeight)
	schedSyncView()

	margin := scaleX(homeSideMargin, screenWidth)
	top := headerBarHeight() + 8
	bottom := screenHeight - bottomBarHeight(schedSceneName) - 16
	contentW := screenWidth - 2*margin

	status, statusColor := schedStatusText()
	headerBottom := UIPageHeaderDraw(renderer, config, UIPageHeader{
		X: margin, Y: top, W: contentW,
		Title:       "Scheduled tasks",
		TitleSize:   menuTitleSize,
		Subtitle:    schedSubtitle(),
		Status:      status,
		StatusColor: statusColor,
		Glyph: func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
			DrawHomeIcon(r, "schedule", box, false)
		},
	})

	font := getFontSized(config, "small", 17)
	small := getFontSized(config, "small", 15)
	if font == nil || small == nil {
		renderStatusBar(renderer, config)
		renderFooter(renderer, config)
		return
	}

	tabsY := headerBottom + 12
	tabsBottom := schedDrawTabs(renderer, small, margin, tabsY)
	list := sdl.Rect{X: margin, Y: tabsBottom + 12, W: contentW, H: bottom - (tabsBottom + 12)}
	schedDrawList(renderer, config, font, small, list)

	renderStatusBar(renderer, config)
	renderFooter(renderer, config)
	if schedUI.sheet {
		schedDrawSheet(renderer, config, font, small)
	}
}

// schedDrawTabs draws the two-tab switch and returns its bottom edge.
func schedDrawTabs(renderer *sdl.Renderer, font *ttf.Font, x, y int32) int32 {
	names := [2]string{"Scheduled", "Startup"}
	if runtime.GOOS == "windows" {
		names[1] = "Startup apps"
	}
	h := int32(font.Height()) + 18
	pad := int32(18)
	accent := glassAccent()
	cx := x
	for tab := 0; tab < 2; tab++ {
		// Every item the tab has, hidden system tasks included.
		count := len(schedUI.itemRows[tab]) + schedUI.hidden[tab]
		if schedUI.tabLabels[tab] == "" || schedUI.tabCounts[tab] != count {
			schedUI.tabCounts[tab] = count
			schedUI.tabLabels[tab] = names[tab]
			if schedUI.loaded[tab] {
				schedUI.tabLabels[tab] = names[tab] + "  " + strconv.Itoa(count)
			}
		}
		label := schedUI.tabLabels[tab]
		lw, lh := measureText(font, label)
		r := sdl.Rect{X: cx, Y: y, W: lw + 2*pad, H: h}
		active := schedUI.tab == schedTab(tab)
		hover := sysToolPointIn(r)
		_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
		switch {
		case active:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, WithAlpha(accent, 34))
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, glassFillFocus())
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, 2, accent)
		case hover:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, glassFillHover())
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, 1, glassEdgeHover())
		default:
			fillRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, glassFill())
			strokeRoundedRect(renderer, r.X, r.Y, r.W, r.H, h/2, 1, glassEdge())
		}
		ink := ColorTextSecondary()
		if active {
			ink = ColorTextPrimary()
		}
		drawText(renderer, font, label, r.X+pad, r.Y+(h-lh)/2, ink, textAlignLeft)
		schedUI.tabRects[tab] = r
		cx += r.W + 10
	}
	return y + h
}

// schedRowHeights are the list's row heights for the current fonts.
func schedRowHeights(font, small *ttf.Font) (item, header, note int32) {
	item = int32(font.Height()) + int32(small.Height()) + 24
	if item < 60 {
		item = 60
	}
	return item, int32(small.Height()) + 22, int32(small.Height()) + 24
}

// schedDrawList draws the current tab: its states, its headed rows with the
// selection kept in view, and the scrollbar.
func schedDrawList(renderer *sdl.Renderer, config *Config, font, small *ttf.Font, list sdl.Rect) {
	tab := schedUI.tab
	schedUI.rowHits = schedUI.rowHits[:0]
	rows := schedUI.rows[tab]

	if len(rows) == 0 {
		switch {
		case schedUI.loading[tab] || !schedUI.loaded[tab]:
			phase := float64(time.Now().UnixMilli()%2000) / 1000
			itemH, _, _ := schedRowHeights(font, small)
			for i := int32(0); i < 5; i++ {
				y := list.Y + i*(itemH+8)
				if y+itemH > list.Y+list.H {
					break
				}
				UILoadingSkeleton(renderer, sdl.Rect{X: list.X, Y: y, W: list.W, H: itemH}, phase+float64(i)*0.15)
			}
		case schedUI.errText[tab] != "":
			UIErrorState(renderer, config, list, "@warning", "Couldn't load this list", schedUI.errText[tab])
		case tab == schedTabTasks && runtime.GOOS == "windows":
			UIEmptyState(renderer, config, list, "@schedule", "No scheduled tasks", "Task Scheduler has nothing set up to run.")
		case tab == schedTabTasks:
			UIEmptyState(renderer, config, list, "@schedule", "No scheduled jobs", "Nothing is set up in cron. Jobs added with crontab -e appear here.")
		default:
			UIEmptyState(renderer, config, list, "@startup", "Nothing starts automatically", "Apps and scripts that start on their own will appear here.")
		}
		return
	}

	itemH, headerH, noteH := schedRowHeights(font, small)
	gap := int32(8)
	scrollW := int32(10)
	rowW := list.W - scrollW

	// Where each row sits on the page, and how tall the page is.
	total := int32(0)
	cursorTop, cursorBottom := int32(-1), int32(-1)
	cursorRow := -1
	if ord := schedUI.cursor[tab]; ord >= 0 && ord < len(schedUI.itemRows[tab]) {
		cursorRow = schedUI.itemRows[tab][ord]
	}
	for i, row := range rows {
		h := itemH
		switch {
		case row.header != "":
			h = headerH
		case row.note != "":
			h = noteH
		}
		if i == cursorRow {
			cursorTop, cursorBottom = total, total+h
		}
		total += h + gap
	}
	maxScroll := total - gap - list.H
	if maxScroll < 0 {
		maxScroll = 0
	}
	scroll := schedUI.scroll[tab]
	if schedUI.ensureVisible && cursorTop >= 0 {
		// Keep a heading above the first item of its group in view too.
		top := cursorTop
		if cursorRow > 0 && rows[cursorRow-1].header != "" {
			top -= headerH + gap
		}
		if top < scroll {
			scroll = top
		}
		if cursorBottom > scroll+list.H {
			scroll = cursorBottom - list.H
		}
		schedUI.ensureVisible = false
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	schedUI.scroll[tab] = scroll

	accent := glassAccent()
	renderWithClip(renderer, list.X, list.Y, list.W, list.H, func(r *sdl.Renderer) {
		y := list.Y - scroll
		ordinal := 0
		for i, row := range rows {
			h := itemH
			switch {
			case row.header != "":
				h = headerH
			case row.note != "":
				h = noteH
			}
			if y+h < list.Y || y > list.Y+list.H {
				if row.header == "" && row.note == "" {
					ordinal++
				}
				y += h + gap
				continue
			}
			switch {
			case row.header != "":
				UISectionLabel(r, config, row.header, list.X+4, y+4, rowW-8, nil)
			case row.note != "":
				nw, nh := measureText(small, row.note)
				drawText(r, small, row.note, list.X+(rowW-nw)/2, y+(h-nh)/2, ColorTextTertiary(), textAlignLeft)
			default:
				if row.item >= 0 && row.item < len(schedUI.items[tab]) {
					rect := sdl.Rect{X: list.X, Y: y, W: rowW, H: h}
					schedDrawRow(r, font, small, rect, schedUI.items[tab][row.item], i == cursorRow, accent)
					visible := rect
					if visible.Y < list.Y {
						visible.H -= list.Y - visible.Y
						visible.Y = list.Y
					}
					if visible.Y+visible.H > list.Y+list.H {
						visible.H = list.Y + list.H - visible.Y
					}
					if visible.H > 0 {
						schedUI.rowHits = append(schedUI.rowHits, schedRowHit{rect: visible, ordinal: ordinal})
					}
				}
				ordinal++
			}
			y += h + gap
		}
	})
	if maxScroll > 0 {
		thumb := float64(list.H) / float64(total)
		drawScrollbar(renderer, list.X+list.W-4, list.Y, 4, list.H, thumb, float64(scroll)/float64(maxScroll))
	}
}

// schedDrawRow draws one item: its icon badge, name, schedule and next run,
// and its status as a chip.
func schedDrawRow(renderer *sdl.Renderer, font, small *ttf.Font, rect sdl.Rect, it schedItem, selected bool, accent sdl.Color) {
	UIListRow(renderer, rect, glassRowRadius, selected, !selected && sysToolPointIn(rect))
	badge := int32(40)
	if badge > rect.H-16 {
		badge = rect.H - 16
	}
	box := drawGlassBadge(renderer, sdl.Rect{X: rect.X + 14, Y: rect.Y + (rect.H-badge)/2, W: badge, H: badge})
	DrawHomeIcon(renderer, it.Icon, box, false)

	// The status chip on the right.
	chipText := ellipsize(small, it.Status, 200)
	cw, ch := measureText(small, chipText)
	chipH := ch + 10
	chipW := cw + 24
	chipX := rect.X + rect.W - 16 - chipW
	chipY := rect.Y + (rect.H-chipH)/2
	fill, ink := glassA(14), ColorTextSecondary()
	lower := strings.ToLower(it.Status)
	switch {
	case !it.Enabled:
		fill, ink = glassA(8), ColorTextTertiary()
	case strings.Contains(lower, "running"):
		fill, ink = WithAlpha(accent, 46), ColorTextPrimary()
	case lower == "on" || strings.HasPrefix(lower, "enabled") || strings.HasPrefix(lower, "ready") ||
		strings.Contains(lower, "boot") || strings.HasPrefix(lower, "starts"):
		fill, ink = WithAlpha(ColorSuccess, 40), SuccessInk()
	}
	if chipText != "" {
		fillRoundedRect(renderer, chipX, chipY, chipW, chipH, chipH/2, fill)
		drawText(renderer, small, chipText, chipX+12, chipY+(chipH-ch)/2, ink, textAlignLeft)
	} else {
		chipX = rect.X + rect.W - 16
	}

	textX := box.X + box.W + (badge-box.W)/2 + 16
	textW := chipX - 16 - textX
	nameInk, subInk := ColorTextPrimary(), ColorTextSecondary()
	if !it.Enabled {
		nameInk, subInk = ColorTextTertiary(), ColorTextTertiary()
	}
	nameH := int32(font.Height())
	subH := int32(small.Height())
	ty := rect.Y + (rect.H-nameH-subH-4)/2
	drawText(renderer, font, ellipsize(font, it.Name, textW), textX, ty, nameInk, textAlignLeft)
	sub := it.Schedule
	if it.Next != "" {
		sub += " · Next: " + it.Next
	}
	drawText(renderer, small, ellipsize(small, sub, textW), textX, ty+nameH+4, subInk, textAlignLeft)
}

// schedDrawSheet draws the details of one item over a dimmed screen.
func schedDrawSheet(renderer *sdl.Renderer, config *Config, font, small *ttf.Font) {
	it := schedUI.sheetItem
	drawOverlayScrim(renderer)
	margin := scaleX(homeSideMargin, screenWidth)
	w := screenWidth - 2*margin
	if w > 860 {
		w = 860
	}
	top := headerBarHeight() + 12
	h := screenHeight - top - 24
	if h > 600 {
		h = 600
	}
	card := sdl.Rect{X: (screenWidth - w) / 2, Y: top, W: w, H: h}
	drawOverlayCard(renderer, card)
	schedUI.sheetRect = card

	header := overlayPageHeader(card, it.Name, it.Group, it.Icon)
	header.Status = it.Status
	header.StatusColor = ColorTextSecondary()
	if it.Enabled {
		header.StatusColor = SuccessInk()
	}
	y := UIPageHeaderDraw(renderer, config, header) + 16

	// Buttons along the bottom.
	btnH := int32(small.Height()) + 22
	btnY := card.Y + card.H - OverlayPadX - btnH
	closeW := int32(140)
	schedUI.closeRect = sdl.Rect{X: card.X + card.W - OverlayPadX - closeW, Y: btnY, W: closeW, H: btnH}
	UIButton(renderer, config, schedUI.closeRect, "Close", UIButtonStyle{Variant: UIButtonSecondary, Font: small, Pill: true},
		UIButtonState{Hovered: sysToolPointIn(schedUI.closeRect)})
	schedUI.toggleRect = sdl.Rect{}
	if it.Toggle != schedToggleNone {
		label, variant := "Turn off", UIButtonSecondary
		if !it.Enabled {
			label, variant = "Turn on", UIButtonPrimary
		}
		toggleW := int32(160)
		schedUI.toggleRect = sdl.Rect{X: schedUI.closeRect.X - 12 - toggleW, Y: btnY, W: toggleW, H: btnH}
		UIButton(renderer, config, schedUI.toggleRect, label, UIButtonStyle{Variant: variant, Font: small, Pill: true},
			UIButtonState{Hovered: sysToolPointIn(schedUI.toggleRect)})
	} else {
		note := "Managed by the system — shown for reference"
		_, nh := measureText(small, note)
		drawText(renderer, small, note, card.X+OverlayPadX, btnY+(btnH-nh)/2, ColorTextTertiary(), textAlignLeft)
	}

	// The facts, label on the left, value wrapped beside it. They are laid out
	// once per item, card width and font, not every frame.
	if schedUI.sheetLines == nil || schedUI.sheetLinesW != card.W || schedUI.sheetLinesFont != font {
		rows := schedDetailRows(it)
		labelW := int32(0)
		for _, row := range rows {
			if lw, _ := measureText(small, row.Label); lw > labelW {
				labelW = lw
			}
		}
		valueW := card.W - 2*OverlayPadX - labelW - 20
		schedUI.sheetLines = schedUI.sheetLines[:0]
		for _, row := range rows {
			lines := wrapTextLines(font, row.Value, valueW, 3)
			for i, line := range lines {
				sl := schedSheetLine{text: line}
				if i == 0 {
					sl.label = row.Label
				}
				schedUI.sheetLines = append(schedUI.sheetLines, sl)
			}
		}
		schedUI.sheetLabelW, schedUI.sheetLinesW, schedUI.sheetLinesFont = labelW, card.W, font
	}
	valueX := card.X + OverlayPadX + schedUI.sheetLabelW + 20
	lineH := int32(font.Height()) + 4
	limit := btnY - 12
	for _, line := range schedUI.sheetLines {
		if y+lineH > limit {
			break
		}
		if line.label != "" {
			y += 6
			_, lh := measureText(small, line.label)
			drawText(renderer, small, line.label, card.X+OverlayPadX, y+(int32(font.Height())-lh)/2, ColorTextTertiary(), textAlignLeft)
		}
		drawText(renderer, font, line.text, valueX, y, ColorTextPrimary(), textAlignLeft)
		y += lineH
	}
}

// ── Scene ────────────────────────────────────────────────────────────────

// schedScene is the registered module for the "Cron" scene.
type schedScene struct{ sceneBase }

func (schedScene) render(r *sdl.Renderer, c *Config) bool { renderSchedScreen(r, c); return true }

func (schedScene) handleKey(e *sdl.KeyboardEvent, c *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN {
		return false
	}
	return schedHandleKey(e, c)
}

func (schedScene) handleController(e *sdl.ControllerButtonEvent, c *Config) bool {
	return schedHandlePad(e, c)
}

func init() { registerScene(schedScene{sceneBase{name: schedSceneName}}) }

package main

// cmddeck_catalog.go — what the Command Deck offers: the categories, the
// command templates with their typed slots, which of them this device can
// run, and the quoting that turns picked values into a safe command line.
//
// A template is a command line with numbered slots ({1}, {2} …) and named
// device tokens ({@sd}, {@app} …). Slots are filled with pickers — a folder or
// file browser, a choice list, a number stepper, a process list — so nothing
// is typed. Tokens are paths the deck resolves once per probe (the card, the
// app folder, RetroArch's cores, PortMaster); an entry whose token did not
// resolve, whose program is not on PATH or whose path does not exist is not
// offered at all, so every row on screen is one that can run here.
//
// Every value lands in the line through cmdDeckQuote: a file called
// "$(reboot).zip" on the card is an argument, never shell syntax. Windows has
// its own set (cmd.exe and PowerShell) chosen by GOOS.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// cmdSlotKind is how a slot is filled.
type cmdSlotKind uint8

const (
	cmdSlotFolder  cmdSlotKind = iota // folder picker
	cmdSlotFile                       // file picker (filter narrows it)
	cmdSlotInt                        // number stepper
	cmdSlotChoice                     // fixed list (plus recent picks)
	cmdSlotPattern                    // fixed list of globs, always quoted
	cmdSlotProcess                    // running process, filled with its PID
)

// cmdQuoteStyle is the syntax a value is quoted for.
type cmdQuoteStyle uint8

const (
	cmdQuoteSh  cmdQuoteStyle = iota // POSIX sh: '…' with '\'' for a quote
	cmdQuoteCmd                      // cmd.exe: "…" when needed
	cmdQuotePS                       // inside a PowerShell '…' the template wrote
)

// cmdSlot is one blank in a template.
type cmdSlot struct {
	label   string
	kind    cmdSlotKind
	def     string   // default value (int and choice slots)
	choices []string // choice/pattern values; "@gw" is the router
	min     int
	max     int
	filter  string // file slots: "log" or "text" narrows the picker
	start   string // picker's first folder: a token name ("sd", "gamelogs" …)
}

// cmdEntry is one command template.
type cmdEntry struct {
	id     string
	cat    string
	title  string
	tmpl   string
	slots  []cmdSlot
	need   []string // programs that must be on PATH
	unless string   // hidden when this program exists (its better twin is listed)
	path   string   // must exist (a /proc or /sys file, say)
	danger string   // why it asks first; "" runs straight away
	quote  cmdQuoteStyle
	keys   string // extra words for search
	ask    string // Ask Juka: the question sent to the assistant
}

// cmdDeckCat is one rail entry.
type cmdDeckCat struct {
	id    string
	title string
}

// cmdDeckCats is the rail, in order. Favorites stays first: what someone
// saved is what they come back for.
var cmdDeckCats = []cmdDeckCat{
	{"fav", "Favorites"},
	{"ask", "Ask Juka"},
	{"system", "System"},
	{"storage", "Storage"},
	{"network", "Network"},
	{"files", "Files"},
	{"proc", "Processes"},
	{"logs", "Logs"},
	{"emu", "Emulators"},
	{"pm", "PortMaster"},
	{"java", "Java"},
	{"juka", "JukaHub"},
}

// Slot shorthands keep the tables readable.
func cmdLines(def int) cmdSlot {
	return cmdSlot{label: "Lines", kind: cmdSlotInt, def: strconv.Itoa(def), min: 1, max: 2000}
}

func cmdFolder(start string) cmdSlot {
	return cmdSlot{label: "Folder", kind: cmdSlotFolder, start: start}
}

func cmdLogFile(start string) cmdSlot {
	return cmdSlot{label: "Log file", kind: cmdSlotFile, filter: "log", start: start}
}

var (
	cmdAnyFile    = cmdSlot{label: "File", kind: cmdSlotFile, start: "sd"}
	cmdTextFile   = cmdSlot{label: "File", kind: cmdSlotFile, filter: "text", start: "sd"}
	cmdProcess    = cmdSlot{label: "Process", kind: cmdSlotProcess}
	cmdHostChoice = cmdSlot{label: "Host", kind: cmdSlotChoice, def: "1.1.1.1", choices: []string{"1.1.1.1", "google.com", "@gw", "8.8.8.8"}}
	cmdNameChoice = cmdSlot{label: "Name", kind: cmdSlotChoice, def: "google.com", choices: []string{"google.com", "github.com", "jukalang.com", "pool.ntp.org"}}
	cmdLogWord    = cmdSlot{label: "Word", kind: cmdSlotChoice, def: "error", choices: []string{"error", "fail", "warn", "crash", "denied", "not found"}}
	cmdSavePat    = cmdSlot{label: "Pattern", kind: cmdSlotPattern, def: "*.zip", choices: []string{"*.zip", "*.7z", "*.srm", "*.state*", "*.png", "*.log", "*.sh"}}
)

// cmdDeckAskEntries are the Ask Juka questions; the assistant runs its own
// device tools to answer (aiAgentAskFromDeck, Chat's Juka AI tab).
func cmdDeckAskEntries() []cmdEntry {
	disk := "What's filling my SD card?"
	if runtime.GOOS == "windows" {
		disk = "What's filling my disk?"
	}
	return []cmdEntry{
		{id: "ask-game", cat: "ask", title: "Why won't my last game start?", ask: "Why won't my last game start? Check the launch logs and tell me how to fix it.", keys: "game crash launch fail"},
		{id: "ask-disk", cat: "ask", title: disk, ask: disk + " Show me the biggest folders and what I can safely delete.", keys: "space storage full"},
		{id: "ask-smooth", cat: "ask", title: "Make games run smoother", ask: "Make my games run smoother. Check CPU speed, temperature and what else is running, then suggest changes.", keys: "lag slow fps performance"},
		{id: "ask-wifi", cat: "ask", title: "Check my Wi-Fi", ask: "Check my Wi-Fi: signal, IP address, router and internet, and tell me what is wrong if anything.", keys: "internet network slow"},
	}
}

// cmdDeckLinuxEntries is the handheld's (and any Linux's) set. Busybox has
// almost all of it; what a firmware lacks is filtered out by the probe.
var cmdDeckLinuxEntries = []cmdEntry{
	// System
	{id: "uptime", cat: "system", title: "Uptime and load", tmpl: "uptime -p 2>/dev/null || uptime", keys: "load running time"},
	{id: "free", cat: "system", title: "Memory in use", tmpl: "free -m", need: []string{"free"}, keys: "ram memory"},
	{id: "gov", cat: "system", title: "CPU governor", tmpl: "cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor", path: "/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor", keys: "cpu performance powersave"},
	{id: "freq", cat: "system", title: "CPU speed now (kHz)", tmpl: "cat /sys/devices/system/cpu/cpu*/cpufreq/scaling_cur_freq", path: "/sys/devices/system/cpu/cpu0/cpufreq/scaling_cur_freq", keys: "cpu mhz clock"},
	{id: "temp", cat: "system", title: "Temperatures (m°C)", tmpl: "cat /sys/class/thermal/thermal_zone*/temp", path: "/sys/class/thermal/thermal_zone0/temp", keys: "heat hot thermal"},
	{id: "battery", cat: "system", title: "Battery level", tmpl: "cat /sys/class/power_supply/*/capacity", path: "/sys/class/power_supply", keys: "charge power"},
	{id: "uname", cat: "system", title: "Kernel and firmware", tmpl: "uname -a", need: []string{"uname"}, keys: "version linux"},
	{id: "osrel", cat: "system", title: "OS release", tmpl: "cat /etc/os-release", path: "/etc/os-release", keys: "version distro"},
	{id: "sysinfo", cat: "system", title: "System summary", tmpl: "sysinfo", keys: "info device"},
	{id: "shell", cat: "system", title: "Interactive shell", tmpl: "shell", keys: "bash sh console prompt interactive pty"},
	// Storage
	{id: "df", cat: "storage", title: "Free space on every drive", tmpl: "df -h", need: []string{"df"}, keys: "disk space full sd card"},
	{id: "du", cat: "storage", title: "Size of a folder", tmpl: "du -sh {1}", slots: []cmdSlot{cmdFolder("sd")}, need: []string{"du"}, keys: "disk usage space"},
	{id: "dutop", cat: "storage", title: "Biggest items in a folder", tmpl: "du -sk {1}/* | sort -rn | head -n {2}", slots: []cmdSlot{cmdFolder("sd"), cmdLines(15)}, need: []string{"du", "sort", "head"}, keys: "disk usage space largest"},
	{id: "mounts", cat: "storage", title: "Mounted drives", tmpl: "cat /proc/mounts", path: "/proc/mounts", keys: "mount sd usb"},
	{id: "sync", cat: "storage", title: "Flush writes to the card", tmpl: "sync", need: []string{"sync"}, keys: "save safe eject"},
	// Network
	{id: "ping", cat: "network", title: "Ping a host", tmpl: "ping -c {2} {1}", slots: []cmdSlot{cmdHostChoice, {label: "Count", kind: cmdSlotInt, def: "4", min: 1, max: 100}}, need: []string{"ping"}, keys: "internet latency reachable"},
	{id: "ipaddr", cat: "network", title: "IP addresses", tmpl: "ip addr", need: []string{"ip"}, keys: "ip address lan"},
	{id: "ifconfig", cat: "network", title: "IP addresses", tmpl: "ifconfig", need: []string{"ifconfig"}, unless: "ip", keys: "ip address lan"},
	{id: "iwlink", cat: "network", title: "Wi-Fi link and signal", tmpl: "iw dev wlan0 link", need: []string{"iw"}, keys: "wifi signal ssid"},
	{id: "iwconfig", cat: "network", title: "Wi-Fi link and signal", tmpl: "iwconfig wlan0", need: []string{"iwconfig"}, unless: "iw", keys: "wifi signal ssid"},
	{id: "route", cat: "network", title: "Routes and router", tmpl: "ip route", need: []string{"ip"}, keys: "gateway router"},
	{id: "dns", cat: "network", title: "DNS servers", tmpl: "cat /etc/resolv.conf", path: "/etc/resolv.conf", keys: "dns resolver"},
	{id: "lookup", cat: "network", title: "Look up a name", tmpl: "nslookup {1}", slots: []cmdSlot{cmdNameChoice}, need: []string{"nslookup"}, keys: "dns resolve"},
	{id: "ports", cat: "network", title: "Listening ports", tmpl: "netstat -tln", need: []string{"netstat"}, keys: "ports server open"},
	// Files
	{id: "ls", cat: "files", title: "List a folder", tmpl: "ls -la {1}", slots: []cmdSlot{cmdFolder("sd")}, keys: "dir files"},
	{id: "find", cat: "files", title: "Find files by pattern", tmpl: "find {1} -iname {2} | head -n 200", slots: []cmdSlot{cmdFolder("sd"), cmdSavePat}, need: []string{"find", "head"}, keys: "search zip saves"},
	{id: "recent", cat: "files", title: "Files changed recently", tmpl: "find {1} -type f -mtime -{2} | head -n 100", slots: []cmdSlot{cmdFolder("sd"), {label: "Days", kind: cmdSlotInt, def: "1", min: 1, max: 365}}, need: []string{"find", "head"}, keys: "modified new"},
	{id: "count", cat: "files", title: "Count files in a folder", tmpl: "find {1} -type f | wc -l", slots: []cmdSlot{cmdFolder("sd")}, need: []string{"find", "wc"}, keys: "how many"},
	{id: "cat", cat: "files", title: "Show a text file", tmpl: "cat {1}", slots: []cmdSlot{cmdTextFile}, keys: "read view"},
	{id: "rm", cat: "files", title: "Delete a file", tmpl: "rm {1}", slots: []cmdSlot{cmdAnyFile}, need: []string{"rm"}, danger: "The file is deleted for good — there is no Recycle Bin.", keys: "remove erase"},
	// Processes
	{id: "ps", cat: "proc", title: "Running processes", tmpl: "ps", need: []string{"ps"}, keys: "tasks programs"},
	{id: "top", cat: "proc", title: "Busiest processes", tmpl: "top -b -n 1 | head -n {1}", slots: []cmdSlot{cmdLines(20)}, need: []string{"top", "head"}, keys: "cpu usage"},
	{id: "toplive", cat: "proc", title: "Live process monitor", tmpl: "top", need: []string{"top"}, unless: "htop", keys: "cpu usage interactive"},
	{id: "htop", cat: "proc", title: "Live process monitor", tmpl: "htop", need: []string{"htop"}, keys: "top cpu usage interactive"},
	{id: "kill", cat: "proc", title: "Stop a process", tmpl: "kill {1}", slots: []cmdSlot{cmdProcess}, need: []string{"kill"}, danger: "The program is asked to quit; unsaved work in it is lost.", keys: "end quit stuck"},
	{id: "kill9", cat: "proc", title: "Force-stop a process", tmpl: "kill -9 {1}", slots: []cmdSlot{cmdProcess}, need: []string{"kill"}, danger: "The program is killed at once, without a chance to save.", keys: "end stuck hung"},
	// Logs
	{id: "dmesg", cat: "logs", title: "Kernel messages", tmpl: "dmesg | tail -n {1}", slots: []cmdSlot{cmdLines(40)}, need: []string{"dmesg", "tail"}, keys: "kernel driver usb"},
	{id: "tail", cat: "logs", title: "End of a log", tmpl: "tail -n {1} {2}", slots: []cmdSlot{cmdLines(50), cmdLogFile("gamelogs")}, need: []string{"tail"}, keys: "read log"},
	{id: "grep", cat: "logs", title: "Search a log", tmpl: "grep -i -n {1} {2} | tail -n 100", slots: []cmdSlot{cmdLogWord, cmdLogFile("gamelogs")}, need: []string{"grep", "tail"}, keys: "find error"},
	{id: "logread", cat: "logs", title: "System log", tmpl: "tail -n {1} /var/log/messages 2>/dev/null || logread | tail -n {1}", slots: []cmdSlot{cmdLines(50)}, need: []string{"logread", "tail"}, keys: "syslog"},
	// Emulators
	{id: "racores", cat: "emu", title: "RetroArch cores", tmpl: "ls {@racores}", keys: "retroarch cores libretro"},
	{id: "racfg", cat: "emu", title: "RetroArch drivers", tmpl: "grep -E '^(video_driver|audio_driver|input_driver|menu_driver) ' {@racfg}", need: []string{"grep"}, keys: "retroarch config video audio"},
	{id: "emus", cat: "emu", title: "Emulator folders", tmpl: "ls {@emus}", keys: "emus systems"},
	{id: "saves", cat: "emu", title: "Find saves and states", tmpl: "find {1} -iname '*.srm' -o -iname '*.state*' | head -n 200", slots: []cmdSlot{cmdFolder("sd")}, need: []string{"find", "head"}, keys: "save state srm"},
	{id: "gamelog", cat: "emu", title: "Last game's log", tmpl: "ls -t {@gamelogs} | head -n 10", need: []string{"head"}, keys: "emulator log launch"},
	// PortMaster
	{id: "pmls", cat: "pm", title: "PortMaster folder", tmpl: "ls -la {@pm}", keys: "ports"},
	{id: "pmlog", cat: "pm", title: "PortMaster log", tmpl: "tail -n {1} {@pmlog}", slots: []cmdSlot{cmdLines(60)}, need: []string{"tail"}, keys: "ports log"},
	{id: "pmlibs", cat: "pm", title: "Port runtimes", tmpl: "ls {@pmlibs}", keys: "ports libs runtime"},
	// JukaHub
	{id: "neofetch", cat: "juka", title: "Device card", tmpl: "neofetch", keys: "info"},
	{id: "appdir", cat: "juka", title: "JukaHub folder", tmpl: "ls -la {@app}", keys: "install"},
	{id: "datasize", cat: "juka", title: "JukaHub data size", tmpl: "du -sh {@data}", need: []string{"du"}, keys: "space settings"},
	{id: "errors", cat: "juka", title: "JukaHub errors", tmpl: "tail -n {1} {@errors}", slots: []cmdSlot{cmdLines(50)}, need: []string{"tail"}, keys: "crash log errors.txt"},
	{id: "history", cat: "juka", title: "Command history", tmpl: "history", keys: "previous"},
}

// cmdDeckWindowsEntries is the PC set: cmd.exe built-ins and the programs
// every Windows has, PowerShell where cmd has no equivalent.
var cmdDeckWindowsEntries = []cmdEntry{
	// System
	{id: "w-uptime", cat: "system", title: "Uptime", tmpl: "uptime", keys: "running time"},
	{id: "w-ver", cat: "system", title: "Windows version", tmpl: "ver", keys: "version build"},
	{id: "w-mem", cat: "system", title: "Memory in use", tmpl: `powershell -NoProfile -Command "Get-CimInstance Win32_OperatingSystem | Select-Object @{n='FreeMB';e={[int]($_.FreePhysicalMemory/1KB)}},@{n='TotalMB';e={[int]($_.TotalVisibleMemorySize/1KB)}} | Format-List"`, need: []string{"powershell"}, keys: "ram memory"},
	{id: "w-cpu", cat: "system", title: "CPU load", tmpl: `powershell -NoProfile -Command "Get-CimInstance Win32_Processor | Select-Object Name,LoadPercentage | Format-List"`, need: []string{"powershell"}, keys: "cpu usage"},
	{id: "w-battery", cat: "system", title: "Battery level", tmpl: `powershell -NoProfile -Command "Get-CimInstance Win32_Battery | Select-Object EstimatedChargeRemaining,BatteryStatus | Format-List"`, need: []string{"powershell"}, keys: "charge power"},
	{id: "w-sysinfo", cat: "system", title: "System summary", tmpl: "sysinfo", keys: "info device"},
	{id: "w-shell", cat: "system", title: "Interactive shell", tmpl: "shell", keys: "cmd console prompt interactive"},
	// Storage
	{id: "w-df", cat: "storage", title: "Free space on every drive", tmpl: `powershell -NoProfile -Command "Get-PSDrive -PSProvider FileSystem | Format-Table -AutoSize"`, need: []string{"powershell"}, keys: "disk space full"},
	{id: "w-du", cat: "storage", title: "Size of a folder (MB)", tmpl: `powershell -NoProfile -Command "[math]::Round((Get-ChildItem -LiteralPath '{1}' -Recurse -File -Force -ErrorAction SilentlyContinue | Measure-Object Length -Sum).Sum / 1MB, 1)"`, slots: []cmdSlot{cmdFolder("home")}, need: []string{"powershell"}, quote: cmdQuotePS, keys: "disk usage"},
	{id: "w-dutop", cat: "storage", title: "Biggest files in a folder", tmpl: `powershell -NoProfile -Command "Get-ChildItem -LiteralPath '{1}' -Recurse -File -Force -ErrorAction SilentlyContinue | Sort-Object Length -Descending | Select-Object -First {2} @{n='MB';e={[math]::Round($_.Length/1MB,1)}},FullName | Format-Table -AutoSize"`, slots: []cmdSlot{cmdFolder("home"), cmdLines(15)}, need: []string{"powershell"}, quote: cmdQuotePS, keys: "disk usage largest"},
	// Network
	{id: "w-ping", cat: "network", title: "Ping a host", tmpl: "ping -n {2} {1}", slots: []cmdSlot{cmdHostChoice, {label: "Count", kind: cmdSlotInt, def: "4", min: 1, max: 100}}, need: []string{"ping"}, keys: "internet latency"},
	{id: "w-ipconfig", cat: "network", title: "IP addresses", tmpl: "ipconfig", need: []string{"ipconfig"}, keys: "ip address lan"},
	{id: "w-wlan", cat: "network", title: "Wi-Fi link and signal", tmpl: "netsh wlan show interfaces", need: []string{"netsh"}, keys: "wifi signal ssid"},
	{id: "w-route", cat: "network", title: "Routes and router", tmpl: "route print -4", need: []string{"route"}, keys: "gateway router"},
	{id: "w-lookup", cat: "network", title: "Look up a name", tmpl: "nslookup {1}", slots: []cmdSlot{cmdNameChoice}, need: []string{"nslookup"}, keys: "dns resolve"},
	{id: "w-ports", cat: "network", title: "Listening ports", tmpl: "netstat -an | findstr LISTENING", need: []string{"netstat", "findstr"}, keys: "ports server open"},
	// Files
	{id: "w-dir", cat: "files", title: "List a folder", tmpl: "dir {1}", slots: []cmdSlot{cmdFolder("home")}, keys: "ls files"},
	{id: "w-find", cat: "files", title: "Find files by pattern", tmpl: `powershell -NoProfile -Command "Get-ChildItem -LiteralPath '{1}' -Recurse -Filter '{2}' -ErrorAction SilentlyContinue | Select-Object -First 200 -ExpandProperty FullName"`, slots: []cmdSlot{cmdFolder("home"), cmdSavePat}, need: []string{"powershell"}, quote: cmdQuotePS, keys: "search zip saves"},
	{id: "w-type", cat: "files", title: "Show a text file", tmpl: "type {1}", slots: []cmdSlot{{label: "File", kind: cmdSlotFile, filter: "text", start: "home"}}, keys: "cat read view"},
	{id: "w-del", cat: "files", title: "Delete a file", tmpl: "del {1}", slots: []cmdSlot{{label: "File", kind: cmdSlotFile, start: "home"}}, danger: "The file is deleted for good — it does not go to the Recycle Bin.", keys: "remove erase rm"},
	// Processes
	{id: "w-tasklist", cat: "proc", title: "Running processes", tmpl: "tasklist", need: []string{"tasklist"}, keys: "ps tasks programs"},
	{id: "w-kill", cat: "proc", title: "Stop a process", tmpl: "taskkill /PID {1}", slots: []cmdSlot{cmdProcess}, need: []string{"taskkill"}, danger: "The program is asked to close; unsaved work in it may be lost.", keys: "end quit stuck kill"},
	{id: "w-kill9", cat: "proc", title: "Force-stop a process", tmpl: "taskkill /F /PID {1}", slots: []cmdSlot{cmdProcess}, need: []string{"taskkill"}, danger: "The program is killed at once, without a chance to save.", keys: "end stuck hung kill"},
	// Logs
	{id: "w-tail", cat: "logs", title: "End of a log", tmpl: `powershell -NoProfile -Command "Get-Content -LiteralPath '{2}' -Tail {1}"`, slots: []cmdSlot{cmdLines(50), cmdLogFile("gamelogs")}, need: []string{"powershell"}, quote: cmdQuotePS, keys: "read log tail"},
	{id: "w-grep", cat: "logs", title: "Search a log", tmpl: `powershell -NoProfile -Command "Select-String -LiteralPath '{2}' -Pattern '{1}' -SimpleMatch | Select-Object -Last 100"`, slots: []cmdSlot{cmdLogWord, cmdLogFile("gamelogs")}, need: []string{"powershell"}, quote: cmdQuotePS, keys: "find error grep"},
	{id: "w-events", cat: "logs", title: "Recent system errors", tmpl: `powershell -NoProfile -Command "Get-WinEvent -FilterHashtable @{LogName='System';Level=2} -MaxEvents {1} -ErrorAction SilentlyContinue | Format-Table TimeCreated,ProviderName,Id -AutoSize"`, slots: []cmdSlot{cmdLines(20)}, need: []string{"powershell"}, keys: "event viewer crash"},
	// Emulators
	{id: "w-racores", cat: "emu", title: "RetroArch cores", tmpl: "dir /b {@racores}", keys: "retroarch cores libretro"},
	{id: "w-racfg", cat: "emu", title: "RetroArch drivers", tmpl: `findstr /b "video_driver audio_driver input_driver menu_driver" {@racfg}`, need: []string{"findstr"}, keys: "retroarch config video audio"},
	{id: "w-gamelog", cat: "emu", title: "Last game's logs", tmpl: "dir /o-d {@gamelogs}", keys: "emulator log launch"},
	// JukaHub
	{id: "w-neofetch", cat: "juka", title: "Device card", tmpl: "neofetch", keys: "info"},
	{id: "w-appdir", cat: "juka", title: "JukaHub folder", tmpl: "dir {@app}", keys: "install"},
	{id: "w-datadir", cat: "juka", title: "JukaHub data folder", tmpl: "dir {@data}", keys: "settings"},
	{id: "w-history", cat: "juka", title: "Command history", tmpl: "history", keys: "previous"},
}

// cmdDeckJavaEntries are the Terminal's Java commands (javaterm.go): its own
// built-ins, the same on every platform.
var cmdDeckJavaEntries = []cmdEntry{
	{id: "java-status", cat: "java", title: "Which Java is used", tmpl: "java status", keys: "java jre jdk version installed"},
	{id: "java-version", cat: "java", title: "Java version", tmpl: "java -version", keys: "java version"},
	{id: "java-jar", cat: "java", title: "Run a .jar program", tmpl: "java -jar {1}", slots: []cmdSlot{{label: "Program", kind: cmdSlotFile, start: "java"}}, keys: "java jar run program"},
	{id: "java-src", cat: "java", title: "Run a .java file", tmpl: "java {1}", slots: []cmdSlot{{label: "Source file", kind: cmdSlotFile, start: "java"}}, keys: "java source run hello jdk"},
	{id: "java-install", cat: "java", title: "Install Java", tmpl: "java install {1} {2}", slots: []cmdSlot{{label: "Version", kind: cmdSlotChoice, def: "25", choices: []string{"25", "21", "17", "11", "8"}}, {label: "Kind", kind: cmdSlotChoice, def: "jre", choices: []string{"jre", "jdk"}}}, keys: "java install temurin jre jdk download"},
	{id: "java-update", cat: "java", title: "Update Java", tmpl: "java update", keys: "java update newer"},
	{id: "jshell", cat: "java", title: "Java shell (jshell, JDK)", tmpl: "jshell", keys: "java repl interactive jdk"},
	{id: "java-remove", cat: "java", title: "Remove JukaHub's Java", tmpl: "java remove", danger: "JukaHub's Java is deleted; java install brings it back.", keys: "java uninstall delete"},
}

// cmdDeckCatalog is this platform's full list, Ask Juka first.
func cmdDeckCatalog() []cmdEntry {
	base := cmdDeckLinuxEntries
	if runtime.GOOS == "windows" {
		base = cmdDeckWindowsEntries
	}
	out := cmdDeckAskEntries()
	out = append(out, base...)
	return append(out, cmdDeckJavaEntries...)
}

// cmdEntryQuote is the entry's quoting, the platform's when it names none.
func cmdEntryQuote(e *cmdEntry) cmdQuoteStyle {
	if e.quote != cmdQuoteSh {
		return e.quote
	}
	if runtime.GOOS == "windows" {
		return cmdQuoteCmd
	}
	return cmdQuoteSh
}

// ── Probe ────────────────────────────────────────────────────────────────

// cmdDeckAvail is what one probe found: the resolved tokens and which entries
// can run here. It is built off the render thread and replaced whole.
type cmdDeckAvail struct {
	ready  bool
	at     time.Time
	tokens map[string]string
	ok     map[string]bool // entry id → offered
	progs  map[string]bool // program → on PATH
}

// cmdDeckProbeMaxAge is how long a probe is trusted; a firmware tool
// installed meanwhile shows up on the next open after it.
const cmdDeckProbeMaxAge = 3 * time.Minute

// cmdDeckFirstDir is the first of dirs that is a folder, or "".
func cmdDeckFirstDir(dirs ...string) string {
	for _, d := range dirs {
		if strings.TrimSpace(d) == "" {
			continue
		}
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			return filepath.Clean(d)
		}
	}
	return ""
}

// cmdDeckFileIf is path when it is a regular file, else "".
func cmdDeckFileIf(path string) string {
	if path == "" {
		return ""
	}
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	return ""
}

// cmdDeckProbeTokens resolves the device tokens. Disk access only; it runs on
// the probe goroutine.
func cmdDeckProbeTokens() map[string]string {
	t := map[string]string{}
	app := exeDir()
	t["app"] = cmdDeckFirstDir(app)
	if dir, err := P().DataDir(); err == nil {
		t["data"] = cmdDeckFirstDir(dir)
	}
	if home, err := os.UserHomeDir(); err == nil {
		t["home"] = cmdDeckFirstDir(home)
	}
	sd := ""
	if IsTrimuiDevice() {
		sd = cmdDeckFirstDir(strings.TrimSpace(defaultTrimuiSDRoot()), "/mnt/SDCARD")
	}
	if sd == "" {
		sd = t["home"]
	}
	t["sd"] = sd
	t["gamelogs"] = cmdDeckFirstDir(gameDiagLogDir())
	t["java"] = cmdDeckFirstDir(javaUserDir())
	t["gw"] = strings.TrimSpace(defaultGateway())
	t["errors"] = cmdDeckFileIf(filepath.Join(app, "errors.txt"))
	if cores := cmdDeckFirstDir(retroArchCoreDirs()...); cores != "" {
		t["racores"] = cores
		t["racfg"] = cmdDeckFileIf(filepath.Join(filepath.Dir(cores), "retroarch.cfg"))
	}
	if runtime.GOOS != "windows" {
		if sd != "" {
			t["emus"] = cmdDeckFirstDir(filepath.Join(sd, "Emus"))
		}
		if pm := cmdDeckFirstDir(westonPortMasterDirs()...); pm != "" {
			t["pm"] = pm
			t["pmlog"] = cmdDeckFileIf(filepath.Join(pm, "log.txt"))
			t["pmlibs"] = cmdDeckFirstDir(filepath.Join(pm, "libs"))
		}
	}
	return t
}

// cmdDeckProbe decides which entries this device offers. It looks every
// program up on PATH once and stats each required path once.
func cmdDeckProbe(entries []cmdEntry) cmdDeckAvail {
	a := cmdDeckAvail{ready: true, at: time.Now(), tokens: cmdDeckProbeTokens(), ok: map[string]bool{}, progs: map[string]bool{}}
	has := func(p string) bool {
		v, seen := a.progs[p]
		if !seen {
			_, err := exec.LookPath(p)
			v = err == nil
			a.progs[p] = v
		}
		return v
	}
	for _, p := range termChipCommandNames() {
		has(p)
	}
	for i := range entries {
		e := &entries[i]
		ok := true
		for _, p := range e.need {
			ok = ok && has(p)
		}
		if ok && e.unless != "" && has(e.unless) {
			ok = false
		}
		if ok && e.path != "" {
			if _, err := os.Stat(e.path); err != nil {
				ok = false
			}
		}
		if ok {
			for _, tok := range cmdTemplateTokens(e.tmpl) {
				if a.tokens[tok] == "" {
					ok = false
				}
			}
		}
		a.ok[e.id] = ok
	}
	return a
}

// cmdTemplateTokens lists the {@name} tokens of a template.
func cmdTemplateTokens(tmpl string) []string {
	var out []string
	for {
		i := strings.Index(tmpl, "{@")
		if i < 0 {
			return out
		}
		j := strings.IndexByte(tmpl[i:], '}')
		if j < 0 {
			return out
		}
		out = append(out, tmpl[i+2:i+j])
		tmpl = tmpl[i+j+1:]
	}
}

// ── Quoting and expansion ────────────────────────────────────────────────

// cmdShSafe reports whether s needs no quoting in sh.
func cmdShSafe(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_./:@%+=,-", r):
		default:
			return false
		}
	}
	return true
}

// cmdDeckQuote quotes one value for style. ok is false for a value that
// cannot be passed safely: a control character anywhere, or on Windows a
// double quote (which cmd.exe cannot nest) or a percent sign (which it
// expands even inside quotes). Such a value is refused, never mangled.
func cmdDeckQuote(v string, style cmdQuoteStyle) (string, bool) {
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	switch style {
	case cmdQuoteCmd:
		if strings.ContainsAny(v, `"%`) {
			return "", false
		}
		if v != "" && !strings.ContainsAny(v, " &|<>^()!,;=\t") {
			return v, true
		}
		return `"` + v + `"`, true
	case cmdQuotePS:
		// The template supplies the surrounding '…'. PowerShell also treats
		// the typographic single quotes as quotes, so all four are doubled;
		// the whole command sits inside cmd.exe's "…", hence the cmd rules.
		if strings.ContainsAny(v, `"%`) {
			return "", false
		}
		var b strings.Builder
		for _, r := range v {
			b.WriteRune(r)
			switch r {
			case '\'', '‘', '’', '‚', '‛':
				b.WriteRune(r)
			}
		}
		return b.String(), true
	}
	if cmdShSafe(v) {
		return v, true
	}
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'", true
}

// cmdSlotValueValid reports whether v is a value slot s may hold: numbers in
// range, choices from the list (or the router token), PIDs as digits, paths
// as anything non-empty (quoting makes them safe). Recent values read from
// disk pass through here before they are offered.
func cmdSlotValueValid(s cmdSlot, v string, tokens map[string]string) bool {
	if strings.TrimSpace(v) == "" {
		return false
	}
	switch s.kind {
	case cmdSlotInt:
		n, err := strconv.Atoi(v)
		return err == nil && n >= s.min && n <= s.max
	case cmdSlotChoice, cmdSlotPattern:
		for _, c := range s.choices {
			if c == v {
				return c != "@gw" || tokens["gw"] != ""
			}
		}
		return false
	case cmdSlotProcess:
		n, err := strconv.Atoi(v)
		return err == nil && n > 1
	}
	// A path. Quoting keeps it one argument, but a leading "-" would still be
	// read as an option ("find -delete …" from an edited favorites file),
	// and cmd.exe's del expands * and ? itself, quoted or not. The pickers
	// only produce absolute paths, so this refuses nothing they offer.
	if strings.HasPrefix(v, "-") || (runtime.GOOS == "windows" && strings.ContainsAny(v, "*?")) {
		return false
	}
	return !strings.ContainsAny(v, "\x00\n\r")
}

// cmdDeckExpand fills a template. Every slot value and token is quoted for
// style; a missing or refused value makes ok false and names the slot.
func cmdDeckExpand(tmpl string, slots []cmdSlot, values []string, tokens map[string]string, style cmdQuoteStyle) (line string, ok bool, missing int) {
	var b strings.Builder
	b.Grow(len(tmpl) + 64)
	for i := 0; i < len(tmpl); i++ {
		c := tmpl[i]
		if c != '{' {
			b.WriteByte(c)
			continue
		}
		end := strings.IndexByte(tmpl[i:], '}')
		if end < 0 {
			b.WriteString(tmpl[i:])
			break
		}
		name := tmpl[i+1 : i+end]
		raw, isSlot, slot := "", false, -1
		switch {
		case strings.HasPrefix(name, "@"):
			raw = tokens[name[1:]]
		case len(name) == 1 && name[0] >= '1' && name[0] <= '9' && int(name[0]-'1') < len(slots):
			slot = int(name[0] - '1')
			isSlot = true
			if slot < len(values) {
				raw = values[slot]
				if raw == "@gw" {
					raw = tokens["gw"]
				}
				if !cmdSlotValueValid(slots[slot], values[slot], tokens) {
					raw = ""
				}
			}
		default:
			// Not a placeholder: PowerShell's @{…} hashtables and {…}
			// script blocks are written as they are.
			b.WriteByte(c)
			continue
		}
		if raw == "" {
			if isSlot {
				return "", false, slot
			}
			return "", false, -1
		}
		q := style
		if isSlot && (slots[slot].kind == cmdSlotInt || slots[slot].kind == cmdSlotProcess) {
			// Digits only (validated above); written bare so "-{2}" in
			// find's -mtime stays one word.
			b.WriteString(raw)
		} else {
			quoted, qok := cmdDeckQuote(raw, q)
			if !qok {
				if isSlot {
					return "", false, slot
				}
				return "", false, -1
			}
			if isSlot && slots[slot].kind == cmdSlotPattern && q == cmdQuoteSh && quoted == raw {
				// A glob must reach find, not be expanded by the shell.
				quoted = "'" + raw + "'"
			}
			b.WriteString(quoted)
		}
		i += end
	}
	return b.String(), true, -1
}

// cmdDeckDisplayTemplate is a template as a row shows it: slots as ‹Label›,
// tokens as the folder's base name.
func cmdDeckDisplayTemplate(e *cmdEntry, tokens map[string]string) string {
	var b strings.Builder
	tmpl := e.tmpl
	for i := 0; i < len(tmpl); i++ {
		c := tmpl[i]
		if c == '{' {
			if end := strings.IndexByte(tmpl[i:], '}'); end > 0 {
				name := tmpl[i+1 : i+end]
				switch {
				case len(name) == 1 && name[0] >= '1' && name[0] <= '9':
					if k := int(name[0] - '1'); k < len(e.slots) {
						b.WriteString("‹" + e.slots[k].label + "›")
						i += end
						continue
					}
				case strings.HasPrefix(name, "@"):
					v := tokens[name[1:]]
					if v == "" {
						v = name[1:]
					}
					b.WriteString(v)
					i += end
					continue
				}
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

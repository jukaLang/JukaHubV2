package main

// sched_model.go — what the Scheduled tasks screen lists and how it reads it.
//
// Everything here is plain parsing with no screen and no system calls, so it is
// tested on any host: crontab lines (with a human-readable schedule and the
// next run worked out from the expression), the Windows Task Scheduler's CSV
// and XML exports, and freedesktop autostart entries. The loaders that run the
// commands and read the files are in sched.go (and the registry in
// sched_windows.go).

import (
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// schedTab is one of the screen's two lists.
type schedTab int

const (
	schedTabTasks schedTab = iota
	schedTabStartup
)

// schedToggle names how X enables or disables an item. Only items that belong
// to the user and can be switched back exactly are toggleable; everything else
// is read-only.
type schedToggle int

const (
	schedToggleNone schedToggle = iota
	// schedToggleJukaHub is JukaHub's own "Start at login" launcher.
	schedToggleJukaHub
	// schedToggleApproved is a Windows startup app of the current user, switched
	// the way Task Manager does it: the StartupApproved flag, not deletion.
	schedToggleApproved
	// schedToggleXDG is a desktop autostart entry in the user's own folder,
	// switched with its Hidden key.
	schedToggleXDG
)

// schedItem is one row of the screen and the details sheet behind it.
type schedItem struct {
	Group    string // section heading in the list
	Name     string
	Schedule string // human-readable: "Daily at 03:00", "At logon"
	Next     string // next run, as the system or the expression says
	Last     string // last run
	Result   string // last result, in words
	Status   string // "Ready", "Disabled", "Runs at boot", ...
	Enabled  bool
	Command  string
	Source   string // where it was found: a file, a registry key, a tool
	User     string // the account it runs as
	Author   string
	Comment  string
	Icon     string // a canonical icon name
	System   bool   // part of the operating system (hidden until asked for)
	Toggle   schedToggle
	// ToggleKey identifies the item for its toggle: a file path, or the
	// StartupApproved key and value name.
	ToggleKey  string
	ToggleName string
	nextAt     time.Time // sort key for Next (zero = unknown)
}

// schedDetailRow is one labelled line of the details sheet.
type schedDetailRow struct {
	Label string
	Value string
}

// schedDetailRows lists an item's facts in reading order, skipping empty ones.
func schedDetailRows(it schedItem) []schedDetailRow {
	rows := make([]schedDetailRow, 0, 10)
	add := func(label, value string) {
		if v := strings.TrimSpace(value); v != "" {
			rows = append(rows, schedDetailRow{Label: label, Value: v})
		}
	}
	add("Schedule", it.Schedule)
	add("Status", it.Status)
	add("Next run", it.Next)
	add("Last run", it.Last)
	add("Last result", it.Result)
	add("Command", it.Command)
	add("Runs as", it.User)
	add("Author", it.Author)
	add("Description", it.Comment)
	add("Found in", it.Source)
	return rows
}

// ── Text decoding ────────────────────────────────────────────────────────

// schedDecodeText turns a tool's output into a Go string: UTF-16 (with or
// without a byte-order mark, which is how some Windows tools write to a pipe),
// UTF-8 with or without its mark, and anything else with invalid bytes
// replaced rather than breaking the font.
func schedDecodeText(b []byte) string {
	switch {
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return schedUTF16(b[2:], false)
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return schedUTF16(b[2:], true)
	case schedLooksUTF16LE(b):
		return schedUTF16(b, false)
	}
	s := strings.TrimPrefix(string(b), string(rune(0xFEFF)))
	return strings.ToValidUTF8(s, "?")
}

// schedLooksUTF16LE spots BOM-less UTF-16LE text: ASCII written that way has a
// zero in every odd byte.
func schedLooksUTF16LE(b []byte) bool {
	if len(b) < 8 {
		return false
	}
	n := len(b)
	if n > 512 {
		n = 512
	}
	zeros := 0
	for i := 1; i < n; i += 2 {
		if b[i] == 0 {
			zeros++
		}
	}
	return zeros*4 >= n
}

func schedUTF16(b []byte, bigEndian bool) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			units = append(units, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	return string(utf16.Decode(units))
}

// ── Cron ─────────────────────────────────────────────────────────────────

// schedCronSpec is a parsed five-field cron expression. Each field is a bit
// set of the values it matches.
type schedCronSpec struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
	reboot                        bool
	expr                          string
}

var schedCronMonthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var schedCronDayNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// schedCronAliases are the @words cron accepts in place of five fields.
var schedCronAliases = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// schedParseCronField parses one field: *, lists, ranges, steps and names.
func schedParseCronField(field string, min, max int, names map[string]int) (uint64, bool, error) {
	field = strings.ToLower(strings.TrimSpace(field))
	if field == "" {
		return 0, false, errors.New("empty field")
	}
	star := field == "*" || field == "?"
	value := func(s string) (int, error) {
		if n, ok := names[s]; ok {
			return n, nil
		}
		return strconv.Atoi(s)
	}
	var bits uint64
	for _, part := range strings.Split(field, ",") {
		step := 1
		if base, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n <= 0 {
				return 0, false, fmt.Errorf("bad step %q", part)
			}
			step, part = n, base
		}
		lo, hi := min, max
		switch {
		case part == "*" || part == "?":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			var err error
			if lo, err = value(a); err != nil {
				return 0, false, fmt.Errorf("bad range %q", part)
			}
			if hi, err = value(b); err != nil {
				return 0, false, fmt.Errorf("bad range %q", part)
			}
		default:
			n, err := value(part)
			if err != nil {
				return 0, false, fmt.Errorf("bad value %q", part)
			}
			lo, hi = n, n
			if step > 1 {
				// "5/15" means from 5 to the end in steps of 15.
				hi = max
			}
		}
		if lo < min || hi > max || lo > hi {
			return 0, false, fmt.Errorf("%q is out of range", part)
		}
		for v := lo; v <= hi; v += step {
			bits |= 1 << uint(v)
		}
	}
	return bits, star, nil
}

// schedParseCronSpec parses the five schedule fields (or an @alias).
func schedParseCronSpec(fields []string) (schedCronSpec, error) {
	if len(fields) == 1 && strings.HasPrefix(fields[0], "@") {
		alias := strings.ToLower(fields[0])
		if alias == "@reboot" {
			return schedCronSpec{reboot: true, expr: "@reboot"}, nil
		}
		expanded, ok := schedCronAliases[alias]
		if !ok {
			return schedCronSpec{}, fmt.Errorf("unknown schedule %q", fields[0])
		}
		spec, err := schedParseCronSpec(strings.Fields(expanded))
		spec.expr = alias
		return spec, err
	}
	if len(fields) != 5 {
		return schedCronSpec{}, errors.New("a cron schedule has five fields")
	}
	var spec schedCronSpec
	var err error
	if spec.minute, _, err = schedParseCronField(fields[0], 0, 59, nil); err != nil {
		return spec, err
	}
	if spec.hour, _, err = schedParseCronField(fields[1], 0, 23, nil); err != nil {
		return spec, err
	}
	if spec.dom, spec.domStar, err = schedParseCronField(fields[2], 1, 31, nil); err != nil {
		return spec, err
	}
	if spec.month, _, err = schedParseCronField(fields[3], 1, 12, schedCronMonthNames); err != nil {
		return spec, err
	}
	if spec.dow, spec.dowStar, err = schedParseCronField(fields[4], 0, 7, schedCronDayNames); err != nil {
		return spec, err
	}
	// 7 is Sunday too.
	if spec.dow&(1<<7) != 0 {
		spec.dow = (spec.dow &^ (1 << 7)) | 1
	}
	spec.expr = strings.Join(fields, " ")
	return spec, nil
}

func schedBit(set uint64, v int) bool { return set&(1<<uint(v)) != 0 }

// schedCronDayMatches applies cron's day rule: when both the day of the month
// and the weekday are restricted, either one matching is enough.
func schedCronDayMatches(s schedCronSpec, t time.Time) bool {
	dom := schedBit(s.dom, t.Day())
	dow := schedBit(s.dow, int(t.Weekday()))
	switch {
	case s.domStar && s.dowStar:
		return true
	case s.domStar:
		return dow
	case s.dowStar:
		return dom
	}
	return dom || dow
}

// schedCronNext is the first time after from that the expression fires. It
// skips whole months, days and hours that cannot match, so even a yearly job
// costs a few hundred steps.
func schedCronNext(s schedCronSpec, from time.Time) (time.Time, bool) {
	if s.reboot || s.minute == 0 || s.hour == 0 || s.month == 0 {
		return time.Time{}, false
	}
	t := from.Truncate(time.Minute).Add(time.Minute)
	limit := from.AddDate(5, 0, 0)
	for t.Before(limit) {
		if !schedBit(s.month, int(t.Month())) {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !schedCronDayMatches(s, t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
			continue
		}
		if !schedBit(s.hour, t.Hour()) {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, t.Location())
			continue
		}
		if !schedBit(s.minute, t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t, true
	}
	return time.Time{}, false
}

// schedBits lists the values set in a field between min and max.
func schedBits(set uint64, min, max int) []int {
	var out []int
	for v := min; v <= max; v++ {
		if schedBit(set, v) {
			out = append(out, v)
		}
	}
	return out
}

// schedEvenStep reports whether vals is min, min+n, min+2n ... covering the
// whole range, and returns n.
func schedEvenStep(vals []int, min, max int) (int, bool) {
	if len(vals) < 2 || vals[0] != min {
		return 0, false
	}
	step := vals[1] - vals[0]
	for i := 1; i < len(vals); i++ {
		if vals[i]-vals[i-1] != step {
			return 0, false
		}
	}
	if vals[len(vals)-1]+step <= max {
		return 0, false
	}
	return step, true
}

var schedWeekdayShort = [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
var schedWeekdayLong = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
var schedMonthShort = [...]string{"", "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}

// schedOrdinal is 1st, 2nd, 3rd, 4th ... 21st, 22nd.
func schedOrdinal(n int) string {
	suffix := "th"
	if n%100 < 11 || n%100 > 13 {
		switch n % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(n) + suffix
}

// schedWeekdays says a set of weekdays the way people do.
func schedWeekdays(days []int) string {
	switch {
	case len(days) == 5 && days[0] == 1 && days[4] == 5:
		return "Weekdays"
	case len(days) == 2 && days[0] == 0 && days[1] == 6:
		return "Weekends"
	case len(days) == 1:
		return "Every " + schedWeekdayLong[days[0]]
	}
	names := make([]string, 0, len(days))
	for _, d := range days {
		names = append(names, schedWeekdayShort[d])
	}
	return strings.Join(names, ", ")
}

// schedCronHuman says when a cron expression runs: "Every 5 minutes", "Daily
// at 03:30", "Weekdays at 09:00", "Monthly on the 1st at 00:00". Expressions
// too irregular to say briefly are shown as written.
func schedCronHuman(s schedCronSpec) string {
	if s.reboot {
		return "At startup"
	}
	mins := schedBits(s.minute, 0, 59)
	hours := schedBits(s.hour, 0, 23)
	days := schedBits(s.dow, 0, 6)
	doms := schedBits(s.dom, 1, 31)
	months := schedBits(s.month, 1, 12)
	allMonths := len(months) == 12
	custom := "Custom: " + s.expr

	at := ""
	if len(mins) == 1 && len(hours) >= 1 && len(hours) <= 4 {
		parts := make([]string, 0, len(hours))
		for _, h := range hours {
			parts = append(parts, fmt.Sprintf("%02d:%02d", h, mins[0]))
		}
		at = " at " + strings.Join(parts, ", ")
	}

	if s.domStar && s.dowStar && allMonths {
		switch {
		case len(mins) == 60 && len(hours) == 24:
			return "Every minute"
		case len(hours) == 24:
			if n, ok := schedEvenStep(mins, 0, 59); ok {
				return fmt.Sprintf("Every %d minutes", n)
			}
			if len(mins) == 1 {
				if mins[0] == 0 {
					return "Every hour"
				}
				return fmt.Sprintf("Every hour at :%02d", mins[0])
			}
		case len(mins) == 1:
			if n, ok := schedEvenStep(hours, 0, 23); ok {
				if mins[0] == 0 {
					return fmt.Sprintf("Every %d hours", n)
				}
				return fmt.Sprintf("Every %d hours at :%02d", n, mins[0])
			}
			if at != "" {
				return "Daily" + at
			}
		}
		return custom
	}
	if at == "" {
		return custom
	}
	if s.domStar && !s.dowStar && allMonths && len(days) > 0 {
		return schedWeekdays(days) + at
	}
	if !s.domStar && s.dowStar && len(doms) > 0 && len(doms) <= 3 {
		ords := make([]string, 0, len(doms))
		for _, d := range doms {
			ords = append(ords, schedOrdinal(d))
		}
		if allMonths {
			return "Monthly on the " + strings.Join(ords, ", ") + at
		}
		if len(months) == 1 && len(doms) == 1 {
			return fmt.Sprintf("Yearly on %s %d%s", schedMonthShort[months[0]], doms[0], at)
		}
	}
	return custom
}

// schedIsEnvLine reports a crontab environment line ("SHELL=/bin/sh").
func schedIsEnvLine(line string) bool {
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return false
	}
	name := strings.TrimSpace(line[:eq])
	if name == "" || strings.ContainsAny(name, " \t*/@") {
		return false
	}
	return name[0] < '0' || name[0] > '9'
}

// schedParseCronLine parses one crontab line into its schedule, user (system
// crontabs only) and command. A commented-out job ("#0 3 * * * backup") is
// reported with disabled set, so the screen can show it switched off.
func schedParseCronLine(line string, withUser bool) (spec schedCronSpec, user, command string, disabled, ok bool) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "#") {
		disabled = true
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
	}
	if line == "" || schedIsEnvLine(line) {
		return spec, "", "", false, false
	}
	fields := strings.Fields(line)
	n := 5
	if strings.HasPrefix(fields[0], "@") {
		n = 1
	}
	need := n + 1
	if withUser {
		need++
	}
	if len(fields) < need {
		return spec, "", "", false, false
	}
	var err error
	if spec, err = schedParseCronSpec(fields[:n]); err != nil {
		return spec, "", "", false, false
	}
	rest := fields[n:]
	if withUser {
		user, rest = rest[0], rest[1:]
	}
	command = schedRestOfLine(line, len(fields)-len(rest))
	return spec, user, command, disabled, true
}

// schedRestOfLine returns line after its first skip whitespace-separated
// fields, with the command's own spacing kept.
func schedRestOfLine(line string, skip int) string {
	s := strings.TrimSpace(line)
	for i := 0; i < skip; i++ {
		idx := strings.IndexAny(s, " \t")
		if idx < 0 {
			return ""
		}
		s = strings.TrimLeft(s[idx:], " \t")
	}
	return s
}

// schedCommandName is a short display name for a command line: the program's
// base name and its first argument when that says more ("run-parts
// /etc/cron.daily" → "run-parts cron.daily").
func schedCommandName(command string) string {
	cmd := strings.TrimSpace(command)
	fields := strings.Fields(cmd)
	// A quoted program path ("C:\Program Files\App\up.exe" /s) is one word.
	if strings.HasPrefix(cmd, `"`) {
		if end := strings.IndexByte(cmd[1:], '"'); end >= 0 {
			fields = append([]string{cmd[1 : end+1]}, strings.Fields(cmd[end+2:])...)
		}
	}
	if len(fields) == 0 {
		return "(no command)"
	}
	base := func(p string) string {
		p = strings.Trim(p, `"'`)
		if i := strings.LastIndexAny(p, `/\`); i >= 0 && i < len(p)-1 {
			return p[i+1:]
		}
		return p
	}
	name := base(fields[0])
	switch name {
	case "sh", "bash", "ash", "run-parts", "python", "python3", "nice", "ionice", "test", "[":
		for _, f := range fields[1:] {
			if strings.HasPrefix(f, "-") || f == "&&" || f == "||" {
				continue
			}
			return name + " " + base(f)
		}
	}
	return name
}

// schedParseCrontab lists the jobs in one crontab. withUser is the system
// format (/etc/crontab, /etc/cron.d) with a user column; defaultUser names the
// owner of a per-user crontab.
func schedParseCrontab(text, source, group, defaultUser string, withUser bool, now time.Time) []schedItem {
	var out []schedItem
	for _, raw := range strings.Split(text, "\n") {
		spec, user, command, disabled, ok := schedParseCronLine(strings.TrimRight(raw, "\r"), withUser)
		if !ok {
			continue
		}
		if user == "" {
			user = defaultUser
		}
		it := schedItem{
			Group:    group,
			Name:     schedCommandName(command),
			Schedule: schedCronHuman(spec),
			Enabled:  !disabled,
			Command:  command,
			Source:   source,
			User:     user,
			Icon:     "schedule",
			Comment:  "Cron expression: " + spec.expr,
		}
		switch {
		case disabled:
			it.Status = "Disabled (commented out)"
		case spec.reboot:
			it.Status = "Runs at boot"
			it.Icon = "power"
		default:
			it.Status = "Enabled"
			if next, ok := schedCronNext(spec, now); ok {
				it.nextAt = next
				it.Next = schedFormatWhen(next, now)
			}
		}
		out = append(out, it)
	}
	return out
}

// schedFormatWhen says when a time is, relative to now: "Today 03:00",
// "Tomorrow 03:00", "Mon 09:00" within a week, else "12 Dec 03:00".
func schedFormatWhen(t, now time.Time) string {
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	today := time.Date(y1, m1, d1, 0, 0, 0, 0, now.Location())
	day := time.Date(y2, m2, d2, 0, 0, 0, 0, now.Location())
	days := int(day.Sub(today).Hours() / 24)
	clock := t.Format("15:04")
	switch {
	case days == 0:
		return "Today " + clock
	case days == 1:
		return "Tomorrow " + clock
	case days > 1 && days < 7:
		return t.Format("Mon") + " " + clock
	case t.Year() == now.Year():
		return t.Format("2 Jan") + " " + clock
	}
	return t.Format("2 Jan 2006") + " " + clock
}

// ── Windows Task Scheduler ───────────────────────────────────────────────

// schedTaskRow is one task from `schtasks /query /fo CSV /v`. The columns are
// read by position, not by title: the titles are translated on a German or
// Russian Windows, the order is not.
type schedTaskRow struct {
	Name, Next, Status, LastRun, LastResult, Author, Run, StartIn, Comment string
	State, RunAs, ScheduleType, StartTime, StartDate, Days, Months, Repeat string
	Schedules                                                              []string
}

// schedParseTasksCSV reads schtasks' CSV, verbose (28 columns) or brief
// (3 columns). Header rows — schtasks repeats them for every task folder — are
// recognised by their task-name column, which for a real task is always a path
// starting with a backslash. A task with several triggers appears once per
// trigger; those rows are merged.
func schedParseTasksCSV(data []byte) ([]schedTaskRow, error) {
	r := csv.NewReader(strings.NewReader(schedDecodeText(data)))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	byName := map[string]int{}
	var out []schedTaskRow
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return out, err
		}
		col := func(i int) string {
			if i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		var row schedTaskRow
		switch {
		case len(rec) >= 19:
			row = schedTaskRow{
				Name: col(1), Next: col(2), Status: col(3), LastRun: col(5), LastResult: col(6),
				Author: col(7), Run: col(8), StartIn: col(9), Comment: col(10), State: col(11),
				RunAs: col(14), ScheduleType: col(18), StartTime: col(19), StartDate: col(20),
				Days: col(22), Months: col(23), Repeat: col(24),
			}
		case len(rec) == 3:
			row = schedTaskRow{Name: col(0), Next: col(1), Status: col(2)}
		default:
			continue
		}
		if !strings.HasPrefix(row.Name, `\`) {
			continue
		}
		sched := schedTaskCSVSchedule(row)
		if i, seen := byName[row.Name]; seen {
			if sched != "" {
				out[i].Schedules = append(out[i].Schedules, sched)
			}
			continue
		}
		if sched != "" {
			row.Schedules = []string{sched}
		}
		byName[row.Name] = len(out)
		out = append(out, row)
	}
	return out, nil
}

// schedTaskTime turns schtasks' start time ("3:00:00 AM", "15:30:00") into
// 24-hour "03:00", or "" when it is not a time.
func schedTaskTime(s string) string {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"3:04:05 PM", "3:04 PM", "15:04:05", "15:04", "03:04:05 PM"} {
		if t, err := time.Parse(layout, strings.ToUpper(s)); err == nil {
			return t.Format("15:04")
		}
	}
	return ""
}

// schedTaskCSVSchedule describes one trigger row in words. The schedule type is
// matched in English; on a translated Windows the type is shown as written,
// with the time when there is one. The XML export, when it can be read, gives
// a better answer (schedTaskXMLSchedule) and wins over this.
func schedTaskCSVSchedule(row schedTaskRow) string {
	typ := strings.TrimSpace(row.ScheduleType)
	if typ == "" || strings.EqualFold(typ, "N/A") {
		return ""
	}
	at := ""
	if clock := schedTaskTime(row.StartTime); clock != "" {
		at = " at " + clock
	}
	lower := strings.ToLower(typ)
	switch {
	case strings.Contains(lower, "logon"):
		return "At logon"
	case strings.Contains(lower, "start up") || strings.Contains(lower, "startup") || strings.Contains(lower, "boot"):
		return "At startup"
	case strings.Contains(lower, "idle"):
		return "When the computer is idle"
	case strings.Contains(lower, "event"):
		return "When an event is logged"
	case strings.Contains(lower, "one time"):
		if row.StartDate != "" && !strings.EqualFold(row.StartDate, "N/A") {
			return "Once on " + row.StartDate + at
		}
		return "Once" + at
	case strings.Contains(lower, "daily"):
		return "Daily" + at + schedTaskRepeat(row.Repeat)
	case strings.Contains(lower, "weekly"):
		days := strings.TrimSpace(row.Days)
		if days == "" || strings.EqualFold(days, "N/A") || strings.HasPrefix(strings.ToLower(days), "every") {
			return "Weekly" + at
		}
		return schedTitleDays(days) + at
	case strings.Contains(lower, "monthly"):
		return "Monthly" + at
	case strings.Contains(lower, "hourly"):
		return "Every hour"
	case strings.Contains(lower, "minute"):
		return "Every few minutes"
	}
	return strings.TrimSpace(typ + at)
}

// schedTaskRepeat adds a repetition ("1 Hour(s), 0 Minute(s)") to a schedule.
func schedTaskRepeat(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "Disabled") || strings.EqualFold(s, "N/A") {
		return ""
	}
	var hours, minutes int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		f := strings.Fields(part)
		if len(f) < 2 {
			continue
		}
		n, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(strings.ToLower(f[1]), "hour"):
			hours = n
		case strings.HasPrefix(strings.ToLower(f[1]), "minute"):
			minutes = n
		}
	}
	return schedRepeatText(time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute)
}

// schedRepeatText is ", repeats every 15 minutes" for a repetition interval.
func schedRepeatText(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d%time.Hour == 0 && d == time.Hour:
		return ", repeats every hour"
	case d%time.Hour == 0:
		return fmt.Sprintf(", repeats every %d hours", int(d/time.Hour))
	case d < time.Hour:
		return fmt.Sprintf(", repeats every %d minutes", int(d/time.Minute))
	}
	return ", repeats every " + d.String()
}

// schedTitleDays turns "MON, WED" into "Mon, Wed" (and a full week into
// "Every day").
func schedTitleDays(days string) string {
	parts := strings.Split(days, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, strings.ToUpper(p[:1])+strings.ToLower(p[1:]))
	}
	if len(out) == 7 {
		return "Every day"
	}
	return strings.Join(out, ", ")
}

// schedTaskResult says a task's last result code in words.
func schedTaskResult(code string) string {
	code = strings.TrimSpace(code)
	if code == "" || strings.EqualFold(code, "N/A") {
		return ""
	}
	n, err := strconv.ParseInt(code, 10, 64)
	if err != nil {
		return code
	}
	switch n {
	case 0:
		return "Succeeded"
	case 0x41300:
		return "Ready to run"
	case 0x41301:
		return "Running now"
	case 0x41302:
		return "Disabled"
	case 0x41303:
		return "Has not run yet"
	case 0x41306:
		return "Stopped by the user"
	case 1:
		return "Failed (code 1)"
	}
	return fmt.Sprintf("Failed (0x%08X)", uint32(int32(n)))
}

// schedXMLTask is one task from `schtasks /query /xml`: its schedule in the
// Task Scheduler's own, untranslated vocabulary.
type schedXMLTask struct {
	Name     string
	Enabled  bool
	Hidden   bool
	Author   string
	Comment  string
	Command  string
	Schedule string
}

type schedXRepetition struct {
	Interval string `xml:"Interval"`
}

type schedXDays struct {
	Sunday    *struct{} `xml:"Sunday"`
	Monday    *struct{} `xml:"Monday"`
	Tuesday   *struct{} `xml:"Tuesday"`
	Wednesday *struct{} `xml:"Wednesday"`
	Thursday  *struct{} `xml:"Thursday"`
	Friday    *struct{} `xml:"Friday"`
	Saturday  *struct{} `xml:"Saturday"`
}

func (d schedXDays) list() []int {
	var out []int
	for i, set := range []*struct{}{d.Sunday, d.Monday, d.Tuesday, d.Wednesday, d.Thursday, d.Friday, d.Saturday} {
		if set != nil {
			out = append(out, i)
		}
	}
	return out
}

type schedXCalendar struct {
	Start      string           `xml:"StartBoundary"`
	Enabled    string           `xml:"Enabled"`
	Repetition schedXRepetition `xml:"Repetition"`
	ByDay      *struct {
		Interval int `xml:"DaysInterval"`
	} `xml:"ScheduleByDay"`
	ByWeek *struct {
		Interval int        `xml:"WeeksInterval"`
		Days     schedXDays `xml:"DaysOfWeek"`
	} `xml:"ScheduleByWeek"`
	ByMonth *struct {
		Days []string `xml:"DaysOfMonth>Day"`
	} `xml:"ScheduleByMonth"`
	ByMonthDOW *struct {
		Weeks []string   `xml:"Weeks>Week"`
		Days  schedXDays `xml:"DaysOfWeek"`
	} `xml:"ScheduleByMonthDayOfWeek"`
}

type schedXTrigger struct {
	Start      string           `xml:"StartBoundary"`
	Enabled    string           `xml:"Enabled"`
	Repetition schedXRepetition `xml:"Repetition"`
	State      string           `xml:"StateChange"`
}

type schedXTask struct {
	RegInfo struct {
		Author      string `xml:"Author"`
		Description string `xml:"Description"`
		URI         string `xml:"URI"`
	} `xml:"RegistrationInfo"`
	Triggers struct {
		Calendar     []schedXCalendar `xml:"CalendarTrigger"`
		Time         []schedXTrigger  `xml:"TimeTrigger"`
		Logon        []schedXTrigger  `xml:"LogonTrigger"`
		Boot         []schedXTrigger  `xml:"BootTrigger"`
		Idle         []schedXTrigger  `xml:"IdleTrigger"`
		Event        []schedXTrigger  `xml:"EventTrigger"`
		Registration []schedXTrigger  `xml:"RegistrationTrigger"`
		Session      []schedXTrigger  `xml:"SessionStateChangeTrigger"`
	} `xml:"Triggers"`
	Settings struct {
		Enabled string `xml:"Enabled"`
		Hidden  string `xml:"Hidden"`
	} `xml:"Settings"`
	Actions struct {
		Exec []struct {
			Command   string `xml:"Command"`
			Arguments string `xml:"Arguments"`
		} `xml:"Exec"`
		Com []struct {
			ClassID string `xml:"ClassId"`
		} `xml:"ComHandler"`
	} `xml:"Actions"`
}

// schedXMLDuration reads the ISO 8601 durations the scheduler uses ("PT15M",
// "PT1H", "P1D").
func schedXMLDuration(s string) time.Duration {
	s = strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(s, "P") {
		return 0
	}
	var total time.Duration
	inTime := false
	num := ""
	for _, r := range s[1:] {
		switch {
		case r == 'T':
			inTime = true
		case r >= '0' && r <= '9':
			num += string(r)
		default:
			n, err := strconv.Atoi(num)
			num = ""
			if err != nil {
				continue
			}
			switch {
			case r == 'D':
				total += time.Duration(n) * 24 * time.Hour
			case r == 'H' && inTime:
				total += time.Duration(n) * time.Hour
			case r == 'M' && inTime:
				total += time.Duration(n) * time.Minute
			case r == 'S' && inTime:
				total += time.Duration(n) * time.Second
			}
		}
	}
	return total
}

// schedXMLClock is the time of day in a StartBoundary ("2024-01-01T03:00:00").
func schedXMLClock(start string) string {
	if i := strings.IndexByte(start, 'T'); i >= 0 && len(start) >= i+6 {
		return start[i+1 : i+6]
	}
	return ""
}

func schedXMLOff(enabled string) bool {
	return strings.EqualFold(strings.TrimSpace(enabled), "false")
}

// schedTaskXMLSchedule describes a task's triggers in words.
func schedTaskXMLSchedule(t schedXTask) string {
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	for _, c := range t.Triggers.Calendar {
		if schedXMLOff(c.Enabled) {
			continue
		}
		at := ""
		if clock := schedXMLClock(c.Start); clock != "" {
			at = " at " + clock
		}
		rep := schedRepeatText(schedXMLDuration(c.Repetition.Interval))
		switch {
		case c.ByDay != nil:
			if c.ByDay.Interval > 1 {
				add(fmt.Sprintf("Every %d days%s%s", c.ByDay.Interval, at, rep))
			} else {
				add("Daily" + at + rep)
			}
		case c.ByWeek != nil:
			days := c.ByWeek.Days.list()
			desc := "Weekly"
			if len(days) > 0 {
				desc = schedWeekdays(days)
			}
			if c.ByWeek.Interval > 1 {
				desc = fmt.Sprintf("Every %d weeks · %s", c.ByWeek.Interval, desc)
			}
			add(desc + at + rep)
		case c.ByMonth != nil:
			days := make([]string, 0, len(c.ByMonth.Days))
			for _, d := range c.ByMonth.Days {
				if n, err := strconv.Atoi(strings.TrimSpace(d)); err == nil {
					days = append(days, schedOrdinal(n))
				} else if strings.EqualFold(strings.TrimSpace(d), "Last") {
					days = append(days, "last day")
				}
			}
			if len(days) == 0 {
				add("Monthly" + at + rep)
			} else {
				add("Monthly on the " + strings.Join(days, ", ") + at + rep)
			}
		case c.ByMonthDOW != nil:
			weekNames := map[string]string{"1": "first", "2": "second", "3": "third", "4": "fourth", "Last": "last"}
			var weeks []string
			for _, w := range c.ByMonthDOW.Weeks {
				if name, ok := weekNames[strings.TrimSpace(w)]; ok {
					weeks = append(weeks, name)
				}
			}
			var names []string
			for _, d := range c.ByMonthDOW.Days.list() {
				names = append(names, schedWeekdayLong[d])
			}
			if len(weeks) > 0 && len(names) > 0 {
				add("Monthly on the " + strings.Join(weeks, " and ") + " " + strings.Join(names, ", ") + at + rep)
			} else {
				add("Monthly" + at + rep)
			}
		default:
			add("On a schedule" + at + rep)
		}
	}
	for _, tr := range t.Triggers.Time {
		if schedXMLOff(tr.Enabled) {
			continue
		}
		if rep := schedXMLDuration(tr.Repetition.Interval); rep > 0 {
			add(strings.TrimPrefix(schedRepeatText(rep), ", repeats "))
			continue
		}
		when := strings.Replace(tr.Start, "T", " ", 1)
		if len(when) > 16 {
			when = when[:16]
		}
		add(strings.TrimSpace("Once " + when))
	}
	simple := []struct {
		list []schedXTrigger
		text string
	}{
		{t.Triggers.Logon, "At logon"},
		{t.Triggers.Boot, "At startup"},
		{t.Triggers.Idle, "When the computer is idle"},
		{t.Triggers.Event, "When an event is logged"},
		{t.Triggers.Registration, "When the task is created or changed"},
	}
	for _, s := range simple {
		for _, tr := range s.list {
			if !schedXMLOff(tr.Enabled) {
				add(s.text)
				break
			}
		}
	}
	for _, tr := range t.Triggers.Session {
		if schedXMLOff(tr.Enabled) {
			continue
		}
		switch strings.TrimSpace(tr.State) {
		case "SessionLock":
			add("When the PC is locked")
		case "SessionUnlock":
			add("When the PC is unlocked")
		case "ConsoleConnect", "RemoteConnect":
			add("When a user connects")
		case "ConsoleDisconnect", "RemoteDisconnect":
			add("When a user disconnects")
		default:
			add("On a session change")
		}
	}
	// Distinct descriptions only, in the order found.
	seen := map[string]bool{}
	uniq := parts[:0]
	for _, p := range parts {
		if !seen[p] {
			seen[p] = true
			uniq = append(uniq, p)
		}
	}
	if len(uniq) == 0 {
		return "On demand only"
	}
	if len(uniq) > 2 {
		return fmt.Sprintf("%s · %s +%d more", uniq[0], uniq[1], len(uniq)-2)
	}
	return strings.Join(uniq, " · ")
}

// schedStripXMLDecls removes every <?xml ...?> declaration: the export may
// repeat one per task, and the text has already been decoded to UTF-8 so the
// "UTF-16" each one announces is no longer true.
func schedStripXMLDecls(s string) string {
	for {
		i := strings.Index(s, "<?xml")
		if i < 0 {
			return s
		}
		j := strings.Index(s[i:], "?>")
		if j < 0 {
			return s[:i]
		}
		s = s[:i] + s[i+j+2:]
	}
}

// schedParseTasksXML reads the multi-task XML export. Each <Task> follows a
// comment naming it ("<!-- \Folder\Name -->"); the task's own URI is the
// fallback name.
func schedParseTasksXML(data []byte) ([]schedXMLTask, error) {
	text := schedStripXMLDecls(schedDecodeText(data))
	dec := xml.NewDecoder(strings.NewReader(text))
	dec.Strict = false
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	var out []schedXMLTask
	pending := ""
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.Comment:
			if name := strings.TrimSpace(string(t)); strings.HasPrefix(name, `\`) {
				pending = name
			}
		case xml.StartElement:
			if t.Name.Local != "Task" {
				continue
			}
			var task schedXTask
			if err := dec.DecodeElement(&task, &t); err != nil {
				return out, err
			}
			name := pending
			if name == "" {
				name = strings.TrimSpace(task.RegInfo.URI)
			}
			pending = ""
			if name == "" {
				continue
			}
			cmd := ""
			if len(task.Actions.Exec) > 0 {
				cmd = strings.TrimSpace(task.Actions.Exec[0].Command + " " + task.Actions.Exec[0].Arguments)
			} else if len(task.Actions.Com) > 0 {
				cmd = "COM handler " + strings.TrimSpace(task.Actions.Com[0].ClassID)
			}
			out = append(out, schedXMLTask{
				Name:     name,
				Enabled:  !schedXMLOff(task.Settings.Enabled),
				Hidden:   strings.EqualFold(strings.TrimSpace(task.Settings.Hidden), "true"),
				Author:   strings.TrimSpace(task.RegInfo.Author),
				Comment:  strings.TrimSpace(task.RegInfo.Description),
				Command:  cmd,
				Schedule: schedTaskXMLSchedule(task),
			})
		}
	}
	return out, nil
}

// schedTaskItems merges the CSV rows (status, next and last run) with the XML
// tasks (schedule, enabled state, command) into screen rows. Either list may
// be empty: without the XML the CSV's own schedule text is used, and without
// the CSV the XML still lists every task.
func schedTaskItems(rows []schedTaskRow, tasks []schedXMLTask) []schedItem {
	byName := make(map[string]schedXMLTask, len(tasks))
	for _, t := range tasks {
		byName[strings.ToLower(t.Name)] = t
	}
	seen := map[string]bool{}
	var out []schedItem
	addTask := func(row schedTaskRow, x schedXMLTask, haveX bool) {
		name := row.Name
		if name == "" {
			name = x.Name
		}
		key := strings.ToLower(name)
		if seen[key] {
			return
		}
		seen[key] = true
		folder, base := schedTaskSplit(name)
		it := schedItem{
			Name:    base,
			Next:    schedTaskWhen(row.Next),
			Last:    schedTaskWhen(row.LastRun),
			Result:  schedTaskResult(row.LastResult),
			Status:  row.Status,
			Command: row.Run,
			User:    row.RunAs,
			Author:  row.Author,
			Comment: row.Comment,
			Source:  "Task Scheduler › " + folder,
			Icon:    "schedule",
			System:  strings.HasPrefix(strings.ToLower(name), `\microsoft\`),
			Enabled: !strings.EqualFold(row.Status, "Disabled") && !strings.EqualFold(row.State, "Disabled"),
		}
		it.Schedule = strings.Join(row.Schedules, " · ")
		if haveX {
			it.Enabled = x.Enabled
			if x.Schedule != "" {
				it.Schedule = x.Schedule
			}
			if it.Command == "" || strings.EqualFold(it.Command, "COM handler") {
				it.Command = x.Command
			}
			if it.Author == "" {
				it.Author = x.Author
			}
			if it.Comment == "" || strings.EqualFold(it.Comment, "N/A") {
				it.Comment = x.Comment
			}
		}
		if it.Schedule == "" {
			it.Schedule = "On demand only"
		}
		if it.Status == "" {
			it.Status = "Ready"
			if !it.Enabled {
				it.Status = "Disabled"
			}
		}
		for _, field := range []*string{&it.Command, &it.User, &it.Author, &it.Comment} {
			if strings.EqualFold(strings.TrimSpace(*field), "N/A") {
				*field = ""
			}
		}
		it.Group = "Task Scheduler"
		if it.System {
			it.Group = "Windows system tasks"
		}
		out = append(out, it)
	}
	for _, row := range rows {
		x, ok := byName[strings.ToLower(row.Name)]
		addTask(row, x, ok)
	}
	for _, x := range tasks {
		addTask(schedTaskRow{Name: x.Name}, x, true)
	}
	return out
}

// schedTaskSplit splits a task path into its folder and its own name.
func schedTaskSplit(path string) (folder, name string) {
	i := strings.LastIndexByte(path, '\\')
	if i < 0 {
		return `\`, path
	}
	folder = path[:i+1]
	return folder, path[i+1:]
}

// schedTaskWhen cleans schtasks' date column: "N/A" and the 1999 placeholder
// it prints for a task that never ran become "".
func schedTaskWhen(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "N/A") || strings.Contains(s, "1999") {
		return ""
	}
	return s
}

// ── systemd timers ───────────────────────────────────────────────────────

// schedParseTimers reads `systemctl list-timers --all --no-legend`. Its columns
// are aligned with spaces and the dates contain spaces too, so a row is found
// by its ".timer" unit: the service after it is what runs, and the first four
// words before it are the next run ("Mon 2026-11-09 00:00:00 UTC") unless the
// row starts with "n/a".
func schedParseTimers(text string) []schedItem {
	var out []schedItem
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		unit := -1
		for i, f := range fields {
			if strings.HasSuffix(f, ".timer") {
				unit = i
				break
			}
		}
		if unit < 0 {
			continue
		}
		it := schedItem{
			Group:    "systemd timers",
			Name:     strings.TrimSuffix(fields[unit], ".timer"),
			Schedule: "systemd timer",
			Status:   "Waiting",
			Enabled:  true,
			Source:   "systemctl list-timers",
			Icon:     "schedule",
		}
		if unit+1 < len(fields) {
			it.Command = fields[unit+1]
		}
		if len(fields) > 0 && !strings.EqualFold(fields[0], "n/a") && unit >= 4 {
			it.Next = strings.Join(fields[0:4], " ")
		} else {
			it.Status = "Inactive"
			it.Enabled = false
		}
		out = append(out, it)
	}
	return out
}

// ── Desktop autostart entries ────────────────────────────────────────────

// schedDesktopEntry is the part of a freedesktop .desktop file the screen
// shows.
type schedDesktopEntry struct {
	Name, Exec, Comment string
	Hidden              bool // Hidden=true or X-GNOME-Autostart-enabled=false
}

// schedParseDesktopEntry reads the [Desktop Entry] group of a .desktop file.
func schedParseDesktopEntry(text string) schedDesktopEntry {
	var e schedDesktopEntry
	inEntry := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if !inEntry || line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "Name":
			e.Name = value
		case "Exec":
			e.Exec = value
		case "Comment":
			e.Comment = value
		case "Hidden":
			if strings.EqualFold(value, "true") {
				e.Hidden = true
			}
		case "X-GNOME-Autostart-enabled":
			if strings.EqualFold(value, "false") {
				e.Hidden = true
			}
		}
	}
	return e
}

// schedSetDesktopHidden returns the .desktop text with its Hidden key set,
// added to the [Desktop Entry] group when missing. Disabling an autostart
// entry this way is what desktop settings panels do, and it is undone by
// setting the key back.
func schedSetDesktopHidden(text string, hidden bool) string {
	value := "false"
	if hidden {
		value = "true"
	}
	lines := strings.Split(text, "\n")
	inEntry, entryEnd, done := false, -1, false
	for i, raw := range lines {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if strings.HasPrefix(line, "[") {
			if inEntry && entryEnd < 0 {
				entryEnd = i
			}
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if !inEntry {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Hidden":
			lines[i] = "Hidden=" + value
			done = true
		case "X-GNOME-Autostart-enabled":
			if hidden {
				lines[i] = "X-GNOME-Autostart-enabled=false"
			} else {
				lines[i] = "X-GNOME-Autostart-enabled=true"
			}
		}
	}
	if done {
		return strings.Join(lines, "\n")
	}
	insert := "Hidden=" + value
	if entryEnd < 0 {
		// The entry group runs to the end of the file: append, keeping a
		// trailing newline where there was one.
		trimmed := strings.TrimRight(text, "\n")
		return trimmed + "\n" + insert + "\n"
	}
	out := append([]string{}, lines[:entryEnd]...)
	out = append(out, insert)
	out = append(out, lines[entryEnd:]...)
	return strings.Join(out, "\n")
}

// ── Sorting ──────────────────────────────────────────────────────────────

// schedSortItems orders a list for the screen: by group (in the order the
// groups first appear), enabled before disabled, then the soonest next run,
// then the name.
func schedSortItems(items []schedItem) {
	order := map[string]int{}
	for _, it := range items {
		if _, ok := order[it.Group]; !ok {
			order[it.Group] = len(order)
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if order[a.Group] != order[b.Group] {
			return order[a.Group] < order[b.Group]
		}
		if a.Enabled != b.Enabled {
			return a.Enabled
		}
		if a.nextAt.IsZero() != b.nextAt.IsZero() {
			return !a.nextAt.IsZero()
		}
		if !a.nextAt.Equal(b.nextAt) {
			return a.nextAt.Before(b.nextAt)
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}

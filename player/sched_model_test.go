package main

// Tests for the Scheduled tasks screen's parsers: cron expressions in words and
// their next run, crontab lines, the Task Scheduler's CSV and XML exports,
// desktop autostart entries and systemd timers. All pure, so they run on any
// host.

import (
	"strings"
	"testing"
	"time"
)

func mustCron(t *testing.T, expr string) schedCronSpec {
	t.Helper()
	spec, err := schedParseCronSpec(strings.Fields(expr))
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	return spec
}

func TestSchedCronHuman(t *testing.T) {
	cases := []struct{ expr, want string }{
		{"* * * * *", "Every minute"},
		{"*/5 * * * *", "Every 5 minutes"},
		{"0 * * * *", "Every hour"},
		{"15 * * * *", "Every hour at :15"},
		{"0 */2 * * *", "Every 2 hours"},
		{"30 3 * * *", "Daily at 03:30"},
		{"0 8,20 * * *", "Daily at 08:00, 20:00"},
		{"0 9 * * 1-5", "Weekdays at 09:00"},
		{"0 10 * * 0,6", "Weekends at 10:00"},
		{"0 10 * * sat,sun", "Weekends at 10:00"},
		{"0 7 * * 1", "Every Monday at 07:00"},
		{"0 7 * * 1,3,5", "Mon, Wed, Fri at 07:00"},
		{"0 0 1 * *", "Monthly on the 1st at 00:00"},
		{"0 0 1 1 *", "Yearly on Jan 1 at 00:00"},
		{"@daily", "Daily at 00:00"},
		{"@hourly", "Every hour"},
		{"@reboot", "At startup"},
		{"7,19,43 2-4 */3 * *", "Custom: 7,19,43 2-4 */3 * *"},
	}
	for _, c := range cases {
		if got := schedCronHuman(mustCron(t, c.expr)); got != c.want {
			t.Errorf("schedCronHuman(%q) = %q, want %q", c.expr, got, c.want)
		}
	}
}

func TestSchedCronRejectsBadExpressions(t *testing.T) {
	for _, expr := range []string{"60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "*/0 * * * *", "a b c d e", "@sometimes", "* * * *"} {
		if _, err := schedParseCronSpec(strings.Fields(expr)); err == nil {
			t.Errorf("schedParseCronSpec(%q) accepted a bad expression", expr)
		}
	}
}

func TestSchedCronNext(t *testing.T) {
	loc := time.UTC
	from := time.Date(2026, 11, 8, 10, 15, 30, 0, loc) // a Sunday
	cases := []struct {
		expr string
		want time.Time
	}{
		{"*/5 * * * *", time.Date(2026, 11, 8, 10, 20, 0, 0, loc)},
		{"30 3 * * *", time.Date(2026, 11, 9, 3, 30, 0, 0, loc)},
		{"0 9 * * 1-5", time.Date(2026, 11, 9, 9, 0, 0, 0, loc)},
		{"0 0 1 * *", time.Date(2026, 12, 1, 0, 0, 0, 0, loc)},
		{"0 12 29 2 *", time.Date(2028, 2, 29, 12, 0, 0, 0, loc)},
		// Both day fields restricted: either one matching is enough.
		{"0 6 15 * 1", time.Date(2026, 11, 9, 6, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		got, ok := schedCronNext(mustCron(t, c.expr), from)
		if !ok || !got.Equal(c.want) {
			t.Errorf("next(%q) = %v (%v), want %v", c.expr, got, ok, c.want)
		}
	}
	if _, ok := schedCronNext(mustCron(t, "@reboot"), from); ok {
		t.Error("@reboot has no next time")
	}
}

func TestSchedParseCrontab(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	text := strings.Join([]string{
		"# m h  dom mon dow   command",
		"SHELL=/bin/sh",
		"MAILTO=\"\"",
		"",
		"*/15 * * * * /usr/bin/sync-saves.sh --quiet",
		"#0 4 * * * /opt/backup.sh",
		"@reboot /mnt/SDCARD/start.sh",
	}, "\n")
	items := schedParseCrontab(text, "crontab -l", "Your crontab", "root", false, now)
	if len(items) != 3 {
		t.Fatalf("parsed %d jobs, want 3: %+v", len(items), items)
	}
	if items[0].Name != "sync-saves.sh" || items[0].Schedule != "Every 15 minutes" || items[0].Command != "/usr/bin/sync-saves.sh --quiet" {
		t.Errorf("job 1 = %+v", items[0])
	}
	if items[0].Next != "Today 10:15" || !items[0].Enabled || items[0].User != "root" {
		t.Errorf("job 1 next/enabled/user = %q %v %q", items[0].Next, items[0].Enabled, items[0].User)
	}
	if items[1].Enabled || !strings.HasPrefix(items[1].Status, "Disabled") {
		t.Errorf("a commented-out job should be listed as disabled: %+v", items[1])
	}
	if items[2].Schedule != "At startup" || items[2].Status != "Runs at boot" {
		t.Errorf("@reboot job = %+v", items[2])
	}

	system := schedParseCrontab("17 *\t* * *\troot    cd / && run-parts --report /etc/cron.hourly\n", "/etc/crontab", "System crontab", "", true, now)
	if len(system) != 1 || system[0].User != "root" || system[0].Name != "cd" && !strings.Contains(system[0].Command, "run-parts") {
		t.Fatalf("system crontab = %+v", system)
	}
	if system[0].Schedule != "Every hour at :17" {
		t.Errorf("system schedule = %q", system[0].Schedule)
	}
}

func TestSchedCommandName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/usr/bin/sync-saves.sh --quiet", "sync-saves.sh"},
		{"run-parts /etc/cron.daily", "run-parts cron.daily"},
		{"sh -c /mnt/SDCARD/do.sh", "sh do.sh"},
		{`"C:\Program Files\App\up.exe" /s`, "up.exe"},
		{"", "(no command)"},
	}
	for _, c := range cases {
		if got := schedCommandName(c.in); got != c.want {
			t.Errorf("schedCommandName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSchedFormatWhen(t *testing.T) {
	now := time.Date(2026, 11, 8, 22, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 11, 8, 23, 30, 0, 0, time.UTC), "Today 23:30"},
		{time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC), "Tomorrow 03:00"},
		{time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC), "Thu 09:00"},
		{time.Date(2026, 12, 25, 9, 0, 0, 0, time.UTC), "25 Dec 09:00"},
		{time.Date(2027, 1, 2, 9, 0, 0, 0, time.UTC), "2 Jan 2027 09:00"},
	}
	for _, c := range cases {
		if got := schedFormatWhen(c.at, now); got != c.want {
			t.Errorf("schedFormatWhen(%v) = %q, want %q", c.at, got, c.want)
		}
	}
}

// The verbose CSV: position-based columns, a repeated header per folder (in
// any language), and one row per trigger merged into one task.
func TestSchedParseTasksCSV(t *testing.T) {
	header := `"HostName","Aufgabenname","Nächste Laufzeit","Status","Anmeldemodus","Letzte Laufzeit","Letztes Ergebnis","Autor","Auszuführende Aufgabe","Starten in","Kommentar","Status der geplanten Aufgabe","Leerlaufzeit","Energieverwaltung","Ausführen als Benutzer","x","x","Zeitplan","Zeitplantyp","Startzeit","Startdatum","Enddatum","Tage","Monate","Wiederholen: Jede","x","x","x"`
	row := func(name, next, status, typ, start, days string) string {
		return `"PC","` + name + `","` + next + `","` + status + `","Interactive only","11/7/2026 3:00:00 AM","0","Juka","C:\Tools\backup.exe /q","N/A","N/A","Enabled","Disabled","","Juka","x","x","sched","` + typ + `","` + start + `","1/1/2026","N/A","` + days + `","N/A","Disabled","x","x","x"`
	}
	data := strings.Join([]string{
		header,
		row(`\Backup`, "11/9/2026 3:00:00 AM", "Ready", "Daily ", "3:00:00 AM", "Every 1 day(s)"),
		row(`\Backup`, "11/9/2026 3:00:00 AM", "Ready", "At logon time", "N/A", "N/A"),
		header,
		row(`\Microsoft\Windows\Defrag\ScheduledDefrag`, "N/A", "Disabled", "Weekly", "1:00:00 AM", "MON, WED"),
	}, "\r\n")
	rows, err := schedParseTasksCSV([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("parsed %d tasks, want 2 (headers skipped, triggers merged): %+v", len(rows), rows)
	}
	if rows[0].Name != `\Backup` || len(rows[0].Schedules) != 2 || rows[0].Schedules[0] != "Daily at 03:00" || rows[0].Schedules[1] != "At logon" {
		t.Errorf("task 1 = %+v", rows[0])
	}
	if rows[1].Schedules[0] != "Mon, Wed at 01:00" {
		t.Errorf("weekly schedule = %q", rows[1].Schedules[0])
	}

	items := schedTaskItems(rows, nil)
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0].Name != "Backup" || items[0].Group != "Task Scheduler" || items[0].Schedule != "Daily at 03:00 · At logon" ||
		items[0].Result != "Succeeded" || items[0].Comment != "" {
		t.Errorf("item 1 = %+v", items[0])
	}
	if !items[1].System || items[1].Enabled || items[1].Group != "Windows system tasks" {
		t.Errorf("a disabled \\Microsoft task should be a hidden-by-default system item: %+v", items[1])
	}

	brief, err := schedParseTasksCSV([]byte(`"\Example, quoted","11/2/2026 11:30 PM","Ready"`))
	if err != nil || len(brief) != 1 || brief[0].Name != `\Example, quoted` || brief[0].Status != "Ready" {
		t.Fatalf("brief CSV = %+v, %v", brief, err)
	}
}

func TestSchedTaskResult(t *testing.T) {
	cases := map[string]string{
		"0":           "Succeeded",
		"267011":      "Has not run yet",
		"267009":      "Running now",
		"1":           "Failed (code 1)",
		"-2147020576": "Failed (0x800710E0)",
		"N/A":         "",
	}
	for in, want := range cases {
		if got := schedTaskResult(in); got != want {
			t.Errorf("schedTaskResult(%q) = %q, want %q", in, got, want)
		}
	}
}

// The XML export says the schedule in the scheduler's own words, whatever the
// display language; the comment before each task names it.
func TestSchedParseTasksXML(t *testing.T) {
	data := `<?xml version="1.0" encoding="UTF-16"?>
<Tasks>
<!-- \Backup -->
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Author>Juka</Author><Description>Nightly backup</Description><URI>\Backup</URI></RegistrationInfo>
  <Triggers>
    <CalendarTrigger>
      <StartBoundary>2026-01-01T03:00:00</StartBoundary>
      <Enabled>true</Enabled>
      <ScheduleByDay><DaysInterval>1</DaysInterval></ScheduleByDay>
    </CalendarTrigger>
    <LogonTrigger><Enabled>true</Enabled></LogonTrigger>
  </Triggers>
  <Settings><Enabled>true</Enabled></Settings>
  <Actions Context="Author"><Exec><Command>C:\Tools\backup.exe</Command><Arguments>/q</Arguments></Exec></Actions>
</Task>
<!-- \Weekly report -->
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    <CalendarTrigger>
      <StartBoundary>2026-01-05T09:30:00</StartBoundary>
      <ScheduleByWeek><DaysOfWeek><Monday /><Friday /></DaysOfWeek><WeeksInterval>1</WeeksInterval></ScheduleByWeek>
      <Repetition><Interval>PT15M</Interval></Repetition>
    </CalendarTrigger>
  </Triggers>
  <Settings><Enabled>false</Enabled></Settings>
  <Actions><Exec><Command>report.cmd</Command></Exec></Actions>
</Task>
</Tasks>`
	tasks, err := schedParseTasksXML([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("parsed %d tasks, want 2: %+v", len(tasks), tasks)
	}
	if tasks[0].Name != `\Backup` || tasks[0].Schedule != "Daily at 03:00 · At logon" || tasks[0].Command != `C:\Tools\backup.exe /q` || !tasks[0].Enabled {
		t.Errorf("task 1 = %+v", tasks[0])
	}
	if tasks[1].Name != `\Weekly report` || tasks[1].Enabled || tasks[1].Schedule != "Mon, Fri at 09:30, repeats every 15 minutes" {
		t.Errorf("task 2 = %+v", tasks[1])
	}

	// The XML wins for the schedule and the enabled state; the CSV keeps the
	// status and the run times.
	rows := []schedTaskRow{{Name: `\Weekly report`, Status: "Ready", Next: "11/9/2026 9:30:00 AM", Schedules: []string{"Weekly"}}}
	items := schedTaskItems(rows, tasks)
	if len(items) != 2 {
		t.Fatalf("merged items = %+v", items)
	}
	if items[0].Name != "Weekly report" || items[0].Enabled || items[0].Schedule != tasks[1].Schedule || items[0].Next != "11/9/2026 9:30:00 AM" {
		t.Errorf("merged item = %+v", items[0])
	}
}

func TestSchedDecodeText(t *testing.T) {
	utf16 := []byte{0xFF, 0xFE, 'O', 0, 'K', 0, 0xE9, 0}
	if got := schedDecodeText(utf16); got != "OKé" {
		t.Errorf("UTF-16 with BOM = %q", got)
	}
	bomless := []byte{'R', 0, 'e', 0, 'a', 0, 'd', 0, 'y', 0}
	if got := schedDecodeText(bomless); got != "Ready" {
		t.Errorf("BOM-less UTF-16 = %q", got)
	}
	if got := schedDecodeText([]byte("bad \xff byte")); got != "bad ? byte" {
		t.Errorf("invalid UTF-8 = %q", got)
	}
}

func TestSchedXMLDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"PT15M":   15 * time.Minute,
		"PT1H":    time.Hour,
		"P1D":     24 * time.Hour,
		"P1DT2H":  26 * time.Hour,
		"PT1H30M": 90 * time.Minute,
		"":        0,
		"bogus":   0,
	}
	for in, want := range cases {
		if got := schedXMLDuration(in); got != want {
			t.Errorf("schedXMLDuration(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSchedDesktopEntry(t *testing.T) {
	text := "[Desktop Entry]\nType=Application\nName=Syncthing\nExec=syncthing -no-browser\nComment=Sync files\n\n[Desktop Action New]\nName=Other\n"
	e := schedParseDesktopEntry(text)
	if e.Name != "Syncthing" || e.Exec != "syncthing -no-browser" || e.Comment != "Sync files" || e.Hidden {
		t.Fatalf("entry = %+v", e)
	}
	off := schedSetDesktopHidden(text, true)
	if !schedParseDesktopEntry(off).Hidden {
		t.Fatalf("Hidden=true was not applied:\n%s", off)
	}
	if !strings.Contains(off, "Hidden=true\n\n[Desktop Action New]") && !strings.Contains(off, "Hidden=true\n[Desktop Action New]") {
		t.Errorf("Hidden landed outside the entry group:\n%s", off)
	}
	on := schedSetDesktopHidden(off, false)
	if schedParseDesktopEntry(on).Hidden {
		t.Fatalf("Hidden=false was not applied:\n%s", on)
	}
	if strings.Count(on, "Hidden=") != 1 {
		t.Errorf("switching back added a second key:\n%s", on)
	}
	gnome := schedParseDesktopEntry("[Desktop Entry]\nName=X\nX-GNOME-Autostart-enabled=false\n")
	if !gnome.Hidden {
		t.Error("X-GNOME-Autostart-enabled=false should read as disabled")
	}
}

func TestSchedParseTimers(t *testing.T) {
	text := "Mon 2026-11-09 00:00:00 UTC 13h left Sun 2026-11-08 00:00:01 UTC 10h ago logrotate.timer logrotate.service\n" +
		"n/a n/a n/a n/a fstrim.timer fstrim.service\n"
	items := schedParseTimers(text)
	if len(items) != 2 {
		t.Fatalf("timers = %+v", items)
	}
	if items[0].Name != "logrotate" || items[0].Command != "logrotate.service" || items[0].Next != "Mon 2026-11-09 00:00:00 UTC" || !items[0].Enabled {
		t.Errorf("timer 1 = %+v", items[0])
	}
	if items[1].Name != "fstrim" || items[1].Enabled || items[1].Next != "" {
		t.Errorf("timer 2 = %+v", items[1])
	}
}

func TestSchedBuildRowsHidesSystemTasks(t *testing.T) {
	items := []schedItem{
		{Group: "Task Scheduler", Name: "Backup"},
		{Group: "Task Scheduler", Name: "Updater"},
		{Group: "Windows system tasks", Name: "Defrag", System: true},
	}
	rows, itemRows, hidden := schedBuildRows(items, false)
	if hidden != 1 || len(itemRows) != 2 {
		t.Fatalf("hidden=%d items=%d, want 1 and 2", hidden, len(itemRows))
	}
	if rows[0].header != "Task Scheduler · 2" || rows[len(rows)-1].note == "" {
		t.Errorf("rows = %+v", rows)
	}
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.header+r.note), "cron") {
			t.Errorf("a Windows list mentions cron: %+v", r)
		}
	}
	rows, itemRows, hidden = schedBuildRows(items, true)
	if hidden != 0 || len(itemRows) != 3 || len(rows) != 5 {
		t.Fatalf("with system tasks: rows=%d items=%d hidden=%d", len(rows), len(itemRows), hidden)
	}
}

func TestSchedSortItems(t *testing.T) {
	now := time.Now()
	items := []schedItem{
		{Group: "A", Name: "later", Enabled: true, nextAt: now.Add(2 * time.Hour)},
		{Group: "B", Name: "other group", Enabled: true},
		{Group: "A", Name: "off", Enabled: false},
		{Group: "A", Name: "sooner", Enabled: true, nextAt: now.Add(time.Hour)},
	}
	schedSortItems(items)
	var names []string
	for _, it := range items {
		names = append(names, it.Name)
	}
	if got := strings.Join(names, ","); got != "sooner,later,off,other group" {
		t.Errorf("sorted = %s", got)
	}
}

func TestSchedDetailRowsSkipEmpty(t *testing.T) {
	rows := schedDetailRows(schedItem{Schedule: "Daily at 03:00", Command: "backup", Source: "/etc/crontab"})
	if len(rows) != 3 || rows[0].Label != "Schedule" || rows[2].Label != "Found in" {
		t.Errorf("detail rows = %+v", rows)
	}
}

package main

// tasks_store.go — the Tasks app's lists and tasks, and the one place they
// change.
//
// Everything lives in one small file, tasks.json in JukaHub's data folder
// (osAppsDataPath), written the crash-safe way the notes and the alarms are
// (AtomicWrite, read back with readJSONWithBackup). A change is saved a couple
// of seconds after it was made, so ticking off a shopping list in the aisle
// does not fsync the SD card once per item, and at once when the app is left
// or JukaHub quits (tasksLeave, tasksFlush).
//
// The phone companion changes tasks from its own goroutines while the main
// loop draws them, so every field below is behind tasksMu, and the screens
// draw from copies (tasksSnapshot) that they refresh when tasksRev moves. The
// alarm list is main-thread state: a change that needs an alarm set or removed
// is queued here and carried out by tasksRemindTick (tasks_remind.go).
//
// Text from anywhere — the on-screen keyboard, a physical keyboard, the phone —
// goes through tasksCleanText: one line, no control characters, at most
// tasksTextMax characters. The file holds at most tasksMaxLists lists and
// tasksMaxItems tasks, so neither a stuck script on the phone nor a damaged
// file can grow it without end.

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	tasksFileName = "tasks.json"
	// tasksFileVersion is written into the file. A file without the field
	// (version 0) is read as version 1: nothing older was ever written.
	tasksFileVersion = 1
	tasksMaxLists    = 50
	tasksMaxItems    = 2000
	tasksTextMax     = 300
	tasksListNameMax = 40
	// tasksSaveDelay is how long after the last change the file is written,
	// and tasksSaveMaxDelay the longest a run of changes may hold it back.
	tasksSaveDelay    = 2 * time.Second
	tasksSaveMaxDelay = 15 * time.Second
	// tasksSaveRetry is the wait before a save that failed is tried again.
	tasksSaveRetry = 30 * time.Second
	// tasksCorruptKeep is how many unreadable copies of tasks.json are kept.
	tasksCorruptKeep = 3
)

// The fixed sentences a change can fail with. The device shows them as toasts
// and the phone companion sends them back as they are.
var (
	errTasksEmpty     = errors.New("Type the task first")
	errTasksFull      = errors.New("Tasks is full: delete some done tasks first")
	errTasksListsFull = errors.New("Tasks keeps at most 50 lists")
	errTasksNoList    = errors.New("That list is not on the handheld any more")
	errTasksNoItem    = errors.New("That task is not on the handheld any more")
	errTasksLastList  = errors.New("Tasks needs at least one list")
	errTasksListName  = errors.New("Name the list first")
	errTasksListTaken = errors.New("There is already a list with that name")
	errTasksNoTime    = errors.New("Give the task a time first")
	errTasksNoDate    = errors.New("Give the task a date first")
)

// taskList is one list.
type taskList struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// taskItem is one task. Due is local wall time ("2006-01-02" or
// "2006-01-02T15:04", tasks_due.go). Reminded is the Due value the reminder
// was shown for — or skipped for, because that moment had already gone when
// the due was set — so a restart does not repeat it and a new due reminds
// again. AlarmID links the alarm "Ring as alarm" made (0: none).
type taskItem struct {
	ID       int    `json:"id"`
	List     int    `json:"list"`
	Text     string `json:"text"`
	Done     bool   `json:"done,omitempty"`
	DoneAt   int64  `json:"done_at,omitempty"`
	Due      string `json:"due,omitempty"`
	Remind   bool   `json:"remind,omitempty"`
	Alarm    bool   `json:"alarm,omitempty"`
	AlarmID  int    `json:"alarm_id,omitempty"`
	Repeat   string `json:"repeat,omitempty"`
	Created  int64  `json:"created,omitempty"`
	Reminded string `json:"reminded,omitempty"`
}

// tasksDoc is tasks.json. Lists and tasks share one id counter: NextID is the
// next id handed out.
type tasksDoc struct {
	Version int        `json:"version"`
	NextID  int        `json:"next_id"`
	Lists   []taskList `json:"lists"`
	Items   []taskItem `json:"items"`
}

var (
	// tasksMu guards tasksState.
	tasksMu    sync.Mutex
	tasksState struct {
		loaded     bool
		doc        tasksDoc
		dirty      bool
		dirtyAt    time.Time // the last change not yet written
		dirtySince time.Time // the first one
		retryAt    time.Time // a failed save waits until then
		saveSeq    uint64
		// noSave keeps an unreadable tasks.json that could not be moved
		// aside from being overwritten this session (tasksEnsureLoadedLocked).
		noSave bool
		// linksChecked is set once the alarm links were checked against the
		// alarm list after a read (tasksAlarmLinksCheckLocked).
		linksChecked bool
		// alarmSync holds the tasks whose alarm must be brought in line,
		// alarmDrop the alarms of tasks that are gone (tasksAlarmApply).
		alarmSync []int
		alarmDrop []int
		// phoneAdded is the last task the phone added, for the device's toast
		// (tasksRemindTick); phoneAddedN counts them since it was shown.
		phoneAdded  string
		phoneAddedN int
	}
	// tasksRev counts changes. Views and caches rebuild when it moves; it is
	// read without the lock.
	tasksRev atomic.Uint64
	// tasksWriter serialises the file writes and drops a snapshot older than
	// one already written, since saves run on their own goroutines. pending
	// counts those goroutines, so the save on exit can wait for them (Add and
	// Wait are both on the main thread).
	tasksWriter struct {
		mu      sync.Mutex
		seq     uint64
		pending sync.WaitGroup
	}
)

// tasksPath is tasks.json in JukaHub's data folder.
func tasksPath() string { return osAppsDataPath(tasksFileName) }

// tasksEnsureLoadedLocked reads the file the first time anything asks for the
// tasks: the reminder tick, the Tools hub, the phone, the app. Caller holds
// tasksMu.
func tasksEnsureLoadedLocked() {
	if tasksState.loaded {
		return
	}
	tasksState.loaded = true
	tasksState.noSave, tasksState.linksChecked = false, false
	var doc tasksDoc
	path := tasksPath()
	recovered, err := readJSONWithBackup(path, &doc)
	if err != nil {
		// Absent is the first run; unreadable has been logged. Either way the
		// default lists start, and the file is only written on a change.
		doc = tasksDoc{}
		if !errors.Is(err, os.ErrNotExist) {
			// An unreadable file is moved aside first, so the first save
			// cannot replace the user's lists with the empty defaults; one
			// that cannot even be moved is not written over this session.
			if mvErr := os.Rename(path, corruptAsidePath(path)); mvErr != nil {
				tasksState.noSave = true
				Log().Warn("tasks file could not be moved aside; changes are not saved this session", "err", mvErr)
			} else {
				pruneCorruptCopies(path, tasksCorruptKeep)
			}
		}
	}
	tasksNormalize(&doc)
	tasksState.doc = doc
	if recovered {
		// The backup answered: make it the live file again.
		tasksChangedLocked()
	}
	// A task that links an alarm it should no longer have (finished, or its
	// alarm switched off, while the alarm list was not saved) gets it removed.
	// One that should ring keeps whatever the alarm list says: the user may
	// have deleted that alarm in Alarms on purpose.
	for _, it := range tasksState.doc.Items {
		if it.AlarmID != 0 && (!it.Alarm || it.Done) {
			tasksState.alarmSync = append(tasksState.alarmSync, it.ID)
		}
	}
	tasksRev.Add(1)
}

// tasksNormalize makes a read file safe to use: clean names and texts, unique
// positive ids, every task on a list that exists, consistent due fields, the
// default lists when there are none, and the limits. Pure.
func tasksNormalize(doc *tasksDoc) {
	doc.Version = tasksFileVersion

	lists := make([]taskList, 0, min(len(doc.Lists), tasksMaxLists))
	for _, l := range doc.Lists {
		if len(lists) >= tasksMaxLists {
			break
		}
		l.Name = tasksCleanText(l.Name, tasksListNameMax)
		if l.Name == "" {
			l.Name = "List"
		}
		lists = append(lists, l)
	}
	items := make([]taskItem, 0, len(doc.Items))
	for _, it := range doc.Items {
		it.Text = tasksCleanText(it.Text, tasksTextMax)
		if it.Text == "" {
			continue
		}
		items = append(items, it)
	}

	// Ids: the first holder of a positive id keeps it, lists before tasks;
	// every other one gets a fresh id above all of them.
	seen := make(map[int]bool, len(lists)+len(items))
	top := 0
	claim := func(id int) bool {
		if id <= 0 || seen[id] {
			return false
		}
		seen[id] = true
		top = max(top, id)
		return true
	}
	for i := range lists {
		if !claim(lists[i].ID) {
			lists[i].ID = 0
		}
	}
	for i := range items {
		if !claim(items[i].ID) {
			items[i].ID = 0
		}
	}
	next := max(doc.NextID, top+1, 1)
	for i := range lists {
		if lists[i].ID == 0 {
			lists[i].ID = next
			next++
		}
	}
	if len(lists) == 0 {
		lists = append(lists, taskList{ID: next, Name: "Inbox"}, taskList{ID: next + 1, Name: "Shopping"})
		next += 2
	}
	known := make(map[int]bool, len(lists))
	for _, l := range lists {
		known[l.ID] = true
	}
	for i := range items {
		it := &items[i]
		if it.ID == 0 {
			it.ID = next
			next++
		}
		if !known[it.List] {
			it.List = lists[0].ID
		}
		tasksFixItem(it)
	}
	if len(items) > tasksMaxItems {
		items = tasksTrimItems(items, tasksMaxItems)
	}
	doc.Lists, doc.Items, doc.NextID = lists, items, next
}

// tasksFixItem keeps a task's fields consistent with each other: a due that
// parses or none, no reminder, alarm or repeat without a due, no alarm
// without a time, a known repeat. Pure.
func tasksFixItem(it *taskItem) {
	if d, ok := tasksParseDue(it.Due); ok {
		it.Due = d.String()
	} else {
		it.Due = ""
	}
	if tasksRepeatStep(it.Repeat) == 0 {
		it.Repeat = tasksRepeatNone
	}
	switch {
	case it.Due == "":
		it.Remind, it.Alarm, it.Repeat, it.Reminded = false, false, tasksRepeatNone, ""
	case !strings.Contains(it.Due, "T"):
		it.Alarm = false
	}
	if it.AlarmID < 0 {
		it.AlarmID = 0
	}
	if !it.Done && it.Repeat == tasksRepeatNone {
		it.DoneAt = 0
	}
}

// tasksTrimItems cuts items to limit, dropping the tasks finished longest ago
// first and open ones only if that is not enough. Pure; keeps the order.
func tasksTrimItems(items []taskItem, limit int) []taskItem {
	if len(items) <= limit {
		return items
	}
	order := make([]int, 0, len(items))
	for i := range items {
		if items[i].Done {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return items[order[a]].DoneAt < items[order[b]].DoneAt })
	drop := make(map[int]bool, len(items)-limit)
	for _, i := range order {
		if len(items)-len(drop) <= limit {
			break
		}
		drop[i] = true
	}
	out := make([]taskItem, 0, limit)
	for i, it := range items {
		if !drop[i] && len(out) < limit {
			out = append(out, it)
		}
	}
	return out
}

// tasksCleanText is text as a task or a list name may hold it: valid UTF-8 on
// one line, runs of white space as one space, no control or bidirectional
// override characters (which could make a row read differently from what it
// holds), trimmed, and at most limit characters. Pure.
func tasksCleanText(s string, limit int) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	n, space := 0, false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = true
			continue
		case unicode.IsControl(r), r == '\ufeff',
			r >= '\u202a' && r <= '\u202e', r >= '\u2066' && r <= '\u2069':
			continue
		}
		if n >= limit {
			break
		}
		if space && n > 0 {
			if n+1 >= limit {
				break
			}
			b.WriteByte(' ')
			n++
		}
		space = false
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// tasksChangedLocked records a change: the file is due a save, and views
// rebuild. Caller holds tasksMu.
func tasksChangedLocked() {
	now := time.Now()
	if !tasksState.dirty {
		tasksState.dirtySince = now
	}
	tasksState.dirty = true
	tasksState.dirtyAt = now
	tasksRev.Add(1)
}

// tasksSaveDue reports whether a pending change has waited long enough.
func tasksSaveDue(now time.Time) bool {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	if !tasksState.dirty || tasksState.noSave {
		return false
	}
	// After a failed save, wait; a retry time further off than that is a
	// clock that jumped back, and does not hold the save up.
	if wait := tasksState.retryAt.Sub(now); wait > 0 && wait <= tasksSaveRetry {
		return false
	}
	return now.Sub(tasksState.dirtyAt) >= tasksSaveDelay || now.Sub(tasksState.dirtySince) >= tasksSaveMaxDelay ||
		now.Before(tasksState.dirtySince)
}

// tasksSaveNow writes the tasks if they changed: on a goroutine of its own
// from the tick and from leaving the app, because the main loop must not wait
// on an SD card's fsync; and waiting when JukaHub is about to exit.
func tasksSaveNow(wait bool) {
	if wait {
		// A save already on its way (leaving the app just before quitting)
		// lands before JukaHub exits.
		defer tasksWriter.pending.Wait()
	}
	tasksMu.Lock()
	if !tasksState.loaded || !tasksState.dirty || tasksState.noSave {
		tasksMu.Unlock()
		return
	}
	data, err := json.MarshalIndent(&tasksState.doc, "", "  ")
	tasksState.dirty = false
	tasksState.saveSeq++
	seq := tasksState.saveSeq
	tasksMu.Unlock()
	if err != nil {
		return
	}
	write := func() {
		tasksWriter.mu.Lock()
		defer tasksWriter.mu.Unlock()
		if seq <= tasksWriter.seq {
			return
		}
		tasksWriter.seq = seq
		if err := AtomicWrite(tasksPath(), data, 0644); err != nil {
			Log().Warn("tasks could not be saved", "err", err)
			// Try again later rather than every tick: a full or read-only
			// card would otherwise log every two seconds.
			tasksMu.Lock()
			now := time.Now()
			if !tasksState.dirty {
				tasksState.dirty, tasksState.dirtySince, tasksState.dirtyAt = true, now, now
			}
			tasksState.retryAt = now.Add(tasksSaveRetry)
			tasksMu.Unlock()
		}
	}
	if wait {
		write()
		return
	}
	tasksWriter.pending.Add(1)
	go func() {
		defer tasksWriter.pending.Done()
		write()
	}()
}

// tasksReload forgets the loaded tasks so the next look reads tasks.json
// again: a restored backup replaced the file, and saving the old copy over it
// would undo the restore. Changes not yet saved are dropped for the same
// reason, and so is a save already queued with them.
func tasksReload() {
	tasksMu.Lock()
	seq := tasksState.saveSeq
	tasksState.loaded, tasksState.dirty = false, false
	tasksState.alarmSync, tasksState.alarmDrop = nil, nil
	tasksMu.Unlock()
	tasksWriter.mu.Lock()
	tasksWriter.seq = max(tasksWriter.seq, seq)
	tasksWriter.mu.Unlock()
	tasksRev.Add(1)
}

// tasksNewIDLocked hands out the next id. Caller holds tasksMu.
func tasksNewIDLocked() int {
	id := tasksState.doc.NextID
	tasksState.doc.NextID++
	return id
}

// tasksListIndexLocked is the index of list id, or -1.
func tasksListIndexLocked(id int) int {
	for i, l := range tasksState.doc.Lists {
		if l.ID == id {
			return i
		}
	}
	return -1
}

// tasksItemIndexLocked is the index of task id, or -1.
func tasksItemIndexLocked(id int) int {
	for i, it := range tasksState.doc.Items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

// tasksSnapshot copies the lists and the tasks, with the change count they
// belong to. Screens draw from the copy, so drawing never holds the lock and a
// phone's change in the middle of a frame cannot tear a row.
func tasksSnapshot() ([]taskList, []taskItem, uint64) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	tasksEnsureLoadedLocked()
	lists := append([]taskList(nil), tasksState.doc.Lists...)
	items := append([]taskItem(nil), tasksState.doc.Items...)
	return lists, items, tasksRev.Load()
}

// tasksSetDueFields gives a task a new due (a stored value, or "" for none)
// with what follows from it: no reminder, alarm or repeat without a due, no
// alarm without a time, the reminder on by default when a time is first set,
// and the reminder armed again — or marked as passed when its moment is
// already behind now, so setting a task for earlier today does not announce
// it at once. Caller holds tasksMu (or owns it).
func tasksSetDueFields(it *taskItem, due string, now time.Time) {
	old := it.Due
	d, ok := tasksParseDue(due)
	if ok {
		it.Due = d.String()
	} else {
		it.Due = ""
	}
	if it.Due == old {
		return
	}
	switch {
	case it.Due == "":
		it.Remind, it.Alarm, it.Repeat = false, false, tasksRepeatNone
	case !d.timed:
		it.Alarm = false
	case !strings.Contains(old, "T"):
		it.Remind = true
	}
	it.Reminded = ""
	if it.Due != "" {
		if from, _ := d.remindWindow(); !now.Before(from) {
			it.Reminded = it.Due
		}
	}
}

// tasksAddItem adds a task to list (0: the first list) with an optional due,
// and returns its id. Any goroutine.
func tasksAddItem(list int, text, due string) (int, error) {
	text = tasksCleanText(text, tasksTextMax)
	if text == "" {
		return 0, errTasksEmpty
	}
	tasksMu.Lock()
	defer tasksMu.Unlock()
	tasksEnsureLoadedLocked()
	doc := &tasksState.doc
	if len(doc.Items) >= tasksMaxItems {
		return 0, errTasksFull
	}
	if list == 0 {
		list = doc.Lists[0].ID
	} else if tasksListIndexLocked(list) < 0 {
		return 0, errTasksNoList
	}
	now := time.Now()
	it := taskItem{ID: tasksNewIDLocked(), List: list, Text: text, Created: now.Unix()}
	tasksSetDueFields(&it, due, now)
	doc.Items = append(doc.Items, it)
	tasksChangedLocked()
	return it.ID, nil
}

// tasksEditItem changes task id with fn under the lock, keeps its fields
// consistent, and queues its alarm when anything the alarm depends on moved.
// Any goroutine.
func tasksEditItem(id int, fn func(it *taskItem, now time.Time) error) error {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	tasksEnsureLoadedLocked()
	i := tasksItemIndexLocked(id)
	if i < 0 {
		return errTasksNoItem
	}
	it := &tasksState.doc.Items[i]
	before := *it
	if err := fn(it, time.Now()); err != nil {
		*it = before
		return err
	}
	tasksFixItem(it)
	if *it == before {
		return nil
	}
	if (it.Alarm || before.Alarm || it.AlarmID != 0) &&
		(it.Due != before.Due || it.Alarm != before.Alarm || it.Done != before.Done || it.Text != before.Text) {
		tasksState.alarmSync = append(tasksState.alarmSync, id)
	}
	tasksChangedLocked()
	return nil
}

// tasksSetText renames a task.
func tasksSetText(id int, text string) error {
	text = tasksCleanText(text, tasksTextMax)
	if text == "" {
		return errTasksEmpty
	}
	return tasksEditItem(id, func(it *taskItem, _ time.Time) error {
		it.Text = text
		return nil
	})
}

// tasksSetDue gives a task a new due ("" for none).
func tasksSetDue(id int, due string) error {
	return tasksEditItem(id, func(it *taskItem, now time.Time) error {
		tasksSetDueFields(it, due, now)
		return nil
	})
}

// tasksSetRemind switches a task's reminder. Switched on for a moment already
// gone it stays quiet until the due changes.
func tasksSetRemind(id int, on bool) error {
	return tasksEditItem(id, func(it *taskItem, now time.Time) error {
		if it.Due == "" {
			return errTasksNoDate
		}
		it.Remind = on
		if d, ok := tasksParseDue(it.Due); ok && on {
			if from, _ := d.remindWindow(); !now.Before(from) {
				it.Reminded = it.Due
			}
		}
		return nil
	})
}

// tasksSetAlarm switches "Ring as alarm", which needs a time.
func tasksSetAlarm(id int, on bool) error {
	return tasksEditItem(id, func(it *taskItem, _ time.Time) error {
		if on && !strings.Contains(it.Due, "T") {
			return errTasksNoTime
		}
		it.Alarm = on
		return nil
	})
}

// tasksSetRepeat sets a task's repeat. A repeat needs a day to repeat from, so
// a task without one is due today.
func tasksSetRepeat(id int, repeat string) error {
	if tasksRepeatStep(repeat) == 0 {
		repeat = tasksRepeatNone
	}
	return tasksEditItem(id, func(it *taskItem, now time.Time) error {
		if repeat != tasksRepeatNone && it.Due == "" {
			tasksSetDueFields(it, tasksDueOn(now).String(), now)
		}
		it.Repeat = repeat
		return nil
	})
}

// tasksMoveItem moves a task to another list.
func tasksMoveItem(id, list int) error {
	return tasksEditItem(id, func(it *taskItem, _ time.Time) error {
		if tasksListIndexLocked(list) < 0 {
			return errTasksNoList
		}
		it.List = list
		return nil
	})
}

// tasksToggle ticks a task off or back on. A repeating task is never done:
// ticking it moves it to its next date (tasksNextDue), which comes back as
// next. Any goroutine.
func tasksToggle(id int) (done bool, next tasksDue, err error) {
	err = tasksEditItem(id, func(it *taskItem, now time.Time) error {
		if !it.Done && it.Repeat != tasksRepeatNone {
			if d, ok := tasksParseDue(it.Due); ok {
				next = tasksNextDue(d, it.Repeat, now)
				tasksSetDueFields(it, next.String(), now)
				it.DoneAt = now.Unix()
				return nil
			}
		}
		it.Done = !it.Done
		it.DoneAt = 0
		if it.Done {
			it.DoneAt = now.Unix()
		}
		done = it.Done
		return nil
	})
	return done, next, err
}

// tasksDeleteItem deletes a task and queues its alarm's removal.
func tasksDeleteItem(id int) error {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	tasksEnsureLoadedLocked()
	i := tasksItemIndexLocked(id)
	if i < 0 {
		return errTasksNoItem
	}
	tasksDropItemLocked(i)
	tasksChangedLocked()
	return nil
}

// tasksDropItemLocked removes the task at index i, queueing its alarm.
func tasksDropItemLocked(i int) {
	items := tasksState.doc.Items
	if aid := items[i].AlarmID; aid != 0 {
		tasksState.alarmDrop = append(tasksState.alarmDrop, aid)
	}
	tasksState.doc.Items = append(items[:i], items[i+1:]...)
}

// tasksListNameTakenLocked reports whether another list (not except) already
// has name, ignoring case.
func tasksListNameTakenLocked(name string, except int) bool {
	for _, l := range tasksState.doc.Lists {
		if l.ID != except && strings.EqualFold(l.Name, name) {
			return true
		}
	}
	return false
}

// tasksAddList adds a list and returns its id. Any goroutine.
func tasksAddList(name string) (int, error) {
	name = tasksCleanText(name, tasksListNameMax)
	if name == "" {
		return 0, errTasksListName
	}
	tasksMu.Lock()
	defer tasksMu.Unlock()
	tasksEnsureLoadedLocked()
	if len(tasksState.doc.Lists) >= tasksMaxLists {
		return 0, errTasksListsFull
	}
	if tasksListNameTakenLocked(name, 0) {
		return 0, errTasksListTaken
	}
	l := taskList{ID: tasksNewIDLocked(), Name: name}
	tasksState.doc.Lists = append(tasksState.doc.Lists, l)
	tasksChangedLocked()
	return l.ID, nil
}

// tasksRenameList renames a list.
func tasksRenameList(id int, name string) error {
	name = tasksCleanText(name, tasksListNameMax)
	if name == "" {
		return errTasksListName
	}
	tasksMu.Lock()
	defer tasksMu.Unlock()
	tasksEnsureLoadedLocked()
	i := tasksListIndexLocked(id)
	if i < 0 {
		return errTasksNoList
	}
	if tasksListNameTakenLocked(name, id) {
		return errTasksListTaken
	}
	if tasksState.doc.Lists[i].Name != name {
		tasksState.doc.Lists[i].Name = name
		tasksChangedLocked()
	}
	return nil
}

// tasksDeleteList deletes a list with its tasks (the sheet says how many) and
// reports how many tasks went. The last list stays.
func tasksDeleteList(id int) (int, error) {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	tasksEnsureLoadedLocked()
	i := tasksListIndexLocked(id)
	if i < 0 {
		return 0, errTasksNoList
	}
	if len(tasksState.doc.Lists) <= 1 {
		return 0, errTasksLastList
	}
	n := 0
	for j := len(tasksState.doc.Items) - 1; j >= 0; j-- {
		if tasksState.doc.Items[j].List == id {
			tasksDropItemLocked(j)
			n++
		}
	}
	lists := tasksState.doc.Lists
	tasksState.doc.Lists = append(lists[:i], lists[i+1:]...)
	tasksChangedLocked()
	return n, nil
}

// tasksClearDone deletes the finished tasks of a list and reports how many.
func tasksClearDone(list int) int {
	tasksMu.Lock()
	defer tasksMu.Unlock()
	tasksEnsureLoadedLocked()
	n := 0
	for j := len(tasksState.doc.Items) - 1; j >= 0; j-- {
		if it := tasksState.doc.Items[j]; it.Done && it.List == list {
			tasksDropItemLocked(j)
			n++
		}
	}
	if n > 0 {
		tasksChangedLocked()
	}
	return n
}

// tasksNotePhoneAdd records a task the phone added, for the device's toast.
func tasksNotePhoneAdd(text string) {
	tasksMu.Lock()
	tasksState.phoneAdded = text
	tasksState.phoneAddedN++
	tasksMu.Unlock()
}

// ── Order and summary ─────────────────────────────────────────────────────

// tasksNoDueKey sorts a task without a due after every task with one.
const tasksNoDueKey = int64(1) << 62

// tasksSortItems puts items in the order a list shows them: open tasks by due
// (earliest first, those without one after), ties in the order they were
// added (ids only grow, and unlike a timestamp they do not depend on a clock
// the handheld may not have set yet); finished tasks after them, the most
// recently finished first. In place.
func tasksSortItems(items []taskItem) {
	keys := make(map[int]int64, len(items))
	for _, it := range items {
		k := tasksNoDueKey
		if d, ok := tasksParseDue(it.Due); ok {
			k = d.sortKey()
		}
		keys[it.ID] = k
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Done != b.Done {
			return !a.Done
		}
		if a.Done {
			if a.DoneAt != b.DoneAt {
				return a.DoneAt > b.DoneAt
			}
			return a.ID > b.ID
		}
		if ka, kb := keys[a.ID], keys[b.ID]; ka != kb {
			return ka < kb
		}
		return a.ID < b.ID
	})
}

// tasksCounts is what is open: in all, due by today (overdue included) and
// overdue.
type tasksCounts struct {
	open, due, overdue int
}

// tasksCount counts items that are open at now; list 0 counts every list.
func tasksCount(items []taskItem, list int, now time.Time) tasksCounts {
	var c tasksCounts
	for _, it := range items {
		if it.Done || (list != 0 && it.List != list) {
			continue
		}
		c.open++
		if d, ok := tasksParseDue(it.Due); ok && d.byDay(now) {
			c.due++
			if d.overdue(now) {
				c.overdue++
			}
		}
	}
	return c
}

// tasksHubCache keeps the Tools hub tile's line until a task changes or the
// minute turns (a task can become overdue). Main thread only.
var tasksHubCache struct {
	ok     bool
	rev    uint64
	minute int64
	text   string
	hot    bool
}

// tasksHubValue is the Tools hub tile's line: "2 due today", "1 overdue ·
// 2 today", "Nothing due"; true when something is due. Cached; the first call
// reads tasks.json. Main thread.
func tasksHubValue() (string, bool) {
	now := time.Now()
	minute := now.Unix() / 60
	c := &tasksHubCache
	if c.ok && c.rev == tasksRev.Load() && c.minute == minute {
		return c.text, c.hot
	}
	tasksMu.Lock()
	tasksEnsureLoadedLocked()
	n := tasksCount(tasksState.doc.Items, 0, now)
	rev := tasksRev.Load()
	tasksMu.Unlock()
	text := "Nothing due"
	switch {
	case n.overdue > 0 && n.due > n.overdue:
		text = strconv.Itoa(n.overdue) + " overdue · " + strconv.Itoa(n.due-n.overdue) + " today"
	case n.overdue > 0:
		text = strconv.Itoa(n.overdue) + " overdue"
	case n.due > 0:
		text = strconv.Itoa(n.due) + " due today"
	}
	c.ok, c.rev, c.minute, c.text, c.hot = true, rev, minute, text, n.due > 0
	return c.text, c.hot
}

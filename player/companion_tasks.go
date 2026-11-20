package main

// companion_tasks.go — the Phone Companion's Tasks card: a task typed on the
// phone, with its list and, if it has one, its day and time, is on the
// handheld at once; tasks can be ticked off and deleted from the phone too.
// Typing a shopping list on a phone keyboard beats the on-screen one.
//
// GET /api/tasks answers the lists with their open counts, every open task,
// the most recently finished ones, and the handheld's own date and time: the
// phone's clock may differ, and "overdue" is the handheld's word. Each task
// carries its due already written the way the handheld writes dates.
// POST /api/tasks takes one action as JSON — add, toggle, delete, addList —
// and answers with the same state and a sentence for the page.
//
// Both routes are behind the PIN (s.guard). A body is at most 64 KB; a task is
// at most tasksTextMax characters and a list name tasksListNameMax, cleaned
// like every other text the app takes (tasksCleanText); ids must name a task
// or a list on the handheld; and a failure is one of a few fixed sentences.
// The store has its own lock, so the handlers change it directly and the
// device's screen shows the change on its next frame; what needs the main
// thread (an alarm to move, a toast) is queued for tasksRemindTick.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

// phoneCardTasks opens the companion page at the Tasks card ("#tasks").
const phoneCardTasks phoneCardKind = "tasks"

const (
	// tasksPhoneBodyMax bounds a POST body.
	tasksPhoneBodyMax = 64 << 10
	// tasksPhoneDoneMax is how many finished tasks the page gets back, most
	// recent first, so one ticked by mistake can be unticked.
	tasksPhoneDoneMax = 30
)

// The phone's own failures, besides the store's (tasks_store.go).
var (
	errTasksPhoneAction = errors.New("Unknown action")
	errTasksPhoneLong   = errors.New("That task is too long: 300 characters at most")
	errTasksPhoneName   = errors.New("That list name is too long: 40 characters at most")
	errTasksPhoneDate   = errors.New("That date or time is not valid")
)

// tasksPhoneList is a list as the page gets it.
type tasksPhoneList struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Open int    `json:"open"`
}

// tasksPhoneItem is a task as the page gets it. Label is its due as the
// handheld writes it ("Tomorrow 9:30").
type tasksPhoneItem struct {
	ID      int    `json:"id"`
	List    int    `json:"list"`
	Text    string `json:"text"`
	Due     string `json:"due,omitempty"`
	Label   string `json:"label,omitempty"`
	Done    bool   `json:"done,omitempty"`
	Overdue bool   `json:"overdue,omitempty"`
	Repeat  string `json:"repeat,omitempty"`
	Remind  bool   `json:"remind,omitempty"`
	Alarm   bool   `json:"alarm,omitempty"`
}

// tasksPhoneState is the answer to every request.
type tasksPhoneState struct {
	Lists    []tasksPhoneList `json:"lists"`
	Items    []tasksPhoneItem `json:"items"`
	Today    string           `json:"today"`
	MaxText  int              `json:"maxText"`
	MaxLists int              `json:"maxLists"`
	Message  string           `json:"message,omitempty"`
	Added    int              `json:"added,omitempty"`
}

// tasksPhoneRequest is a POST body. Date is "2006-01-02" and Time "15:04"
// (the page's date and time fields), both optional.
type tasksPhoneRequest struct {
	Action string `json:"action"`
	ID     int    `json:"id"`
	List   int    `json:"list"`
	Text   string `json:"text"`
	Date   string `json:"date"`
	Time   string `json:"time"`
	Name   string `json:"name"`
}

// registerTaskRoutes adds the Tasks card's endpoint, behind the PIN.
func (s *companionServer) registerTaskRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/tasks", s.guard(s.handleTasks))
}

// handleTasks answers GET with the state and POST with one action.
func (s *companionServer) handleTasks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Cache-Control", "no-store")
		writeCompanionJSON(w, tasksPhoneSnapshot("", 0))
		return
	case http.MethodPost:
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, tasksPhoneBodyMax)
	var req tasksPhoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	msg, added, err := tasksPhoneDo(req, time.Now())
	if err != nil {
		http.Error(w, err.Error(), tasksPhoneStatus(err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeCompanionJSON(w, tasksPhoneSnapshot(msg, added))
}

// tasksPhoneStatus is the HTTP status for one of the fixed failures.
func tasksPhoneStatus(err error) int {
	switch {
	case errors.Is(err, errTasksNoItem), errors.Is(err, errTasksNoList):
		return http.StatusNotFound
	case errors.Is(err, errTasksFull), errors.Is(err, errTasksListsFull),
		errors.Is(err, errTasksListTaken), errors.Is(err, errTasksLastList):
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

// tasksPhoneDo carries out one action and returns the page's sentence and,
// for an add, the new task's id.
func tasksPhoneDo(req tasksPhoneRequest, now time.Time) (string, int, error) {
	switch req.Action {
	case "add":
		if utf8.RuneCountInString(strings.TrimSpace(req.Text)) > tasksTextMax {
			return "", 0, errTasksPhoneLong
		}
		if req.List < 0 {
			return "", 0, errTasksNoList
		}
		due, ok := tasksPhoneDue(req.Date, req.Time, now)
		if !ok {
			return "", 0, errTasksPhoneDate
		}
		id, err := tasksAddItem(req.List, req.Text, due)
		if err != nil {
			return "", 0, err
		}
		tasksNotePhoneAdd(tasksCleanText(req.Text, tasksTextMax))
		return "Added on the handheld", id, nil
	case "toggle":
		if req.ID <= 0 {
			return "", 0, errTasksNoItem
		}
		done, next, err := tasksToggle(req.ID)
		switch {
		case err != nil:
			return "", 0, err
		case next.set():
			return "Done — it comes back " + tasksDueLabel(nil, next, now), 0, nil
		case done:
			return "Done", 0, nil
		}
		return "Back on the list", 0, nil
	case "delete":
		if req.ID <= 0 {
			return "", 0, errTasksNoItem
		}
		if err := tasksDeleteItem(req.ID); err != nil {
			return "", 0, err
		}
		return "Deleted", 0, nil
	case "addList":
		if utf8.RuneCountInString(strings.TrimSpace(req.Name)) > tasksListNameMax {
			return "", 0, errTasksPhoneName
		}
		id, err := tasksAddList(req.Name)
		if err != nil {
			return "", 0, err
		}
		return "List added", id, nil
	}
	return "", 0, errTasksPhoneAction
}

// tasksPhoneDue turns the page's date and time fields into a stored due: none,
// a day, or a day and a time. A time without a day is today's. Some phones
// send seconds with the time ("15:04:05"); they are dropped.
func tasksPhoneDue(date, clock string, now time.Time) (string, bool) {
	date, clock = strings.TrimSpace(date), strings.TrimSpace(clock)
	if date == "" && clock == "" {
		return "", true
	}
	if date == "" {
		date = tasksDueOn(now).String()
	}
	if len(date) != len(tasksDayLayout) {
		return "", false
	}
	value := date
	if clock != "" {
		if len(clock) == len("15:04:05") && clock[5] == ':' {
			clock = clock[:5]
		}
		if len(clock) != len("15:04") {
			return "", false
		}
		value = date + "T" + clock
	}
	d, ok := tasksParseDue(value)
	if !ok {
		return "", false
	}
	return d.String(), true
}

// tasksPhoneSnapshot is the state the page draws: lists with their open
// counts, the open tasks in list order and the latest finished ones.
func tasksPhoneSnapshot(msg string, added int) tasksPhoneState {
	lists, items, _ := tasksSnapshot()
	now := time.Now()
	st := tasksPhoneState{
		Lists:    make([]tasksPhoneList, 0, len(lists)),
		Items:    make([]tasksPhoneItem, 0, len(items)),
		Today:    tasksDueOn(now).String(),
		MaxText:  tasksTextMax,
		MaxLists: tasksMaxLists,
		Message:  msg,
		Added:    added,
	}
	open := make(map[int]int, len(lists))
	for _, it := range items {
		if !it.Done {
			open[it.List]++
		}
	}
	for _, l := range lists {
		st.Lists = append(st.Lists, tasksPhoneList{ID: l.ID, Name: l.Name, Open: open[l.ID]})
	}
	tasksSortItems(items)
	done := 0
	for _, it := range items {
		if it.Done {
			if done >= tasksPhoneDoneMax {
				continue
			}
			done++
		}
		pi := tasksPhoneItem{ID: it.ID, List: it.List, Text: it.Text, Due: it.Due, Done: it.Done,
			Repeat: it.Repeat, Remind: it.Remind, Alarm: it.Alarm}
		if d, ok := tasksParseDue(it.Due); ok {
			pi.Label = tasksDueLabel(nil, d, now)
			pi.Overdue = !it.Done && d.overdue(now)
		}
		st.Items = append(st.Items, pi)
	}
	return st
}

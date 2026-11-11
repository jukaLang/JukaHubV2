package main

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/veandco/go-sdl2/sdl"
	"github.com/veandco/go-sdl2/ttf"
)

// ──────────────────────────────────────────────────────────────────────
// Notification Center
// ──────────────────────────────────────────────────────────────────────

type notifPriority int

const (
	notifInfo notifPriority = iota
	notifSuccess
	notifWarning
	notifError
)

type notification struct {
	Message   string
	Priority  notifPriority
	Timestamp time.Time
	Read      bool
	// Repeat counts how many identical events were folded into this row, so a
	// chatty producer (a retry loop, a volume slider) shows "×3" instead of
	// pushing everything else out of the 50-entry history.
	Repeat int
}

type notifCenter struct {
	history   []notification
	open      bool // dropdown visible
	cursor    int  // focused row in dropdown
	bellRect  sdl.Rect
	dropRect  sdl.Rect
	clearRect sdl.Rect // "Clear All" hit area
	enter     uiEnter  // drop-down + fade entrance
	// visibleRows and rowRects describe the rows the last frame drew; paging
	// and click selection are driven by them instead of a hard-coded row
	// count.
	visibleRows int
	rowRects    []sdl.Rect
	// rowFirst is the history index of rowRects[0]; scroll is where the next
	// frame's window starts, so the list follows the cursor instead of always
	// drawing from the newest row (notifListWindow).
	rowFirst int
	scroll   int
	// seenAdded is notifAdded as of the last frame drawn while open
	// (notifFollowArrivals).
	seenAdded uint64
}

var nc notifCenter

// toastKindPriority maps a toast severity onto the history's own scale.
func toastKindPriority(k ToastKind) notifPriority {
	switch k {
	case ToastKindSuccess:
		return notifSuccess
	case ToastKindWarning:
		return notifWarning
	case ToastKindError:
		return notifError
	default:
		return notifInfo
	}
}

// priorityToastKind is the reverse mapping, used when a caller supplies the
// priority itself (notifPush) and the toast still has to render.
func priorityToastKind(p notifPriority) ToastKind {
	switch p {
	case notifSuccess:
		return ToastKindSuccess
	case notifWarning:
		return ToastKindWarning
	case notifError:
		return ToastKindError
	default:
		return ToastKindInfo
	}
}

// notifRecord files a message in the history without showing a toast. It is
// the single writer for nc.history: ShowToast calls it for every user-facing
// toast, so downloads, update prompts, screenshots and errors all survive the
// toast timeout instead of vanishing after three seconds.
//
// A repeat of the newest unread row is folded into it (Timestamp refreshed,
// Repeat bumped) rather than appended, so a loop cannot flush the history.
func notifRecord(msg string, p notifPriority) {
	notifRecordAs(msg, p, false)
}

// notifRecordToast files a toast the user just watched appear. Confirmations
// (info and success: "Saved", "Copied", "Connected") go into the history
// already read, so they can be re-read but do not raise the bell's count; the
// count is for what still needs a look — warnings and errors — and for the
// events nobody saw happen (a missed alarm, a new release, a milestone), which
// file themselves unread through notifRecord and notifPush. Every toast used
// to count, so a session of ordinary confirmations put "42" on the bell.
func notifRecordToast(msg string, kind ToastKind) {
	p := toastKindPriority(kind)
	notifRecordAs(msg, p, p == notifInfo || p == notifSuccess)
}

// notifRecordAs is notifRecord with the row's read state chosen by the caller.
// A repeat folds into the newest row only when that row has the same read
// state, so a run of confirmations cannot swallow an unread warning.
func notifRecordAs(msg string, p notifPriority, read bool) {
	// Stored as the toast shows it, so the toast can find its row (notifPush
	// hands its text over untrimmed; showToastPrimitive trims the toast's).
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	notifHistoryMu.Lock()
	defer notifHistoryMu.Unlock()
	if len(nc.history) > 0 {
		top := &nc.history[0]
		if top.Message == msg && top.Priority == p && top.Read == read {
			top.Timestamp = time.Now()
			top.Repeat++
			return
		}
	}
	nc.history = append([]notification{{Message: msg, Priority: p, Timestamp: time.Now(), Read: read, Repeat: 1}}, nc.history...)
	notifAdded++
	if len(nc.history) > 50 {
		nc.history = nc.history[:50]
	}
}

// notifBadgeLabel is the count drawn on the bell: exact up to nine, "9+" past
// it. A two-digit number is not more informative at a glance than "9+", and on
// a 32px bell it reads as noise.
func notifBadgeLabel(unread int) string {
	if unread <= 0 {
		return ""
	}
	if unread > 9 {
		return "9+"
	}
	return fmt.Sprintf("%d", unread)
}

// notifPush files a notification and shows its toast. It records directly
// (not through ShowToast) so the message is stored exactly once.
func notifPush(msg string, p notifPriority) {
	notifRecord(msg, p)
	showToastPrimitive(msg, priorityToastKind(p))
}

// notifHistoryMu guards nc.history. Toasts — and with them notifRecord — are
// raised from goroutines (downloads, updates, network results), while the
// render loop reads the history every frame for the bell's unread count; an
// append racing that read could hand it a torn slice header.
var notifHistoryMu sync.Mutex

// notifAdded counts the rows ever prepended to nc.history (a folded repeat is
// not a new row). Guarded by notifHistoryMu. The open centre compares it with
// the value it last saw to keep its highlight on the row being read.
var notifAdded uint64

// notifHistoryState copies the history for a reader on the render thread,
// with notifAdded read under the same lock.
func notifHistoryState() ([]notification, uint64) {
	notifHistoryMu.Lock()
	defer notifHistoryMu.Unlock()
	return append([]notification(nil), nc.history...), notifAdded
}

// notifHistoryLen is the number of rows in the history.
func notifHistoryLen() int {
	notifHistoryMu.Lock()
	defer notifHistoryMu.Unlock()
	return len(nc.history)
}

// notifRowIndex is the newest row whose message is msg, or -1.
func notifRowIndex(msg string) int {
	notifHistoryMu.Lock()
	defer notifHistoryMu.Unlock()
	return notifRowIndexLocked(msg)
}

// notifRowIndexLocked is notifRowIndex for a caller holding notifHistoryMu.
func notifRowIndexLocked(msg string) int {
	msg = strings.TrimSpace(msg)
	for i, n := range nc.history {
		if n.Message == msg {
			return i
		}
	}
	return -1
}

func notifUnreadCount() int {
	notifHistoryMu.Lock()
	defer notifHistoryMu.Unlock()
	n := 0
	for _, notif := range nc.history {
		if !notif.Read {
			n++
		}
	}
	return n
}

const (
	// notifBellHeightInset keeps the bell a step quieter than the labelled
	// header buttons (Back, Menu, the release offer) it sits beside.
	notifBellHeightInset = int32(2)
	// notifBellMinHeight is the floor: below this the drawn silhouette and its
	// unread badge stop reading as a control.
	notifBellMinHeight = int32(24)
	// notifBellIconInset is the padding the glyph gives up inside the chip.
	notifBellIconInset = int32(12)
	// notifBadgeFontSize is the unread count's size in pixels: 13, up from 11,
	// which was under the legibility floor on the 1280 canvas and smaller
	// still on the Brick. The badge grows with it and still clears the bar's
	// top edge (uiIconButtonBadgeRect, components.go).
	notifBadgeFontSize = 13
)

// notifBellSize returns the bell chip's width and height, so the top bar can
// reserve its corner before drawing.
//
// The bell is a header button, so it takes the shared header button height
// rather than one derived from the status font: that produced a 54×40 chip
// with a 32px glyph on the 1280 canvas — taller than the Back pill and nearly
// twice the height of the caption text beside it. It is square because a bell
// is not wider than it is tall, which also stops the corner widening the bar's
// right edge on the narrow handheld canvas.
func notifBellSize(config *Config) (int32, int32) {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return 0, 0
	}
	h := headerPillHeight() - notifBellHeightInset
	if h < notifBellMinHeight {
		h = notifBellMinHeight
	}
	return h, h
}

// notifBadgeFont is the unread count's digit font. The chip's height comes from
// the header button metrics, which do not follow the text-size setting, so the
// digits are opened at a fixed size too: a status-sized badge was taller than
// the pill it hangs on and hung over the bar's top edge.
func notifBadgeFont(config *Config) *ttf.Font {
	if config == nil {
		return nil
	}
	return loadRoleFont(config, "small", notifBadgeFontSize)
}

// drawBellIcon paints a notification bell centred in a size×size box using only
// SDL primitives. A text glyph such as "(*)" was hard to read on the handheld's
// small screen and looked like an error marker, not a notification control.
func drawBellIcon(renderer *sdl.Renderer, cx, cy, size int32, col sdl.Color) {
	if renderer == nil || size < 10 {
		return
	}
	_ = renderer.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	setDrawColor(renderer, col)

	f := float64(size)
	bodyTop := float64(cy) - f*0.36
	bodyH := f * 0.58
	bodyBottom := bodyTop + bodyH
	halfMax := f * 0.34

	// Dome and flared skirt. The width grows smoothly from the rounded top to
	// the rim, which reads as a bell even at 14–16 px.
	for y := int32(math.Round(bodyTop)); y <= int32(math.Round(bodyBottom)); y++ {
		t := (float64(y) - bodyTop) / bodyH
		if t < 0 || t > 1 {
			continue
		}
		half := halfMax * (0.40 + 0.60*t)
		if t < 0.28 {
			half = halfMax * math.Sqrt(0.16+(t/0.28)*0.84)
		}
		renderer.DrawLine(cx-int32(math.Round(half)), y, cx+int32(math.Round(half)), y)
	}

	// Hanger knob.
	knobR := int32(math.Max(1, f*0.09))
	fillCircle(renderer, cx, int32(math.Round(bodyTop))-knobR+1, knobR, col)

	// Rim and clapper.
	rimW := int32(math.Round(f * 0.88))
	rimH := int32(math.Max(2, f*0.13))
	rimY := int32(math.Round(bodyBottom)) - rimH/2
	fillRoundedRect(renderer, cx-rimW/2, rimY, rimW, rimH, rimH/2, col)
	clapR := int32(math.Max(1, f*0.11))
	fillCircle(renderer, cx, int32(math.Round(bodyBottom+f*0.20)), clapR, col)

	// Soft highlight on the left shoulder gives the silhouette a little depth.
	if size >= 18 {
		sheen := sdl.Color{R: 255, G: 255, B: 255, A: 55}
		for y := int32(math.Round(bodyTop + f*0.20)); y < int32(math.Round(bodyTop+f*0.45)); y++ {
			t := (float64(y) - bodyTop) / bodyH
			half := halfMax * (0.40 + 0.60*t)
			x := cx - int32(math.Round(half)) + 2
			renderer.SetDrawColor(sheen.R, sheen.G, sheen.B, sheen.A)
			renderer.DrawLine(x, y, x+1, y)
		}
	}
}

// drawNotifBell paints the bell pill, focus/open ring, icon and unread badge.
// Every screen (home header and the scene status bar) funnels through here, so
// the control cannot drift between the home screen and the rest of the app.
// The pixels come from the shared icon-button component, which is the recipe
// every icon-only chip should use.
func drawNotifBell(renderer *sdl.Renderer, config *Config, font *ttf.Font, rect sdl.Rect) {
	if renderer == nil || font == nil || rect.W <= 0 || rect.H <= 0 {
		return
	}

	// Focus ring when the D-pad/arrows sit on the bell. Home has its own focus
	// graph rather than the scene Chrome indexes, so account for both owners;
	// otherwise the bell is keyboard/controller-focusable on Home but looks
	// unselected while the focus cursor is on it.
	focused := focusEngine.ElementIndex() == ChromeBellIndex
	if homeLayoutActive && homeLayout != nil && homeLayout.Focus != nil {
		focused = focused || homeLayout.Focus.Current() == HomeFocusBell
	}

	badgeFont := notifBadgeFont(config)
	if badgeFont == nil {
		badgeFont = font
	}
	button := UIIconButton{
		Rect: rect,
		Style: UIIconButtonStyle{
			// Background pill in the active theme's input surface.
			Surface:     ColorIconSurface,
			OpenSurface: ColorIconSurface,
			Ring:        accentColor,
			RingWidth:   glassFocusRingWidth,
			// Bell icon: primary text colour so it reads as a control, with the
			// accent ring already marking focus/open state. The glyph is inset from
			// the chip so the silhouette never touches the pill's edge at handheld
			// sizes.
			Icon:      ColorTextPrimary(),
			IconInset: notifBellIconInset,
			Glyph: func(r *sdl.Renderer, box sdl.Rect, tint sdl.Color) {
				icon := box.W
				if icon < 12 {
					icon = 12
				}
				drawBellIcon(r, box.X+box.W/2, box.Y+box.H/2, icon, tint)
			},
		},
		State:     UIIconButtonState{Focused: focused, Open: nc.open},
		BadgeFont: badgeFont,
	}
	button.Badge = notifBadgeLabel(notifUnreadCount())
	UIIconButtonDraw(renderer, button)

	nc.bellRect = rect
}

// renderNotifBell draws the bell icon at the top-right of the status bar.
// Returns its rect for hit testing.
func renderNotifBell(renderer *sdl.Renderer, config *Config, x, y int32) sdl.Rect {
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return sdl.Rect{}
	}
	bellW, bellH := notifBellSize(config)
	if bellW <= 0 {
		return sdl.Rect{}
	}
	rect := sdl.Rect{X: x - bellW, Y: y, W: bellW, H: bellH}
	drawNotifBell(renderer, config, font, rect)
	return rect
}

const (
	// notifDropW is the card's width on a narrow canvas; a wide one gives it
	// three eighths of the screen, up to notifDropMaxW (notifDropWidth).
	notifDropW    = int32(400)
	notifDropMaxW = int32(520)
	notifHeaderH  = int32(40)
	// notifListPadB is the space under the last row.
	notifListPadB = int32(12)
	// notifRowMinH is the shortest row (one line plus its timestamp); taller
	// rows grow from their measured content instead.
	notifRowMinH = int32(52)
	notifMaxRows = 8
	// notifMsgLines is how many lines a row's message may use while it is not
	// highlighted; the highlighted row shows the whole message, up to as many
	// lines as the card holds (notifExpandedLines: 18 under the Smart Pro's
	// bell at the default text size, some 650 characters). Every row used to
	// stop at two lines, so the end of a long error could not be read anywhere.
	notifMsgLines = 2
	notifRowPad   = int32(8)
)

// notifRowLayout is one history row's measured geometry: the wrapped message
// lines and the height they plus the timestamp need.
type notifRowLayout struct {
	lines  []string
	height int32
}

// notifDropWidth is the card width: 400 px, or three eighths of a wide canvas
// (480 on the 1280 one) so a long message takes fewer lines, clamped so it
// never runs off the panel.
func notifDropWidth() int32 {
	w := min(max(notifDropW, screenWidth*3/8), notifDropMaxW)
	if maxW := screenWidth - 24; w > maxW {
		w = maxW
	}
	return w
}

// notifMessageWidth is the inner width a message may occupy: the card minus
// the priority-dot gutter and the right padding.
func notifMessageWidth(w int32) int32 {
	m := w - 32 - 16
	if m < 40 {
		m = 40
	}
	return m
}

// notifRowMessage is the text a row shows: the message plus the fold count when
// identical events were merged. ASCII on purpose, since the handheld's bundled
// fonts are not guaranteed to carry U+00D7.
func notifRowMessage(n notification) string {
	if n.Repeat > 1 {
		return fmt.Sprintf("%s  x%d", n.Message, n.Repeat)
	}
	return n.Message
}

// notifWrapKey identifies one wrapped row. The open centre lays out every row
// on every frame, and a wrap is a FreeType measurement per word through cgo;
// a row only changes when a message arrives, the highlight moves or the font
// (the text size) changes.
type notifWrapKey struct {
	font     *ttf.Font
	text     string
	maxW     int32
	maxLines int
}

// notifWrapCur and notifWrapPrev hold the wrapped rows in two generations: a
// lookup that hits the older one moves the row into the current one, and when
// the current one is full it becomes the older one. The rows on the card
// survive the turnover, so no frame re-measures the whole history at once (a
// repeat counter, "x2", "x3", makes a new key every time it counts).
var (
	notifWrapMu   sync.Mutex
	notifWrapCur  = map[notifWrapKey][]string{}
	notifWrapPrev = map[notifWrapKey][]string{}
)

// notifWrapCacheMax bounds a generation; a frame looks up one key per row of
// the 50-row history.
const notifWrapCacheMax = 128

// notifWrapLines word-wraps a notification message into at most maxLines lines
// (wrapWordsMeasured). A path or URL wider than the line is cut into pieces
// that fit, at a separator where it can be, instead of being ellipsized to its
// folder and losing the file name and the reason (".../Dracula X Chroni...").
func notifWrapLines(font *ttf.Font, text string, maxW int32, maxLines int) []string {
	if font == nil || maxLines <= 0 || maxW <= 0 {
		return nil
	}
	key := notifWrapKey{font: font, text: text, maxW: maxW, maxLines: maxLines}
	notifWrapMu.Lock()
	lines, ok := notifWrapCur[key]
	if !ok {
		if lines, ok = notifWrapPrev[key]; ok {
			notifWrapKeepLocked(key, lines)
		}
	}
	notifWrapMu.Unlock()
	if ok {
		return lines
	}
	lines, _ = wrapWordsMeasured(text, maxW, maxLines, func(s string) int32 {
		w, _, _ := font.SizeUTF8(s)
		return int32(w)
	})
	notifWrapMu.Lock()
	notifWrapKeepLocked(key, lines)
	notifWrapMu.Unlock()
	return lines
}

// notifWrapKeepLocked files a wrapped row in the current generation, turning
// the generations over first when it is full. The caller holds notifWrapMu.
func notifWrapKeepLocked(key notifWrapKey, lines []string) {
	if len(notifWrapCur) >= notifWrapCacheMax {
		notifWrapPrev = notifWrapCur
		notifWrapCur = make(map[notifWrapKey][]string, notifWrapCacheMax)
	}
	notifWrapCur[key] = lines
}

// notifMeasureRows wraps every message and gives its row a height that fits
// the lines plus the timestamp: two lines at most, except the highlighted row
// (cursor), which may use expanded lines, its whole message on any card that
// can hold it. The fixed height these rows once shared ellipsized long errors
// to one line, exactly the messages worth reading.
func notifMeasureRows(font *ttf.Font, history []notification, maxW int32, cursor, expanded int) []notifRowLayout {
	if font == nil || len(history) == 0 {
		return nil
	}
	lineH := int32(font.Height())
	rows := make([]notifRowLayout, 0, len(history))
	for i, n := range history {
		limit := notifMsgLines
		if i == cursor {
			limit = max(expanded, notifMsgLines)
		}
		lines := notifWrapLines(font, notifRowMessage(n), maxW, limit)
		rows = append(rows, notifRowLayout{lines: lines, height: notifRowHeight(len(lines), lineH)})
	}
	return rows
}

// notifEmptyContentH is the vertical space the empty state needs below the
// dropdown header: the drawn bell glyph, both text lines and breathing room.
// A single row (52px) was smaller than that stack, so the card used to end
// above the second line and the empty-state text spilled out of its surface.
func notifEmptyContentH(config *Config) int32 {
	content := uiStateBlockHeight(config, "@bell", "No notifications", "You're all caught up.")
	if content <= 0 {
		content = 72
	}
	return content + SpaceLG*2
}

// notifDropMaxH is the tallest the card may be when it hangs from by: down to
// 12 px above the bottom edge, and never less than a header and one row.
func notifDropMaxH(by int32) int32 {
	return max(screenHeight-by-12, notifHeaderH+notifRowMinH+notifListPadB)
}

// notifDropdownLayout sizes the dropdown so it always fits on screen: the
// panel is measured from the bell's anchor and holds the window of rows that
// notifListWindow picks, the highlighted row always among them and whole. Rows
// have their own measured heights, so a message is never clipped to make the
// arithmetic work. An empty history gets the empty state's measured height
// instead of a guessed row count.
func notifDropdownLayout(config *Config, by int32, heights []int32, cursor, scroll int) (w, h int32, first, count int) {
	w = notifDropWidth()
	maxH := notifDropMaxH(by)
	if len(heights) == 0 {
		return w, min(notifHeaderH+notifEmptyContentH(config), maxH), 0, 0
	}
	first, count = notifListWindow(heights, maxH-notifHeaderH-notifListPadB, notifMaxRows, cursor, scroll)
	h = notifHeaderH + notifSumHeights(heights[first:first+count]) + notifListPadB
	return w, h, first, count
}

// notifFollowArrivals keeps the highlight on the row being read when rows
// arrive while the centre is open. New rows are prepended, so without this
// the highlight, and with it the expanded message, would slide onto a
// neighbour, the newest row included: a message opened from its toast would
// collapse under the reader as soon as anything else was filed. Arrivals
// show at the top, collapsed, while the list is at its top; a scrolled list
// keeps its rows where they are. When every row of the history (rows) is new
// there is no row to follow (Clear All, then a burst), and the highlight goes
// to the newest instead of the oldest of the burst.
func notifFollowArrivals(added uint64, rows int) {
	if added <= nc.seenAdded {
		nc.seenAdded = added
		return
	}
	d := int(added - nc.seenAdded)
	nc.seenAdded = added
	if d >= rows {
		nc.cursor, nc.scroll = 0, 0
		return
	}
	nc.cursor += d
	if nc.scroll > 0 {
		nc.scroll += d
	}
}

// renderNotifDropdown draws the notification history dropdown.
func renderNotifDropdown(renderer *sdl.Renderer, config *Config) {
	if !nc.open {
		return
	}

	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}

	// Position below the bell, then size the card to the rows that fit. Rows
	// are measured first: their heights depend on the wrapped message (the
	// highlighted one in full), and the card has to be tall enough for
	// whatever survives the wrap.
	by := nc.bellRect.Y + nc.bellRect.H + 8
	history, added := notifHistoryState()
	notifFollowArrivals(added, len(history))
	nc.cursor = clampIndex(nc.cursor, len(history))
	lineH := int32(font.Height())
	expanded := notifExpandedLines(notifDropMaxH(by)-notifHeaderH-notifListPadB, lineH)
	rowLayouts := notifMeasureRows(font, history, notifMessageWidth(notifDropWidth()), nc.cursor, expanded)
	heights := make([]int32, len(rowLayouts))
	for i, r := range rowLayouts {
		heights[i] = r.height
	}
	dropW, dropH, first, numRows := notifDropdownLayout(config, by, heights, nc.cursor, nc.scroll)
	nc.scroll = first
	bx := nc.bellRect.X
	if bx+dropW > screenWidth {
		bx = screenWidth - dropW - 12
	}
	if bx < 12 {
		bx = 12
	}
	// Paging and click selection need the rows the render drew, otherwise a
	// click could pick a row that is not on the card.
	nc.visibleRows = numRows
	nc.rowFirst = first
	nc.rowRects = nc.rowRects[:0]
	headerH := notifHeaderH

	// Entrance keyframes: the scrim fades while the card drops out from under
	// the bell, so the panel arrives as one layer instead of popping.
	progress := 1.0
	if uiMotionEnabled(config) {
		progress = nc.enter.progress(uiClockMS())
	} else {
		nc.enter.cancel()
	}
	fade := uiFadeIn.at(progress)
	slide := int32(math.Round(uiDropDown.at(progress)))

	// The shared overlay dim (the backdrop's navy), faded in with the card.
	drawOverlayScrimFade(renderer, uiScrimProgress(progress, uiPanelEnterMS))

	compositeUILayerIn(renderer, fade, slide, sdl.Rect{X: bx, Y: by, W: dropW, H: dropH}, func() {
		// Dropdown card: the shared overlay sheet.
		nc.dropRect = sdl.Rect{X: bx, Y: by, W: dropW, H: dropH}
		drawOverlayCard(renderer, nc.dropRect)

		// Header: "Notifications" + Clear All.
		title := "Notifications"
		renderText(renderer, config, font, title, ColorTextPrimary(), bx+16, by+12)

		nc.clearRect = sdl.Rect{}
		if len(history) > 0 {
			clearStr := "Clear All"
			cw, ch, _ := font.SizeUTF8(clearStr)
			clearX := bx + dropW - int32(cw) - 16
			nc.clearRect = sdl.Rect{X: clearX - 8, Y: by + 6, W: int32(cw) + 16, H: int32(ch) + 12}
			renderText(renderer, config, font, clearStr,
				sdl.Color{R: 220, G: 100, B: 100, A: 200},
				clearX, by+12)
		}

		// Notification rows: the window notifListWindow picked. Each row is as
		// tall as its wrapped message needs, the highlighted one showing the
		// whole message, so its timestamp stays inside the row instead of
		// overlapping clipped text.
		listY := by + headerH
		ry := listY
		// With rows above or below the window a scrollbar takes the card's
		// right edge, and the rows (the highlight's wash) stop short of it.
		scrollable := numRows > 0 && numRows < len(history)
		rowW := dropW - 8
		if scrollable {
			rowW -= 8
		}
		for i := first; i < first+numRows && i < len(history) && i < len(rowLayouts); i++ {
			n := history[i]
			row := rowLayouts[i]
			focused := i == nc.cursor

			if ry+row.height > by+dropH {
				break
			}

			rowRect := sdl.Rect{X: bx + 4, Y: ry, W: rowW, H: row.height - 2}
			nc.rowRects = append(nc.rowRects, rowRect)

			// Row background: the cursor row is a selected list row.
			if focused {
				UIListRow(renderer, rowRect, 10, true, false)
			}

			// Priority dot, aligned with the first message line.
			dotCol := sdl.Color{R: 80, G: 160, B: 255, A: 220}
			switch n.Priority {
			case notifSuccess:
				dotCol = sdl.Color{R: 80, G: 200, B: 120, A: 220}
			case notifWarning:
				dotCol = sdl.Color{R: 240, G: 180, B: 60, A: 220}
			case notifError:
				dotCol = sdl.Color{R: 220, G: 70, B: 70, A: 220}
			}
			fillCircle(renderer, bx+20, ry+notifRowPad+lineH/2, 4, dotCol)

			// Message lines, wrapped by notifMeasureRows. The highlighted row
			// is the one being read, so it takes the primary text colour.
			msgCol := WithAlpha(ColorTextSecondary(), 220)
			if n.Read {
				msgCol = WithAlpha(ColorTextTertiary(), 180)
			}
			if focused {
				msgCol = ColorTextPrimary()
			}
			for li, line := range row.lines {
				renderText(renderer, config, font, line, msgCol,
					bx+32, ry+notifRowPad+int32(li)*lineH)
			}

			// Timestamp under the last message line.
			ago := time.Since(n.Timestamp)
			timeStr := ""
			switch {
			case ago < time.Minute:
				timeStr = "just now"
			case ago < time.Hour:
				timeStr = fmt.Sprintf("%dm ago", int(ago.Minutes()))
			default:
				timeStr = fmt.Sprintf("%dh ago", int(ago.Hours()))
			}
			renderText(renderer, config, font, timeStr,
				WithAlpha(ColorTextTertiary(), 150),
				bx+32, ry+notifRowPad+int32(len(row.lines))*lineH+2)

			ry += row.height
		}

		// Rows above or below the window: the scrollbar says there is more,
		// and where the card is in the history.
		if scrollable {
			listH := dropH - headerH - notifListPadB
			thumb := float64(numRows) / float64(len(history))
			pos := float64(first) / float64(len(history)-numRows)
			drawScrollbar(renderer, bx+dropW-9, listY+2, 3, listH-4, thumb, pos)
		}

		if len(history) == 0 {
			UIEmptyState(renderer, config, sdl.Rect{X: bx, Y: listY, W: dropW, H: dropH - headerH},
				"@bell", "No notifications", "You're all caught up.")
		}
	})
}

// notifToggle opens/closes the notification dropdown.
func notifToggle() {
	notifSetOpen(!nc.open, true)
}

// notifSetOpen opens or closes the dropdown. Home has its own focus graph: a
// tap on the bell or a chrome activation on Home is a deliberate choice, so
// with focusBell the focus follows it and the D-pad continues from the bell
// instead of jumping back to a card. A click on a toast is not a visit to the
// bell, and leaves the focus on the card the user was on.
func notifSetOpen(open, focusBell bool) {
	nc.open = open
	nc.cursor = 0
	nc.scroll = 0
	nc.rowFirst = 0
	nc.visibleRows = 0
	if focusBell && homeLayoutActive && homeLayout != nil {
		if homeLayout.Focus.Set(HomeFocusBell) {
			homeLayout.Focus.Remember()
		}
	}
	if nc.open {
		nc.enter.begin(uiClockMS(), uiPanelEnterMS)
		// The top-bar overlays are exclusive.
		tb.startOpen = false
		homeCalendarOpen = false
		dismissWeatherPopover()
		dismissVolumePopover()
		dismissBrightnessPopover()
		// Mark all as read. Rows that arrive from here on move the highlight
		// with them (notifFollowArrivals).
		notifHistoryMu.Lock()
		for i := range nc.history {
			nc.history[i].Read = true
		}
		nc.seenAdded = notifAdded
		notifHistoryMu.Unlock()
	}
	PlayActivateSound()
}

// notifOpenOn opens the centre with the highlight on the newest row carrying
// msg, and the highlighted row shows its message whole: a click on a toast
// that was cut short lands here (notifToastOpensCentre).
func notifOpenOn(msg string) {
	if !nc.open {
		notifSetOpen(true, false)
	}
	// The row is looked up and the arrivals count taken under one lock, so a
	// row filed in between cannot leave the highlight one row off.
	notifHistoryMu.Lock()
	i := notifRowIndexLocked(msg)
	nc.seenAdded = notifAdded
	notifHistoryMu.Unlock()
	if i >= 0 {
		nc.cursor = i
	}
}

// notifToastOpensCentre reports whether a click on the toast showing msg
// opens the centre on it. The card must have cut that very message (cutMsg is
// what the last frame drew cut short), the centre must keep it (the Live
// Builder's pairing-code toasts are not filed), and an opened centre must be
// seen and get the pad: no panel or menu owns the pad (inmapOverlayOpen) and
// nothing is drawn over the centre (notifCentreCovered). Otherwise the click
// only dismisses the toast, as it always did.
func notifToastOpensCentre(msg, cutMsg string) bool {
	if msg == "" || msg != cutMsg || nc.open {
		return false
	}
	if inmapOverlayOpen() || notifCentreCovered() {
		return false
	}
	return notifRowIndex(msg) >= 0
}

// notifCentreCovered reports whether something is drawn over the centre's
// place: the full-screen player, the dock and the screensaver cover the scene
// and its chrome, and the Chat panel's member list, profile card and file
// sheet are drawn above the top-bar overlays. A centre opened under them
// would take the pad unseen.
func notifCentreCovered() bool {
	return (videoFullscreen && playerPhase() != "idle") || ambientIsActive() || ss.active || chatOverlayOpen()
}

// notifCentreShowsRow reports whether the open, uncovered centre has msg's row
// on its card: rowFirst and visibleRows are what renderNotifDropdown drew,
// earlier in the same frame than the toast. renderToast keeps the toast off
// the centre only then; a row scrolled off the card still gets its toast.
func notifCentreShowsRow(msg string) bool {
	if !nc.open || nc.visibleRows <= 0 || notifCentreCovered() {
		return false
	}
	i := notifRowIndex(msg)
	return i >= nc.rowFirst && i < nc.rowFirst+nc.visibleRows
}

// notifNavigate moves the cursor in the dropdown, over the whole history: the
// list scrolls to keep the cursor's row on the card (notifListWindow). It used
// to stop at the rows the first card-full drew, so an older notification could
// not be reached at all.
func notifNavigate(dr int) {
	if !nc.open {
		return
	}
	nc.cursor = clampIndex(nc.cursor+dr, notifHistoryLen())
}

// notifWheel walks the open centre's rows under the mouse wheel, a row a
// notch like the D-pad. It reports whether the wheel was the centre's.
func notifWheel(dy int32) bool {
	if !nc.open {
		return false
	}
	switch {
	case dy > 0:
		notifNavigate(-1)
	case dy < 0:
		notifNavigate(1)
	}
	return true
}

// notifClearAll clears all notifications. The dropdown stays open so the
// empty state is visible (touch users get direct feedback).
func notifClearAll() {
	notifHistoryMu.Lock()
	nc.history = nc.history[:0]
	notifHistoryMu.Unlock()
	nc.cursor = 0
	nc.scroll = 0
}

// notifDismiss closes the dropdown.
func notifDismiss() {
	nc.open = false
}

// notifIsPointInDrop tests if a point is inside the notification dropdown.
func notifIsPointInDrop(x, y int32) bool {
	if !nc.open {
		return false
	}
	return x >= nc.dropRect.X && x <= nc.dropRect.X+nc.dropRect.W &&
		y >= nc.dropRect.Y && y <= nc.dropRect.Y+nc.dropRect.H
}

// notifIsPointInBell tests if a point is inside the bell icon. The chip is a
// header pill (24–30 px tall), so the hit area is the shared 44 px touch
// target centred on it; the drawn bell does not move.
func notifIsPointInBell(x, y int32) bool {
	if nc.bellRect.W <= 0 || nc.bellRect.H <= 0 {
		return false
	}
	return hitTouchTarget(nc.bellRect, x, y)
}

// notifHandleClick consumes clicks on the open dropdown: the header's "Clear
// All" clears the history, any other click inside the card is swallowed so it
// cannot reach the scene underneath. It reports whether the click was inside
// the open dropdown.
func notifHandleClick(x, y int32) bool {
	if !nc.open || !notifIsPointInDrop(x, y) {
		return false
	}
	if nc.clearRect.W > 0 && x >= nc.clearRect.X && x <= nc.clearRect.X+nc.clearRect.W &&
		y >= nc.clearRect.Y && y <= nc.clearRect.Y+nc.clearRect.H {
		notifClearAll()
		PlayActivateSound()
		return true
	}
	// Clicking a row moves the selection there, which also shows its whole
	// message; rowRects[0] is history row rowFirst once the list has scrolled.
	for i, r := range nc.rowRects {
		if pointInRect(x, y, r) {
			if row := nc.rowFirst + i; nc.cursor != row {
				nc.cursor = row
				PlayNavSound()
			}
			break
		}
	}
	return true
}

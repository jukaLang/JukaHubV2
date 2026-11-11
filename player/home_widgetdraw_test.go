package main

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

// TestHomeWidgetPlayedParts checks the Playtime widget's wording: hours and
// minutes as figure/unit pairs, a whole hour without "0 m", under a minute,
// and nothing played yet.
func TestHomeWidgetPlayedParts(t *testing.T) {
	cases := []struct {
		secs  int64
		want  string
		plain string
	}{
		{0, "", "Not yet today"},
		{-5, "", "Not yet today"},
		{30, "<1 m", ""},
		{60, "1 m", ""},
		{12*60 + 59, "12 m", ""},
		{3600, "1 h", ""},
		{3600 + 12*60, "1 h 12 m", ""},
		{2*3600 + 5*60 + 40, "2 h 5 m", ""},
	}
	for _, tc := range cases {
		nums, units, n, plain := hwpPlayedParts(tc.secs)
		got := ""
		for i := 0; i < n; i++ {
			if i > 0 {
				got += " "
			}
			got += nums[i] + " " + units[i]
		}
		if got != tc.want || plain != tc.plain {
			t.Errorf("hwpPlayedParts(%d) = %q / %q, want %q / %q", tc.secs, got, plain, tc.want, tc.plain)
		}
	}
}

// TestHomeWidgetTrophyText checks the trophy line's singular and plural.
func TestHomeWidgetTrophyText(t *testing.T) {
	for n, want := range map[int]string{0: "No trophies yet", 1: "1 trophy", 2: "2 trophies", 101: "101 trophies"} {
		if got := hwpTrophyText(n); got != want {
			t.Errorf("hwpTrophyText(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestHomeWidgetMonthGrid checks the calendar's Monday-first weeks on months
// that start on a Monday, a Thursday, a Saturday and a Sunday, a leap February
// among them.
func TestHomeWidgetMonthGrid(t *testing.T) {
	cases := []struct {
		year  int
		month time.Month
		first int
		days  int
		rows  int
	}{
		{2026, time.November, 6, 30, 6},
		{2027, time.February, 0, 28, 4},
		{2026, time.June, 0, 30, 5},
		{2026, time.August, 5, 31, 6},
		{2024, time.February, 3, 29, 5},
	}
	for _, tc := range cases {
		first, days, rows := hwpMonthGrid(tc.year, tc.month)
		if first != tc.first || days != tc.days || rows != tc.rows {
			t.Errorf("hwpMonthGrid(%d, %s) = %d, %d, %d; want %d, %d, %d",
				tc.year, tc.month, first, days, rows, tc.first, tc.days, tc.rows)
		}
	}
	if hwpDayLabel(1) != "1" || hwpDayLabel(31) != "31" || hwpDayLabel(0) != "0" || hwpDayLabel(32) != "32" {
		t.Errorf("hwpDayLabel: 1, 31, 0, 32 = %q %q %q %q", hwpDayLabel(1), hwpDayLabel(31), hwpDayLabel(0), hwpDayLabel(32))
	}
}

// TestHomeWidgetSplitClock checks that the 12-hour suffix comes off the
// figures and a 24-hour time keeps none.
func TestHomeWidgetSplitClock(t *testing.T) {
	cases := [][3]string{
		{"15:04", "15:04", ""},
		{"3:04 PM", "3:04", "PM"},
		{"12:00 AM", "12:00", "AM"},
	}
	for _, tc := range cases {
		if digits, suffix := hwpSplitClock(tc[0]); digits != tc[1] || suffix != tc[2] {
			t.Errorf("hwpSplitClock(%q) = %q, %q; want %q, %q", tc[0], digits, suffix, tc[1], tc[2])
		}
	}
}

// TestHomeWidgetWeatherText checks the forecast's whole degrees (no "-0°"),
// the unit conversion, the sky glyph rule and the weekday of a forecast date.
func TestHomeWidgetWeatherText(t *testing.T) {
	temps := []struct {
		celsius float64
		unit    string
		want    string
	}{
		{-0.4, "C", "0°"},
		{21.6, "C", "22°"},
		{-7.5, "C", "-8°"},
		{10, "F", "50°"},
	}
	for _, tc := range temps {
		if got := hwpTemp(tc.celsius, tc.unit); got != tc.want {
			t.Errorf("hwpTemp(%v, %s) = %q, want %q", tc.celsius, tc.unit, got, tc.want)
		}
	}
	skies := []struct {
		code, hour int
		want       string
	}{
		{0, 12, "sun"},
		{1, 21, "moon"},
		{1, 5, "moon"},
		{2, 12, "cloud"},
		{61, 23, "cloud"},
	}
	for _, tc := range skies {
		if got := hwpWeatherGlyph(tc.code, tc.hour); got != tc.want {
			t.Errorf("hwpWeatherGlyph(%d, %d) = %q, want %q", tc.code, tc.hour, got, tc.want)
		}
	}
	if got := hwpShortWeekday("2026-11-11"); got != "Wed" {
		t.Errorf("hwpShortWeekday(2026-11-11) = %q, want Wed", got)
	}
	if got := hwpShortWeekday("soon"); got != "" {
		t.Errorf("hwpShortWeekday(soon) = %q, want empty", got)
	}
}

// TestHomeWidgetSplitSize checks that the Storage figure and its unit come
// apart the way formatBytes writes them.
func TestHomeWidgetSplitSize(t *testing.T) {
	if value, unit := hwpSplitSize(formatBytes(23_400_000_000)); value != "21.8" || unit != "GB" {
		t.Errorf("hwpSplitSize(formatBytes(23.4e9)) = %q, %q; want 21.8, GB", value, unit)
	}
	if value, unit := hwpSplitSize("512"); value != "512" || unit != "" {
		t.Errorf("hwpSplitSize(512) = %q, %q; want 512 and no unit", value, unit)
	}
}

// TestHomeWidgetNames checks which names the painter knows and offers: an
// unknown or prefixed name is never offered, so the add sheet cannot list a
// widget that would draw as a plain tile.
func TestHomeWidgetNames(t *testing.T) {
	for _, name := range []string{"clock", "weather", "storage", "nowplaying", "calendar", "playtime"} {
		if !hwpKnown(name) || !homeWidgetAvailable(name) {
			t.Errorf("%q: known %v, available %v; want both", name, hwpKnown(name), homeWidgetAvailable(name))
		}
	}
	if !hwpKnown("battery") {
		t.Error("battery is not a known widget")
	}
	for _, name := range []string{"", "w:clock", "Clock", "radio"} {
		if hwpKnown(name) || homeWidgetAvailable(name) {
			t.Errorf("%q is offered as a widget", name)
		}
	}
}

// TestHomeWidgetSizeBuckets checks the compositions the tile sizes of both
// canvases get: the grid's 1x1, 2x1 and 2x2 and the carousel's cards on the
// 1280 canvas, the same grid tiles on the Brick's 960.
func TestHomeWidgetSizeBuckets(t *testing.T) {
	cases := []struct {
		width      int32
		w, h       int32
		wide, tall bool
	}{
		{1280, 189, 152, false, false},
		{1280, 395, 152, true, false},
		{1280, 395, 320, true, true},
		{1280, 520, 330, true, true},
		{1280, 260, 165, false, false},
		{960, 142, 152, false, false},
		{960, 296, 152, true, false},
		{960, 296, 320, true, true},
	}
	for _, tc := range cases {
		layout := &HomeLayout{Width: tc.width, Height: 720}
		if b := layout.hwpFrame(sdl.Rect{W: tc.w, H: tc.h}); b.wide != tc.wide || b.tall != tc.tall {
			t.Errorf("%d canvas, %dx%d: wide %v tall %v, want %v %v", tc.width, tc.w, tc.h, b.wide, b.tall, tc.wide, tc.tall)
		}
	}
}

// TestHomeWidgetSettledLayout checks that a card the focus lift grew keeps its
// settled layout, centred in the grown card, that a pressed card follows its
// dip, and that a card the carousel scales lays out at its drawn size.
func TestHomeWidgetSettledLayout(t *testing.T) {
	layout := &HomeLayout{Width: 1280, Height: 720}
	base := sdl.Rect{X: 100, Y: 200, W: 189, H: 152}
	lifted := sdl.Rect{X: 97, Y: 197, W: 195, H: 158}
	if got := layout.hwpSettled(base, lifted); got != base {
		t.Errorf("lifted card: %+v, want the settled %+v", got, base)
	}
	pressed := sdl.Rect{X: 100, Y: 203, W: 189, H: 152}
	if got := layout.hwpSettled(base, pressed); got != pressed {
		t.Errorf("pressed card: %+v, want %+v", got, pressed)
	}
	side := sdl.Rect{X: 40, Y: 250, W: 260, H: 165}
	for _, settled := range []sdl.Rect{{W: 520, H: 330}, {}} {
		if got := layout.hwpSettled(settled, side); got != side {
			t.Errorf("settled %+v, drawn %+v: got %+v, want the drawn rect", settled, side, got)
		}
	}
}

// TestHomeWidgetTallBadge checks the tall app tile's badge: 72 to 96 design
// px in 8 px steps, scaled to the canvas.
func TestHomeWidgetTallBadge(t *testing.T) {
	cases := []struct {
		width int32
		w, h  int32
		want  int32
	}{
		{1280, 395, 320, 88},
		{1280, 520, 330, 88},
		{1280, 395, 250, 72},
		{1280, 1000, 400, 96},
		{960, 296, 320, 66},
	}
	for _, tc := range cases {
		layout := &HomeLayout{Width: tc.width, Height: 720}
		if got := layout.hwpTallBadge(sdl.Rect{W: tc.w, H: tc.h}); got != tc.want {
			t.Errorf("%d canvas, %dx%d: badge %d, want %d", tc.width, tc.w, tc.h, got, tc.want)
		}
	}
}

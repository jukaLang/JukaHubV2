package main

// home_tip_focus_test.go — the tip of the day is a focus stop, not just a
// painted line (SHELL-9).

import (
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"
)

func TestHomeTipIsAFocusStopAboveTheFooter(t *testing.T) {
	oldShow, oldPill := homeTip.show, homeTip.pill
	defer func() { homeTip.show, homeTip.pill = oldShow, oldPill }()
	homeTip.show = true
	homeTip.pill = sdl.Rect{W: 200, H: 30}

	f := NewHomeFocusController()
	if f.IsEnabled(HomeFocusTip) {
		t.Fatal("the tip's stop was on before Home drew it")
	}
	f.SetEnabled(HomeFocusTip, true)
	if !f.Set(HomeFocusFavorites) {
		t.Fatal("could not focus the last row")
	}
	if got := f.Move(FocusDown); got != HomeFocusTip {
		t.Fatalf("Down from the last row reached %q, want the tip", got)
	}
	if got := f.Move(FocusDown); got != HomeFocusExit {
		t.Fatalf("Down from the tip reached %q, want the power button", got)
	}
	if got := f.Move(FocusUp); got != HomeFocusTip {
		t.Fatalf("Up from the footer reached %q, want the tip", got)
	}
	if got := f.Move(FocusUp); got != HomeFocusFavorites {
		t.Fatalf("Up from the tip reached %q, want the last row", got)
	}

	// Once the tip is gone, the old row-to-footer edge is back.
	f.SetEnabled(HomeFocusTip, false)
	f.Set(HomeFocusFavorites)
	if got := f.Move(FocusDown); got != HomeFocusExit {
		t.Fatalf("with the tip hidden Down reached %q, want the power button", got)
	}
}

func TestHomeTipHiddenDayIsBookkeepingAndSurvivesTheDay(t *testing.T) {
	if !cfgdocRuntimeKeys[helpTipHiddenSetting] {
		t.Fatalf("%s is not registered as a runtime key, so the settings reference would offer it", helpTipHiddenSetting)
	}
	c := &Config{}
	c.Variables.Custom = map[string]interface{}{}
	helpHideTodaysTip(c)
	if got, want := helpTipHiddenDay(c), helpTipDay(time.Now()); got != want {
		t.Fatalf("hidden day = %d, want today (%d)", got, want)
	}
	if helpTipsEnabled(c) != true {
		t.Fatal("hiding one day's tip switched the whole tip setting off")
	}
}

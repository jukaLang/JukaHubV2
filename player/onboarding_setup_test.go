package main

// The tour's "Get set up" page: the rows it draws, how the D-pad and mouse move
// between them, and the rule that the tour steps aside while a panel it opened
// is on screen.

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func onboardingSetupStepIndex(t *testing.T) int {
	t.Helper()
	for i, step := range onboardingSteps {
		if step.kind == "setup" {
			return i
		}
	}
	t.Fatal("the tour has no setup checklist step")
	return -1
}

// pinOnboardingSetup puts the tour on the checklist step with a stubbed set of
// actions, and restores the tour afterwards.
func pinOnboardingSetup(t *testing.T, calls *[]string) {
	t.Helper()
	prev := onboarding
	prevActions := setupActionsFor
	t.Cleanup(func() {
		onboarding = prev
		setupActionsFor = prevActions
	})
	withScreen(t, probeW, probeH)
	onboardingStart()
	onboarding.step = onboardingSetupStepIndex(t)
	setupActionsFor = func() setupActions { return setupTestActions(calls) }
	onboarding.setupRows = nil
	onboardingSetupRefresh(&Config{Variables: Variables{Custom: map[string]interface{}{}}}, true)
	if len(onboarding.setupRows) == 0 {
		t.Fatal("the checklist rebuilt no rows")
	}
}

func TestOnboardingChecklistRendersEveryRow(t *testing.T) {
	if onboardingCoveredByOverlay() {
		t.Fatal("a modal from another test is still open, the tour cannot draw")
	}
	var calls []string
	pinOnboardingSetup(t, &calls)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}

	renderFunProbe(t, func(r *sdl.Renderer) { renderOnboarding(r, cfg) })

	if len(onboarding.setupHits) != len(onboarding.setupRows) {
		t.Fatalf("drew %d row rects for %d rows", len(onboarding.setupHits), len(onboarding.setupRows))
	}
	for i, rect := range onboarding.setupHits {
		if rect.W <= 0 || rect.H <= 0 {
			t.Fatalf("row %d has an empty rect: %+v", i, rect)
		}
		if rect.X < 0 || rect.X+rect.W > probeW || rect.Y < 0 || rect.Y+rect.H > probeH {
			t.Fatalf("row %d escapes the screen: %+v", i, rect)
		}
		if rect.Y+rect.H > onboarding.next.Y {
			t.Fatalf("row %d %+v runs into the Next button %+v", i, rect, onboarding.next)
		}
		if i > 0 {
			prev := onboarding.setupHits[i-1]
			if rect.Y <= prev.Y {
				t.Fatalf("row %d is not below row %d (%+v vs %+v)", i, i-1, rect, prev)
			}
			if rect.H != prev.H || rect.W != prev.W {
				t.Fatalf("rows are not uniform: %+v vs %+v", rect, prev)
			}
		}
	}
}

func TestOnboardingChecklistMovesAndActivatesRows(t *testing.T) {
	var calls []string
	pinOnboardingSetup(t, &calls)
	down := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_DOWN}}
	up := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_UP}}

	if onboarding.setupCursor != 0 {
		t.Fatalf("cursor starts at %d, want 0", onboarding.setupCursor)
	}
	if !handleOnboardingKey(down) {
		t.Fatal("Down was not consumed by the checklist")
	}
	if onboarding.setupCursor != 1 {
		t.Fatalf("cursor after Down = %d, want 1", onboarding.setupCursor)
	}

	// A opens the focused row instead of advancing the tour.
	handleOnboardingKey(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_a}})
	if len(calls) != 1 || calls[0] != "retroachievements" {
		t.Fatalf("A fired %v, want the RetroAchievements row", calls)
	}
	if !onboarding.active || onboarding.step != onboardingSetupStepIndex(t) {
		t.Fatal("A moved the tour instead of opening the focused row")
	}

	// The cursor never leaves the list.
	for i := 0; i < 10; i++ {
		handleOnboardingKey(down)
	}
	if got, want := onboarding.setupCursor, len(onboarding.setupRows)-1; got != want {
		t.Fatalf("cursor at the bottom = %d, want %d", got, want)
	}
	for i := 0; i < 10; i++ {
		handleOnboardingKey(up)
	}
	if onboarding.setupCursor != 0 {
		t.Fatalf("cursor at the top = %d, want 0", onboarding.setupCursor)
	}
}

func TestOnboardingChecklistClickChoosesRow(t *testing.T) {
	var calls []string
	pinOnboardingSetup(t, &calls)
	cfg := &Config{Variables: Variables{Custom: map[string]interface{}{}}}
	renderFunProbe(t, func(r *sdl.Renderer) { renderOnboarding(r, cfg) })

	hit := onboarding.setupHits[2]
	if !handleOnboardingMouse(hit.X+hit.W/2, hit.Y+hit.H/2, cfg) {
		t.Fatal("a click on a checklist row was not consumed")
	}
	if len(calls) != 1 || calls[0] != "mediatools" {
		t.Fatalf("click fired %v, want the Media tools row", calls)
	}
	// A click outside every row still belongs to the tour (it is a modal).
	if !handleOnboardingMouse(4, probeH-4, cfg) {
		t.Fatal("the tour released a click that was outside its rows")
	}
}

func TestOnboardingStepsAsideForAnOpenPanel(t *testing.T) {
	var calls []string
	pinOnboardingSetup(t, &calls)
	prev := raPanel.open
	raPanel.open = true
	t.Cleanup(func() { raPanel.open = prev })

	key := &sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Sym: sdl.K_a}}
	if handleOnboardingKey(key) {
		t.Fatal("the tour consumed a key while a panel it opened was on screen")
	}
	if handleOnboardingMouse(30, 30, nil) {
		t.Fatal("the tour consumed a click while a panel was on screen")
	}
	if got := len(calls); got != 0 {
		t.Fatalf("a covered tour still fired %v", calls)
	}
}

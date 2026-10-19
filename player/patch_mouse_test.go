package main

import (
	"testing"

	"github.com/veandco/go-sdl2/sdl"
)

func TestPatchMouseBackupRowOpensRestoreModal(t *testing.T) {
	prevSnap, prevRects := patchSnap, patchRowRects
	prevFocus, prevModal := patchFocusIndex, patchModal
	t.Cleanup(func() {
		patchSnap, patchRowRects = prevSnap, prevRects
		patchFocusIndex, patchModal = prevFocus, prevModal
	})
	patchSnap = &PatchSnapshot{Backups: []string{"backup-1"}}
	patchModal = nil
	patchFocusIndex = 0

	// The focusables list is actions + rows + packages + backups; the backup
	// row is last, and clicking it must open the restore confirmation.
	n := len(patchFocusables())
	if n == 0 {
		t.Fatal("no Patch focusables")
	}
	patchRowRects = make([]sdl.Rect, n)
	patchRowRects[n-1] = sdl.Rect{X: 24, Y: 200, W: 900, H: 64}

	if !handlePatchMouse(60, 220, &Config{}) {
		t.Fatal("click on the backup row was not consumed")
	}
	if patchModal == nil || patchModal.ID != "confirm-restore" {
		t.Fatalf("modal = %+v, want the restore confirmation", patchModal)
	}
}

func TestPatchMouseModalCancelClick(t *testing.T) {
	prevModal := patchModal
	prevConfirm, prevCancel := patchConfirmRect, patchCancelRect
	t.Cleanup(func() {
		patchModal = prevModal
		patchConfirmRect, patchCancelRect = prevConfirm, prevCancel
	})
	patchModal = &patchModalState{ID: "confirm-restore", Title: "Restore?", Confirm: "Restore"}
	patchConfirmRect = sdl.Rect{X: 800, Y: 500, W: 200, H: 40}
	patchCancelRect = sdl.Rect{X: 600, Y: 500, W: 140, H: 40}

	if !handlePatchMouse(620, 510, &Config{}) {
		t.Fatal("click on Cancel was not consumed")
	}
	if patchModal != nil {
		t.Fatalf("Cancel left the modal open: %+v", patchModal)
	}
}

func TestPatchMouseConsumesClicksBehindModal(t *testing.T) {
	prevModal, prevRects := patchModal, patchRowRects
	prevFocus := patchFocusIndex
	t.Cleanup(func() {
		patchModal, patchRowRects = prevModal, prevRects
		patchFocusIndex = prevFocus
	})
	patchModal = &patchModalState{ID: "confirm-restore", Title: "Restore?", Confirm: "Restore"}
	patchConfirmRect, patchCancelRect = sdl.Rect{}, sdl.Rect{}
	patchRowRects = []sdl.Rect{{X: 24, Y: 100, W: 900, H: 64}}
	patchFocusIndex = 0

	if !handlePatchMouse(60, 120, &Config{}) {
		t.Fatal("click behind the modal was not swallowed")
	}
	if patchFocusIndex != 0 {
		t.Fatalf("click behind the modal moved the list focus to %d", patchFocusIndex)
	}
}

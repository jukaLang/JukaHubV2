package main

import (
	"context"
	"testing"
	"time"
)

func TestMlibUnchangedScanKeepsPublishedModel(t *testing.T) {
	oldDir, oldRoots := mlibDirOverride, mlibBaseRoots
	oldScanning := mlibScanning.Load()
	mlibState.mu.Lock()
	oldLoaded, oldItems, oldDirs, oldResume := mlibState.loaded, mlibState.items, mlibState.dirs, mlibState.resume
	oldModel, oldGen, oldCancel, oldTicket := mlibState.model, mlibState.gen, mlibState.cancel, mlibState.ticket
	oldFound, oldScanned, oldSaving, oldAgain := mlibState.found, mlibState.scanned, mlibState.saving, mlibState.again
	if oldSaving || oldScanning {
		mlibState.mu.Unlock()
		t.Fatal("another library scan or save is active")
	}
	mlibState.loaded, mlibState.items, mlibState.dirs, mlibState.resume = false, nil, nil, nil
	mlibState.model, mlibState.gen, mlibState.cancel, mlibState.ticket = nil, 0, nil, 1
	mlibState.found, mlibState.scanned, mlibState.saving, mlibState.again = 0, time.Time{}, false, false
	mlibState.mu.Unlock()

	mlibDirOverride = t.TempDir()
	root := t.TempDir()
	mlibBaseRoots = func() []string { return []string{root} }
	t.Cleanup(func() {
		deadline := time.Now().Add(3 * time.Second)
		for {
			mlibState.mu.Lock()
			saving := mlibState.saving
			mlibState.mu.Unlock()
			if !saving || time.Now().After(deadline) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		mlibDirOverride, mlibBaseRoots = oldDir, oldRoots
		mlibScanning.Store(oldScanning)
		mlibState.mu.Lock()
		mlibState.loaded, mlibState.items, mlibState.dirs, mlibState.resume = oldLoaded, oldItems, oldDirs, oldResume
		mlibState.model, mlibState.gen, mlibState.cancel, mlibState.ticket = oldModel, oldGen, oldCancel, oldTicket
		mlibState.found, mlibState.scanned, mlibState.saving, mlibState.again = oldFound, oldScanned, oldSaving, oldAgain
		mlibState.mu.Unlock()
	})

	mlibRunScan(context.Background(), 1, nil, "", false)
	first := mlibGen()
	if first == 0 {
		t.Fatal("the saved index did not publish its initial model")
	}
	mlibState.mu.Lock()
	mlibState.ticket = 2
	mlibState.mu.Unlock()
	mlibRunScan(context.Background(), 2, nil, "", false)
	if second := mlibGen(); second != first {
		t.Fatalf("unchanged scan republished the model: %d -> %d", first, second)
	}
}

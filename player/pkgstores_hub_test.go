package main

import "testing"

func TestPkgHubTriggerCycle(t *testing.T) {
	wantRight := []int{pkgHubAPK, pkgHubPorts, pkgHubWindows, pkgHubPackages}
	wantLeft := []int{pkgHubWindows, pkgHubPackages, pkgHubAPK, pkgHubPorts}
	for from := pkgHubPackages; from < pkgHubCount; from++ {
		if got := pkgHubStep(from, padR2); got != wantRight[from] {
			t.Errorf("R2 from %d = %d, want %d", from, got, wantRight[from])
		}
		if got := pkgHubStep(from, padL2); got != wantLeft[from] {
			t.Errorf("L2 from %d = %d, want %d", from, got, wantLeft[from])
		}
	}
	if got := pkgHubStep(-1, padR2); got != -1 {
		t.Errorf("invalid origin = %d, want -1", got)
	}
	if got := pkgHubStep(pkgHubPackages, padL1); got != -1 {
		t.Errorf("L1 changed stores: %d", got)
	}
}

package main

import "testing"

func TestHomeTabsDeleteQuestionStartsOnKeep(t *testing.T) {
	homeTabsColStore.mu.Lock()
	oldLoaded, oldFile := homeTabsColStore.loaded, homeTabsColStore.file
	homeTabsColStore.loaded = true
	homeTabsColStore.file = homeTabsColFile{Collections: []homeTabsCollection{{ID: "keep-test", Name: "Keep test"}}}
	homeTabsColStore.mu.Unlock()
	oldSheet := homeTabsSheet
	t.Cleanup(func() {
		homeTabsSheet = oldSheet
		homeTabsColStore.mu.Lock()
		homeTabsColStore.loaded, homeTabsColStore.file = oldLoaded, oldFile
		homeTabsColStore.mu.Unlock()
	})

	homeTabsSheet.item = homeTabItem{Ref: homeTabRefCollection + "keep-test", Title: "Keep test", Folder: true}
	homeTabsSheet.open = true
	homeTabsSheetPage(homeTabsSheetConfirm)
	if homeTabsSheet.mode != homeTabsSheetConfirm || homeTabsSheet.rows[homeTabsSheet.cursor].label != "Keep it" {
		t.Fatalf("delete question opened on %q", homeTabsSheet.rows[homeTabsSheet.cursor].label)
	}
	homeTabsSheetChoose() // A immediately after opening must only keep the collection.
	if homeTabsSheet.mode != homeTabsSheetMain {
		t.Fatalf("Keep returned to page %d, want the collection actions", homeTabsSheet.mode)
	}
	if _, ok := homeTabsColGet("keep-test"); !ok {
		t.Fatal("A on the initial choice deleted the collection")
	}
}

func psTestChooseAction(t *testing.T, action psPanelAction) {
	t.Helper()
	for i, row := range chiakiRows() {
		if row.action == action {
			chiakiPanel.cursor = i
			chiakiPanelActivate()
			return
		}
	}
	t.Fatalf("action %d is missing on page %d", action, chiakiPanel.page)
}

func psTestPairedConsole(t *testing.T) {
	t.Helper()
	psUseTempHosts(t)
	oldOptsHost := psOptsHost
	t.Cleanup(func() { psOptsHost = oldOptsHost })
	c := psConsole{Host: "192.168.0.10", Name: "Living room", RegistKey: "a", RPKey: "b", LoginPIN: "1234"}
	psHostsMu.Lock()
	err := savePSHostsLocked(psHostsFile{Hosts: []psConsole{c}})
	psHostsMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	chiakiPanel = chiakiPanelState{open: true, page: psPageConsoles, consoles: []psConsole{c}}
}

func psTestConfirmStartsOnKeep(t *testing.T) {
	t.Helper()
	if chiakiPanel.page != psPageConsoleConfirm {
		t.Fatalf("page = %d, want confirmation", chiakiPanel.page)
	}
	rows := chiakiRows()
	if rows[chiakiPanel.cursor].action != psActionOptsKeep {
		t.Fatalf("confirmation opened on %q", rows[chiakiPanel.cursor].label)
	}
}

func TestPSConsoleOptionsConfirmDestructiveActions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action psPanelAction
	}{
		{"saved passcode", psActionOptsClearPIN},
		{"pairing keys", psActionOptsForget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			psTestPairedConsole(t)
			psPanelOpenConsoleOptionsFor("192.168.0.10")
			psTestChooseAction(t, tc.action)
			psTestConfirmStartsOnKeep(t)
			chiakiPanelActivate() // Keep is the first A.
			if chiakiPanel.page != psPageConsoleOptions {
				t.Fatalf("Keep returned to page %d", chiakiPanel.page)
			}
			if state := loadPSHosts(); len(state.Hosts) != 1 || state.Hosts[0].LoginPIN != "1234" {
				t.Fatalf("Keep changed the console: %+v", state.Hosts)
			}

			psTestChooseAction(t, tc.action)
			psTestConfirmStartsOnKeep(t)
			psTestChooseAction(t, psActionOptsConfirm)
			state := loadPSHosts()
			if tc.action == psActionOptsClearPIN {
				if len(state.Hosts) != 1 || state.Hosts[0].LoginPIN != "" || chiakiPanel.page != psPageConsoleOptions {
					t.Fatalf("confirmed passcode removal: hosts=%+v page=%d", state.Hosts, chiakiPanel.page)
				}
			} else if len(state.Hosts) != 0 || chiakiPanel.page != psPageConsoles {
				t.Fatalf("confirmed forget: hosts=%+v page=%d", state.Hosts, chiakiPanel.page)
			}
		})
	}
}

func TestPSConsoleYForgetAsksFirst(t *testing.T) {
	psTestPairedConsole(t)
	psPanelForget()
	psTestConfirmStartsOnKeep(t)
	psPanelBack()
	if chiakiPanel.page != psPageConsoles || len(loadPSHosts().Hosts) != 1 {
		t.Fatal("B from the Y confirmation did not leave the console paired on the list")
	}
}

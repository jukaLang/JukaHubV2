package main

// stockcheck.go — "another firmware changed the stock system": the
// modified-stock warning.
//
// MinUI, NextUI and TortOS 1.x install by replacing the handheld's own
// launcher on the internal memory, /usr/trimui/bin/runtrimui.sh, with one
// that reads the card's .tmp_update (autostart.go explains why JukaHub never
// does). Switching to JukaHub or back to the stock menu leaves that launcher
// behind, and a handheld that then boots oddly, loses its menu after a card
// swap or will not take a stock update looks broken for no visible reason.
//
// Once per start, in the background after the first frame, on a TrimUI only
// (not under JukaHub OS's own system), this reads two stock files and changes
// nothing: the launcher script (its first 64 KB) and the stock menu,
// /usr/trimui/bin/MainUI (it is only looked for: JukaHub's boot hook covers it
// with a bind mount for a moment at boot, never removes it). The launcher is
// changed when it mentions .tmp_update or a custom firmware by name, or when
// either file is missing while the other is there; both missing is a system
// JukaHub does not know, and says nothing.
//
// A change is one warning toast (and a line in the log), shown once for that
// exact launcher: its fingerprint is kept in stockCheckSeen (bookkeeping), so
// the next start says nothing until the launcher changes again. Settings →
// About → Stock system shows the answer every time.

import (
	"fmt"
	"hash/fnv"
	"os"
	"runtime"
	"strings"
	"sync"
)

// stockCheckSeenKey keeps the fingerprint of the change already shown.
const stockCheckSeenKey = "stockCheckSeen"

// stockCheckMarkers name the firmwares by what their launcher mentions, the
// more specific first (NextUI's scripts still say MinUI). The last one, with
// no name, is the MinUI family's card hook itself.
var stockCheckMarkers = []struct {
	needle string
	name   string
}{
	{"nextui", "NextUI"},
	{"tortos", "TortOS"},
	{"onion", "Onion"},
	{"spruce", "spruceOS"},
	{"minui", "MinUI"},
	{".tmp_update", ""},
}

// stockCheckResult is what the check found.
type stockCheckResult struct {
	checked     bool
	changed     bool
	by          string // the firmware's name, "" when unknown
	why         string // for the log
	fingerprint string
}

var stockCheck struct {
	mu  sync.Mutex
	res stockCheckResult
}

func init() {
	cfgdocRuntimeKeys[stockCheckSeenKey] = true
	deferStartupTask("stock-check", func() {
		if !stockCheckSupported() || appConfig == nil {
			return
		}
		seen, _ := customString(appConfig.Variables.Custom, stockCheckSeenKey)
		go stockCheckRun(seen)
	})
}

// stockCheckSupported reports whether there is a stock system to look at.
func stockCheckSupported() bool {
	return runtime.GOOS == "linux" && IsTrimuiDevice() && !jukaosNativeActive()
}

// stockCheckRun reads the files, keeps the answer and warns once per
// fingerprint. Its own goroutine.
func stockCheckRun(seen string) {
	defer func() {
		if r := recover(); r != nil {
			logWarn("stockcheck", "check stopped: %v", r)
		}
	}()
	script, serr := readFileHead(trimuiLauncherScript, 64<<10)
	_, merr := os.Stat(deviceMainUIPath)
	res := stockCheckClassify(script, serr == nil, merr == nil)
	stockCheck.mu.Lock()
	stockCheck.res = res
	stockCheck.mu.Unlock()
	switch {
	case !res.checked:
		logInfo("stockcheck", "no stock start-up files found; not checked")
		return
	case !res.changed:
		logInfo("stockcheck", "stock start-up files unchanged")
		return
	}
	logWarn("stockcheck", "the stock system was changed by %s: %s", stockCheckByName(res.by), res.why)
	if res.fingerprint == seen {
		return
	}
	tbxPost(func() {
		msg := "Another firmware changed this handheld's stock system — if it misbehaves, reflash the stock firmware"
		if res.by != "" {
			msg = res.by + " changed this handheld's stock system — if it misbehaves, reflash the stock firmware"
		}
		ShowToast(msg, ToastKindWarning)
		if appConfig == nil {
			return
		}
		if appConfig.Variables.Custom == nil {
			appConfig.Variables.Custom = map[string]interface{}{}
		}
		appConfig.Variables.Custom[stockCheckSeenKey] = res.fingerprint
		saveUserConfigDebounced(userConfigFromVariables(appConfig))
	})
}

// stockCheckClassify judges the launcher script (when it could be read) and
// whether the stock menu is there. Pure.
func stockCheckClassify(script []byte, scriptOK, mainUIOK bool) stockCheckResult {
	if !scriptOK && !mainUIOK {
		return stockCheckResult{}
	}
	res := stockCheckResult{checked: true}
	switch {
	case !scriptOK:
		res.changed, res.why = true, trimuiLauncherScript+" is missing"
	case !mainUIOK:
		res.changed, res.why = true, deviceMainUIPath+" is missing"
	default:
		lower := strings.ToLower(string(script))
		for _, m := range stockCheckMarkers {
			if strings.Contains(lower, m.needle) {
				res.changed, res.by = true, m.name
				res.why = trimuiLauncherScript + " mentions " + m.needle
				break
			}
		}
	}
	if res.changed {
		h := fnv.New64a()
		_, _ = h.Write(script)
		fmt.Fprintf(h, "|%t|%t", scriptOK, mainUIOK)
		res.fingerprint = fmt.Sprintf("%016x", h.Sum64())
	}
	return res
}

// stockCheckByName is the firmware's name for a sentence.
func stockCheckByName(by string) string {
	if by == "" {
		return "another firmware"
	}
	return by
}

// stockCheckAboutRow is Settings → About → Stock system.
func stockCheckAboutRow() setRow {
	return setRow{
		ID: "stockSystem", Label: "Stock system", Kind: setKindInfo,
		Visible: func(*Config) bool { return stockCheckSupported() },
		Value: func(*Config) string {
			stockCheck.mu.Lock()
			res := stockCheck.res
			stockCheck.mu.Unlock()
			switch {
			case !res.checked:
				return "Not checked"
			case res.changed:
				return "Changed by " + stockCheckByName(res.by)
			}
			return "Unchanged"
		},
		Detail: func(*Config) string {
			stockCheck.mu.Lock()
			changed := stockCheck.res.changed
			stockCheck.mu.Unlock()
			if changed {
				return "Another firmware replaced the start-up files · reflash stock firmware if the handheld misbehaves"
			}
			return "Whether another firmware changed the handheld's own start-up files"
		},
	}
}

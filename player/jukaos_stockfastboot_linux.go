package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var jukaosStockFastBootMu sync.Mutex

// jukaosStockFastBootSwitch installs or restores the verified stock Smart Pro
// hooks when the user changes OS boot settings. Unsupported firmware simply
// uses the regular SD-card hook; the script checks exact stock file hashes.
func jukaosStockFastBootSwitch(on bool) error {
	jukaosStockFastBootMu.Lock()
	defer jukaosStockFastBootMu.Unlock()
	if !autostartIsTrimui() || DetectDevice() != DeviceTrimuiSmartPro || os.Geteuid() != 0 {
		return nil
	}
	appDir := jukaosAppDir()
	if on && (!jukaosLoadState(appDir).Enabled || !autoStartInstalled()) {
		return nil // a settings change superseded a queued post-frame install
	}
	script := filepath.Join(appDir, "stock_fastboot", "stock-fastboot.sh")
	if _, err := os.Stat(script); os.IsNotExist(err) {
		return nil // installed app predates the optional fast boot package
	} else if err != nil {
		return err
	}
	mode := "restore"
	if on {
		mode = "install"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/bin/sh", script, mode).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

package main

import (
	"context"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Detection before main initialises logging used to deadlock: its identity
// message tried to set the logger up, which asked DetectDevice for a default
// log level while detection's sync.Once was still held.
func TestDetectDeviceBeforeInitLogging(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("device identity logging is Linux-only")
	}
	if os.Getenv("JUKAHUB_TEST_DETECT_BEFORE_LOGGING") == "1" {
		logger = nil
		deviceModelOnce = sync.Once{}
		deviceIdentityLogOnce = sync.Once{}
		appLog.Clear()
		DetectDevice()
		InitLogging()
		InitLogging()
		if got := strings.Count(appLog.String(), "device: "); got != 1 {
			t.Fatalf("device identity logged %d times, want once", got)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := hiddenCommandContext(ctx, os.Args[0], "-test.run=^TestDetectDeviceBeforeInitLogging$")
	cmd.Env = append(os.Environ(), "JUKAHUB_TEST_DETECT_BEFORE_LOGGING=1", "JUKAHUB_DEVICE_MODEL=", "JUKAHUB_LOG_LEVEL=info")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("device detection hung before logging: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("device detection before logging: %v\n%s", err, out)
	}
}

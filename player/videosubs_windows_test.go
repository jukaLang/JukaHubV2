//go:build windows

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const subtitleTreeRoleEnv = "JUKAHUB_TEST_SUBTITLE_TREE_ROLE"
const subtitleTreeDirEnv = "JUKAHUB_TEST_SUBTITLE_TREE_DIR"

// The test binary stands in for yt-dlp's one-file launcher and its unpacked
// child. Killing just the launcher leaves the child with the temp file open.
func TestSubtitleCancellationKillsWindowsProcessTree(t *testing.T) {
	if role := os.Getenv(subtitleTreeRoleEnv); role != "" {
		subtitleTreeHelper(t, role, os.Getenv(subtitleTreeDirEnv))
		return
	}

	dir, err := os.MkdirTemp("", "juka-subs-tree-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := hiddenCommandContext(ctx, os.Args[0], "-test.run=^TestSubtitleCancellationKillsWindowsProcessTree$")
	cmd.Env = append(os.Environ(), subtitleTreeRoleEnv+"=launcher", subtitleTreeDirEnv+"="+dir)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.Cancel = func() error { return termKillTree(cmd) }
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()

	var leafPID int
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(filepath.Join(dir, "leaf.pid"))
		if readErr == nil {
			leafPID, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if leafPID > 0 {
				if _, readyErr := os.Stat(filepath.Join(dir, "ready")); readyErr == nil {
					break
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if leafPID <= 0 {
		t.Fatal("yt-dlp stand-in did not start its child")
	}
	// Retain a handle across taskkill so a reused PID cannot pass this check.
	leaf, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(leafPID))
	if err != nil {
		t.Fatalf("open child process: %v", err)
	}
	defer windows.CloseHandle(leaf)
	defer func() {
		state, waitErr := windows.WaitForSingleObject(leaf, 0)
		if waitErr == nil && state == uint32(windows.WAIT_TIMEOUT) {
			kill := hiddenCommand("taskkill", "/T", "/F", "/PID", strconv.Itoa(leafPID))
			_ = kill.Run()
		}
	}()
	if _, err := os.Stat(filepath.Join(dir, "ready")); err != nil {
		t.Fatalf("yt-dlp stand-in did not open its temp file: %v", err)
	}

	cancel()
	_ = cmd.Wait()
	state, err := windows.WaitForSingleObject(leaf, 3000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		t.Fatalf("yt-dlp child survived cancellation: state=%d err=%v", state, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("subtitle temp folder still in use: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("subtitle temp folder remains: %v", err)
	}
}

func subtitleTreeHelper(t *testing.T, role, dir string) {
	switch role {
	case "launcher":
		child := hiddenCommand(os.Args[0], "-test.run=^TestSubtitleCancellationKillsWindowsProcessTree$")
		child.Env = append(os.Environ(), subtitleTreeRoleEnv+"=child", subtitleTreeDirEnv+"="+dir)
		child.Stdout, child.Stderr = io.Discard, io.Discard
		child.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: termCreateNoWindow}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "leaf.pid"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = child.Wait()
	case "child":
		file, err := os.OpenFile(filepath.Join(dir, "held.tmp"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
	default:
		t.Fatalf("unknown helper role %q", role)
	}
}


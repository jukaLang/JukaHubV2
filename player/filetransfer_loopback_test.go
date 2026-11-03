package main

// The File Explorer's "connect and transfer" feature has two halves in this
// package: ssh_server.go serves SFTP, and filetransfer.go dials another device
// as a client. Each half has its own tests; this one joins them, because the
// pair is only proven to work when the client that ships can talk to the server
// that ships. Everything runs on 127.0.0.1 with an ephemeral port, so no
// network and no second machine is involved.

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSFTPLoopbackTransferAgainstOwnServer(t *testing.T) {
	// The console's side of the share: a folder with a song to pull and a
	// folder for what gets pushed.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "media"), 0o755); err != nil {
		t.Fatalf("mkdir media: %v", err)
	}
	const song = "song on the card"
	mustWrite(t, filepath.Join(root, "media", "song.txt"), song)

	startTestSSHServer(t, root)

	// feRemoteDial refuses to run while the network monitor says the device is
	// offline. Looping back needs no internet, so the monitor is put in its
	// optimistic state for the test and restored afterwards.
	netMon.mu.Lock()
	prevOnline, prevChecked := netMon.online, netMon.checked
	netMon.online, netMon.checked = true, true
	netMon.mu.Unlock()
	t.Cleanup(func() {
		netMon.mu.Lock()
		netMon.online, netMon.checked = prevOnline, prevChecked
		netMon.mu.Unlock()
	})

	// The host-key pin is per run; start from a clean pinning so the assertions
	// below describe this dial and nothing an earlier test trusted.
	prevPins := feRemoteHostKeys
	feRemoteHostKeys = map[string]string{}
	t.Cleanup(func() { feRemoteHostKeys = prevPins })

	target := feRemote{User: sshUserName, Host: "127.0.0.1", Port: sshSrv.Port(), Path: "/"}
	addr := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))

	// --- connect -----------------------------------------------------------
	if rejected, err := feRemoteDial(target, "definitely-wrong"); err == nil {
		rejected.Close()
		t.Fatal("the client connected with the wrong password")
	}
	// The host key arrives before authentication, so even the refused attempt
	// pins it. That is the fingerprint the client compares against on the next
	// dial, and it must be the one the server shows the user.
	if got := feRemoteHostKeys[addr]; got != sshSrv.Fingerprint() {
		t.Fatalf("pinned after a refused login = %q, server advertises %q", got, sshSrv.Fingerprint())
	}

	client, err := feRemoteDial(target, sshSrv.Password())
	if err != nil {
		t.Fatalf("connect to the app's own server: %v", err)
	}
	t.Cleanup(client.Close)

	if client.sf == nil {
		t.Fatal("connect succeeded without an SFTP client")
	}
	if client.trusted != "" {
		t.Fatalf("an already-pinned device reported a new key: %q", client.trusted)
	}

	// With no pin yet, a first contact records the fingerprint it was shown.
	delete(feRemoteHostKeys, addr)
	fresh, err := feRemoteDial(target, sshSrv.Password())
	if err != nil {
		t.Fatalf("connect with a clean pin: %v", err)
	}
	if fresh.trusted != sshSrv.Fingerprint() {
		fresh.Close()
		t.Fatalf("first contact pinned %q, server advertises %q", fresh.trusted, sshSrv.Fingerprint())
	}
	fresh.Close()

	// A folder must stat as itself, not as its first child: clients use stat to
	// decide whether a path exists and whether it is a directory.
	if info, err := client.sf.Stat("/media"); err != nil || !info.IsDir() {
		t.Fatalf("stat /media = %+v, %v; want a directory", info, err)
	}
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatalf("mkdir empty: %v", err)
	}
	if info, err := client.sf.Stat("/empty"); err != nil || !info.IsDir() {
		t.Fatalf("stat an empty folder = %+v, %v; want a directory", info, err)
	}

	entries, err := client.List("/")
	if err != nil {
		t.Fatalf("list the share root: %v", err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name == "media" && entry.IsDir {
			found = true
		}
	}
	if !found {
		t.Fatalf("root listing has no media folder: %+v", entries)
	}

	// A second dial to the same address re-uses the pinned key instead of
	// trusting whatever arrives this time.
	again, err := feRemoteDial(target, sshSrv.Password())
	if err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if again.trusted != "" {
		again.Close()
		t.Fatalf("already-pinned device reported a new key: %q", again.trusted)
	}
	again.Close()

	// --- download ----------------------------------------------------------
	localDir := t.TempDir()
	localSong := filepath.Join(localDir, "song.txt")
	var got, total int64
	if err := client.Download("/media/song.txt", localSong, func(done, size int64) {
		got, total = done, size
	}); err != nil {
		t.Fatalf("download: %v", err)
	}
	if data, err := os.ReadFile(localSong); err != nil || string(data) != song {
		t.Fatalf("downloaded file = %q, %v; want %q", data, err, song)
	}
	if got != int64(len(song)) || total != int64(len(song)) {
		t.Fatalf("download progress = %d/%d, want %d/%d", got, total, len(song), len(song))
	}

	// --- upload ------------------------------------------------------------
	const note = "written by the console"
	localNote := filepath.Join(t.TempDir(), "note.txt")
	mustWrite(t, localNote, note)
	if err := client.Upload(localNote, "/media/note.txt", nil); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if landed, err := os.ReadFile(filepath.Join(root, "media", "note.txt")); err != nil || string(landed) != note {
		t.Fatalf("uploaded file on the share = %q, %v; want %q", landed, err, note)
	}

	// Uploading a folder walks it; the remote side mirrors the names.
	album := filepath.Join(t.TempDir(), "album")
	if err := os.MkdirAll(album, 0o755); err != nil {
		t.Fatalf("mkdir album: %v", err)
	}
	mustWrite(t, filepath.Join(album, "one.txt"), "one")
	mustWrite(t, filepath.Join(album, "two.txt"), "two")
	if err := client.Upload(album, "/media/album", nil); err != nil {
		t.Fatalf("upload folder: %v", err)
	}
	for name, want := range map[string]string{"one.txt": "one", "two.txt": "two"} {
		if landed, err := os.ReadFile(filepath.Join(root, "media", "album", name)); err != nil || string(landed) != want {
			t.Fatalf("uploaded folder file %s = %q, %v; want %q", name, landed, err, want)
		}
	}

	// The transfers travelled through the app's server: it is still listening,
	// and stopping it drops the session the client is holding.
	if !sshSrv.Running() {
		t.Fatal("server stopped during the transfers")
	}
	client.Close()
	sshSrv.Stop()
	if _, err := client.List("/"); err == nil {
		t.Fatal("the client still lists files after the server stopped")
	}

	// A changed host key is refused rather than trusted again.
	if err := sshSrv.startAt(root, "127.0.0.1:0"); err != nil {
		t.Fatalf("restart server: %v", err)
	}
	changed := net.JoinHostPort(target.Host, strconv.Itoa(sshSrv.Port()))
	feRemoteHostKeys[changed] = "SHA256:some-other-device"
	impostor, err := feRemoteDial(feRemote{User: sshUserName, Host: target.Host, Port: sshSrv.Port()}, sshSrv.Password())
	if err == nil {
		impostor.Close()
		t.Fatal("a changed host key was accepted")
	}
	if !strings.Contains(err.Error(), "host key changed") {
		t.Fatalf("refusal = %v, want it to name the changed host key", err)
	}
}

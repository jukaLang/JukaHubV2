package main

// filetransfer.go — moving files between this console and another device.
//
// The hub already serves files: ssh_server.go answers SFTP on port 2222 and
// ftp_server.go answers FTP, so a laptop can browse the card. What was missing
// is the other direction — reaching another machine from the handheld, so files
// can be pulled onto the card or pushed off it without a PC in between.
//
// This file is the client half:
//   - one address line ("user@host:port/folder", or the same with an sftp://
//     prefix) parsed into a target,
//   - a password prompt that is never written to disk,
//   - a remote panel that browses the other device's folders and downloads the
//     highlighted file into the folder the Files screen is showing,
//   - "upload" (Y) pushing the local highlighted entry to the remote folder,
//   - progress, reported by the reading side so the copy is never a silent
//     freeze.
//
// Host keys are trusted on first use for the run: a device reached again in the
// same session must present the same key, and a changed one is refused instead
// of being silently accepted. Nothing is stored on disk, so a wiped session
// re-trusts rather than pinning a key this build did not see.
//
// Network work runs on goroutines that only post events; the panel, the
// progress line and the toasts are all written on the SDL thread by
// feRemoteDrain, the same contract partyui.go follows.

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/veandco/go-sdl2/sdl"
	"golang.org/x/crypto/ssh"
)

// --- The Files keyboard session -------------------------------------------

// filesEditKind names what the Files screen's keyboard session is editing. One
// keyboard target serves all four fields, the way the PS Remote panel drives
// its several fields through psPanelEditField.
type filesEditKind int

const (
	filesEditAddress filesEditKind = iota
	filesEditPassword
	filesEditNewFolder
	filesEditRename
)

var (
	filesKeyboard    filesEditKind
	filesInputBuffer string
	// filesRenamePath is the entry the rename prompt was opened for.
	filesRenamePath string
	// feRemotePassword holds the passphrase for this session only. It is never
	// written to the config, and it is dropped when the panel is closed.
	feRemotePassword string
)

// filesSubmitInput applies whatever the Files keyboard was opened for. The
// keyboard has already closed by the time it runs.
func filesSubmitInput(text string) {
	switch filesKeyboard {
	case filesEditAddress:
		feRemoteAddressSubmitted(appConfig, text)
	case filesEditPassword:
		// Passphrases are not trimmed: a leading or trailing space is part of
		// the secret, and quietly removing it would fail with "wrong password".
		feRemotePassword = text
		if feRemotePanel.tgt.Host == "" {
			showToast("Enter a device address first", ToastInfo())
			return
		}
		feRemotePanel.open = true
		feRemoteConnectAsync()
	case filesEditNewFolder:
		created, err := feNewFolder(appConfig, text)
		if err != nil {
			showToast("Cannot create the folder: "+err.Error(), ToastError())
			return
		}
		feListDirectory(appConfig)
		feFocusPath(appConfig, created)
		showToast("Created "+filepath.Base(created), ToastSuccess())
		PlayActivateSound()
	case filesEditRename:
		renamed, err := feRenameEntry(appConfig, filesRenamePath, text)
		if err != nil {
			showToast("Cannot rename: "+err.Error(), ToastError())
			return
		}
		feListDirectory(appConfig)
		feFocusPath(appConfig, renamed)
		showToast("Renamed to "+filepath.Base(renamed), ToastSuccess())
		PlayActivateSound()
	}
}

// --- The address ----------------------------------------------------------

// feRemote is one device to transfer with. The path is the folder the remote
// panel opens on.
type feRemote struct {
	User string
	Host string
	Port int
	Path string
}

// key identifies the device for the host-key pin and for the panel's subtitle.
func (r feRemote) key() string {
	port := r.Port
	if port <= 0 {
		port = sshDefaultPort
	}
	return fmt.Sprintf("%s@%s:%d", r.User, r.Host, port)
} // address is the canonical form, which is what the field is seeded with next
// time instead of whatever spelling was typed. The folder is cleaned on the way
// out as well as on the way in, so an address that was pasted or built by hand
// cannot carry a ".." segment back to the field.
func (r feRemote) address() string {
	dir := path.Clean("/" + strings.TrimPrefix(strings.TrimSpace(r.Path), "/"))
	if dir == "." || dir == "/" {
		return r.key() + "/"
	}
	return r.key() + dir
}

// parseFeRemote reads the address line. Accepted forms, in the order the
// placeholder offers them:
//
//	user@host:port/folder
//	host
//	sftp://user@host:port/folder
//
// A missing user is the hub's own SFTP account, and a missing port is the port
// that account listens on — so "192.168.1.7" is enough to reach another
// console, which is the common case.
func parseFeRemote(input string) (feRemote, error) {
	text := strings.TrimSpace(input)
	if text == "" {
		return feRemote{}, errors.New("the address is empty")
	}
	if idx := strings.Index(text, "://"); idx >= 0 {
		scheme := strings.ToLower(text[:idx])
		if scheme != "sftp" {
			return feRemote{}, fmt.Errorf("only sftp:// addresses work, not %s://", scheme)
		}
		text = text[idx+3:]
	}
	target := feRemote{User: sshUserName, Port: sshDefaultPort, Path: "/"}
	if slash := strings.IndexByte(text, '/'); slash >= 0 {
		target.Path = text[slash:]
		text = text[:slash]
	}
	if at := strings.LastIndexByte(text, '@'); at >= 0 {
		target.User = text[:at]
		text = text[at+1:]
	}
	if text == "" {
		return feRemote{}, errors.New("the address has no device name")
	}
	host := text
	if colon := strings.LastIndexByte(text, ':'); colon >= 0 {
		// "user@host:/folder" is a shape people type: an empty port is the
		// default one, not a typo.
		portText := text[colon+1:]
		if portText != "" {
			port, err := strconv.Atoi(portText)
			if err != nil || port <= 0 || port > 65535 {
				return feRemote{}, fmt.Errorf("%q is not a port", portText)
			}
			target.Port = port
		}
		host = text[:colon]
	}
	if host == "" {
		return feRemote{}, errors.New("the address has no device name")
	}
	if strings.ContainsAny(host, " \t") {
		return feRemote{}, errors.New("the device name contains a space")
	}
	if strings.ContainsAny(target.User, " \t@/") || target.User == "" {
		return feRemote{}, errors.New("the user name is not valid")
	}
	target.Host = host
	target.Path = path.Clean(target.Path)
	if !strings.HasPrefix(target.Path, "/") {
		target.Path = "/" + target.Path
	}
	return target, nil
}

// feRemoteAddressHint is the sheet's right-hand detail for "Connect to device":
// the last address used, so a repeat transfer is recognisable, or the shape of
// one when there is nothing to remember yet.
func feRemoteAddressHint() string {
	if feRemotePanel.tgt.Host != "" {
		return feRemotePanel.tgt.key()
	}
	if appConfig != nil {
		if last, ok := appConfig.Variables.Custom["fe_remote_last"].(string); ok && strings.TrimSpace(last) != "" {
			return strings.TrimSpace(last)
		}
	}
	return fmt.Sprintf("%s@device-ip:%d", sshUserName, sshDefaultPort)
}

// openFeRemoteAddressEntry asks for an address, seeded with the last one so a
// repeat device is a press rather than a retype on a D-pad.
func openFeRemoteAddressEntry(config *Config) {
	seed := ""
	if config != nil {
		seed, _ = config.Variables.Custom["fe_remote_last"].(string)
	}
	vkOpenFilesInput(filesEditAddress, seed)
}

// feRemoteAddressSubmitted parses the typed address and moves on to the
// password, or straight to the connection when this session already has one.
func feRemoteAddressSubmitted(config *Config, text string) {
	target, err := parseFeRemote(text)
	if err != nil {
		showToast("That address: "+err.Error(), ToastError())
		return
	}
	// One network step at a time. Resetting the panel here cleared its busy
	// flag under a step still running, so a second connect started beside it:
	// the session that finished last replaced the other without closing it,
	// and uploads could go to the device the panel no longer showed.
	if feRemotePanel.busy {
		showToast("Wait for "+feRemotePanel.tgt.key()+" to finish first", ToastInfo())
		return
	}
	feRemoteCloseConn()
	feRemotePanel = feRemotePanelState{open: true, tgt: target, status: "Connecting to " + target.key() + "…"}
	if config != nil {
		if config.Variables.Custom == nil {
			config.Variables.Custom = map[string]interface{}{}
		}
		config.Variables.Custom["fe_remote_last"] = target.address()
	}
	if feRemotePassword == "" {
		vkOpenFilesInput(filesEditPassword, "")
		return
	}
	feRemoteConnectAsync()
}

// --- The client -----------------------------------------------------------

// feRemoteClient is one open SFTP session. The SSH client is kept because
// closing it is what ends the session; the SFTP client is what transfers.
type feRemoteClient struct {
	conn *ssh.Client
	sf   *sftp.Client
	// trusted is the fingerprint this run pinned on first contact, empty when
	// the device was already known.
	trusted string
}

var (
	feRemoteConnMu sync.Mutex
	feRemoteConn   *feRemoteClient
	// feRemoteHostKeys pins a device's host key for the run. A second device
	// that answers with a different key under the same address is refused: the
	// cheap defence against a same-LAN impersonation, without the key-management
	// screen a full SSH client would need.
	feRemoteHostKeys = map[string]string{}
	// feRemoteHostKeysMu guards the pins: the check runs on the dialling
	// goroutine.
	feRemoteHostKeysMu sync.Mutex
)

// feTransferErr keeps a transfer error to its first line: an SFTP failure can
// carry the whole server banner, and the panel has one line for it.
func feTransferErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(firstLine(err.Error()))
}

func feRemoteFingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// feRemoteHostKeyCheck implements trust-on-first-use per run.
func feRemoteHostKeyCheck(hostname string, key ssh.PublicKey, pinned *string) error {
	fingerprint := feRemoteFingerprint(key)
	feRemoteHostKeysMu.Lock()
	defer feRemoteHostKeysMu.Unlock()
	if known, ok := feRemoteHostKeys[hostname]; ok {
		if known != fingerprint {
			return fmt.Errorf("the device's host key changed (was %s, now %s)", known, fingerprint)
		}
		return nil
	}
	feRemoteHostKeys[hostname] = fingerprint
	*pinned = fingerprint
	return nil
}

// feRemoteDial opens the session. The password is the caller's; it is not
// stored anywhere but in the panel's session field.
func feRemoteDial(target feRemote, password string) (*feRemoteClient, error) {
	if !IsOnline() {
		return nil, errors.New("no network connection")
	}
	addr := net.JoinHostPort(target.Host, strconv.Itoa(target.Port))
	var pinned string
	config := &ssh.ClientConfig{
		User: target.User,
		Auth: []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			return feRemoteHostKeyCheck(addr, key, &pinned)
		},
		Timeout: 10 * time.Second,
	}
	conn, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, feTransferErr(err)
	}
	client, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, feTransferErr(err)
	}
	return &feRemoteClient{conn: conn, sf: client, trusted: pinned}, nil
}

func (c *feRemoteClient) Close() {
	if c == nil {
		return
	}
	if c.sf != nil {
		c.sf.Close()
	}
	if c.conn != nil {
		c.conn.Close()
	}
}

// feRemoteTakeConn returns the open session, or nil. It is the only reader of
// the shared handle, so a transfer started from the UI thread and the panel
// drawing its state can never race on it.
func feRemoteTakeConn() *feRemoteClient {
	feRemoteConnMu.Lock()
	defer feRemoteConnMu.Unlock()
	return feRemoteConn
}

// feRemoteStoreConn keeps c as the session and closes whichever one it
// replaces, so a session is never dropped while still open.
func feRemoteStoreConn(c *feRemoteClient) {
	feRemoteConnMu.Lock()
	prev := feRemoteConn
	feRemoteConn = c
	feRemoteConnMu.Unlock()
	if prev != nil && prev != c {
		prev.Close()
	}
}

func feRemoteCloseConn() {
	feRemoteConnMu.Lock()
	c := feRemoteConn
	feRemoteConn = nil
	feRemoteConnMu.Unlock()
	c.Close()
}

func feRemoteConnected() bool {
	feRemoteConnMu.Lock()
	defer feRemoteConnMu.Unlock()
	return feRemoteConn != nil
}

// feRemoteEntry is one row of the remote listing.
type feRemoteEntry struct {
	Name  string
	Path  string
	IsDir bool
	Size  int64
	Mod   time.Time
}

// List reads one remote folder, directories first and ".." before them. The
// remote side is POSIX, so its own path package joins the names.
func (c *feRemoteClient) List(dir string) ([]feRemoteEntry, error) {
	if dir == "" {
		dir = "/"
	}
	infos, err := c.sf.ReadDir(dir)
	if err != nil {
		return nil, feTransferErr(err)
	}
	entries := make([]feRemoteEntry, 0, len(infos)+1)
	if parent := path.Dir(dir); parent != dir {
		entries = append(entries, feRemoteEntry{Name: "..", Path: parent, IsDir: true})
	}
	for _, info := range infos {
		entries = append(entries, feRemoteEntry{
			Name:  info.Name(),
			Path:  path.Join(dir, info.Name()),
			IsDir: info.IsDir(),
			Size:  info.Size(),
			Mod:   info.ModTime(),
		})
	}
	sortRemoteEntries(entries)
	return entries, nil
}

// sortRemoteEntries keeps folders above files and ".." first, the same order
// the local list uses so the two lists read alike.
func sortRemoteEntries(entries []feRemoteEntry) {
	sortStable := func(less func(i, j int) bool) {
		for i := 1; i < len(entries); i++ {
			for j := i; j > 0 && less(j, j-1); j-- {
				entries[j], entries[j-1] = entries[j-1], entries[j]
			}
		}
	}
	sortStable(func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.Name == ".." || b.Name == ".." {
			return a.Name == ".." && b.Name != ".."
		}
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}

// Download copies one remote file onto the card, reporting bytes as they land.
// A failed copy removes the partial file, so a half-written video never looks
// like a complete one.
func (c *feRemoteClient) Download(remotePath, localPath string, progress func(done, total int64)) error {
	remote, err := c.sf.Open(remotePath)
	if err != nil {
		return feTransferErr(err)
	}
	defer remote.Close()
	var total int64
	if info, err := remote.Stat(); err == nil {
		total = info.Size()
	}
	local, err := os.OpenFile(localPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	buf := make([]byte, 256<<10)
	var done int64
	for {
		n, readErr := remote.Read(buf)
		if n > 0 {
			if _, writeErr := local.Write(buf[:n]); writeErr != nil {
				local.Close()
				os.Remove(localPath)
				return writeErr
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			local.Close()
			os.Remove(localPath)
			return feTransferErr(readErr)
		}
	}
	return local.Close()
}

// Upload copies a local file — or a whole folder — into the remote folder.
func (c *feRemoteClient) Upload(localPath, remotePath string, progress func(done, total int64)) error {
	info, err := os.Stat(localPath)
	if err != nil {
		return err
	}
	if info.IsDir() {
		if err := c.sf.MkdirAll(remotePath); err != nil {
			return feTransferErr(err)
		}
		children, err := os.ReadDir(localPath)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := c.Upload(filepath.Join(localPath, child.Name()), path.Join(remotePath, child.Name()), progress); err != nil {
				return err
			}
		}
		return nil
	}
	source, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer source.Close()
	remote, err := c.sf.Create(remotePath)
	if err != nil {
		return feTransferErr(err)
	}
	buf := make([]byte, 256<<10)
	var done int64
	total := info.Size()
	for {
		n, readErr := source.Read(buf)
		if n > 0 {
			if _, writeErr := remote.Write(buf[:n]); writeErr != nil {
				remote.Close()
				return feTransferErr(writeErr)
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			remote.Close()
			return feTransferErr(readErr)
		}
	}
	return remote.Close()
}

// --- Panel state ----------------------------------------------------------

type feRemotePanelState struct {
	open   bool
	busy   bool
	err    string
	status string
	tgt    feRemote
	path   string
	// entries is the remote folder as last read.
	entries []feRemoteEntry
	cursor  int
	scroll  int
}

var feRemotePanel feRemotePanelState

// feRemotePanelOnScreen reports whether the remote browser is up on the screen
// on display. The panel is a Files session: the folder it lists, the row rects
// it was tapped through and the transfer it started all belong to the Files
// screen, so outside Files it is neither drawn nor given input. Its state is
// kept rather than dropped — an upload started just before the user walked away
// finishes and is reported when they come back — and the transfer pump below
// runs whether or not the panel is on screen, so a hidden panel can never leave
// the connection stuck busy.
func feRemotePanelOnScreen(config *Config) bool {
	return feRemotePanel.open && feSceneActive(config)
}

var feRemoteRowHits []struct {
	index int
	rect  sdl.Rect
}

var feRemoteRect sdl.Rect

// feRemoteJob is the transfer progress line, written by the copying goroutine
// and read by the renderer.
var feRemoteJob struct {
	mu    sync.Mutex
	done  int64
	total int64
	label string
}

func feRemoteSetProgress(label string, done, total int64) {
	feRemoteJob.mu.Lock()
	feRemoteJob.label = label
	feRemoteJob.done = done
	feRemoteJob.total = total
	feRemoteJob.mu.Unlock()
}

func feRemoteProgress() (label string, done, total int64) {
	feRemoteJob.mu.Lock()
	defer feRemoteJob.mu.Unlock()
	return feRemoteJob.label, feRemoteJob.done, feRemoteJob.total
}

type feRemoteEventKind int

const (
	feRemoteListed feRemoteEventKind = iota
	feRemoteFinished
	feRemoteFailed
)

type feRemoteEvent struct {
	kind         feRemoteEventKind
	entries      []feRemoteEntry
	path         string
	message      string
	localChanged bool
}

var feRemoteEvents = make(chan feRemoteEvent, 16)

// feRemoteAsync runs one network step off the SDL thread. Only one runs at a
// time: two transfers at once on a slow card is not a feature.
func feRemoteAsync(label string, work func() (feRemoteEvent, error)) {
	if feRemotePanel.busy {
		return
	}
	feRemotePanel.busy = true
	feRemotePanel.err = ""
	feRemotePanel.status = label
	feRemoteSetProgress(label, 0, 0)
	go func() {
		event, err := work()
		if err != nil {
			feRemoteEvents <- feRemoteEvent{kind: feRemoteFailed, message: err.Error()}
			return
		}
		feRemoteEvents <- event
	}()
}

// feRemoteDrain applies finished work on the SDL thread. It is called once per
// frame by the main loop, whether or not the panel is on screen: work that
// finished while the user was on another scene still has to clear the busy
// flag, or the next connect would be refused for ever.
func feRemoteDrain(config *Config) {
	for {
		select {
		case event := <-feRemoteEvents:
			feRemotePanel.busy = false
			feRemoteSetProgress("", 0, 0)
			switch event.kind {
			case feRemoteFailed:
				feRemotePanel.status = ""
				feRemotePanel.err = event.message
			case feRemoteListed:
				feRemotePanel.err = ""
				feRemotePanel.status = ""
				feRemotePanel.path = event.path
				feRemotePanel.entries = event.entries
				feRemotePanel.cursor = 0
				feRemotePanel.scroll = 0
			case feRemoteFinished:
				feRemotePanel.status = event.message
				feRemotePanel.err = ""
				if event.localChanged {
					feListDirectory(config)
				}
			}
		default:
			return
		}
	}
}

// feRemoteFocus is the highlighted remote row.
func feRemoteFocus() (feRemoteEntry, bool) {
	if feRemotePanel.cursor < 0 || feRemotePanel.cursor >= len(feRemotePanel.entries) {
		return feRemoteEntry{}, false
	}
	return feRemotePanel.entries[feRemotePanel.cursor], true
}

func feRemotePanelMove(delta int) {
	if len(feRemotePanel.entries) == 0 {
		return
	}
	feRemotePanel.cursor += delta
	if feRemotePanel.cursor < 0 {
		feRemotePanel.cursor = len(feRemotePanel.entries) - 1
	}
	if feRemotePanel.cursor >= len(feRemotePanel.entries) {
		feRemotePanel.cursor = 0
	}
	PlayNavSound()
}

// openFeRemotePanel opens the remote browser, connecting first when there is no
// session yet.
func openFeRemotePanel(config *Config) {
	if feRemotePanel.tgt.Host == "" {
		openFeRemoteAddressEntry(config)
		return
	}
	feRemotePanel.open = true
	PlayActivateSound()
	if feRemoteConnected() {
		feRemoteListAsync(feRemotePanel.path)
		return
	}
	feRemoteConnectAsync()
}

func closeFeRemotePanel() {
	if !feRemotePanel.open {
		return
	}
	feRemotePanel.open = false
	feRemoteRowHits = nil
	feRemoteRect = sdl.Rect{}
	PlayBackSound()
}

// feRemoteConnectAsync dials and lists the target's starting folder.
func feRemoteConnectAsync() {
	target := feRemotePanel.tgt
	password := feRemotePassword
	feRemoteAsync("Connecting to "+target.key()+"…", func() (feRemoteEvent, error) {
		feRemoteCloseConn()
		client, err := feRemoteDial(target, password)
		if err != nil {
			return feRemoteEvent{}, err
		}
		feRemoteStoreConn(client)
		entries, err := client.List(target.Path)
		if err != nil {
			return feRemoteEvent{}, err
		}
		message := "Connected to " + target.key()
		if client.trusted != "" {
			message = "Trusted " + target.Host + " (" + client.trusted + ")"
		}
		return feRemoteEvent{kind: feRemoteListed, entries: entries, path: target.Path, message: message}, nil
	})
}

// feRemoteListAsync reads a remote folder on an open session.
func feRemoteListAsync(dir string) {
	feRemoteAsync("Reading "+dir+"…", func() (feRemoteEvent, error) {
		client := feRemoteTakeConn()
		if client == nil {
			return feRemoteEvent{}, errors.New("the device is not connected")
		}
		entries, err := client.List(dir)
		if err != nil {
			return feRemoteEvent{}, err
		}
		return feRemoteEvent{kind: feRemoteListed, entries: entries, path: dir}, nil
	})
}

// feRemoteDownloadAsync pulls the highlighted remote file onto the card, into
// the folder the Files screen is showing.
func feRemoteDownloadAsync(config *Config) {
	entry, ok := feRemoteFocus()
	if !ok || entry.Name == ".." {
		return
	}
	if entry.IsDir {
		feRemoteListAsync(entry.Path)
		return
	}
	dir := feCurrentPath(config)
	remoteName := entry.Name
	remotePath := entry.Path
	size := entry.Size
	feRemoteAsync("Downloading "+remoteName+"…", func() (feRemoteEvent, error) {
		client := feRemoteTakeConn()
		if client == nil {
			return feRemoteEvent{}, errors.New("the device is not connected")
		}
		// The remote name is untrusted: a device could offer a row called
		// "../../.ssh/authorized_keys". safeJoinFile reduces it to a plain name
		// inside the folder on screen.
		local, err := safeJoinFile(dir, remoteName)
		if err != nil {
			return feRemoteEvent{}, err
		}
		if _, statErr := os.Lstat(local); statErr == nil {
			local, err = feFreeTarget(dir, filepath.Base(local))
			if err != nil {
				return feRemoteEvent{}, err
			}
		}
		err = client.Download(remotePath, local, func(done, _ int64) {
			feRemoteSetProgress("Downloading "+remoteName, done, size)
		})
		if err != nil {
			return feRemoteEvent{}, err
		}
		trophyEvent(tkFileTransfer)
		return feRemoteEvent{kind: feRemoteFinished, message: "Downloaded " + remoteName, localChanged: true}, nil
	})
}

// feRemoteUploadAsync pushes the marked local rows (or the highlighted one)
// to the remote folder on screen. Several files travel as one job: the transfer
// is single-flight, and five separate presses would be five connections.
func feRemoteUploadAsync(config *Config) {
	targets := feSelectionTargets(config)
	var locals []string
	skippedFolders := 0
	for _, target := range targets {
		if target.Name == ".." {
			continue
		}
		if target.IsDir {
			// Upload is a file copy; a folder is not something the other end
			// can be handed half of.
			skippedFolders++
			continue
		}
		local, err := feResolveEntry(config, target.Path)
		if err != nil {
			showToast("Cannot upload: "+err.Error(), ToastError())
			continue
		}
		locals = append(locals, local)
	}
	if len(locals) == 0 {
		if skippedFolders > 0 {
			showToast("Folders are not uploaded — mark files instead", ToastInfo())
			return
		}
		showToast("Highlight a file to upload", ToastInfo())
		return
	}
	if skippedFolders > 0 {
		showToast("Uploading files only — "+pluralizeCount(skippedFolders, "folder", "folders")+" skipped", ToastInfo())
	}
	remoteDir := feRemotePanel.path
	label := "Uploading " + filepath.Base(locals[0]) + "…"
	if len(locals) > 1 {
		label = "Uploading " + feItemCountLabel(len(locals)) + "…"
	}
	feRemoteAsync(label, func() (feRemoteEvent, error) {
		client := feRemoteTakeConn()
		if client == nil {
			return feRemoteEvent{}, errors.New("the device is not connected")
		}
		uploaded := 0
		var lastErr error
		for _, local := range locals {
			name := filepath.Base(local)
			size := int64(0)
			if info, statErr := os.Stat(local); statErr == nil {
				size = info.Size()
			}
			err := client.Upload(local, path.Join(remoteDir, name), func(done, _ int64) {
				feRemoteSetProgress("Uploading "+name, done, size)
			})
			if err != nil {
				lastErr = err
				continue
			}
			uploaded++
			trophyEvent(tkFileTransfer)
		}
		if uploaded == 0 && lastErr != nil {
			return feRemoteEvent{}, lastErr
		}
		// Refresh the remote list so the new files are visible where they landed.
		entries, listErr := client.List(remoteDir)
		if listErr != nil {
			entries = nil
		}
		message := "Uploaded " + feItemCountLabel(uploaded)
		if lastErr != nil {
			message = fmt.Sprintf("Uploaded %d, %d failed: %v", uploaded, len(locals)-uploaded, lastErr)
		}
		return feRemoteEvent{kind: feRemoteFinished, message: message, entries: entries, path: remoteDir}, nil
	})
}

// feRemoteUp moves the remote browser one folder up, closing it at the root.
func feRemoteUp() {
	dir := feRemotePanel.path
	if dir == "" || dir == "/" {
		closeFeRemotePanel()
		return
	}
	feRemoteListAsync(path.Dir(dir))
}

// --- Input ----------------------------------------------------------------

func handleFeRemoteKey(e *sdl.KeyboardEvent, config *Config) bool {
	if e == nil || e.Type != sdl.KEYDOWN || !feRemotePanel.open {
		return false
	}
	if virtualKeyboardActive {
		// The address and password prompts float above the panel; the keyboard
		// owns the keys while it is up.
		return false
	}
	switch e.Keysym.Sym {
	case sdl.K_UP:
		feRemotePanelMove(-1)
	case sdl.K_DOWN:
		feRemotePanelMove(1)
	case sdl.K_RETURN, sdl.K_SPACE, sdl.K_a:
		feRemoteDownloadAsync(config)
	case sdl.K_y:
		feRemoteUploadAsync(config)
	case sdl.K_x:
		openFeRemoteAddressEntry(config)
	case sdl.K_ESCAPE, sdl.K_b:
		feRemoteUp()
	default:
		return true
	}
	return true
}

func ctrlFeRemote(e *sdl.ControllerButtonEvent, config *Config) bool {
	if e == nil {
		return true
	}
	if virtualKeyboardActive {
		return false
	}
	switch e.Button {
	case sdl.CONTROLLER_BUTTON_DPAD_UP:
		feRemotePanelMove(-1)
	case sdl.CONTROLLER_BUTTON_DPAD_DOWN:
		feRemotePanelMove(1)
	case sdl.CONTROLLER_BUTTON_A:
		feRemoteDownloadAsync(config)
	case sdl.CONTROLLER_BUTTON_Y:
		feRemoteUploadAsync(config)
	case sdl.CONTROLLER_BUTTON_X:
		openFeRemoteAddressEntry(config)
	case sdl.CONTROLLER_BUTTON_B, sdl.CONTROLLER_BUTTON_BACK:
		feRemoteUp()
	}
	return true
}

func handleFeRemoteMouse(mx, my int32, config *Config) bool {
	if !feRemotePanel.open {
		return false
	}
	if virtualKeyboardActive {
		return false
	}
	for _, hit := range feRemoteRowHits {
		if pointInRect(mx, my, hit.rect) {
			feRemotePanel.cursor = hit.index
			feRemoteDownloadAsync(config)
			return true
		}
	}
	if feRemoteRect.W > 0 && pointInRect(mx, my, feRemoteRect) {
		return true
	}
	closeFeRemotePanel()
	return true
}

// --- Rendering ------------------------------------------------------------

// feRemoteShortAddress is the panel subtitle: the device, and where on it.
func feRemoteShortAddress() string {
	if feRemotePanel.tgt.Host == "" {
		return "No device yet — X for an address"
	}
	dir := feRemotePanel.path
	if dir == "" {
		dir = feRemotePanel.tgt.Path
	}
	return feRemotePanel.tgt.key() + "  " + dir
}

// feRemoteProgressLine says what the copy is doing, in bytes and percent. A
// transfer with no Content-Length (the remote never said how big the file is)
// shows bytes alone rather than a made-up percentage.
func feRemoteProgressLine() string {
	label, done, total := feRemoteProgress()
	if label == "" {
		return ""
	}
	if total <= 0 {
		return label + " · " + humanBytes(done)
	}
	percent := done * 100 / total
	if percent > 100 {
		percent = 100
	}
	return fmt.Sprintf("%s · %s / %s · %d%%", label, humanBytes(done), humanBytes(total), percent)
}

// renderFeRemotePanel draws the remote browser: a folder of the other device,
// with the transfer's progress under the list.
func renderFeRemotePanel(renderer *sdl.Renderer, config *Config) {
	if renderer == nil || !feRemotePanel.open {
		return
	}
	font, _ := getCachedFont(config, "small")
	if font == nil {
		return
	}
	smallH := int32(font.Height())
	rowH := smallH + 14

	subtitle := feRemoteShortAddress()
	subtitleInk := ColorTextSecondary()
	if feRemotePanel.err != "" {
		subtitle = feRemotePanel.err
		subtitleInk = ToastErrorInk()
	} else if line := feRemoteProgressLine(); line != "" {
		subtitle = line
		subtitleInk = ColorTextAccent()
	}
	header := UIPanelHeader("Remote files", subtitle, "files")
	header.SubtitleColor = subtitleInk
	headerH := UIPanelHeaderBand(config, header, 9)
	footerH := 12 + smallH

	panelW := int32(900)
	if panelW > screenWidth-48 {
		panelW = screenWidth - 48
	}
	maxRows := (screenHeight - 120 - headerH - footerH) / rowH
	if maxRows < 3 {
		maxRows = 3
	}
	rows := int32(len(feRemotePanel.entries))
	if rows == 0 {
		rows = 1
	}
	if rows > maxRows {
		rows = maxRows
	}
	panelH := headerH + rows*rowH + footerH
	panelX := (screenWidth - panelW) / 2
	panelY := (screenHeight - panelH) / 2
	feRemoteRect = sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}

	if feRemotePanel.cursor < feRemotePanel.scroll {
		feRemotePanel.scroll = feRemotePanel.cursor
	}
	if feRemotePanel.cursor >= feRemotePanel.scroll+int(maxRows) {
		feRemotePanel.scroll = feRemotePanel.cursor - int(maxRows) + 1
	}
	if feRemotePanel.scroll > len(feRemotePanel.entries)-int(maxRows) {
		feRemotePanel.scroll = len(feRemotePanel.entries) - int(maxRows)
	}
	if feRemotePanel.scroll < 0 {
		feRemotePanel.scroll = 0
	}

	card := sdl.Rect{X: panelX, Y: panelY, W: panelW, H: panelH}
	drawOverlayScrim(renderer)
	drawOverlayCard(renderer, card)
	header = overlayHeaderOnCard(header, card)
	UIPageHeaderDraw(renderer, config, header)

	feRemoteRowHits = feRemoteRowHits[:0]
	y := panelY + headerH
	if len(feRemotePanel.entries) == 0 {
		message := "Connecting…"
		ink := ColorTextTertiary()
		switch {
		case feRemotePanel.err != "":
			message = "Not connected — B closes, X for another address"
			ink = ColorTextSecondary()
		case !feRemotePanel.busy:
			message = "This folder has nothing in it"
		}
		// The panel is narrower than the sentence at a large text scale; keep
		// the empty state inside the card instead of letting it run off.
		message = ellipsize(font, message, panelW-32)
		msgW, _, _ := font.SizeUTF8(message)
		renderText(renderer, config, font, message, ink, panelX+(panelW-int32(msgW))/2, panelY+headerH+12)
	} else {
		for i := feRemotePanel.scroll; i < len(feRemotePanel.entries) && i < feRemotePanel.scroll+int(maxRows); i++ {
			entry := feRemotePanel.entries[i]
			rect := sdl.Rect{X: panelX + 12, Y: y, W: panelW - 24, H: rowH - 4}
			if i == feRemotePanel.cursor {
				fillRoundedRect(renderer, rect.X, rect.Y, rect.W, rect.H, 8, sdl.Color{R: 52, G: 66, B: 110, A: 230})
			}
			// The remote listing is read with the same eyes as the local one, so
			// it uses the same chip, the same name column and the same right-hand
			// size and date rather than a look of its own.
			file := FileEntry{Name: entry.Name, Path: entry.Path, IsDir: entry.IsDir, Size: entry.Size, Mod: entry.Mod}
			badge, badgeInk := feEntryBadge(file)
			chipW := int32(46)
			chipH := int32(20)
			chipX := rect.X + 12
			chipY := rect.Y + (rect.H-chipH)/2
			fillRoundedRect(renderer, chipX, chipY, chipW, chipH, 5, sdl.Color{R: badgeInk.R, G: badgeInk.G, B: badgeInk.B, A: 40})
			if file.IsDir && file.Name != ".." {
				drawFeFolderIcon(renderer, chipX, chipY, badgeInk)
			} else if w, _, err := font.SizeUTF8(badge); err == nil {
				renderText(renderer, config, font, badge, badgeInk, chipX+(chipW-int32(w))/2, rect.Y+7)
			}
			meta := feEntryMeta(file)
			ink := ColorTextPrimary()
			if entry.Name == ".." {
				ink = ColorTextSecondary()
			}
			metaW := int32(0)
			if w, _, err := font.SizeUTF8(meta); err == nil {
				metaW = int32(w)
			}
			nameX := chipX + chipW + 12
			budget := rect.X + rect.W - metaW - 28 - nameX
			if budget < 120 {
				budget = 120
			}
			renderText(renderer, config, font, ellipsize(font, feEntryTitle(file), budget), ink, nameX, rect.Y+7)
			if meta != "" {
				renderText(renderer, config, font, meta, ColorTextTertiary(), rect.X+rect.W-16-metaW, rect.Y+7)
			}
			feRemoteRowHits = append(feRemoteRowHits, struct {
				index int
				rect  sdl.Rect
			}{index: i, rect: rect})
			y += rowH
		}
	}

	hint := hintLine(FooterHint{"A / Enter", "Download"}, FooterHint{"Y", "Upload here"},
		FooterHint{"X", "Address"}, FooterHint{"B / Esc", "Up"})
	if w, _, err := font.SizeUTF8(hint); err == nil && int32(w) > panelW-44 {
		hint = hintLine(FooterHint{"A", "Download"}, FooterHint{"Y", "Upload"}, FooterHint{"B / Esc", "Up"})
	}
	renderText(renderer, config, font, hint, ColorTextSecondary(), panelX+22, panelY+panelH-footerH+8)
}

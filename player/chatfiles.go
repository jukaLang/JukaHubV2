package main

// chatfiles.go — files in the chat.
//
// A message can carry files (ChatMessage.Files): a picture, a save, a log, a
// patch, anything. This file holds what a file is to the chat, the tray of
// files a tab is about to send, the trip to Files that picks them, and what A
// does on a file somebody sent: show it, play it, or save it into Downloads
// and open it from there.
//
// Where files come from:
//
//   - X on the Chat screen (or the paperclip in the composer) opens Files in
//     pick mode: A on a file, or on the rows X marked, puts it in the tray and
//     comes back. Leaving Files without picking ends pick mode, so nothing is
//     attached later by surprise (the old picture flag stayed set until the
//     next picture was opened anywhere in Files, and then attached that);
//   - "Send to chat" in Files' Y sheet, for the marked rows;
//   - a file dropped onto the window while the chat is on screen;
//   - the attach_image and send_to_chat triggers of a custom layout.
//
// The tray belongs to its tab, like the draft. Community sends its files to
// the Discord channel (chatupload.go), Juka AI reads text files (the question
// carries an excerpt), and Saved keeps a reference to files on this device.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

// ChatFile is one file a message carries. A file from the channel has a URL
// (Discord's CDN) and, once saved on this device, a Path; a file this device
// attached has a Path from the start and gains the URL when Discord takes it.
// Only this device ever writes Path: a message built from Discord carries
// links, never local paths.
type ChatFile struct {
	Name string `json:"name"`
	Size int64  `json:"size,omitempty"`
	// Type is the MIME type when it is known ("image/png").
	Type string `json:"type,omitempty"`
	URL  string `json:"url,omitempty"`
	Path string `json:"path,omitempty"`
	// Msg is the Discord message the link belongs to. Discord's file links
	// expire after about a day, and the message is how a fresh one is asked
	// for (chatRefreshAttachmentURL).
	Msg string `json:"msg,omitempty"`
}

const (
	// chatFilesMax is how many files one message carries: Discord's limit.
	chatFilesMax = 10
	// chatAIFilesMax keeps a question for Juka to what one request can show.
	chatAIFilesMax = 3
	// chatDownloadMax bounds a file saved from the channel. Discord's biggest
	// upload is 100 MiB; anything far past that is not a chat attachment.
	chatDownloadMax = 512 << 20
)

// chatFileKind sorts a file by what opens it.
type chatFileKind int

const (
	chatFileOther chatFileKind = iota
	chatFileImage
	chatFileAudio
	chatFileVideo
	chatFileDocument
	chatFileText
	chatFileArchive
)

// chatTextExts are the files Juka AI reads and the cards label as text.
var chatTextExts = map[string]bool{
	".txt": true, ".log": true, ".md": true, ".json": true, ".cfg": true, ".conf": true,
	".ini": true, ".xml": true, ".yml": true, ".yaml": true, ".csv": true, ".lua": true,
	".py": true, ".sh": true, ".js": true, ".html": true, ".css": true, ".go": true,
	".c": true, ".h": true, ".cpp": true, ".srt": true, ".ass": true, ".m3u": true,
	".nfo": true, ".toml": true,
}

// chatArchiveExts are the files a card shows as an archive.
var chatArchiveExts = map[string]bool{
	".zip": true, ".7z": true, ".rar": true, ".tar": true, ".gz": true, ".tgz": true,
	".xz": true, ".bz2": true,
}

// chatFileExt is the lower-case extension of a file's name, or of its local
// path when the name has none.
func chatFileExt(f ChatFile) string {
	ext := strings.ToLower(filepath.Ext(f.Name))
	if ext == "" && f.Path != "" {
		ext = strings.ToLower(filepath.Ext(f.Path))
	}
	return ext
}

// chatFileKindOf sorts a file by its extension, and by the type Discord gave
// it when the name has no extension.
func chatFileKindOf(f ChatFile) chatFileKind {
	ext := chatFileExt(f)
	ctype := strings.ToLower(f.Type)
	switch {
	case ext != "" && isImageExtension(ext), ext == "" && strings.HasPrefix(ctype, "image/"):
		return chatFileImage
	case isAudioFile(f.Name), ext == "" && strings.HasPrefix(ctype, "audio/"):
		return chatFileAudio
	case hasMediaExtension(f.Name), ext == "" && strings.HasPrefix(ctype, "video/"):
		return chatFileVideo
	case ext == ".pdf" || ext == ".epub":
		return chatFileDocument
	case chatTextExts[ext], ext == "" && strings.HasPrefix(ctype, "text/"):
		return chatFileText
	case chatArchiveExts[ext]:
		return chatFileArchive
	}
	return chatFileOther
}

// chatFileIcon is the glyph a file's card and chip carry.
func chatFileIcon(f ChatFile) string {
	switch chatFileKindOf(f) {
	case chatFileImage:
		return "image"
	case chatFileAudio:
		return "music"
	case chatFileVideo:
		return "video"
	case chatFileDocument:
		return "pdf"
	case chatFileText:
		return "text"
	case chatFileArchive:
		return "box"
	}
	switch chatFileExt(f) {
	case ".srm", ".sav", ".state":
		return "saves"
	}
	if filesROMSystem(f.Name) != "" {
		return "rom"
	}
	return "file"
}

// chatMIMEType is the type a file is uploaded as. Text gets a charset so
// Discord previews it; anything unknown is plain bytes.
func chatMIMEType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if chatTextExts[ext] && ext != ".html" && ext != ".js" && ext != ".css" && ext != ".json" && ext != ".xml" {
		return "text/plain; charset=utf-8"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

// chatSizeLabel is a file size the way a chat shows it: "512 B", "14 KB",
// "2.4 MB".
func chatSizeLabel(n int64) string {
	switch {
	case n < 0:
		return ""
	case n < 1024:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1024*1024:
		return strconv.FormatInt((n+512)/1024, 10) + " KB"
	case n < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1024*1024*1024))
}

// chatFileTypeLabel is the short type a card shows: the extension, upper case.
func chatFileTypeLabel(f ChatFile) string {
	ext := strings.TrimPrefix(chatFileExt(f), ".")
	if ext == "" || len(ext) > 6 {
		return "FILE"
	}
	return strings.ToUpper(ext)
}

// chatFileMetaKey is what a file's meta line depends on.
type chatFileMetaKey struct {
	name string
	size int64
}

// chatFileMetas caches the meta lines: the cards ask every frame, and the size
// is formatted with fmt. Render thread only; dropped whole when full.
var chatFileMetas = make(map[chatFileMetaKey]string, 32)

// chatFileMeta is a card's second line: "2.4 MB · ZIP".
func chatFileMeta(f ChatFile) string {
	key := chatFileMetaKey{name: f.Name, size: f.Size}
	if s, ok := chatFileMetas[key]; ok {
		return s
	}
	if len(chatFileMetas) >= 256 {
		chatFileMetas = make(map[chatFileMetaKey]string, 32)
	}
	s := chatFileTypeLabel(f)
	if f.Size > 0 {
		s = chatSizeLabel(f.Size) + " · " + s
	}
	chatFileMetas[key] = s
	return s
}

// chatPictureExts are the extensions a preview line calls a picture. A plain
// list on purpose: previews are also made on the Discord goroutine, where
// isImageExtension must not run (it may ask SDL whether WebP decodes).
var chatPictureExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".webp": true,
}

// chatMessagePreview is a message in one line for the places that show only
// text (the idle screen's feed, a profile's last message): its text, or what
// it carried when it was only files. Safe from any goroutine.
func chatMessagePreview(msg ChatMessage) string {
	if t := strings.TrimSpace(msg.Text); t != "" {
		return t
	}
	switch n := len(msg.Files); {
	case n == 1 && chatPictureExts[chatFileExt(msg.Files[0])]:
		return "Sent a picture"
	case n == 1:
		return "Sent " + msg.Files[0].Name
	case n > 1:
		return "Sent " + strconv.Itoa(n) + " files"
	}
	if strings.TrimSpace(msg.ImageURL) != "" {
		return "Sent a picture"
	}
	return ""
}

// chatFileImageSource is what a picture is drawn from: the copy on this device
// when there is one, else the channel's link.
func chatFileImageSource(f ChatFile) string {
	if f.Path != "" && (f.URL == "" || chatPathExists(f.Path)) {
		return f.Path
	}
	return f.URL
}

// chatInlineImageIndex is the file a bubble shows as its picture: the first
// image it carries. A message saved before files existed keeps its picture in
// ImageURL, and then none of its files is the picture.
func chatInlineImageIndex(msg ChatMessage) int {
	if strings.TrimSpace(msg.ImageURL) != "" {
		return -1
	}
	for i, f := range msg.Files {
		if chatFileKindOf(f) == chatFileImage && (f.Path != "" || f.URL != "") {
			return i
		}
	}
	return -1
}

// chatSetFile rewrites file idx of m through fn. The slice is copied first: a
// message is shared with the frame being drawn (chatUpdateLocked copies the
// history, not the files inside it).
func chatSetFile(m *ChatMessage, idx int, fn func(*ChatFile)) {
	if m == nil || idx < 0 || idx >= len(m.Files) {
		return
	}
	files := append([]ChatFile(nil), m.Files...)
	fn(&files[idx])
	m.Files = files
}

// ── Local paths ──────────────────────────────────────────────────────────

// chatTrusted holds the local paths this device's user put into the chat
// (attached, or saved from it). The bubbles draw pictures from these without
// the Files-root check the old single-picture attachment needed, which broke
// as soon as Files browsed another location. The image fetcher reads it from
// its own goroutine.
var chatTrusted struct {
	mu    sync.Mutex
	paths map[string]bool
}

// chatTrustedMax bounds the set: three full tabs of ten-file messages.
const chatTrustedMax = 16384

// chatTrustPath records a path the user attached or saved.
func chatTrustPath(path string) {
	path = strings.TrimSpace(path)
	if path == "" || strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return
	}
	clean := filepath.Clean(path)
	chatTrusted.mu.Lock()
	defer chatTrusted.mu.Unlock()
	if chatTrusted.paths == nil {
		chatTrusted.paths = make(map[string]bool, 32)
	}
	if len(chatTrusted.paths) < chatTrustedMax {
		chatTrusted.paths[clean] = true
	}
}

// chatPathTrusted reports whether path is one the user put into the chat.
func chatPathTrusted(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	clean := filepath.Clean(path)
	chatTrusted.mu.Lock()
	defer chatTrusted.mu.Unlock()
	return chatTrusted.paths[clean]
}

// chatTrustHistory trusts the local files of a loaded history: they were all
// written by this device.
func chatTrustHistory(lists ...[]ChatMessage) {
	for _, list := range lists {
		for _, msg := range list {
			for _, f := range msg.Files {
				chatTrustPath(f.Path)
			}
			if msg.Own {
				chatTrustPath(msg.ImageURL)
			}
		}
	}
}

// chatPathStat is one cached existence check; pending while a goroutine asks
// the filesystem again.
type chatPathStat struct {
	ok      bool
	at      time.Time
	pending bool
}

// chatPathSeen caches chatPathExists: every card and every picture asks on
// every frame, and the render thread must not wait on the handheld's card.
var chatPathSeen struct {
	mu sync.Mutex
	m  map[string]chatPathStat
}

// chatPathTTL is how long one existence check is believed.
const chatPathTTL = 5 * time.Second

// chatPathExists reports whether path is a regular file, for drawing. It
// answers from a cache at once and has a goroutine ask the filesystem again
// once the answer is a few seconds old; a path never asked about counts as
// there until the first answer (a file just attached or saved is).
func chatPathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	now := time.Now()
	chatPathSeen.mu.Lock()
	defer chatPathSeen.mu.Unlock()
	st, known := chatPathSeen.m[path]
	if (!known || now.Sub(st.at) >= chatPathTTL) && !st.pending {
		if chatPathSeen.m == nil || len(chatPathSeen.m) >= 512 {
			chatPathSeen.m = make(map[string]chatPathStat, 32)
		}
		if !known {
			st.ok = true
		}
		st.pending = true
		chatPathSeen.m[path] = st
		go chatStatPath(path)
	}
	return st.ok
}

// chatStatPath asks the filesystem about path and caches the answer.
func chatStatPath(path string) {
	ok := chatPathOnDevice(path)
	chatPathSeen.mu.Lock()
	if chatPathSeen.m == nil {
		chatPathSeen.m = make(map[string]chatPathStat, 32)
	}
	chatPathSeen.m[path] = chatPathStat{ok: ok, at: time.Now()}
	chatPathSeen.mu.Unlock()
}

// chatPathOnDevice asks the filesystem now: for a press, where the answer
// decides what happens and a moment's wait is fine.
func chatPathOnDevice(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// chatForgetPath drops a cached check after the path changed (a download
// landed there).
func chatForgetPath(path string) {
	chatPathSeen.mu.Lock()
	delete(chatPathSeen.m, path)
	chatPathSeen.mu.Unlock()
}

// chatFileLocal returns the copy of f on this device, or "", from the cached
// check (for drawing).
func chatFileLocal(f ChatFile) string {
	if f.Path != "" && chatPathExists(f.Path) {
		return f.Path
	}
	return ""
}

// chatFileOnDevice is chatFileLocal asked of the filesystem now (for a press).
func chatFileOnDevice(f ChatFile) string {
	if f.Path != "" && chatPathOnDevice(f.Path) {
		return f.Path
	}
	return ""
}

// chatMessageHasRemoteFile reports whether a message carries a file that is
// in the channel but not on this device yet: Download has something to do.
func chatMessageHasRemoteFile(msg ChatMessage) bool {
	for _, f := range msg.Files {
		if f.URL != "" && chatFileOnDevice(f) == "" {
			return true
		}
	}
	return false
}

// ── The tray ─────────────────────────────────────────────────────────────

// chatStagedFiles returns the files waiting in a tab's tray.
func chatStagedFiles(tab chatTab) []ChatFile {
	if tab < 0 || tab >= chatTabCount {
		return nil
	}
	return chatViews[tab].files
}

// chatStagedCount is how many files wait in a tab's tray.
func chatStagedCount(tab chatTab) int {
	return len(chatStagedFiles(tab))
}

// chatStageLimit is how many files a message of the tab may carry.
func chatStageLimit(tab chatTab) int {
	if tab == chatTabAI {
		return chatAIFilesMax
	}
	return chatFilesMax
}

// chatStageFile puts one file of this device into a tab's tray. What keeps a
// file out is returned as the sentence the toast shows.
func chatStageFile(tab chatTab, path string) error {
	if tab < 0 || tab >= chatTabCount {
		return errors.New("there is no chat to attach it to")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("no file was picked")
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s can't be read", filepath.Base(path))
	}
	name := info.Name()
	switch {
	case info.IsDir():
		return fmt.Errorf("%s is a folder — pick the files inside it", name)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s isn't a file that can be sent", name)
	case info.Size() == 0:
		return fmt.Errorf("%s is empty", name)
	}
	view := &chatViews[tab]
	for _, f := range view.files {
		if filesSamePath(f.Path, path) {
			return fmt.Errorf("%s is already attached", name)
		}
	}
	if limit := chatStageLimit(tab); len(view.files) >= limit {
		return fmt.Errorf("one message carries up to %d files", limit)
	}
	switch tab {
	case chatTabCommunity:
		if most := chatUploadLimit(); info.Size() > most {
			return fmt.Errorf("%s is %s — Discord takes files up to %s here",
				name, chatSizeLabel(info.Size()), chatSizeLabel(most))
		}
	case chatTabAI:
		if binary, err := chatLooksBinary(path); err != nil || binary {
			return fmt.Errorf("Juka reads text files (logs, notes, configs) — %s isn't one", name)
		}
	}
	files := make([]ChatFile, 0, len(view.files)+1)
	files = append(files, view.files...)
	view.files = append(files, ChatFile{Name: name, Size: info.Size(), Type: chatMIMEType(name), Path: path})
	chatTrustPath(path)
	return nil
}

// chatUnstage takes file idx out of a tab's tray.
func chatUnstage(tab chatTab, idx int) bool {
	if tab < 0 || tab >= chatTabCount {
		return false
	}
	view := &chatViews[tab]
	if idx < 0 || idx >= len(view.files) {
		return false
	}
	next := make([]ChatFile, 0, len(view.files)-1)
	next = append(next, view.files[:idx]...)
	next = append(next, view.files[idx+1:]...)
	view.files = next
	return true
}

// chatTakeStaged empties a tab's tray: its files went out with a message.
func chatTakeStaged(tab chatTab) {
	if tab < 0 || tab >= chatTabCount {
		return
	}
	chatViews[tab].files = nil
	if tab == chatActiveTabID() {
		chatTrayFocus = -1
	}
}

// chatStageEntries puts Files rows into a tab's tray and says what happened in
// one toast. Every path is resolved under the Files root first, like every
// other action on a row. It returns how many files went in.
func chatStageEntries(config *Config, tab chatTab, entries []FileEntry) int {
	staged := 0
	var first error
	lastName := ""
	for _, entry := range entries {
		if entry.IsDir || entry.Name == ".." {
			if first == nil {
				first = fmt.Errorf("%s is a folder — pick the files inside it", entry.Name)
			}
			continue
		}
		path, err := resolveExplorerEntry(config, entry.Path)
		if err == nil {
			err = chatStageFile(tab, path)
		} else {
			err = fmt.Errorf("%s can't be read", entry.Name)
		}
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		staged++
		lastName = entry.Name
	}
	switch {
	case staged == 0 && first != nil:
		showToast(chatSentence(first.Error()), ToastWarn())
	case staged == 0:
		showToast("Pick a file to attach", ToastInfo())
	case first != nil:
		showToast(fmt.Sprintf("Attached %d · %s", staged, first.Error()), ToastWarn())
	case staged == 1:
		showToast("Attached "+lastName, ToastSuccess())
	default:
		showToast(fmt.Sprintf("Attached %d files", staged), ToastSuccess())
	}
	return staged
}

// chatSentence capitalises the first letter of a reason for a toast.
func chatSentence(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError || size == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// chatStageDropped attaches a file dropped onto the window to the tab on
// screen. It waits in the tray until the message is sent.
func chatStageDropped(path string) {
	tab := chatActiveTabID()
	if err := chatStageFile(tab, path); err != nil {
		showToast(chatSentence(err.Error()), ToastWarn())
		return
	}
	PlayActivateSound()
	showToast("Attached "+filepath.Base(path), ToastSuccess())
}

// ── Picking in Files ─────────────────────────────────────────────────────

// chatPick is Files in pick mode for the chat. seenFiles notes that Files has
// been on screen: the scene change is a fade, so the first frames after the
// request still show the chat.
var chatPick struct {
	active    bool
	tab       chatTab
	seenFiles bool
	started   time.Time
}

// chatPickActive reports whether Files is picking files for the chat.
func chatPickActive() bool { return chatPick.active }

// chatPickBegin opens Files to pick files for tab.
func chatPickBegin(config *Config, tab chatTab) bool {
	if config == nil || tab < 0 || tab >= chatTabCount {
		return false
	}
	if tab == chatTabAI && !chatHasGroqKey() {
		showToast("Add your Groq API key to talk to Juka", ToastWarn())
		return false
	}
	if chatStagedCount(tab) >= chatStageLimit(tab) {
		showToast(fmt.Sprintf("One message carries up to %d files", chatStageLimit(tab)), ToastInfo())
		return false
	}
	idx := sceneIndexExact(config, "FileExplorer")
	if idx < 0 {
		showToast("Files isn't part of this layout", ToastWarn())
		return false
	}
	// A scene change that is still fading would swallow this one
	// (switchScene ignores a change mid-fade): the press is ignored instead
	// of starting a pick that never opens.
	if transitionPhase != "none" {
		return false
	}
	chatPick.active = true
	chatPick.tab = tab
	chatPick.seenFiles = currentSceneIndex == idx
	chatPick.started = time.Now()
	chatStopInlineTyping()
	clearChatSelection()
	// Rows marked on an earlier visit to Files are not this pick's.
	feSelectionClear()
	PlayActivateSound()
	if currentSceneIndex != idx {
		changeSceneTo(config, idx)
	}
	return true
}

// chatPickBack is B in Files while it picks for the chat: the pick is called
// off and the chat comes back, however deep Files had gone (Left still climbs
// a folder, as it always does there).
func chatPickBack(config *Config) bool {
	if !chatPick.active {
		return false
	}
	if transitionPhase != "none" {
		// Files is still fading in: the way back would be ignored, so the
		// press is taken and the pick goes on; the next B leaves.
		return true
	}
	chatPickCancel()
	feSelectionClear()
	PlayBackSound()
	chatReturnToChat(config)
	return true
}

// chatPickCancel ends pick mode without attaching anything.
func chatPickCancel() {
	chatPick.active = false
	chatPick.seenFiles = false
}

// chatPickTick ends pick mode once Files has gone (B on the Locations list,
// Home, the Start menu): a pick belongs to that one visit.
func chatPickTick(config *Config) {
	if !chatPick.active {
		return
	}
	if feSceneActive(config) {
		chatPick.seenFiles = true
		return
	}
	if chatPick.seenFiles || time.Since(chatPick.started) > 3*time.Second {
		chatPickCancel()
	}
}

// chatTabShortName names a tab where Files says what it is picking for.
func chatTabShortName(tab chatTab) string {
	switch tab {
	case chatTabAI:
		return "Juka AI"
	case chatTabSaved:
		return "Saved"
	}
	return chatCommunityTitle()
}

// chatPickLabelCache keeps the bar's line for the count and tab name it was
// built from: Files draws it every frame.
var chatPickLabelCache struct {
	marked int
	name   string
	text   string
}

// chatPickLabel is the line Files' bar shows while it picks, "" otherwise.
func chatPickLabel() string {
	if !chatPick.active {
		return ""
	}
	n, name := feSelectionCount(), chatTabShortName(chatPick.tab)
	c := &chatPickLabelCache
	if c.text != "" && c.marked == n && c.name == name {
		return c.text
	}
	c.marked, c.name = n, name
	if n > 0 {
		c.text = fmt.Sprintf("%d marked for %s", n, name)
	} else {
		c.text = "Pick a file for " + name
	}
	return c.text
}

// chatPickFromFiles is A on a Files row in pick mode: the rows X marked, or
// else the row itself, go into the tray and the chat comes back. A folder is
// entered as usual, so it is declined.
func chatPickFromFiles(config *Config, entry FileEntry) bool {
	if !chatPick.active || entry.IsDir || entry.Name == ".." {
		return false
	}
	if transitionPhase != "none" {
		// Files is still fading in: the way back to the chat would be
		// ignored, so the press is taken and nothing happens yet.
		return true
	}
	targets := []FileEntry{entry}
	if feSelectionCount() > 0 {
		targets = feSelectionTargets(config)
	}
	tab := chatPick.tab
	if chatStageEntries(config, tab, targets) == 0 {
		// The toast says why; Files stays up for another pick.
		return true
	}
	chatPickCancel()
	feSelectionClear()
	chatSelectTab(tab)
	chatReturnToChat(config)
	return true
}

// chatSendFromFiles is Files' "Send to chat" (and the send_to_chat trigger):
// the files go into the tray of the tab that is picking, or the community
// tab, and the chat comes up with them ready to send.
func chatSendFromFiles(config *Config, entries []FileEntry) {
	tab := chatTabCommunity
	if chatPick.active {
		tab = chatPick.tab
	}
	if chatStageEntries(config, tab, entries) == 0 {
		return
	}
	chatPickCancel()
	feSelectionClear()
	chatSelectTab(tab)
	chatReturnToChat(config)
}

// chatReturnToChat brings the Chat scene back after a pick. When Files was
// opened from the chat, its history entry is taken, so B in the chat still
// goes where it went before the pick.
func chatReturnToChat(config *Config) {
	if config == nil {
		return
	}
	idx := sceneIndexExact(config, chatSceneName)
	if idx < 0 {
		showToast("Open Chat to send it", ToastInfo())
		return
	}
	if idx == currentSceneIndex {
		return
	}
	if n := len(sceneHistory); n > 0 && sceneHistory[n-1] == idx {
		sceneHistory = sceneHistory[:n-1]
		switchScene(config, idx, false)
		if pendingSceneIndex != idx {
			// A transition was running: keep the way back for the next press.
			pushSceneHistory(idx)
		}
		return
	}
	changeSceneTo(config, idx)
}

// ── Opening files ────────────────────────────────────────────────────────

// chatFindMessage returns the message with id in a tab.
func chatFindMessage(tab chatTab, id string) (ChatMessage, bool) {
	if id == "" || tab < 0 || tab >= chatTabCount {
		return ChatMessage{}, false
	}
	for _, msg := range chatTabMessages(tab) {
		if msg.ID == id {
			return msg, true
		}
	}
	return ChatMessage{}, false
}

// chatFileOpenLabel is the word the action bar shows for A on a file. A file
// from the channel that has no viewer here is saved into Downloads on the
// way, but it is still "Open": "Save" is the action that keeps a copy of the
// message in the Saved tab.
func chatFileOpenLabel(f ChatFile) string {
	switch chatFileKindOf(f) {
	case chatFileImage:
		return "View"
	case chatFileAudio, chatFileVideo:
		return "Play"
	}
	return "Open"
}

// chatFileActivate is A (or a tap) on a file: a copy on this device opens the
// way Files would open it, a picture or a clip from the channel is shown or
// played straight from its link, and anything else is saved into Downloads
// first and opened from there.
func chatFileActivate(config *Config, tab chatTab, msgID string, idx int) {
	msg, ok := chatFindMessage(tab, msgID)
	if !ok || idx < 0 || idx >= len(msg.Files) {
		return
	}
	f := msg.Files[idx]
	if path := chatFileOnDevice(f); path != "" {
		chatOpenLocalFile(config, path)
		return
	}
	if f.URL == "" {
		showToast(f.Name+" isn't on this device any more", ToastWarn())
		return
	}
	if chatLinkExpired(f.URL, time.Now()) {
		// An old link no longer works: Discord gives out a fresh one first.
		renewed := chatRenewLink(tab, msgID, idx, f, true, func(config *Config, link string) {
			if chatIdleInChat(config) {
				chatActOnRemote(config, tab, msgID, idx, f, link)
			}
		})
		if renewed {
			showToast("Opening "+f.Name+"…", ToastInfo())
			return
		}
	}
	chatActOnRemote(config, tab, msgID, idx, f, f.URL)
}

// chatActOnRemote shows or plays a file from the channel straight from its
// link; anything else is saved into Downloads and opened from there.
func chatActOnRemote(config *Config, tab chatTab, msgID string, idx int, f ChatFile, link string) {
	switch chatFileKindOf(f) {
	case chatFileImage:
		PlayActivateSound()
		openImageViewerURL(link)
	case chatFileAudio, chatFileVideo:
		PlayActivateSound()
		playSmartURL(config, link)
	default:
		chatDownloadFile(config, tab, msgID, idx, true)
	}
}

// chatIdleInChat reports whether the user is on the chat with nothing else
// going on: no typing, nothing open over it, no scene change under way. Only
// then may a file that finished downloading (or a link that was renewed) open
// by itself; otherwise it would pull the user out of whatever they went on to
// do.
func chatIdleInChat(config *Config) bool {
	return chatSceneActive(config) && !virtualKeyboardActive && !chatComposerInline &&
		!chatOverlayOpen() && !chatSelectionBar.open && imageViewerPath == "" && transitionPhase == "none"
}

// ── Expiring links ───────────────────────────────────────────────────────

// chatLinkExpiry reads when a Discord file link stops working: its ex=
// parameter, Unix seconds in hex. A link from elsewhere, or without one,
// gives 0 (it does not expire).
func chatLinkExpiry(link string) int64 {
	u, err := url.Parse(link)
	if err != nil {
		return 0
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, "discordapp.com") && !strings.HasSuffix(host, "discordapp.net") {
		return 0
	}
	n, err := strconv.ParseInt(u.Query().Get("ex"), 16, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// chatLinkExpiries caches chatLinkExpiry by link. Main thread only; dropped
// whole when full.
var chatLinkExpiries = make(map[string]int64, 32)

// chatLinkExpired reports whether a Discord file link has passed (or is a
// minute from) the expiry it carries.
func chatLinkExpired(link string, now time.Time) bool {
	if link == "" {
		return false
	}
	exp, ok := chatLinkExpiries[link]
	if !ok {
		if len(chatLinkExpiries) >= 512 {
			chatLinkExpiries = make(map[string]int64, 32)
		}
		exp = chatLinkExpiry(link)
		chatLinkExpiries[link] = exp
	}
	return exp > 0 && now.Unix() >= exp-60
}

// chatRelinks notes when each file's link was last renewed in the background,
// so a picture on screen asks Discord once rather than every frame.
var chatRelinks struct {
	mu   sync.Mutex
	when map[string]time.Time
}

// chatRenewLink asks Discord, in the background, for a fresh link to file idx
// of a message, stores it, and hands it to then on the main thread. asked is
// a press of the user's, which always asks; without it (a picture on screen)
// a file is renewed at most every five minutes. It reports false when there
// is nothing to ask: no session, or not a file from Discord.
func chatRenewLink(tab chatTab, msgID string, idx int, f ChatFile, asked bool, then func(config *Config, link string)) bool {
	dg, ch := chatLiveNow()
	if dg == nil || ch == "" || !isDiscordSnowflake(f.Msg) {
		return false
	}
	key := chatDownloadKey(msgID, idx)
	chatRelinks.mu.Lock()
	if chatRelinks.when == nil {
		chatRelinks.when = make(map[string]time.Time, 8)
	}
	if last, ok := chatRelinks.when[key]; ok && !asked && time.Since(last) < 5*time.Minute {
		chatRelinks.mu.Unlock()
		return false
	}
	chatRelinks.when[key] = time.Now()
	chatRelinks.mu.Unlock()
	go func() {
		fresh := chatRefreshAttachmentURL(dg, ch, f)
		if fresh == "" {
			if asked {
				showToast("The link to "+f.Name+" has expired", ToastWarn())
			}
			return
		}
		chatUpdateMessage(tab, msgID, func(m *ChatMessage) {
			chatSetFile(m, idx, func(cf *ChatFile) { cf.URL = fresh })
		})
		if then != nil {
			chatOnMain(func(config *Config) { then(config, fresh) })
		}
	}()
	return true
}

// chatMainQueue is work a goroutine hands to the main thread, where scenes and
// viewers may be opened.
var chatMainQueue struct {
	mu  sync.Mutex
	fns []func(*Config)
}

// chatOnMain runs fn on the main thread, from the next chatTick.
func chatOnMain(fn func(*Config)) {
	chatMainQueue.mu.Lock()
	chatMainQueue.fns = append(chatMainQueue.fns, fn)
	chatMainQueue.mu.Unlock()
}

// chatOpenLocalFile opens a file on this device where Files opens it: the
// image viewer, the players, the reader. Anything else is shown in Files,
// where A gives it the editor, unpacks it or starts the game.
func chatOpenLocalFile(config *Config, path string) {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case isImageExtension(ext):
		openImageViewer(config, path)
	case isAudioFile(path):
		PlayActivateSound()
		openAudioPlayer(config, path)
	case hasMediaExtension(path):
		PlayActivateSound()
		playSmartURL(config, path)
	case ext == ".pdf" || ext == ".epub":
		PlayActivateSound()
		if !pdfvOpenDocument(config, path) {
			chatShowInFiles(config, path)
		}
	default:
		chatShowInFiles(config, path)
	}
}

// chatShowInFiles opens Files on the folder that holds path, with the cursor
// on the file.
func chatShowInFiles(config *Config, path string) {
	dir := filepath.Dir(path)
	idx := sceneIndexExact(config, "FileExplorer")
	if config == nil || idx < 0 {
		showToast("Saved in "+dir, ToastInfo())
		return
	}
	if config.Variables.Custom == nil {
		config.Variables.Custom = map[string]interface{}{}
	}
	// The location that holds the folder becomes the one Files browses, so the
	// folder is inside its root and the listing is allowed.
	locs := filesBuildLocations(config)
	if i := filesLocationIndex(locs, dir); i >= 0 {
		filesNav.location = locs[i].Path
	} else {
		filesNav.location = filesVolumeRoot(dir)
	}
	filesNav.atLocations = false
	config.Variables.Custom["fe_path"] = dir
	filesSetFocusMemory(dir, path)
	filesNav.restore = true
	feSelectionClear()
	chatPickCancel()
	PlayActivateSound()
	if currentSceneIndex == idx {
		feListDirectory(config)
		return
	}
	changeSceneTo(config, idx)
}

// ── Saving files from the channel ────────────────────────────────────────

// chatSaveDirOverride redirects saved files for tests.
var chatSaveDirOverride string

// chatSaveDir is where files from the chat are saved: Downloads in the user's
// folder on a PC, Downloads at the top of the SD card on the handheld, where
// Files and a PC reading the card both find it.
func chatSaveDir() string {
	if dir := strings.TrimSpace(chatSaveDirOverride); dir != "" {
		return dir
	}
	if IsTrimuiDevice() {
		card := strings.TrimSpace(defaultTrimuiSDRoot())
		if card == "" {
			card = "/mnt/SDCARD"
		}
		return filepath.Join(card, "Downloads")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, "Downloads")
	}
	return filepath.Join(downloadsRoot(), "chat")
}

// chatDownloadState is one running download, for its card.
type chatDownloadState struct {
	got   int64
	total int64
}

// chatDownloads tracks the downloads in flight and the finished ones waiting
// to be opened on the main thread.
var chatDownloads struct {
	mu   sync.Mutex
	jobs map[string]*chatDownloadState
	open []chatOpenRequest
}

// chatOpenRequest is a finished download that was asked to open: where it
// landed, and when the user asked.
type chatOpenRequest struct {
	path  string
	asked time.Time
}

// chatOpenWait is how long after the press a finished download still opens by
// itself; later it only says where it went.
const chatOpenWait = 2 * time.Minute

func chatDownloadKey(msgID string, idx int) string {
	return msgID + "#" + strconv.Itoa(idx)
}

// chatDownloadProgress reports a running download of file idx of a message:
// the bytes so far and the total (0 when unknown).
func chatDownloadProgress(msgID string, idx int) (int64, int64, bool) {
	chatDownloads.mu.Lock()
	defer chatDownloads.mu.Unlock()
	st, ok := chatDownloads.jobs[chatDownloadKey(msgID, idx)]
	if !ok || st == nil {
		return 0, 0, false
	}
	return st.got, st.total, true
}

// chatDownloadJob is one file to save, copied out of the history so the
// goroutine reads no shared state.
type chatDownloadJob struct {
	tab     chatTab
	msgID   string
	idx     int
	file    ChatFile
	open    bool
	asked   time.Time
	dir     string
	session *discordgo.Session
	channel string
	// fresh is the link the job ended up using when the saved one had expired.
	fresh string
}

// chatDownloadFile saves file idx of a message into Downloads; with open set
// the file opens once it is there. A file already on this device just opens.
func chatDownloadFile(config *Config, tab chatTab, msgID string, idx int, open bool) bool {
	msg, ok := chatFindMessage(tab, msgID)
	if !ok || idx < 0 || idx >= len(msg.Files) {
		return false
	}
	f := msg.Files[idx]
	if path := chatFileOnDevice(f); path != "" {
		if open {
			chatOpenLocalFile(config, path)
		}
		return true
	}
	if !strings.HasPrefix(f.URL, "https://") && !strings.HasPrefix(f.URL, "http://") {
		showToast(f.Name+" isn't available to download", ToastWarn())
		return false
	}
	if !IsOnline() {
		showToast("Connect to the internet to download "+f.Name, ToastWarn())
		return false
	}
	key := chatDownloadKey(msgID, idx)
	chatDownloads.mu.Lock()
	if chatDownloads.jobs == nil {
		chatDownloads.jobs = make(map[string]*chatDownloadState, 4)
	}
	if _, busy := chatDownloads.jobs[key]; busy {
		chatDownloads.mu.Unlock()
		showToast("Already downloading "+f.Name, ToastInfo())
		return false
	}
	state := &chatDownloadState{total: f.Size}
	chatDownloads.jobs[key] = state
	chatDownloads.mu.Unlock()
	session, channel := chatLiveNow()
	job := &chatDownloadJob{
		tab: tab, msgID: msgID, idx: idx, file: f, open: open, asked: time.Now(),
		dir: chatSaveDir(), session: session, channel: channel,
	}
	PlayActivateSound()
	showToast("Downloading "+f.Name+"…", ToastInfo())
	go chatRunDownload(job, key)
	return true
}

// chatDownloadSlots lets two files download at once: Download on a message of
// ten files must not open ten connections on the handheld's Wi-Fi.
var chatDownloadSlots = make(chan struct{}, 2)

// chatRunDownload saves one file and records where it went.
func chatRunDownload(job *chatDownloadJob, key string) {
	chatDownloadSlots <- struct{}{}
	defer func() {
		<-chatDownloadSlots
		if r := recover(); r != nil {
			logWarn("chat", "download recovered from: %v", r)
		}
		chatDownloads.mu.Lock()
		delete(chatDownloads.jobs, key)
		chatDownloads.mu.Unlock()
	}()
	path, err := chatFetchToDir(job, func(got, total int64) {
		chatDownloads.mu.Lock()
		if st := chatDownloads.jobs[key]; st != nil {
			st.got = got
			if total > 0 {
				st.total = total
			}
		}
		chatDownloads.mu.Unlock()
	})
	if err != nil {
		// The link carries a signature in its query string: the name is
		// logged, and the error without the URL a transport error quotes.
		logWarn("chat", "saving %s failed: %v", job.file.Name, chatPlainError(err))
		showToast(chatDownloadProblem(err, job.file.Name), ToastWarn())
		return
	}
	chatTrustPath(path)
	chatForgetPath(path)
	chatUpdateMessage(job.tab, job.msgID, func(m *ChatMessage) {
		chatSetFile(m, job.idx, func(f *ChatFile) {
			f.Path = path
			if job.fresh != "" {
				f.URL = job.fresh
			}
		})
	})
	if job.open {
		chatDownloads.mu.Lock()
		chatDownloads.open = append(chatDownloads.open, chatOpenRequest{path: path, asked: job.asked})
		chatDownloads.mu.Unlock()
		return
	}
	showToast("Saved "+filepath.Base(path)+" in Downloads", ToastSuccess())
}

// errChatLinkExpired is a file link Discord no longer serves and could not
// renew.
var errChatLinkExpired = errors.New("the link to the file has expired")

// errChatTooBig is a download past chatDownloadMax.
var errChatTooBig = errors.New("the file is too big to save")

// chatFetchToDir downloads job.file into job.dir under a free name and returns
// the path. The bytes land in a hidden partial file first, so an interrupted
// download never leaves a broken file with the real name.
func chatFetchToDir(job *chatDownloadJob, progress func(got, total int64)) (string, error) {
	if err := os.MkdirAll(job.dir, 0o755); err != nil {
		return "", err
	}
	chatSweepParts(job.dir)
	if err := downloadsRequireSpace(job.dir, freeBytes); err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := chatGetFile(ctx, job.file.URL)
	if err == nil && (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound) {
		// Discord's file links are signed and expire; the message gives out
		// a fresh one.
		resp.Body.Close()
		fresh := chatRefreshAttachmentURL(job.session, job.channel, job.file)
		if fresh == "" {
			return "", errChatLinkExpired
		}
		job.fresh = fresh
		resp, err = chatGetFile(ctx, fresh)
	}
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the server answered %s", resp.Status)
	}
	total := resp.ContentLength
	if total <= 0 {
		total = job.file.Size
	}
	if total > chatDownloadMax {
		return "", errChatTooBig
	}
	if free, ferr := freeBytes(job.dir); ferr == nil && free > 0 && total > 0 && total > free-downloadsMinFreeBytes {
		return "", fmt.Errorf("not enough space: %s needs %s", job.file.Name, chatSizeLabel(total))
	}
	tmp, err := os.CreateTemp(job.dir, ".jukahub-*.part")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	kept := false
	defer func() {
		if !kept {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	touch, stop := chatStallWatch(cancel, chatTransferStall)
	defer stop()
	reader := &chatProgressReader{r: io.LimitReader(resp.Body, chatDownloadMax+1), on: func(n int64) {
		touch()
		if progress != nil {
			progress(n, total)
		}
	}}
	written, err := io.Copy(tmp, reader)
	if err != nil {
		return "", err
	}
	if written > chatDownloadMax {
		return "", errChatTooBig
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// The free name is picked and taken in one step: two downloads of
	// "save.srm" finishing together would otherwise both pick "save.srm", and
	// the second rename would replace the first file.
	chatSaveNameMu.Lock()
	defer chatSaveNameMu.Unlock()
	base, ext := chatSaveName(job.file.Name)
	final := uniqueDownloadPath(job.dir, base, ext)
	if err := os.Rename(tmpName, final); err != nil {
		return "", err
	}
	kept = true
	return final, nil
}

// chatSaveNameMu makes picking a free name in Downloads and moving the file
// there one step.
var chatSaveNameMu sync.Mutex

// chatRunArm is a script in Downloads waiting for its second press.
var chatRunArm struct {
	path string
	at   time.Time
}

// chatConfirmRun guards A on a script in Files. A script in Downloads may
// have come from anyone in the chat, and A runs it straight away (as root
// on the handheld): it runs on a second press within five seconds, after a
// toast that says where it came from. Scripts anywhere else run as before.
func chatConfirmRun(path string) bool {
	dir := chatSaveDir()
	if dir == "" || !isPathWithin(path, dir) {
		return true
	}
	if filesSamePath(chatRunArm.path, path) && time.Since(chatRunArm.at) <= 5*time.Second {
		chatRunArm.path = ""
		return true
	}
	chatRunArm.path, chatRunArm.at = path, time.Now()
	PlayNavSound()
	showToast(hintSentence("This script is in Downloads — it may have come from the chat. Press %s again to run it", "A", "Run"), ToastWarn())
	return false
}

// chatPartSweep clears partial downloads a crash or a power cut left behind,
// once per session and folder.
var chatPartSweep struct {
	mu   sync.Mutex
	done map[string]bool
}

// chatSweepParts removes ".jukahub-*.part" files older than an hour from dir:
// a download in progress keeps writing to its own, so those are left alone.
func chatSweepParts(dir string) {
	chatPartSweep.mu.Lock()
	if chatPartSweep.done == nil {
		chatPartSweep.done = make(map[string]bool, 2)
	}
	if chatPartSweep.done[dir] {
		chatPartSweep.mu.Unlock()
		return
	}
	chatPartSweep.done[dir] = true
	chatPartSweep.mu.Unlock()
	matches, err := filepath.Glob(filepath.Join(dir, ".jukahub-*.part"))
	if err != nil {
		return
	}
	for _, p := range matches {
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && time.Since(info.ModTime()) > time.Hour {
			os.Remove(p)
		}
	}
}

// chatGetFile starts a GET for a file link.
func chatGetFile(ctx context.Context, rawURL string) (*http.Response, error) {
	if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "http://") {
		return nil, errors.New("not a web link")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub")
	return chatTransferClient().Do(req)
}

// chatRefreshAttachmentURL asks Discord for the message a file came with and
// returns the file's current link, or "".
func chatRefreshAttachmentURL(session *discordgo.Session, channel string, f ChatFile) string {
	if session == nil || channel == "" || !isDiscordSnowflake(f.Msg) {
		return ""
	}
	m, err := session.ChannelMessage(channel, f.Msg)
	if err != nil || m == nil {
		return ""
	}
	for _, att := range m.Attachments {
		if att != nil && att.URL != "" && att.Filename == f.Name {
			return att.URL
		}
	}
	// A file this device sent went up under a cleaned name; its size still
	// tells it apart from the others on the message.
	for _, att := range m.Attachments {
		if att != nil && att.URL != "" && f.Size > 0 && int64(att.Size) == f.Size {
			return att.URL
		}
	}
	return ""
}

// chatSaveName splits a file name into a safe base and extension for the
// Downloads folder: "../../x.zip" becomes "x" and ".zip".
func chatSaveName(name string) (string, string) {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	ext := strings.ToLower(filepath.Ext(name))
	clean := make([]rune, 0, len(ext))
	for _, r := range strings.TrimPrefix(ext, ".") {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) && len(clean) < 10 {
			clean = append(clean, r)
		}
	}
	ext = ""
	if len(clean) > 0 {
		ext = "." + string(clean)
	}
	base := safeDownloadName(strings.TrimSuffix(name, filepath.Ext(name)))
	if base == "" {
		base = "file"
	}
	return base, ext
}

// chatDownloadProblem is the toast for a file that could not be saved.
func chatDownloadProblem(err error, name string) string {
	switch {
	case errors.Is(err, errChatLinkExpired):
		return "The link to " + name + " has expired"
	case errors.Is(err, errChatTooBig):
		return name + " is too big to save"
	case chatIsNetworkError(err), errors.Is(err, context.Canceled):
		return "Couldn't download " + name + " — check the connection"
	case strings.HasPrefix(err.Error(), "not enough space"):
		return chatSentence(err.Error())
	}
	return "Couldn't save " + name
}

// ── Main-thread work ─────────────────────────────────────────────────────

// chatFilesTick runs from chatTick on the main thread: pick mode follows the
// scene, work goroutines handed over runs, and a download that was asked to
// open its file opens it here, where scenes may change — when the user is
// still on the chat with nothing else going on, and not long after asking.
func chatFilesTick(config *Config) {
	chatPickTick(config)
	chatMainQueue.mu.Lock()
	fns := chatMainQueue.fns
	chatMainQueue.fns = nil
	chatMainQueue.mu.Unlock()
	for _, fn := range fns {
		fn(config)
	}
	chatDownloads.mu.Lock()
	open := chatDownloads.open
	chatDownloads.open = nil
	chatDownloads.mu.Unlock()
	for _, req := range open {
		if time.Since(req.asked) < chatOpenWait && chatIdleInChat(config) {
			chatOpenLocalFile(config, req.path)
			continue
		}
		showToast("Saved "+filepath.Base(req.path)+" in Downloads", ToastSuccess())
	}
}

// ── A message's files as a list ──────────────────────────────────────────

// chatFileSheet lists the files of a message that carries more than one; A
// opens the file under the cursor (setsheet.go draws it and owns its keys).
var chatFileSheet setSheetState

// chatFileSheetFor names the message the sheet lists.
var chatFileSheetFor struct {
	tab chatTab
	id  string
}

// chatOpenFileSheet shows the list of a message's files.
func chatOpenFileSheet(tab chatTab, msg ChatMessage) bool {
	if len(msg.Files) == 0 {
		return false
	}
	options := make([]setOption, 0, len(msg.Files))
	for i, f := range msg.Files {
		note := chatFileMeta(f)
		if chatFileOnDevice(f) != "" && !chatIsOwn(msg) {
			note += " · on this device"
		}
		options = append(options, setOption{Value: strconv.Itoa(i), Label: f.Name, Note: note})
	}
	chatFileSheetFor.tab, chatFileSheetFor.id = tab, msg.ID
	title := strconv.Itoa(len(msg.Files)) + " files"
	setSheetOpen(&chatFileSheet, title, "From "+msg.Sender, options, "", func(value string) {
		idx, err := strconv.Atoi(value)
		if err != nil {
			return
		}
		chatFileActivate(appConfig, chatFileSheetFor.tab, chatFileSheetFor.id, idx)
	})
	return true
}

// ── Juka AI reads text files ─────────────────────────────────────────────

// chatAIExcerptBudget is how much of the attached text Juka is shown, in
// bytes. The request also carries the conversation, and Groq's free tier
// counts every token of it against a per-minute budget.
const chatAIExcerptBudget = 8000

// chatAIFileQuestion is what a file sent to Juka without a question asks.
const chatAIFileQuestion = "Have a look at this file and tell me what stands out."

// errChatNotText is a file that turned out not to be text.
var errChatNotText = errors.New("not a text file")

// chatLooksBinary sniffs the start of a file for a NUL byte, the mark of data
// that is not text. Unlike the editor's test it takes text in an old code page
// (a Windows log with a stray "é"): Juka is shown that with the odd bytes
// replaced, where the editor must not rewrite them.
func chatLooksBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, 8192)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}

// chatExcerptText turns a piece of a text file into what the request carries:
// bytes that are not UTF-8 become U+FFFD, and a NUL means it is not text.
func chatExcerptText(b []byte) (string, error) {
	if bytes.IndexByte(b, 0) >= 0 {
		return "", errChatNotText
	}
	return strings.ToValidUTF8(string(b), "\uFFFD"), nil
}

// chatAIFileExcerpt reads what Juka is shown of a text file: all of a short
// one, or its start and its end — the end is where a log says what went
// wrong.
func chatAIFileExcerpt(path string, budget int) (string, error) {
	if budget <= 0 {
		budget = chatAIExcerptBudget
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() <= int64(budget) {
		data, err := io.ReadAll(io.LimitReader(f, int64(budget)))
		if err != nil {
			return "", err
		}
		return chatExcerptText(data)
	}
	headN := budget / 4
	tailN := budget - headN
	head := make([]byte, headN)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	head = chatTrimRuneEnd(head[:n])
	if _, err := f.Seek(-int64(tailN), io.SeekEnd); err != nil {
		return "", err
	}
	tail := make([]byte, tailN)
	m, err := io.ReadFull(f, tail)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	tail = chatTrimRuneStart(tail[:m])
	headText, err := chatExcerptText(head)
	if err != nil {
		return "", err
	}
	tailText, err := chatExcerptText(tail)
	if err != nil {
		return "", err
	}
	skipped := info.Size() - int64(len(head)) - int64(len(tail))
	return headText + "\n[… " + chatSizeLabel(skipped) + " from the middle left out …]\n" + tailText, nil
}

// chatTrimRuneEnd drops a character cut in half at the end of b: the bytes
// from the last character's first byte, when they do not make the whole
// character. Anything else (a stray byte that is not UTF-8) stays for
// chatExcerptText to replace.
func chatTrimRuneEnd(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			return b
		}
	}
	return b
}

// chatTrimRuneStart drops the tail of a character cut in half at the start
// of b.
func chatTrimRuneStart(b []byte) []byte {
	for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.RuneStart(b[0]); i++ {
		b = b[1:]
	}
	return b
}

// chatAIFileContext is the text that goes with a question for Juka: the files
// the question carries, or for a follow-up the latest files sent earlier in
// this conversation (each request starts from nothing, so a follow-up about
// "line 30" needs the file again). history is the conversation before the
// question.
func chatAIFileContext(prompt ChatMessage, history []ChatMessage) (string, error) {
	files := prompt.Files
	earlier := false
	if len(files) == 0 {
		for i := len(history) - 1; i >= 0; i-- {
			msg := history[i]
			if chatIsSystem(msg) {
				break // "New conversation": Juka starts afresh
			}
			if chatIsOwn(msg) && len(msg.Files) > 0 {
				files, earlier = msg.Files, true
				break
			}
		}
	}
	if len(files) == 0 {
		return "", nil
	}
	budget := chatAIExcerptBudget / len(files)
	var b strings.Builder
	if earlier {
		b.WriteString("(The file from earlier in this conversation, for reference.)\n")
	}
	shown := 0
	for _, f := range files {
		if f.Path == "" {
			continue
		}
		text, err := chatAIFileExcerpt(f.Path, budget)
		if err != nil {
			if earlier {
				continue // a follow-up still goes, without the file
			}
			return "", fmt.Errorf("can't read %s: %w", f.Name, err)
		}
		fmt.Fprintf(&b, "\nFile %s (%s):\n```\n%s\n```\n", f.Name, chatSizeLabel(f.Size), strings.TrimRight(text, "\r\n"))
		shown++
	}
	if shown == 0 {
		return "", nil
	}
	return strings.TrimSpace(b.String()), nil
}

// chatAIMessageText is how an earlier message reads in the conversation Juka
// is sent: its text and the names of the files it carried. The contents went
// with the question that attached them.
func chatAIMessageText(msg ChatMessage) string {
	text := strings.TrimSpace(msg.Text)
	if len(msg.Files) == 0 {
		return text
	}
	names := make([]string, 0, len(msg.Files))
	for _, f := range msg.Files {
		names = append(names, f.Name)
	}
	note := "[attached: " + strings.Join(names, ", ") + "]"
	if text == "" {
		return note
	}
	return text + " " + note
}

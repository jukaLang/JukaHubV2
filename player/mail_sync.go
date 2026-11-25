package main

// mail_sync.go — keeping the inboxes current in the background
// (docs/EMAIL_CALENDAR.md).
//
// One goroutine (mailStart) syncs every enabled account every
// mailcalSettings.poll() minutes and whenever something asks (mailSyncNow).
// A sync reads INBOX only: the unread count and the newest mailKeep
// messages' headers with a one-line snippet, kept in mailcalDataDir()/
// mail-<id>.json so the list and the Home badge are there at once after a
// boot, offline included. Bodies are fetched when a message is opened and
// kept in a small in-memory cache.
//
// New mail is decided by UID, never by a date: a UID above the highest one
// already seen, under the same UIDVALIDITY, is new. The handheld often boots
// before its clock is set, and a Date header is whatever the sender's
// machine thought. An account's first sync, and the first after the server
// renumbered its inbox, only learns the UIDs and announces nothing.
//
// While a game is running nothing syncs (the network and the SD card belong
// to the game); the next sync runs a minute after it ends. Errors are written
// to the account (MailError) as a sentence only when the sentence changes,
// and logged on change only, so an offline handheld does not fill the log.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// mailKeep is how many of the newest inbox messages are listed.
	mailKeep = 50
	// mailSnippetBytes is how much of each new message's text is fetched
	// for its snippet.
	mailSnippetBytes = 2048
	// mailBodyCacheMax and mailBodyCacheBytes bound the opened-message cache.
	mailBodyCacheMax   = 12
	mailBodyCacheBytes = 8 << 20
)

// mailHeaderFields are the headers a sync fetches for the list and a reply.
const mailHeaderFields = "FROM TO CC REPLY-TO SUBJECT DATE MESSAGE-ID REFERENCES IN-REPLY-TO CONTENT-TYPE CONTENT-TRANSFER-ENCODING"

// mailCacheMsg is one inbox message as the list shows it.
type mailCacheMsg struct {
	UID        uint32    `json:"uid"`
	From       string    `json:"from"`
	FromAddr   string    `json:"from_addr,omitempty"`
	ReplyTo    string    `json:"reply_to,omitempty"`
	To         string    `json:"to,omitempty"`
	Cc         string    `json:"cc,omitempty"`
	Subject    string    `json:"subject"`
	Date       time.Time `json:"date"`
	Snippet    string    `json:"snippet,omitempty"`
	Unread     bool      `json:"unread,omitempty"`
	Size       int       `json:"size,omitempty"`
	MessageID  string    `json:"message_id,omitempty"`
	References string    `json:"references,omitempty"`
	Attach     bool      `json:"attach,omitempty"`
}

// sender is how the list names who wrote.
func (m mailCacheMsg) sender() string {
	return setOr(m.From, setOr(m.FromAddr, "Unknown sender"))
}

// mailCache is one account's inbox as last synced (mail-<id>.json).
type mailCache struct {
	V        int    `json:"v"`
	Validity uint32 `json:"uidvalidity"`
	// LastUID is the highest UID seen; Primed is set once a sync completed,
	// so the next higher UID is new mail.
	LastUID   uint32         `json:"last_uid"`
	Primed    bool           `json:"primed"`
	Unread    int            `json:"unread"`
	Folders   mailFolders    `json:"folders"`
	FoldersAt time.Time      `json:"folders_at"`
	Messages  []mailCacheMsg `json:"messages"`
	SyncedAt  time.Time      `json:"synced_at"`
}

func (c *mailCache) clone() *mailCache {
	if c == nil {
		return &mailCache{}
	}
	out := *c
	out.Messages = append([]mailCacheMsg(nil), c.Messages...)
	return &out
}

// mailState is every account's cache and the loop's state.
var mailState struct {
	mu     sync.Mutex
	caches map[string]*mailCache
	loaded map[string]bool
	// errLogged is the last error logged per account, so a repeat is quiet.
	errLogged map[string]string
}

// mailKick wakes the loop for a sync (mailSyncNow).
var mailKick = make(chan string, 1)

var (
	mailLoopOnce sync.Once
	// mailUnread is the unread INBOX total over enabled accounts.
	mailUnread atomic.Int64
	// mailGen changes whenever a cache does, so the screen rebuilds.
	mailGen atomic.Uint64
	// mailSyncing is set while a sync pass runs; mailOffline when the last
	// pass found no network.
	mailSyncing atomic.Bool
	mailOffline atomic.Bool
)

// ── Public API ──────────────────────────────────────────────────────────

// mailStart starts the background sync. Idempotent; any thread.
func mailStart() {
	mailLoopOnce.Do(func() {
		// The Microsoft client ID may come from the config, which only the
		// main loop reads.
		tbxPost(func() { msOAuthLoad(appConfig) })
		go mailRunLoop()
	})
}

// mailSyncNow asks for a sync soon. Non-blocking; any thread.
func mailSyncNow(reason string) {
	mailStart()
	select {
	case mailKick <- reason:
	default:
	}
}

// mailUnreadTotal is the cached unread INBOX total over enabled accounts.
// Cheap; any goroutine.
func mailUnreadTotal() int {
	return int(mailUnread.Load())
}

// mailSummary is one message for the AI tools.
type mailSummary struct {
	AccountID, AccountLabel, From, Subject, Snippet string
	Date                                            time.Time
	Unread                                          bool
}

// mailRecent is the newest n messages over enabled accounts, from the
// caches. Any goroutine.
func mailRecent(n int) []mailSummary {
	if n <= 0 {
		return nil
	}
	s := mailcalSettingsGet()
	var out []mailSummary
	for _, a := range s.Accounts {
		if a.MailOff {
			continue
		}
		c := mailCacheGet(a.ID)
		for _, m := range c.Messages {
			out = append(out, mailSummary{AccountID: a.ID, AccountLabel: a.label(), From: m.sender(), Subject: m.Subject,
				Snippet: m.Snippet, Date: m.Date, Unread: m.Unread})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// mailCheckAccount signs in to the account's IMAP server and opens INBOX,
// then signs in to its SMTP server. The error is a sentence for a person.
// Any goroutine.
func mailCheckAccount(ctx context.Context, a mailcalAccount) error {
	err := func() error {
		c, err := mailIMAPOpen(ctx, a)
		if err != nil {
			return err
		}
		defer c.logout()
		_, err = c.selectBox("INBOX", true)
		return err
	}()
	if err != nil {
		return mailPersonErr(err, a)
	}
	sc, done, err := mailSMTPOpen(ctx, a)
	if err != nil {
		e := mailPersonErr(err, a)
		var me *mailError
		if errors.As(e, &me) {
			me.msg = "Incoming mail works, but sending does not: " + mailLowerFirst(me.msg)
		}
		return e
	}
	_ = sc.Quit()
	done()
	return nil
}

// mailForgetAccount drops a removed account's cache, opened messages and
// tokens. The store row is removed by the caller.
func mailForgetAccount(id string) {
	mailState.mu.Lock()
	delete(mailState.caches, id)
	delete(mailState.loaded, id)
	delete(mailState.errLogged, id)
	mailState.mu.Unlock()
	mailBodyForget(id + "/")
	msForgetToken(id)
	if p := mailCachePath(id); p != "" {
		go func() { _ = os.Remove(p) }()
	}
	mailRecount()
}

// ── Errors as sentences ─────────────────────────────────────────────────

// mailUpperFirst and mailLowerFirst change a sentence's first letter.
func mailUpperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

func mailLowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || (n < len(s) && unicode.IsUpper(rune(s[n]))) {
		return s // an acronym ("IMAP …") stays as it is
	}
	return string(unicode.ToLower(r)) + s[n:]
}

// mailErrorText is an error as a sentence for this account: a refused
// password names the provider and where its app passwords are made.
func mailErrorText(err error, a mailcalAccount) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "The mail server did not answer in time"
	case errors.Is(err, context.Canceled):
		return "Stopped"
	}
	var me *mailError
	if !errors.As(err, &me) {
		return mailUpperFirst(mailClean(firstLine(err.Error()), 200))
	}
	if me.kind != mailErrAuth {
		return mailUpperFirst(me.msg)
	}
	p, known := mailcalPresetByKey(a.Provider)
	name := "The server"
	if known {
		name = p.Name
	}
	switch {
	case me == errMSSignInExpired, strings.HasPrefix(me.msg, "the Microsoft"), strings.HasPrefix(me.msg, "this build"),
		strings.HasPrefix(me.msg, "no password"), strings.HasPrefix(me.msg, "Incoming mail"):
		return mailUpperFirst(me.msg)
	case a.OAuth == mailcalOAuthMicrosoft && a.RefreshToken != "":
		return "Microsoft refused the sign-in — sign in again in Settings → Email & calendar"
	case a.OAuth == mailcalOAuthMicrosoft || a.Provider == "outlook":
		return name + " refused the password — Microsoft turned off password sign-in for most accounts, so sign in with Microsoft instead (Settings → Email & calendar)"
	case known && p.PasswordHelp != "":
		return name + " refused the password — use an app password (" + p.PasswordHelp + ")"
	}
	if me.detail != "" {
		return name + " refused the user name or password: " + me.detail
	}
	return name + " refused the user name or password"
}

// mailPersonErr keeps an error's kind with mailErrorText's sentence.
func mailPersonErr(err error, a mailcalAccount) error {
	if err == nil {
		return nil
	}
	kind := mailErrServer
	var me *mailError
	if errors.As(err, &me) {
		kind = me.kind
	} else if errors.Is(err, context.DeadlineExceeded) {
		kind = mailErrNet
	}
	return &mailError{kind: kind, msg: mailErrorText(err, a), err: err}
}

// ── Caches ──────────────────────────────────────────────────────────────

// mailIDSafe accepts an account ID as a file name part.
func mailIDSafe(id string) bool {
	if id == "" || len(id) > 40 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// mailCachePath is an account's cache file ("" when it has none).
func mailCachePath(id string) string {
	dir := mailcalDataDir()
	if dir == "" || !mailIDSafe(id) {
		return ""
	}
	return filepath.Join(dir, "mail-"+id+".json")
}

// mailCacheGet is a copy of an account's cache, read from disk the first
// time. Any goroutine.
func mailCacheGet(id string) *mailCache {
	mailState.mu.Lock()
	if mailState.caches == nil {
		mailState.caches, mailState.loaded, mailState.errLogged = map[string]*mailCache{}, map[string]bool{}, map[string]string{}
	}
	if !mailState.loaded[id] {
		mailState.loaded[id] = true
		if p := mailCachePath(id); p != "" {
			var c mailCache
			if _, err := readJSONWithBackup(p, &c); err == nil {
				if len(c.Messages) > mailKeep {
					c.Messages = c.Messages[:mailKeep]
				}
				mailState.caches[id] = &c
			} else if !os.IsNotExist(err) {
				logWarn("mail", "ignoring an unreadable mail cache: %s", firstLine(err.Error()))
			}
		}
	}
	c := mailState.caches[id].clone()
	mailState.mu.Unlock()
	return c
}

// mailCacheIO keeps a cache's change and its write to disk in one piece, so
// a sync and an action finishing together cannot leave the older copy on
// disk, and two actions cannot lose each other's edit. (A sync replaces the
// whole cache with what the server said; an edit made while it ran is
// corrected by the next one.)
var mailCacheIO sync.Mutex

// mailCachePut replaces an account's cache and writes it.
func mailCachePut(id string, c *mailCache) {
	mailCacheIO.Lock()
	defer mailCacheIO.Unlock()
	mailCachePutLocked(id, c)
}

// mailCachePutLocked is mailCachePut with mailCacheIO held.
func mailCachePutLocked(id string, c *mailCache) {
	c.V = 1
	mailState.mu.Lock()
	if mailState.caches == nil {
		mailState.caches, mailState.loaded, mailState.errLogged = map[string]*mailCache{}, map[string]bool{}, map[string]string{}
	}
	mailState.caches[id], mailState.loaded[id] = c.clone(), true
	mailState.mu.Unlock()
	mailGen.Add(1)
	mailRecount()
	p := mailCachePath(id)
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	if data, err := json.Marshal(c); err == nil {
		if err := AtomicWrite(p, data, 0o600); err != nil {
			logWarn("mail", "could not save the mail cache: %s", firstLine(err.Error()))
		}
	}
}

// mailCacheEdit changes an account's cache in memory and on disk.
func mailCacheEdit(id string, change func(c *mailCache)) {
	mailCacheIO.Lock()
	defer mailCacheIO.Unlock()
	c := mailCacheGet(id)
	change(c)
	mailCachePutLocked(id, c)
}

// mailCachedFolders is the account's Trash/Archive/Sent as last listed.
func mailCachedFolders(id string) mailFolders {
	return mailCacheGet(id).Folders
}

// mailRecount sums the unread counts of enabled accounts.
func mailRecount() {
	s := mailcalSettingsGet()
	total := 0
	for _, a := range s.Accounts {
		if a.MailOff {
			continue
		}
		total += mailCacheGet(a.ID).Unread
	}
	mailUnread.Store(int64(total))
}

// ── Loop ────────────────────────────────────────────────────────────────

func mailRunLoop() {
	mailRecount()
	// The first pass waits for the boot to settle (Wi-Fi, the clock) unless
	// something asks sooner.
	wait := 40 * time.Second
	for {
		reason := "poll"
		t := time.NewTimer(wait)
		select {
		case reason = <-mailKick:
		case <-t.C:
		}
		t.Stop()
		wait = time.Duration(mailcalSettingsGet().poll()) * time.Minute
		if gameScreenSuspended() {
			wait = time.Minute
			continue
		}
		mailSyncAll(reason)
	}
}

// mailCanSignIn reports whether an account has what its sign-in needs.
func mailCanSignIn(a mailcalAccount) bool {
	return (a.OAuth != "" && a.RefreshToken != "") || a.Password != ""
}

// mailSyncAll syncs every enabled account once.
func mailSyncAll(reason string) {
	s := mailcalSettingsGet()
	if !IsOnline() {
		mailOffline.Store(true)
		mailGen.Add(1)
		return
	}
	mailOffline.Store(false)
	mailSyncing.Store(true)
	mailGen.Add(1)
	defer func() {
		mailSyncing.Store(false)
		mailGen.Add(1)
	}()
	notify := !s.NoMailNotify
	for _, a := range s.Accounts {
		if a.MailOff || !mailCanSignIn(a) || strings.TrimSpace(a.IMAPHost) == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err := mailSyncAccount(ctx, a, notify)
		cancel()
		mailRecordError(a, err)
	}
	_ = reason
	mailRecount()
}

// mailRecordError writes the account's error sentence when it changed, and
// logs it once.
func mailRecordError(a mailcalAccount, err error) {
	text := mailErrorText(err, a)
	if text == "Stopped" {
		return
	}
	mailState.mu.Lock()
	if mailState.errLogged == nil {
		mailState.errLogged = map[string]string{}
	}
	logged := mailState.errLogged[a.ID]
	mailState.errLogged[a.ID] = text
	mailState.mu.Unlock()
	if text != "" && text != logged {
		logWarn("mail", "mail sync for one account failed: %s", text)
	}
	if text == a.MailError {
		return
	}
	if uerr := mailcalSettingsUpdate(func(s *mailcalSettings) {
		for i := range s.Accounts {
			if s.Accounts[i].ID == a.ID {
				s.Accounts[i].MailError = text
			}
		}
	}); uerr != nil && text != logged {
		logWarn("mail", "could not save the mail status: %s", firstLine(uerr.Error()))
	}
	mailGen.Add(1)
}

// mailSyncAccount syncs one account's inbox list.
func mailSyncAccount(ctx context.Context, a mailcalAccount, notify bool) error {
	c, err := mailIMAPOpen(ctx, a)
	if err != nil {
		return err
	}
	defer c.logout()
	box, err := c.selectBox("INBOX", true)
	if err != nil {
		return err
	}
	cache := mailCacheGet(a.ID)
	if cache.Validity != box.validity || box.validity == 0 {
		// A new inbox, or the server renumbered it: start over, quietly.
		cache = &mailCache{Validity: box.validity, Folders: cache.Folders, FoldersAt: cache.FoldersAt}
	}
	unseen, err := c.searchUIDs("UNSEEN")
	if err != nil {
		return err
	}
	unread := make(map[uint32]bool, len(unseen))
	for _, u := range unseen {
		unread[u] = true
	}
	// The newest messages by sequence number, with their UIDs.
	var window []uint32
	if box.exists > 0 {
		lo := uint32(1)
		if box.exists > mailKeep {
			lo = box.exists - mailKeep + 1
		}
		// "*" rather than the count: an EXPUNGE may have arrived during the
		// UID SEARCH, and a number past the end is an error.
		items, err := c.fetch(imapCmdTimeout, fmt.Sprintf("FETCH %d:* (UID FLAGS)", lo))
		if err != nil {
			return err
		}
		for _, it := range items {
			if uid := it["UID"].num(); uid > 0 {
				window = append(window, uid)
				if _, ok := it["FLAGS"]; ok {
					unread[uid] = !imapHasFlag(imapFlags(it["FLAGS"]), `\Seen`)
				}
			}
		}
	}
	known := make(map[uint32]mailCacheMsg, len(cache.Messages))
	for _, m := range cache.Messages {
		known[m.UID] = m
	}
	var need []uint32
	for _, uid := range window {
		if _, ok := known[uid]; !ok {
			need = append(need, uid)
		}
	}
	for start := 0; start < len(need); start += 25 {
		end := min(start+25, len(need))
		items, err := c.fetch(90*time.Second, "UID FETCH "+imapUIDSet(need[start:end])+
			" (UID FLAGS INTERNALDATE RFC822.SIZE BODY.PEEK[HEADER.FIELDS ("+mailHeaderFields+")] BODY.PEEK[TEXT]<0."+
			strconv.Itoa(mailSnippetBytes)+">)")
		if err != nil {
			return err
		}
		for _, it := range items {
			if m, ok := mailMsgFromFetch(it); ok {
				known[m.UID] = m
			}
		}
	}
	sort.Slice(window, func(i, j int) bool { return window[i] > window[j] })
	msgs := make([]mailCacheMsg, 0, len(window))
	var fresh []mailCacheMsg
	maxUID := cache.LastUID
	for _, uid := range window {
		m, ok := known[uid]
		if !ok {
			continue
		}
		m.Unread = unread[uid]
		msgs = append(msgs, m)
		if cache.Primed && uid > cache.LastUID && m.Unread {
			fresh = append(fresh, m)
		}
		maxUID = max(maxUID, uid)
	}
	cache.Messages = msgs
	cache.Unread = len(unseen)
	cache.LastUID = maxUID
	cache.Primed = true
	cache.SyncedAt = time.Now()
	if cache.Folders == (mailFolders{}) || time.Since(cache.FoldersAt) > 24*time.Hour || cache.FoldersAt.After(time.Now()) {
		if list, err := c.list(); err == nil {
			cache.Folders, cache.FoldersAt = imapPickFolders(list), time.Now()
		}
	}
	mailCachePut(a.ID, cache)
	if notify && len(fresh) > 0 && !mailcalSettingsGet().NoMailNotify {
		mailNotify(a, fresh)
	}
	return nil
}

// mailMsgFromFetch builds a list row from a FETCH answer.
func mailMsgFromFetch(it map[string]imapValue) (mailCacheMsg, bool) {
	uid := it["UID"].num()
	if uid == 0 {
		return mailCacheMsg{}, false
	}
	block := []byte(it["HEADER"].str())
	h := mailReadHeader(block)
	m := mailCacheMsg{UID: uid, Size: int(it["RFC822.SIZE"].num())}
	m.From, m.FromAddr = mailParseAddr(h.Get("From"))
	_, m.ReplyTo = mailParseAddr(h.Get("Reply-To"))
	m.To = mailAddrList(h.Get("To"))
	m.Cc = mailAddrList(h.Get("Cc"))
	m.Subject = mailDecodeHeader(h.Get("Subject"))
	if t, err := h.Date(); err == nil {
		m.Date = t
	} else if t, err := time.Parse("2-Jan-2006 15:04:05 -0700", strings.TrimSpace(it["INTERNALDATE"].str())); err == nil {
		m.Date = t
	}
	m.MessageID = mailMsgIDList(h.Get("Message-ID"), 1)
	m.References = mailMsgIDList(h.Get("References")+" "+h.Get("In-Reply-To"), 20)
	m.Attach = strings.Contains(strings.ToLower(h.Get("Content-Type")), "multipart/mixed")
	m.Snippet = mailSnippet(block, []byte(it["TEXT"].str()))
	if flags, ok := it["FLAGS"]; ok {
		m.Unread = !imapHasFlag(imapFlags(flags), `\Seen`)
	}
	return m, true
}

// mailNotify announces new mail: each of up to three messages, or a count.
func mailNotify(a mailcalAccount, fresh []mailCacheMsg) {
	// A in the bell opens Mail (notifications_actions.go).
	open := notifAction{Label: "Open Mail", Kind: notifActScene, Arg: mailSceneName}
	if len(fresh) > 3 {
		notifPost(notifSpec{Message: strconv.Itoa(len(fresh)) + " new emails in " + a.label(), Priority: notifInfo,
			Source: notifSrcMail, Action: open, Toast: true})
		return
	}
	for i := len(fresh) - 1; i >= 0; i-- {
		m := fresh[i]
		subject := setOr(m.Subject, "(no subject)")
		notifPost(notifSpec{Message: "New email from " + mailClean(m.sender(), 40) + ": " + mailClean(subject, 80),
			Priority: notifInfo, Source: notifSrcMail, Action: open, Toast: true})
	}
}

// ── Opened messages ─────────────────────────────────────────────────────

// mailBodyEntry is one opened message.
type mailBodyEntry struct {
	key  string
	raw  []byte
	body mailBody
}

var mailBodies struct {
	mu   sync.Mutex
	list []*mailBodyEntry // oldest first
}

// mailBodyKey names a message across UIDVALIDITY changes.
func mailBodyKey(id string, validity, uid uint32) string {
	return id + "/" + strconv.FormatUint(uint64(validity), 10) + "/" + strconv.FormatUint(uint64(uid), 10)
}

func mailBodyGet(key string) (*mailBodyEntry, bool) {
	mailBodies.mu.Lock()
	defer mailBodies.mu.Unlock()
	for i, e := range mailBodies.list {
		if e.key == key {
			mailBodies.list = append(append(mailBodies.list[:i:i], mailBodies.list[i+1:]...), e)
			return e, true
		}
	}
	return nil, false
}

func mailBodyPut(e *mailBodyEntry) {
	mailBodies.mu.Lock()
	defer mailBodies.mu.Unlock()
	mailBodies.list = append(mailBodies.list, e)
	total := 0
	for _, x := range mailBodies.list {
		total += len(x.raw) + len(x.body.Text)
	}
	for len(mailBodies.list) > 1 && (len(mailBodies.list) > mailBodyCacheMax || total > mailBodyCacheBytes) {
		total -= len(mailBodies.list[0].raw) + len(mailBodies.list[0].body.Text)
		mailBodies.list = mailBodies.list[1:]
	}
}

// mailBodyForget drops one opened message (a mailBodyKey), or every one of
// an account's (prefix "<id>/").
func mailBodyForget(prefix string) {
	mailBodies.mu.Lock()
	defer mailBodies.mu.Unlock()
	kept := mailBodies.list[:0]
	for _, e := range mailBodies.list {
		if e.key != prefix && !(strings.HasSuffix(prefix, "/") && strings.HasPrefix(e.key, prefix)) {
			kept = append(kept, e)
		}
	}
	mailBodies.list = kept
}

var errMailBoxChanged = &mailError{kind: mailErrServer, msg: "the inbox changed on the server — sync and try again"}

// mailOpenInbox signs in and selects INBOX for changes, checking that its
// numbering is the one the cache knows.
func mailOpenInbox(ctx context.Context, a mailcalAccount, validity uint32) (*imapConn, error) {
	c, err := mailIMAPOpen(ctx, a)
	if err != nil {
		return nil, err
	}
	box, err := c.selectBox("INBOX", false)
	if err == nil && box.validity != validity {
		err = errMailBoxChanged
	}
	if err != nil {
		c.logout()
		return nil, err
	}
	return c, nil
}

// mailFetchBody fetches (or recalls) a message's body and marks it read.
// Any goroutine.
func mailFetchBody(ctx context.Context, a mailcalAccount, validity, uid uint32) (*mailBodyEntry, error) {
	key := mailBodyKey(a.ID, validity, uid)
	if e, ok := mailBodyGet(key); ok {
		return e, nil
	}
	c, err := mailOpenInbox(ctx, a, validity)
	if err != nil {
		return nil, err
	}
	defer c.logout()
	set := strconv.FormatUint(uint64(uid), 10)
	items, err := c.fetch(120*time.Second, "UID FETCH "+set+" (UID RFC822.SIZE BODY.PEEK[]<0."+strconv.Itoa(mailBodyCap)+">)")
	if err != nil {
		return nil, err
	}
	var raw string
	size, found := 0, false
	for _, it := range items {
		if it["UID"].num() == uid {
			raw, size, found = it["BODY"].str(), int(it["RFC822.SIZE"].num()), true
		}
	}
	if !found {
		return nil, &mailError{kind: mailErrServer, msg: "this message is no longer on the server"}
	}
	truncated := len(raw) >= mailBodyCap && size > len(raw)
	e := &mailBodyEntry{key: key, raw: []byte(raw)}
	e.body = mailParseBody(e.raw, truncated)
	mailBodyPut(e)
	if err := c.storeFlags(uid, true, `\Seen`); err == nil {
		mailMarkLocal(a.ID, uid, false)
	}
	return e, nil
}

// mailMarkLocal changes a message's unread state in the cache.
func mailMarkLocal(id string, uid uint32, unread bool) {
	mailCacheEdit(id, func(c *mailCache) {
		for i := range c.Messages {
			if c.Messages[i].UID != uid {
				continue
			}
			if c.Messages[i].Unread != unread {
				c.Messages[i].Unread = unread
				if unread {
					c.Unread++
				} else if c.Unread > 0 {
					c.Unread--
				}
			}
		}
	})
}

// mailSetUnread marks a message read or unread on the server.
func mailSetUnread(ctx context.Context, a mailcalAccount, validity, uid uint32, unread bool) error {
	c, err := mailOpenInbox(ctx, a, validity)
	if err != nil {
		return err
	}
	defer c.logout()
	if err := c.storeFlags(uid, !unread, `\Seen`); err != nil {
		return err
	}
	mailMarkLocal(a.ID, uid, unread)
	return nil
}

// mailMoveMessage moves a message to Trash ("trash") or the archive
// ("archive").
func mailMoveMessage(ctx context.Context, a mailcalAccount, validity, uid uint32, where string) error {
	c, err := mailOpenInbox(ctx, a, validity)
	if err != nil {
		return err
	}
	defer c.logout()
	folders := mailCachedFolders(a.ID)
	if (where == "trash" && folders.Trash == "") || (where == "archive" && folders.Archive == "") {
		if list, err := c.list(); err == nil {
			folders = imapPickFolders(list)
		}
	}
	dest := folders.Trash
	if where == "archive" {
		dest = folders.Archive
	}
	if dest == "" {
		if where == "archive" {
			return &mailError{kind: mailErrServer, msg: "this account has no Archive folder"}
		}
		return &mailError{kind: mailErrServer, msg: "this account has no Trash folder"}
	}
	if err := c.move(uid, dest); err != nil {
		return err
	}
	mailCacheEdit(a.ID, func(m *mailCache) {
		kept := m.Messages[:0]
		for _, msg := range m.Messages {
			if msg.UID == uid {
				if msg.Unread && m.Unread > 0 {
					m.Unread--
				}
				continue
			}
			kept = append(kept, msg)
		}
		m.Messages = kept
	})
	mailBodyForget(mailBodyKey(a.ID, validity, uid))
	return nil
}

// mailMarkAllRead marks every unread inbox message read.
func mailMarkAllRead(ctx context.Context, a mailcalAccount, validity uint32) error {
	c, err := mailOpenInbox(ctx, a, validity)
	if err != nil {
		return err
	}
	defer c.logout()
	unseen, err := c.searchUIDs("UNSEEN")
	if err != nil {
		return err
	}
	for start := 0; start < len(unseen); start += 500 {
		if err := c.storeFlagsSet(unseen[start:min(start+500, len(unseen))], true, `\Seen`); err != nil {
			return err
		}
	}
	mailCacheEdit(a.ID, func(m *mailCache) {
		for i := range m.Messages {
			m.Messages[i].Unread = false
		}
		m.Unread = 0
	})
	return nil
}

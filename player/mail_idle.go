package main

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MAIL-3: push mail through IMAP IDLE (RFC 2177).
//
// One connection per enabled account sits in IDLE while JukaHub is in the
// foreground: the server pushes when the mailbox changes, the account is synced
// at once, so a new message notifies in seconds instead of at the next poll.
// IDLE is renewed every 25 minutes (the RFC wants it re-issued before 29) and
// dropped when the app sleeps or the screen goes off, then restored on wake.
// Polling (mailRunLoop) stays the fallback and is what every case that cannot
// hold an IDLE connection falls back to: a server that does not advertise it, a
// background app, or an account whose connection keeps failing.

// mailIdleRenew is how long one IDLE stands before it is re-issued.
var mailIdleRenew = 25 * time.Minute

// mailIdleRetry is how long a failed IDLE session waits before trying again.
var mailIdleRetry = 30 * time.Second

// mailIdleTick is how often the supervisor looks at the accounts and at
// whether the app is in the foreground.
var mailIdleTick = 2 * time.Second

// mailIdleForeground reports whether the app is awake and on screen: the
// wakeups only make sense then, and sleep must drop the connections.
var mailIdleForeground = func() bool {
	return !powerSleepActive() && !ambientIsScreenOff() && !gameScreenSuspended()
}

// mailIdleOnline is IsOnline, indirect so a test can hold the network steady.
var mailIdleOnline = IsOnline

// mailIdleSync, when set, replaces the sync request IDLE makes on a change. A
// test sets it to watch; the app leaves it nil and calls mailSyncNow.
var mailIdleSync func(a mailcalAccount)

// mailIdleAskSync is what a new message on an IDLE connection does.
func mailIdleAskSync(a mailcalAccount) {
	if mailIdleSync != nil {
		mailIdleSync(a)
		return
	}
	mailSyncNow("idle")
}

// errMailIdleUnsupported is a server that does not advertise IDLE: polling
// covers that account, and nothing reconnects for it until its settings change.
var errMailIdleUnsupported = errors.New("the mail server does not offer IDLE")

// mailIdleWaiter is one account's running IDLE session.
type mailIdleWaiter struct {
	cancel context.CancelFunc
	print  string // the account fields the session depends on
}

// mailIdleGroup is the supervisor's state: who is waiting, and which accounts
// already said they have no IDLE.
type mailIdleGroup struct {
	mu          sync.Mutex
	waiters     map[string]mailIdleWaiter
	unsupported map[string]string
}

var (
	mailIdleOnce sync.Once
	mailIdle     = mailIdleGroup{
		waiters:     map[string]mailIdleWaiter{},
		unsupported: map[string]string{},
	}
)

// mailIdleStart starts the supervisor. Idempotent; any goroutine.
func mailIdleStart() {
	mailIdleOnce.Do(func() { go mailIdleRun() })
}

// mailIdleRun reconciles once, then on every tick. Never returns.
func mailIdleRun() {
	mailIdleReconcile()
	t := time.NewTicker(mailIdleTick)
	defer t.Stop()
	for range t.C {
		mailIdleReconcile()
	}
}

// mailIdleReconcile starts a waiter for every account that should have one and
// stops the waiters that should not: an account removed, switched off, signed
// out or edited, and every account at all while the app is asleep, off screen,
// in a game or offline.
func mailIdleReconcile() {
	want := map[string]mailcalAccount{}
	if mailIdleForeground() && mailIdleOnline() {
		for _, a := range mailcalSettingsGet().Accounts {
			if a.MailOff || !mailCanSignIn(a) || strings.TrimSpace(a.IMAPHost) == "" {
				continue
			}
			want[a.ID] = a
		}
	}
	type job struct {
		ctx context.Context
		a   mailcalAccount
	}
	var start []job
	mailIdle.mu.Lock()
	for id, w := range mailIdle.waiters {
		a, keep := want[id]
		if !keep || mailIdlePrint(a) != w.print {
			w.cancel()
			delete(mailIdle.waiters, id)
		}
	}
	for id, a := range want {
		if _, running := mailIdle.waiters[id]; running {
			continue
		}
		// A server that said it has no IDLE is not asked again until its
		// settings change, so a polled account does not redial every retry.
		if mailIdle.unsupported[id] == mailIdlePrint(a) {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		mailIdle.waiters[id] = mailIdleWaiter{cancel: cancel, print: mailIdlePrint(a)}
		start = append(start, job{ctx: ctx, a: a})
	}
	mailIdle.mu.Unlock()
	for _, j := range start {
		go mailIdleWaiterRun(j.ctx, j.a)
	}
}

// mailIdlePrint is what a session depends on; a change to any of it ends the
// session and a new one starts with the new settings.
func mailIdlePrint(a mailcalAccount) string {
	return strings.Join([]string{
		a.IMAPHost, strconv.Itoa(a.IMAPPort), a.IMAPSec, a.user(), a.OAuth,
		strconv.FormatBool(a.hasSecret()),
	}, "|")
}

// mailIdleWaiterRun keeps one account's IDLE session alive until ctx ends.
func mailIdleWaiterRun(ctx context.Context, a mailcalAccount) {
	defer mailIdle.forget(a.ID)
	for ctx.Err() == nil {
		err := mailIdleSession(ctx, a)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, errMailIdleUnsupported):
			mailIdle.markUnsupported(a)
			return
		case err != nil:
			logDebug("mail", "IDLE for %s ended: %s", a.label(), firstLine(err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(mailIdleRetry):
		}
	}
}

// forget drops the waiter's entry once its goroutine is done, so the
// supervisor starts a fresh one on its next look.
func (g *mailIdleGroup) forget(id string) {
	g.mu.Lock()
	if w, ok := g.waiters[id]; ok {
		w.cancel()
		delete(g.waiters, id)
	}
	g.mu.Unlock()
}

// markUnsupported records that this account's server has no IDLE.
func (g *mailIdleGroup) markUnsupported(a mailcalAccount) {
	g.mu.Lock()
	g.unsupported[a.ID] = mailIdlePrint(a)
	delete(g.waiters, a.ID)
	g.mu.Unlock()
	logDebug("mail", "%s: the server does not offer IDLE, polling covers it", a.label())
}

// mailIdleSession holds one connection in IDLE until ctx ends or the connection
// does. It returns errMailIdleUnsupported when the server does not advertise
// IDLE, so the caller stops asking.
func mailIdleSession(ctx context.Context, a mailcalAccount) error {
	c, err := mailIMAPOpen(ctx, a)
	if err != nil {
		return err
	}
	defer c.logout()
	if !c.caps["IDLE"] {
		return errMailIdleUnsupported
	}
	if _, err := c.selectBox("INBOX", true); err != nil {
		return err
	}
	for ctx.Err() == nil {
		changed, err := c.idleWait(ctx, mailIdleRenew)
		if changed && ctx.Err() == nil {
			mailIdleAskSync(a)
		}
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

// idleWait sends IDLE and waits up to d for the server to report a change. It
// re-arms by sending DONE and returning; the caller starts the next IDLE. The
// context ending, or d running out, is not an error: both end one IDLE, not the
// session. changed is true when the mailbox moved (new or moved messages).
func (c *imapConn) idleWait(ctx context.Context, d time.Duration) (bool, error) {
	tag, err := c.begin(d + imapCmdTimeout)
	if err != nil {
		return false, err
	}
	if err := c.write(tag + " IDLE\r\n"); err != nil {
		return false, err
	}
	// The server answers "+ idling" once it is watching, or NO/BAD if it will
	// not (RFC 2177 makes the continuation the only success).
	for {
		r, err := c.readResp()
		if err != nil {
			return false, err
		}
		if r.tag == "+" {
			break
		}
		if r.tag == tag {
			return false, c.statusErr(r)
		}
	}
	changed := false
	_ = c.conn.SetDeadline(time.Now().Add(d))
	for {
		r, err := c.readResp()
		if err != nil {
			if ctx.Err() == nil && !imapTimedOut(err) {
				// The connection itself failed: there is nobody to tell DONE.
				return changed, err
			}
			break // the renew window closed, or the app is sleeping: DONE below
		}
		if r.tag == tag {
			// The server ended the IDLE itself.
			return changed, imapIdleStatus(r)
		}
		if r.tag != "*" {
			continue
		}
		switch r.kind {
		case "EXISTS", "RECENT", "EXPUNGE", "FETCH":
			changed = true
		}
		if changed {
			// The point of IDLE: one DONE and the caller syncs now, rather than
			// sitting out the rest of the window.
			break
		}
	}
	if ctx.Err() != nil {
		// Stopped on purpose: the connection is dropped, not talked to.
		return changed, ctx.Err()
	}
	_ = c.conn.SetDeadline(time.Now().Add(imapCmdTimeout))
	if err := c.write("DONE\r\n"); err != nil {
		return changed, err
	}
	for {
		r, err := c.readResp()
		if err != nil {
			return changed, err
		}
		if r.tag == tag {
			return changed, c.statusErrOK(r)
		}
	}
}

// statusErrOK is statusErr for a command whose OK is the normal end (DONE):
// only NO and BAD are errors.
func (c *imapConn) statusErrOK(r *imapResp) error {
	if strings.EqualFold(r.status, "OK") {
		return nil
	}
	return c.statusErr(r)
}

// imapIdleStatus is the server ending an IDLE by itself: OK is fine, BYE, NO
// and BAD are not.
func imapIdleStatus(r *imapResp) error {
	if strings.EqualFold(r.status, "OK") || strings.EqualFold(r.status, "BYE") {
		return nil
	}
	return &mailError{kind: mailErrServer, msg: setOr(mailClean(r.text, 160), "the mail server ended the IDLE with "+r.status)}
}

// imapTimedOut reports whether err is a read that ran past its deadline rather
// than a connection that broke.
func imapTimedOut(err error) bool {
	var ne interface{ Timeout() bool }
	return errors.As(err, &ne) && ne.Timeout()
}

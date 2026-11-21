package main

// raoffline_replay.go — sending the unlocks Offline RetroAchievements kept
// (raoffline.go) once retroachievements.org can be reached again.
//
// One worker goroutine, started the first time something is queued or found
// waiting at start-up, sends the queue in order whenever JukaHub is online.
// It never blocks anything: each request has the proxy's short timeout, and
// between rounds it sleeps — on a refused connection for a doubling back-off
// (raoReplayFirstWait up to raoReplayMaxWait), offline until the next check,
// and after a refused login token until RetroArch signs in again (a new
// token is kept by the proxy) or "Send now" is pressed.
//
// Each unlock goes as RetroArch made it, with the current login token and,
// where the protocol allows, how long ago it happened: rcheevos' "o" (seconds
// since the unlock) with the "v" signature made again for it. The signature
// recipe is checked against RetroArch's own request first
// (raoAwardSignatureHolds); when it does not match, or the server refuses the
// request with the age, it goes exactly as RetroArch sent it and the server
// dates it at its arrival. An unlock the account already has counts as sent;
// one the server refuses raoReplayMaxTries times is dropped (and counted),
// so a withdrawn achievement cannot block the queue for ever.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	raoReplayFirstWait = 30 * time.Second
	raoReplayMaxWait   = 15 * time.Minute
	// raoReplayOfflineWait is how often an offline JukaHub looks again.
	raoReplayOfflineWait = 30 * time.Second
	// raoReplayLoginWait is the wait after the server refused the token.
	raoReplayLoginWait = 30 * time.Minute
	// raoReplayMaxTries refusals drop an unlock.
	raoReplayMaxTries = 3
)

var raoReplay struct {
	once sync.Once
	kick chan struct{}
	// now is "Send now": the next round ignores the back-off and the wait
	// for a new token.
	now atomic.Bool
}

// raoReplayKick wakes the worker, starting it the first time.
func raoReplayKick() {
	raoReplay.once.Do(func() {
		raoReplay.kick = make(chan struct{}, 1)
		go raoReplayLoop()
	})
	select {
	case raoReplay.kick <- struct{}{}:
	default:
	}
}

// raoReplaySendNow is Settings' "Send now".
func raoReplaySendNow() {
	raoReplay.now.Store(true)
	raoReplayKick()
}

// raoSendResult is what the server said to one unlock.
type raoSendResult int

const (
	raoSendOK        raoSendResult = iota // it has it
	raoSendRetry                          // no answer, or busy: later
	raoSendNeedLogin                      // the token was refused
	raoSendVerbatim                       // refused with the age: send it plain
	raoSendRefused                        // refused
)

// raoPassResult is how a round ended.
type raoPassResult int

const (
	raoPassDone raoPassResult = iota
	raoPassRetry
	raoPassNeedLogin
)

func raoReplayLoop() {
	defer func() {
		if p := recover(); p != nil {
			logWarn("raoffline", "sending offline unlocks stopped: %v", p)
		}
	}()
	backoff := time.Duration(0)
	needLogin := false
	var loginGen uint64
	for {
		if raoReplay.now.Swap(false) {
			needLogin, backoff = false, 0
		}
		var wait time.Duration
		switch {
		case raoCounts.pending.Load() == 0:
			wait = 0 // until something is queued
		case needLogin && raoLoginGeneration() == loginGen:
			wait = raoReplayLoginWait
		case !IsOnline():
			wait = raoReplayOfflineWait
		default:
			needLogin = false
			sent, res := raoReplayPass()
			switch res {
			case raoPassNeedLogin:
				needLogin, loginGen = true, raoLoginGeneration()
				wait = raoReplayLoginWait
				logWarn("raoffline", "RetroAchievements refused the saved sign-in; offline unlocks wait for RetroArch to sign in again")
			case raoPassRetry:
				switch {
				case backoff == 0:
					backoff = raoReplayFirstWait
				case backoff < raoReplayMaxWait:
					backoff *= 2
				}
				if backoff > raoReplayMaxWait {
					backoff = raoReplayMaxWait
				}
				wait = backoff
			default:
				backoff = 0
			}
			if sent > 0 {
				msg := "RetroAchievements: 1 unlock earned offline was sent"
				if sent > 1 {
					msg = fmt.Sprintf("RetroAchievements: %d unlocks earned offline were sent", sent)
				}
				logInfo("raoffline", "%d offline unlock(s) sent", sent)
				tbxPost(func() { ShowToast(msg, ToastKindSuccess) })
			}
		}
		raoPersistAges(false)
		if wait == 0 && raoCounts.pending.Load() > 0 {
			// Something is still queued after a round that ended well (an
			// unlock refused once): never spin.
			wait = raoReplayFirstWait
		}
		var timer *time.Timer
		var fire <-chan time.Time
		if wait > 0 {
			timer = time.NewTimer(wait)
			fire = timer.C
		}
		select {
		case <-raoReplay.kick:
		case <-fire:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// raoReplayPass sends the queue once, in order, and reports how many went.
func raoReplayPass() (int, raoPassResult) {
	sent := 0
	res := raoPassDone
	for _, q := range raoQueueSnapshot() {
		l, ok := raoLoginFor(q.User)
		if !ok || l.Token == "" {
			// No sign-in kept for this account (the cache was made by an
			// older version, or another account): wait for one.
			res = raoPassNeedLogin
			continue
		}
		r := raoReplayOne(q, l)
		if r == raoSendVerbatim {
			raoQueueResolve(q.ID, raoOutcomeVerbatim)
			q.Verbatim = true
			r = raoReplayOne(q, l)
		}
		switch r {
		case raoSendOK:
			raoQueueResolve(q.ID, raoOutcomeSent)
			sent++
		case raoSendRetry:
			return sent, raoPassRetry
		case raoSendNeedLogin:
			return sent, raoPassNeedLogin
		default:
			if q.Tries+1 >= raoReplayMaxTries {
				logWarn("raoffline", "RetroAchievements refused achievement %s %d times; it was dropped", q.Params["a"], raoReplayMaxTries)
				raoQueueResolve(q.ID, raoOutcomeRejected)
			} else {
				raoQueueResolve(q.ID, raoOutcomeTried)
				res = raoPassRetry
			}
		}
	}
	return sent, res
}

// raoReplayOne sends one unlock and sorts the server's answer.
func raoReplayOne(q raoQueued, l raoLogin) raoSendResult {
	form := url.Values{}
	for k, v := range q.Params {
		form.Set(k, v)
	}
	form.Set("t", l.Token)
	withAge := false
	if !q.Verbatim && raoAwardSignatureHolds(q.Params) {
		// RetroArch's own retry may have sent an age already: this one adds
		// to it.
		before, _ := strconv.Atoi(q.Params["o"])
		if secs := before + int(q.AgeSec); secs > 0 {
			id, _ := strconv.Atoi(q.Params["a"])
			mode := q.Params["h"]
			if mode == "" {
				mode = "0"
			}
			form.Set("o", strconv.Itoa(secs))
			form.Set("v", raoAwardSignature(id, q.Params["u"], mode, secs))
			withAge = true
		}
	}
	status, body, err := raoPostForm(form, q.Agent)
	if err != nil || status >= 500 || status == http.StatusTooManyRequests {
		return raoSendRetry
	}
	var ans struct {
		Success bool   `json:"Success"`
		Error   string `json:"Error"`
		Code    string `json:"Code"`
	}
	if _, ok := raoJSONField(body, "Success"); !ok || json.Unmarshal(body, &ans) != nil {
		// Not the server's answer: a captive portal's or a proxy's page
		// (often a 200). Counting it as a refusal would drop the unlock
		// after raoReplayMaxTries such rounds; it is tried again later.
		return raoSendRetry
	}
	why := strings.ToLower(ans.Error + " " + ans.Code)
	switch {
	case ans.Success && status == http.StatusOK:
		return raoSendOK
	case strings.Contains(why, "already"):
		return raoSendOK
	case status == http.StatusUnauthorized || status == http.StatusForbidden ||
		strings.Contains(why, "token") || strings.Contains(why, "credential") ||
		strings.Contains(why, "password") || strings.Contains(why, "login"):
		// Before the age: a refused token is no verdict on the age, which
		// would otherwise be given up for good.
		return raoSendNeedLogin
	case withAge:
		return raoSendVerbatim
	}
	msg := ans.Error
	if len(msg) > 120 {
		msg = msg[:120]
	}
	logInfo("raoffline", "RetroAchievements refused offline unlock %s: %s", q.Params["a"], msg)
	return raoSendRefused
}

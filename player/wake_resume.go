package main

// wake_resume.go — noticing that the handheld slept, and getting Chat and Juka
// back the moment the network is.
//
// The firmware owns the power key on the TrimUI: a short press suspends the
// whole device and JukaHub simply stops wherever it was, then carries on when
// the device wakes, with no signal that anything happened. Since firmware 1.0.4
// the stock image also turns Wi-Fi off for the sleep (it used to wake the device
// by itself), so every wake starts with the radio coming back from nothing: the
// supplicant joins again and DHCP runs again, five to ten seconds on a good
// day. JukaHub then took up to twenty more seconds to notice (the connectivity
// probe's interval), and Juka's request back-off and discordgo's own reconnect
// back-off (1, 2, 4 … 64 s while the radio was still down) added the rest:
// "a minute before I can look at the community chat or use AI".
//
// The watcher here reads two clocks once a second. On Linux CLOCK_BOOTTIME
// keeps counting while the device is suspended and CLOCK_MONOTONIC does not, so
// a second in which BOOTTIME moved on by minutes is a sleep of that length —
// and neither clock is moved by network time or a time-zone change
// (clockjump.go), so neither can pass for a sleep. A tick that arrives late in
// monotonic time means the process itself was stopped (a firmware that freezes
// the foreground app instead of suspending), and counts too. On Windows Go's
// monotonic clock reads the interrupt time, which keeps counting through sleep
// and hibernation, so there it is the late tick that shows the sleep; the wall
// clock running ahead of the monotonic one (less JukaHub's own clock jumps)
// covers a platform whose monotonic clock stops.
//
// A wake opens a reconnect window in the network monitor (netmon.go): the probe
// runs at once and every 1.5 s until the internet answers, "offline" is not
// announced meanwhile, and Chat, the voice orb and voice typing wait for the
// network instead of failing. On the handheld the radio gets a nudge if it is
// slow (wifi_resume.go). When the internet answers, Juka's back-off is
// forgotten and the community chat's gateway is reopened at once.
//
// Anything else that cares about a wake can subscribeWake (a channel, like
// subscribeForegroundWake) or compare wakeCount with the count it last saw.

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	// wakeTick is how often the watcher reads the clocks.
	wakeTick = time.Second
	// wakeMinGap is the shortest absence that counts as a sleep. A busy
	// handheld runs a tick milliseconds late, not seconds; a power-key sleep is
	// never shorter than the two presses it takes.
	wakeMinGap = 3 * time.Second
)

// wakeEvent is one wake: when it was noticed and how long the device was away.
type wakeEvent struct {
	At    time.Time
	Slept time.Duration
	// Suspend is true for a system sleep. It is false only where the clocks
	// can tell (Linux) that the system kept running while this process was
	// stopped.
	Suspend bool
}

// wakeSample is one reading of the two clocks: one that counts time spent
// asleep and the monotonic one.
type wakeSample struct {
	withSleep time.Duration
	mono      time.Duration
}

// wakes is the fan-out. count can be read every frame without the lock; the
// lock keeps last and the subscribers.
var wakes struct {
	count atomic.Uint64
	mu    sync.Mutex
	last  wakeEvent
	subs  map[chan struct{}]struct{}
}

var wakeWatchOnce sync.Once

// startWakeWatcher starts the clock watcher once per process. The network
// monitor starts it; subscribeWake does too, so a subscriber never waits on a
// watcher nobody started.
func startWakeWatcher() {
	wakeWatchOnce.Do(func() { go wakeWatchLoop() })
}

// subscribeWake returns a channel that receives one value after each wake.
// Subscribe once and only read it; a second wake before the first was read
// costs nothing.
func subscribeWake() <-chan struct{} {
	startWakeWatcher()
	ch := make(chan struct{}, 1)
	wakes.mu.Lock()
	if wakes.subs == nil {
		wakes.subs = make(map[chan struct{}]struct{}, 2)
	}
	wakes.subs[ch] = struct{}{}
	wakes.mu.Unlock()
	return ch
}

// wakeCount is how many wakes this run has seen; an observer on its own tick
// compares it with the count it last saw.
func wakeCount() uint64 { return wakes.count.Load() }

// wakeLast is the latest wake, if there was one.
func wakeLast() (wakeEvent, bool) {
	wakes.mu.Lock()
	defer wakes.mu.Unlock()
	return wakes.last, wakes.count.Load() > 0
}

// wakeWatchLoop reads the clocks once a second and reports a gap.
func wakeWatchLoop() {
	defer func() {
		if r := recover(); r != nil {
			logWarn("wake", "watcher stopped: %v", r)
		}
	}()
	ticker := time.NewTicker(wakeTick)
	defer ticker.Stop()
	// Only clock jumps after this point are taken off the wall clock.
	var jumps clockJumpView
	clockJumpSeen(&jumps)
	prev, havePrev := wakeClocks()
	for range ticker.C {
		cur, ok := wakeClocks()
		if !ok {
			havePrev = false
			continue
		}
		shift, _ := clockJumpSeen(&jumps)
		if !havePrev {
			prev, havePrev = cur, true
			continue
		}
		jump := time.Duration(0)
		if wakeClockIsWall {
			jump = shift.clock
		}
		gap, suspend := wakeGap(prev, cur, wakeTick, jump)
		prev = cur
		if gap >= wakeMinGap {
			wakeNotice(wakeEvent{At: time.Now(), Slept: gap, Suspend: suspend || !wakeCanTellFrozen})
		}
	}
}

// wakeGap is how long the device (or this process) was away between two
// readings taken tick apart, and whether the system itself slept. slept is the
// time the sleep-counting clock moved on beyond the monotonic one (jump, a
// clock change JukaHub made itself, taken off first); stalled is how late the
// tick came in monotonic time. Pure.
func wakeGap(prev, cur wakeSample, tick, jump time.Duration) (time.Duration, bool) {
	dMono := cur.mono - prev.mono
	slept := cur.withSleep - prev.withSleep - jump - dMono
	stalled := dMono - tick
	if slept >= stalled {
		return slept, true
	}
	return stalled, false
}

// wakeNotice records a wake, logs it once and tells the network side first, so
// a request made in the same instant already sees the reconnect window.
func wakeNotice(ev wakeEvent) {
	wakes.mu.Lock()
	wakes.last = ev
	wakes.count.Add(1)
	subs := make([]chan struct{}, 0, len(wakes.subs))
	for ch := range wakes.subs {
		subs = append(subs, ch)
	}
	wakes.mu.Unlock()
	if ev.Suspend {
		logInfo("wake", "slept %s", ev.Slept.Round(time.Second))
	} else {
		logInfo("wake", "JukaHub was stopped for %s (the system kept running)", ev.Slept.Round(time.Second))
	}
	netWakeNoted()
	wifiResumeAfterWake()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// ── Chat and Juka when the internet answers again ────────────────────────

// discordCloseResumable is the close code the gateway is reopened with:
// websocket.CloseServiceRestart. discordgo's Close sends 1000, which ends the
// session on Discord's side; after 1012 the reopen resumes it, and Discord
// replays the messages the handheld missed while it slept.
const discordCloseResumable = 1012

const (
	// chatGatewayStaleAck is how old the last heartbeat answer may be on a
	// gateway that is still alive: Discord asks for one every 41.25 s.
	chatGatewayStaleAck = 50 * time.Second
	// chatCatchUpEvery spaces the history catch-ups after a reopen.
	chatCatchUpEvery = 30 * time.Second
	// chatGatewayRetryMax caps the wait between failed reopens.
	chatGatewayRetryMax = time.Minute
)

var (
	// chatGatewayKickWanted asks the sender loop to look at the gateway
	// (chatGatewayKickIfWanted). chatGatewayKickAt holds a failed reopen's
	// next attempt (UnixNano; 0 is now) and chatGatewayRetry its back-off.
	chatGatewayKickWanted atomic.Bool
	chatGatewayKickAt     atomic.Int64
	chatGatewayRetry      atomic.Int64
	// chatGatewayKicking is set while a reopen runs, so two never overlap.
	chatGatewayKicking atomic.Bool
	// chatCatchUpAt is when the history was last fetched after a reopen.
	chatCatchUpAt atomic.Int64
)

// chatNetworkBack runs when the internet answers again, after a wake or after
// an offline spell (netmon.go calls it from the probe goroutine). Juka's
// back-off from the requests that failed meanwhile is forgotten, the Discord
// connect and send back-offs too (what the banner's Retry does), the gateway
// is looked at, and the sender loop runs now instead of at its next tick.
func chatNetworkBack() {
	chatAIRetryReset()
	chatGatewayKickAt.Store(0)
	chatGatewayRetry.Store(0)
	chatGatewayKickWanted.Store(true)
	chatRetryRequested.Store(true)
	chatWakeSync()
}

// chatGatewayKickIfWanted reopens the community chat's gateway when the
// network came back and the connection is not known to be alive. It runs on
// the sender loop while online (chatSyncDiscord) and only starts the reopen;
// the reopen itself runs on its own goroutine because discordgo's Close waits
// a second and its Open can wait on a slow handshake.
//
// The session is kept, never replaced: discordgo's own reconnect loop may be
// sleeping in its back-off for this very session, and a second session would
// deliver every message twice. Close and Open on the same one leave that loop
// nothing to do (its next Open answers "already open" and it stops).
func chatGatewayKickIfWanted(now time.Time) {
	if !chatGatewayKickWanted.Load() || chatGatewayKicking.Load() {
		return
	}
	if at := chatGatewayKickAt.Load(); at != 0 && now.UnixNano() < at {
		return
	}
	dg, ch := chatLiveNow()
	if dg == nil || ch == "" {
		// Nothing open: the loop's own connect, its back-off just reset, opens one.
		chatGatewayKickWanted.Store(false)
		return
	}
	if !dg.TryRLock() {
		// discordgo is opening or closing the connection right now; the next
		// pass looks again.
		return
	}
	ack := dg.LastHeartbeatAck
	dg.RUnlock()
	chatGatewayKickWanted.Store(false)
	if chatGatewayUp.Load() && !ack.IsZero() && time.Since(ack) < chatGatewayStaleAck {
		// Its heartbeat was answered a moment ago: the connection survived.
		return
	}
	chatGatewayKicking.Store(true)
	go chatGatewayReopen(dg, ch)
}

// chatGatewayReopen closes the gateway connection the way that keeps the
// session resumable and opens it again. A failed open is tried again with a
// back-off, from the sender loop: after the close nothing else would reopen it.
// A reopen that worked fetches the recent history too, for a session Discord
// would not resume (it then sends nothing that was missed).
func chatGatewayReopen(dg *discordgo.Session, ch string) {
	defer chatGatewayKicking.Store(false)
	defer func() {
		if r := recover(); r != nil {
			logDebug("chat", "gateway reopen recovered from: %v", r)
		}
	}()
	_ = dg.CloseWithCode(discordCloseResumable)
	if err := dg.Open(); err != nil && !errors.Is(err, discordgo.ErrWSAlreadyOpen) {
		wait := time.Duration(chatGatewayRetry.Load()) * 2
		if wait < 5*time.Second {
			wait = 5 * time.Second
		}
		if wait > chatGatewayRetryMax {
			wait = chatGatewayRetryMax
		}
		chatGatewayRetry.Store(int64(wait))
		chatGatewayKickAt.Store(time.Now().Add(wait).UnixNano())
		chatGatewayKickWanted.Store(true)
		logWarn("chat", "community chat: reopening the connection failed, trying again in %s: %v", wait, err)
		return
	}
	chatGatewayRetry.Store(0)
	if cur, _ := chatLiveNow(); cur != dg {
		// The chat moved to another session meanwhile (a new token or
		// channel): this one must not live on beside it, or every message
		// would arrive twice.
		_ = dg.Close()
		return
	}
	logInfo("chat", "community chat: connection reopened after the network came back")
	if last := chatCatchUpAt.Load(); last == 0 || time.Since(time.Unix(0, last)) >= chatCatchUpEvery {
		chatCatchUpAt.Store(time.Now().UnixNano())
		discordLoadHistory(dg, ch)
	}
}

// chatReconnectBanner is the strip the community and Juka tabs show while the
// Wi-Fi comes back after a wake, instead of "Offline": what is typed meanwhile
// waits and goes out by itself (chatsync.go). A Juka tab without a key keeps
// its own banner.
func chatReconnectBanner(tab chatTab) (chatBanner, bool) {
	if (tab != chatTabCommunity && tab != chatTabAI) || !netReconnecting() {
		return chatBanner{}, false
	}
	if tab == chatTabAI && !chatHasGroqKey() {
		return chatBanner{}, false
	}
	return chatBanner{show: true, icon: "wifi", text: netReconnectingText, button: "Wi-Fi", action: chatBannerWiFi}, true
}

// orbNetWaitSub is the line under the orb's "Thinking…" while a clip waits for
// the network after a wake. It only ever replaces an empty line or its own
// words, so nothing else the orb shows there is overwritten.
func orbNetWaitSub(sub string) string {
	if sub != "" && sub != netReconnectingText {
		return sub
	}
	return netWaitLabel("")
}

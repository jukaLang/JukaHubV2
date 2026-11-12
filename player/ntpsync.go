package main

// ntpsync.go — setting the clock from the network (NET-3).
//
// The handheld appears to have no battery-backed clock (FIX-2 confirms it on
// hardware): after a power cycle the time is wrong until something sets it,
// and a wrong clock breaks TLS validity checks, alarms and the playtime day.
// Each time the connectivity monitor (netmon.go) sees the internet come back,
// and hourly while it stays, JukaHub asks a few SNTP servers for the time;
// when they cannot be reached it reads the Date header of an HTTPS endpoint
// the app already uses. A clock more than two seconds off is set — only on the
// handheld, as root (how the TrimUI runs apps), with Settings → Date & time →
// Set time automatically on. Windows, macOS and Linux PCs keep their own time:
// JukaHub never asks a time server there by itself (Network tools' Clock
// check compares on request). Nothing runs while offline, in airplane mode or
// behind a captive portal (the monitor reports none of those as online), and
// nothing runs on the render thread. A sync that finds no time is retried
// after 1, 2, 5, 10 and 30 minutes until one succeeds, then hourly.
//
// SNTP is not authenticated: anyone on the same network can answer for the
// server. A correction larger than ten minutes (counted together with the
// SNTP-only corrections since the last confirmed one) is therefore only a
// claim until the HTTPS date, checked at the claimed time, agrees with it
// within a minute (clockConfirmHTTPS). That sum gives back ten minutes per day
// of uptime, so a long run still corrects its drift without HTTPS, while a
// burst of steps adds up. A device with no CA certificate at all can confirm
// nothing over HTTPS: there SNTP alone may set a clock that is before this
// build (a cold boot), never one that already shows a plausible time
// (clockUnconfirmedSetOK).
//
// After the clock moves, clockJumpNotify tells the alarms and the playtime day
// to re-anchor (clockjump.go).

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// clockSetThreshold is how far off the clock may be before it is set.
const clockSetThreshold = 2 * time.Second

// netTimeInterval is how often the clock is checked while online once a sync
// found the time; netTimeRetry is the wait after each failure until one has.
const netTimeInterval = time.Hour

var netTimeRetry = [...]time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute}

// clockConfirmAbove is the most SNTP alone may move the clock (summed since
// the last correction the HTTPS date confirmed); clockConfirmTolerance is how
// far the HTTPS date may then differ from the SNTP claim; clockConfirmDecay is
// the uptime over which that sum gives back clockConfirmAbove.
const (
	clockConfirmAbove     = 10 * time.Minute
	clockConfirmTolerance = time.Minute
	clockConfirmDecay     = 24 * time.Hour
)

// sntpMaxDelay is the longest round trip an answer is trusted with: the
// offset can be wrong by up to half of it.
const sntpMaxDelay = 2 * time.Second

// sntpServers are asked in order until one answers.
var sntpServers = []string{"pool.ntp.org", "time.cloudflare.com", "time.google.com"}

// clockHTTPSURL is the fallback's HTTPS endpoint: the connectivity monitor's
// own generate_204 (netmon.go), over TLS. clockHTTPURL is the same over plain
// HTTP, used only to learn a time to check the certificate at (see
// httpsDateFallback).
const (
	clockHTTPSURL = "https://connectivitycheck.gstatic.com/generate_204"
	clockHTTPURL  = "http://connectivitycheck.gstatic.com/generate_204"
)

// clockFloor is the earliest time any answer may claim: before this build
// existed, a clock (or a server) is certainly wrong. Two days of slack cover
// a build machine whose own clock ran a little ahead.
func clockFloor() time.Time {
	const slack = 48 * time.Hour
	if t, ok := parseReleaseTimestamp(buildTime); ok {
		return t.Add(-slack)
	}
	if t, ok := parseReleaseTimestamp(buildDate); ok {
		return t.Add(-slack)
	}
	return time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
}

// clockCeiling is the latest time an answer may claim (a server answering with
// garbage, or an NTP era mistake).
var clockCeiling = time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)

// ── SNTP (RFC 4330) ───────────────────────────────────────────────────────

// sntpReading is one answered SNTP exchange.
type sntpReading struct {
	Offset  time.Duration // network time minus this device's clock
	Delay   time.Duration // round trip, less the server's own processing
	Stratum int
	Server  time.Time // the server's transmit time
	Local   time.Time // this device's clock at the middle of the exchange
}

// ntpPutTime writes t as an NTP timestamp (seconds since 1900 in the 32-bit
// era field, then the binary fraction).
func ntpPutTime(b []byte, t time.Time) {
	secs := uint64(t.Unix() + ntpEpochOffset)
	frac := (uint64(t.Nanosecond()) << 32) / 1e9
	binary.BigEndian.PutUint32(b[0:4], uint32(secs))
	binary.BigEndian.PutUint32(b[4:8], uint32(frac))
}

// ntpReadTime reads an NTP timestamp. The seconds field wraps in February
// 2036; as RFC 4330 §3 says, a value with the top bit clear belongs to the
// next era (2036-2104), so the clock keeps working past the wrap.
func ntpReadTime(b []byte) (time.Time, bool) {
	if len(b) < 8 {
		return time.Time{}, false
	}
	secs := binary.BigEndian.Uint32(b[0:4])
	frac := binary.BigEndian.Uint32(b[4:8])
	if secs == 0 && frac == 0 {
		return time.Time{}, false
	}
	unix := int64(secs) - ntpEpochOffset
	if secs&0x80000000 == 0 {
		unix += 1 << 32
	}
	nanos := (int64(frac) * 1e9) >> 32
	return time.Unix(unix, nanos), true
}

// sntpRequest is a version-4 client request carrying t1 as its transmit
// time; the server echoes it as the originate time, which is how a reply is
// matched to this request.
func sntpRequest(t1 time.Time) []byte {
	p := make([]byte, 48)
	p[0] = 0x23 // leap 0, version 4, mode 3 (client)
	ntpPutTime(p[40:48], t1)
	return p
}

// sntpParse checks a reply to req (sent at t1, received at t4 on this
// device's clock) and works out the offset and the delay from the four
// timestamps:
//
//	offset = ((T2 - T1) + (T3 - T4)) / 2
//	delay  = (T4 - T1) - (T3 - T2)
func sntpParse(reply, req []byte, t1, t4 time.Time) (sntpReading, error) {
	if len(reply) < 48 || len(req) < 48 {
		return sntpReading{}, errors.New("the answer was too short to read")
	}
	leap, mode := reply[0]>>6, reply[0]&0x07
	if mode != 4 && mode != 5 {
		return sntpReading{}, errors.New("the answer was not from a server")
	}
	if leap == 3 {
		return sntpReading{}, errors.New("the server's own clock is not synchronised")
	}
	stratum := int(reply[1])
	if stratum == 0 || stratum > 15 {
		return sntpReading{}, errors.New("the server refused to give a usable time")
	}
	if !bytes.Equal(reply[24:32], req[40:48]) {
		return sntpReading{}, errors.New("the answer does not belong to this request")
	}
	t2, ok2 := ntpReadTime(reply[32:40])
	t3, ok3 := ntpReadTime(reply[40:48])
	if !ok2 || !ok3 {
		return sntpReading{}, errors.New("the answer carried no time")
	}
	// The four times must fit together: the server cannot send before it
	// received, nor spend longer on the request than the whole round trip;
	// and a long round trip leaves the offset too uncertain to set a clock by.
	if t3.Before(t2) {
		return sntpReading{}, errors.New("the answer's times are out of order")
	}
	offset := (t2.Sub(t1) + t3.Sub(t4)) / 2
	delay := t4.Sub(t1) - t3.Sub(t2)
	if delay < -time.Millisecond {
		return sntpReading{}, errors.New("the answer's times do not fit the round trip")
	}
	if delay < 0 {
		delay = 0
	}
	if delay > sntpMaxDelay {
		return sntpReading{}, fmt.Errorf("the answer took %d ms, too long to trust", delay.Milliseconds())
	}
	return sntpReading{Offset: offset, Delay: delay, Stratum: stratum, Server: t3, Local: t1.Add(t4.Sub(t1) / 2)}, nil
}

// sntpQuery runs one exchange with server within timeout. Datagrams that do
// not answer this request (a late reply to an earlier one, or a forged one)
// are skipped until a good answer or the deadline.
func sntpQuery(ctx context.Context, server string, timeout time.Duration) (sntpReading, error) {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "udp", net.JoinHostPort(server, "123"))
	if err != nil {
		return sntpReading{}, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return sntpReading{}, err
	}
	t1 := time.Now()
	req := sntpRequest(t1)
	if _, err := conn.Write(req); err != nil {
		return sntpReading{}, err
	}
	reply := make([]byte, 128)
	var bad error
	for {
		n, err := conn.Read(reply)
		t4 := time.Now()
		if err != nil {
			if bad != nil {
				return sntpReading{}, bad
			}
			return sntpReading{}, err
		}
		r, perr := sntpParse(reply[:n], req, t1, t4)
		if perr == nil {
			return r, nil
		}
		bad = perr
	}
}

// sntpQueryAny asks the servers in turn and returns the first good answer.
func sntpQueryAny(ctx context.Context) (sntpReading, string, error) {
	var firstErr error
	for _, server := range sntpServers {
		if ctx.Err() != nil {
			break
		}
		r, err := sntpQuery(ctx, server, 3*time.Second)
		if err == nil {
			return r, server, nil
		}
		if firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", server, err)
		}
	}
	if firstErr == nil {
		firstErr = ctx.Err()
	}
	return sntpReading{}, "", firstErr
}

// ── HTTPS Date fallback ───────────────────────────────────────────────────

// httpDateQuery reads the Date header of url. verifyAt, when set, is the time
// the TLS certificate is checked at. The header has whole seconds; the answer
// is taken as half a second past it, at the middle of the round trip.
func httpDateQuery(ctx context.Context, url string, verifyAt time.Time) (time.Duration, time.Time, error) {
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	if !verifyAt.IsZero() {
		tlsConf.Time = func() time.Time { return verifyAt }
	}
	// A firmware without a CA bundle in the usual place: the same search
	// the bug reporter uses (nil keeps the system's roots).
	if pool := bugrepRootCAs(); pool != nil {
		tlsConf.RootCAs = pool
	}
	client := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSClientConfig:     tlsConf,
			TLSHandshakeTimeout: 5 * time.Second,
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, time.Time{}, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	end := time.Now()
	if err != nil {
		return 0, time.Time{}, err
	}
	resp.Body.Close()
	date, err := http.ParseTime(resp.Header.Get("Date"))
	if err != nil {
		return 0, time.Time{}, errors.New("the answer carried no date")
	}
	server := date.Add(500 * time.Millisecond)
	midpoint := start.Add(end.Sub(start) / 2)
	return server.Sub(midpoint), server, nil
}

// clockCertTimeError reports whether err is a certificate that does not fit
// the time it was checked at — what a far-off clock does to every TLS
// connection.
func clockCertTimeError(err error) bool {
	var invalid x509.CertificateInvalidError
	return errors.As(err, &invalid) && invalid.Reason == x509.Expired
}

// httpsDateFallback reads the time from the HTTPS endpoint. A clock far
// enough off breaks the certificate check itself, so the check runs at this
// device's time only when that is plausible (not before this build), and
// otherwise at the build time. When the certificate does not fit that time
// either, a time is learned from the same endpoint over plain HTTP and
// accepted only if the HTTPS certificate is valid at it and the HTTPS answer
// agrees with it: a forged plain answer cannot produce both.
func httpsDateFallback(ctx context.Context) (time.Duration, error) {
	verifyAt := time.Now()
	if floor := clockFloor(); verifyAt.Before(floor) {
		verifyAt = floor
	}
	offset, _, err := httpDateQuery(ctx, clockHTTPSURL, verifyAt)
	if err == nil {
		return offset, nil
	}
	if !clockCertTimeError(err) {
		return 0, err
	}
	_, claimed, err := httpDateQuery(ctx, clockHTTPURL, time.Time{})
	if err != nil {
		return 0, err
	}
	offset, confirmed, err := httpDateQuery(ctx, clockHTTPSURL, claimed)
	if err != nil {
		return 0, err
	}
	if d := confirmed.Sub(claimed); d > time.Minute || d < -time.Minute {
		return 0, errors.New("the secure and plain answers disagree")
	}
	return offset, nil
}

// clockNeedsConfirm reports whether an SNTP correction by offset must wait
// for the HTTPS date: when it, together with the SNTP-only corrections made
// since the last confirmed one (unconfirmed, less their decay), is more than
// ten minutes. The sum stops a forger from walking the clock off in steps
// just under the limit.
func clockNeedsConfirm(offset, unconfirmed time.Duration) bool {
	if offset < 0 {
		offset = -offset
	}
	return offset+unconfirmed > clockConfirmAbove
}

// clockUnconfirmedAfter is what is left of the SNTP-only sum unconfirmed after
// elapsed of uptime: it gives back clockConfirmAbove every clockConfirmDecay
// (ten minutes a day). Steps in a burst still add up, so a forger walking the
// clock gains at most that much a day; the hourly drift corrections of a run
// that lasts weeks are not all pushed behind the HTTPS date.
func clockUnconfirmedAfter(unconfirmed, elapsed time.Duration) time.Duration {
	if elapsed <= 0 || unconfirmed <= 0 {
		return max(unconfirmed, 0)
	}
	// The rate as one whole ratio (a day / ten minutes = 144): multiplying
	// elapsed by clockConfirmAbove first would overflow after 15 ms.
	back := elapsed / (clockConfirmDecay / clockConfirmAbove)
	if back >= unconfirmed {
		return 0
	}
	return unconfirmed - back
}

// clockUnconfirmedSetOK reports whether an SNTP claim the HTTPS date could
// not confirm (confirmErr) may set the clock anyway. Only when HTTPS cannot
// work on this device at all — it has no CA certificate (noRoots, a fact
// about the device that no network can fake) and the check failed on exactly
// that (an unknown authority, or no system roots) — and only while the clock
// is clearly stale (now before floor, as after a cold boot without a
// battery-backed clock): the clock is certainly wrong then, and SNTP is the
// only time there is. A clock that already shows a plausible time is never
// moved far on an unconfirmed claim, and where CA certificates exist a forged
// certificate, an unknown authority too, never takes this path.
func clockUnconfirmedSetOK(confirmErr error, noRoots bool, now, floor time.Time) bool {
	if confirmErr == nil || !noRoots || !now.Before(floor) {
		return false
	}
	var unknown x509.UnknownAuthorityError
	var noSystem x509.SystemRootsError
	return errors.As(confirmErr, &unknown) || errors.As(confirmErr, &noSystem)
}

// clockNoTrustedRoots reports whether this device has no CA certificate at
// all: an empty system store, and none of the bundles bugrepRootCAs looks for
// (bugrepCAPaths). No HTTPS answer can be verified then.
func clockNoTrustedRoots() bool {
	if bugrepRootCAs() != nil {
		return false
	}
	pool, err := x509.SystemCertPool()
	return err != nil || pool == nil || pool.Equal(x509.NewCertPool())
}

// clockConfirmHTTPS checks an SNTP claim (the clock is offset off) against the
// HTTPS date. The certificate is checked at the claimed time, so a claim far
// from the truth fails the handshake, and one near enough for the certificate
// must still agree with the server's authenticated Date within a minute.
func clockConfirmHTTPS(ctx context.Context, offset time.Duration) error {
	got, _, err := httpDateQuery(ctx, clockHTTPSURL, time.Now().Add(offset))
	if err != nil {
		return err
	}
	if d := got - offset; d > clockConfirmTolerance || d < -clockConfirmTolerance {
		return fmt.Errorf("the HTTPS date puts the clock %s, not %s", clockOffsetWords(got), clockOffsetWords(offset))
	}
	return nil
}

// ── The decision ──────────────────────────────────────────────────────────

// clockAction is what a measured offset calls for.
type clockAction int

const (
	clockKeep   clockAction = iota // within the threshold: leave the clock alone
	clockSet                       // off by more, and JukaHub may set it
	clockReport                    // off by more, but the system owns the clock here
	clockReject                    // the answer itself is implausible
)

// clockDecide is the "should set" rule: a network time outside
// [floor, clockCeiling) is rejected whatever the offset; within two seconds
// the clock is kept; past that it is set where JukaHub may set it and only
// reported elsewhere.
func clockDecide(offset time.Duration, network, floor time.Time, maySet bool) clockAction {
	if network.Before(floor) || !network.Before(clockCeiling) {
		return clockReject
	}
	if offset < clockSetThreshold && offset > -clockSetThreshold {
		return clockKeep
	}
	if maySet {
		return clockSet
	}
	return clockReport
}

// ── Scheduling ────────────────────────────────────────────────────────────

// netTime is the scheduler's state. enabled mirrors the Settings switch (the
// monitor goroutine must not read the config's maps); busy keeps one sync at
// a time, so the fields below mu's are only touched by the sync that holds it.
var netTime struct {
	enabled atomic.Bool
	busy    atomic.Bool
	mu      sync.Mutex
	nextRun time.Time // when the next sync is due (monotonic; zero: now)

	failures      int           // syncs in a row that found no usable time; the first one warns
	synced        bool          // a sync found the time in this run
	unconfirmed   time.Duration // SNTP-only corrections since the last confirmed one, less the decay
	unconfirmedAt time.Time     // when unconfirmed was brought up to date (monotonic reading)
}

// netTimeSetEnabled mirrors the Settings switch (main thread).
func netTimeSetEnabled(on bool) { netTime.enabled.Store(on) }

// clockSetSupported reports whether JukaHub may set the clock here: the
// handheld, as root, where nothing else keeps time. A Linux PC run as root
// is left alone even without a desktop: it has its own time service
// (systemd-timesyncd, chrony, ntpd) for that.
func clockSetSupported() bool {
	return IsTrimuiDevice() && clockSetPermitted() && dateTimeZoneOwner() == tzOwnerJukaHub
}

// netTimeRetryAfter is the wait before the next sync: after failures syncs in
// a row found no time, and none has yet in this run, 1, 2, 5, 10 and then 30
// minutes; otherwise the hourly check.
func netTimeRetryAfter(failures int, synced bool) time.Duration {
	if synced || failures <= 0 || failures > len(netTimeRetry) {
		return netTimeInterval
	}
	return netTimeRetry[failures-1]
}

// netTimeAfterProbe is the connectivity monitor's hook (netmon.go), called on
// its goroutine after every probe. online is verified internet (a captive
// portal is not online); cameOnline marks the first probe or a change to
// online. Only where JukaHub sets the clock, and only with the switch on, it
// syncs on every connect and whenever the next run is due; nowhere else does
// it ask a time server by itself.
func netTimeAfterProbe(online, cameOnline bool) {
	if !online || !netTime.enabled.Load() || !clockSetSupported() {
		return
	}
	netTime.mu.Lock()
	due := cameOnline || !time.Now().Before(netTime.nextRun)
	if due {
		due = netTime.busy.CompareAndSwap(false, true)
	}
	if due {
		// Until the sync reports back and sets the real next run.
		netTime.nextRun = time.Now().Add(netTimeInterval)
	}
	netTime.mu.Unlock()
	if due {
		go netTimeSync(true)
	}
}

// netTimeSyncNow runs a sync at once (the Settings switch turned on), when
// online and none is running.
func netTimeSyncNow() {
	if !IsOnline() || !clockSetSupported() || !netTime.busy.CompareAndSwap(false, true) {
		return
	}
	netTime.mu.Lock()
	netTime.nextRun = time.Now().Add(netTimeInterval)
	netTime.mu.Unlock()
	go netTimeSync(true)
}

// netTimeFailed counts a sync that found no usable time and says so: once at
// Warn (errors.txt), then quietly while it lasts. Busy holder only.
func netTimeFailed(check clockCheck, format string, args ...any) {
	if netTime.failures == 0 {
		logWarn("clock", format, args...)
	} else {
		logDebug("clock", format, args...)
	}
	netTime.failures++
	clockNoteCheck(check)
}

// netTimeUnconfirmed brings the SNTP-only sum up to date and returns it. It
// decays over uptime measured on the monotonic clock, which setting the clock
// does not move. Busy holder only.
func netTimeUnconfirmed() time.Duration {
	now := time.Now()
	if !netTime.unconfirmedAt.IsZero() {
		netTime.unconfirmed = clockUnconfirmedAfter(netTime.unconfirmed, now.Sub(netTime.unconfirmedAt))
	}
	netTime.unconfirmedAt = now
	return netTime.unconfirmed
}

// netTimeNoteSet books a clock set into the SNTP-only sum: a confirmed set
// clears it, an unconfirmed one adds its size, up to clockConfirmAbove — past
// that every correction waits for the HTTPS date anyway, and the cap lets the
// years a cold boot's unconfirmed set can span decay like any other step.
// Busy holder only.
func netTimeNoteSet(offset time.Duration, confirmed bool) {
	sum := netTimeUnconfirmed()
	switch {
	case confirmed:
		sum = 0
	case offset < 0:
		sum -= offset
	default:
		sum += offset
	}
	netTime.unconfirmed = min(sum, clockConfirmAbove)
}

// netTimeSync measures the clock and sets it when it should. It runs on its
// own goroutine; busy is already held.
func netTimeSync(maySet bool) {
	defer netTime.busy.Store(false)
	// Runs before busy is released: the next run follows this one's outcome.
	defer func() {
		wait := netTimeRetryAfter(netTime.failures, netTime.synced)
		netTime.mu.Lock()
		netTime.nextRun = time.Now().Add(wait)
		netTime.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	source := ""
	var offset time.Duration
	// confirmed: the time came over HTTPS, which an attacker on the network
	// cannot forge; SNTP's can be.
	confirmed := false
	r, server, err := sntpQueryAny(ctx)
	if err == nil {
		source, offset = server, r.Offset
	} else {
		logDebug("clock", "SNTP unavailable (%v); trying the HTTPS date", err)
		var herr error
		offset, herr = httpsDateFallback(ctx)
		if herr != nil {
			netTimeFailed(clockCheck{At: time.Now(), Err: "no time server answered"},
				"network time unavailable: SNTP %v; HTTPS %v", err, herr)
			return
		}
		source, confirmed = "connectivitycheck.gstatic.com", true
	}
	network := time.Now().Add(offset)
	action := clockDecide(offset, network, clockFloor(), maySet)
	if action == clockSet && !confirmed && clockNeedsConfirm(offset, netTimeUnconfirmed()) {
		cerr := clockConfirmHTTPS(ctx, offset)
		switch {
		case cerr == nil:
			confirmed = true
		case clockUnconfirmedSetOK(cerr, clockNoTrustedRoots(), time.Now(), clockFloor()):
			// No CA certificate on this device: HTTPS can confirm nothing
			// here, and a clock before this build is certainly wrong.
			logWarn("clock", "%s says the clock is %s off; HTTPS confirmation is unavailable (this device has no CA certificates: %v) and the clock is before this build, so SNTP alone sets it", source, offset, cerr)
		default:
			netTimeFailed(clockCheck{At: time.Now(), Offset: offset, Source: source, Err: source + " said the clock is " + clockOffsetWords(offset) + "; the HTTPS date did not confirm it"},
				"%s says the clock is %s off, not confirmed over HTTPS (%v); clock left alone", source, offset, cerr)
			return
		}
	}
	switch action {
	case clockReject:
		netTimeFailed(clockCheck{At: time.Now(), Source: source, Err: source + " gave an impossible time"},
			"%s answered %s, which cannot be right; clock left alone", source, network.UTC().Format(time.RFC3339))
		return
	case clockKeep:
		logDebug("clock", "in sync with %s (%s)", source, offset)
		clockNoteCheck(clockCheck{At: time.Now(), Offset: offset, Source: source})
		// Everything dated so far was dated right (clockjump.go).
		clockConfirmNotify()
	case clockReport:
		logInfo("clock", "system clock is %s off %s (left to the system)", offset, source)
		clockNoteCheck(clockCheck{At: time.Now(), Offset: offset, Source: source})
	case clockSet:
		if !netTime.enabled.Load() {
			// Switched off while this sync measured — the clock set by hand
			// (clocksheet.go) does that — so its offset no longer applies.
			logInfo("clock", "network time switched off during the sync; clock left alone")
			return
		}
		if err := clockSetSystem(time.Now().Add(offset)); err != nil {
			netTimeFailed(clockCheck{At: time.Now(), Offset: offset, Source: source, Err: "could not set it: " + err.Error()},
				"clock is %s off %s but could not be set: %v", offset, source, err)
			return
		}
		// At once, before anything slow: the alarms and the playtime day
		// re-anchor by this shift (clockjump.go).
		clockJumpNotify(offset, offset)
		netTimeNoteSet(offset, confirmed)
		at := time.Now()
		logInfo("clock", "clock set from %s: it was %s off", source, offset)
		clockNoteCheck(clockCheck{At: at, Offset: offset, Source: source, Set: true})
		clockRecordSave(clockRecord{At: at, How: "network time · " + source, OffsetMS: offset.Milliseconds()})
	}
	netTime.failures = 0
	netTime.synced = true
}

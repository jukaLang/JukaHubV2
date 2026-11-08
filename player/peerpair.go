package main

// peerpair.go — pairing two devices with a code both screens show.
//
// The exchange runs over the same TLS connection as everything else, so each
// side already holds the key the other proved in the handshake. What pairing
// adds is the user's word that the key belongs to the device in their hand:
//
//  1. The initiator (the device where "Pair" was pressed) sends a commitment,
//     SHA-256 of a random 32-byte nonce, without the nonce itself.
//  2. The responder answers with its own random nonce.
//  3. The initiator reveals its nonce; the responder checks it against the
//     commitment.
//  4. Both compute code = SHA-256(both key ids, initiator first, both nonces)
//     mod 1,000,000 and show it. The users compare the two screens and choose
//     "Codes match" on both; the initiator polls until the responder's user has
//     answered, and each side then stores the other's key id.
//
// Because the initiator commits before it sees the responder's nonce, and the
// responder's nonce is fixed before the initiator's is revealed, a device in
// the middle has to fix its own nonce in both halves before it can know what
// either code will be: it matches the two screens with probability one in a
// million per attempt, and every attempt needs both users to press "Codes
// match" (the same argument as Bluetooth's numeric comparison).

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	// peerPairTimeout is how long a pairing may take, both users included.
	peerPairTimeout = 2 * time.Minute
	// peerPairNonceSize is the size of each side's nonce.
	peerPairNonceSize = 32
)

type peerPairStage int

const (
	// peerPairConnecting: the initiator is asking the other device.
	peerPairConnecting peerPairStage = iota
	// peerPairHello: the responder holds the initiator's commitment.
	peerPairHello
	// peerPairCompare: both sides have the code on screen.
	peerPairCompare
	// peerPairDone: both users confirmed; the key is stored.
	peerPairDone
	// peerPairFailed: cancelled, refused, timed out or tampered with.
	peerPairFailed
)

// peerPairSession is one pairing in progress, on either side.
type peerPairSession struct {
	initiator  bool
	peerID     string
	peerName   string
	peerModel  string
	addr       string
	commit     []byte
	myNonce    []byte
	theirNonce []byte
	code       string
	stage      peerPairStage
	localOK    bool
	remoteOK   bool
	reason     string
	started    time.Time
	token      int64
}

// peerPair holds at most one incoming and one outgoing session.
var peerPair struct {
	mu        sync.Mutex
	in        *peerPairSession
	out       *peerPairSession
	seq       int64
	lastHello time.Time
}

func (s *peerPairSession) expired(now time.Time) bool {
	return now.Sub(s.started) > peerPairTimeout
}

// live reports whether the session is still waiting for something. A nil
// session is not live.
func (s *peerPairSession) live(now time.Time) bool {
	return s != nil && !s.expired(now) && s.stage != peerPairDone && s.stage != peerPairFailed
}

// peerPairCommit is the initiator's promise: the hash of its nonce, sent
// before it sees the responder's.
func peerPairCommit(nonce []byte) []byte {
	h := sha256.New()
	h.Write([]byte("jukahub-pair-commit-v1"))
	h.Write(nonce)
	return h.Sum(nil)
}

// peerPairCode is the 6-digit code both screens show: a hash over both key ids
// (initiator first) and both nonces. "" when an id is malformed.
func peerPairCode(initiatorID, responderID string, initiatorNonce, responderNonce []byte) string {
	a, errA := hex.DecodeString(initiatorID)
	b, errB := hex.DecodeString(responderID)
	if errA != nil || errB != nil || len(a) != sha256.Size || len(b) != sha256.Size {
		return ""
	}
	h := sha256.New()
	h.Write([]byte("jukahub-pair-code-v1"))
	h.Write(a)
	h.Write(b)
	h.Write(initiatorNonce)
	h.Write(responderNonce)
	sum := h.Sum(nil)
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(sum[:4])%1000000)
}

// peerFormatCode is a code the way it is shown: "123 456".
func peerFormatCode(code string) string {
	if len(code) == 6 {
		return code[:3] + " " + code[3:]
	}
	return code
}

// ── Responder ─────────────────────────────────────────────────────────────

type peerPairHelloReq struct {
	V      int    `json:"v"`
	Commit string `json:"commit"`
	Name   string `json:"name"`
	Model  string `json:"model"`
}

type peerPairHelloResp struct {
	Nonce string `json:"nonce"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

type peerPairRevealReq struct {
	Nonce string `json:"nonce"`
}

type peerPairConfirmReq struct {
	Accept bool `json:"accept"`
}

type peerPairConfirmResp struct {
	State string `json:"state"`
}

// peerHandlePairHello is step 1 on the responder: store the commitment and
// answer with this device's nonce.
func peerHandlePairHello(w http.ResponseWriter, r *http.Request, call peerCall) {
	var req peerPairHelloReq
	if !peerDecodeBody(w, r, peerMaxJSONBody, &req) {
		return
	}
	commit, err := hex.DecodeString(strings.TrimSpace(req.Commit))
	if err != nil || len(commit) != sha256.Size || req.V != 1 {
		http.Error(w, "bad pairing request", http.StatusBadRequest)
		return
	}
	self, ok := peerSelf()
	if !ok {
		http.Error(w, "no identity", http.StatusServiceUnavailable)
		return
	}
	if peerSameID(call.id, self.id) {
		http.Error(w, "this device has the same identity as yours — reset it under Paired devices", http.StatusConflict)
		return
	}
	nonce := make([]byte, peerPairNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		http.Error(w, "no randomness", http.StatusInternalServerError)
		return
	}
	name := peerCleanName(req.Name)
	if name == "" {
		name = "JukaHub " + peerShortID(call.id)
	}
	now := time.Now()
	peerPair.mu.Lock()
	if now.Sub(peerPair.lastHello) < 2*time.Second {
		peerPair.mu.Unlock()
		http.Error(w, "too many pairing requests — wait a moment", http.StatusTooManyRequests)
		return
	}
	if cur := peerPair.in; cur.live(now) && cur.peerID != call.id {
		peerPair.mu.Unlock()
		http.Error(w, "busy pairing with another device", http.StatusConflict)
		return
	}
	if peerPair.out.live(now) {
		peerPair.mu.Unlock()
		http.Error(w, "busy pairing with another device", http.StatusConflict)
		return
	}
	peerPair.lastHello = now
	peerPair.seq++
	peerPair.in = &peerPairSession{
		peerID:    call.id,
		peerName:  name,
		peerModel: partyCleanLabel(req.Model, 40),
		commit:    commit,
		myNonce:   nonce,
		stage:     peerPairHello,
		started:   now,
		token:     peerPair.seq,
	}
	peerPair.mu.Unlock()
	peerWriteJSONResponse(w, http.StatusOK, peerPairHelloResp{
		Nonce: hex.EncodeToString(nonce),
		Name:  self.name,
		Model: peerModelName(),
	})
}

// peerHandlePairReveal is step 3 on the responder: check the nonce against the
// commitment and put the code on screen.
func peerHandlePairReveal(w http.ResponseWriter, r *http.Request, call peerCall) {
	var req peerPairRevealReq
	if !peerDecodeBody(w, r, peerMaxJSONBody, &req) {
		return
	}
	nonce, err := hex.DecodeString(strings.TrimSpace(req.Nonce))
	if err != nil || len(nonce) != peerPairNonceSize {
		http.Error(w, "bad pairing request", http.StatusBadRequest)
		return
	}
	selfID := peerSelfID()
	now := time.Now()
	peerPair.mu.Lock()
	sess := peerPair.in
	if !sess.live(now) || sess.peerID != call.id || sess.stage != peerPairHello {
		peerPair.mu.Unlock()
		http.Error(w, "no pairing in progress", http.StatusConflict)
		return
	}
	if subtle.ConstantTimeCompare(peerPairCommit(nonce), sess.commit) != 1 {
		sess.stage = peerPairFailed
		sess.reason = sess.peerName + " broke the pairing protocol"
		peerPair.mu.Unlock()
		http.Error(w, "the nonce does not match the commitment", http.StatusBadRequest)
		return
	}
	sess.theirNonce = nonce
	sess.code = peerPairCode(call.id, selfID, nonce, sess.myNonce)
	sess.stage = peerPairCompare
	token, name := sess.token, sess.peerName
	peerPair.mu.Unlock()
	tbxPost(func() { peerPairNotifyIncoming(token, name) })
	peerWriteJSONResponse(w, http.StatusOK, map[string]bool{"ok": true})
}

// peerHandlePairConfirm is step 4 on the responder: the initiator's user
// confirmed (or cancelled). The answer says whether this user has too.
func peerHandlePairConfirm(w http.ResponseWriter, r *http.Request, call peerCall) {
	var req peerPairConfirmReq
	if !peerDecodeBody(w, r, peerMaxJSONBody, &req) {
		return
	}
	now := time.Now()
	peerPair.mu.Lock()
	sess := peerPair.in
	if sess == nil || sess.peerID != call.id || sess.expired(now) {
		peerPair.mu.Unlock()
		http.Error(w, "no pairing in progress", http.StatusConflict)
		return
	}
	state := "waiting"
	finish, cancelled := false, false
	var rec peerRecord
	switch {
	case sess.stage == peerPairDone:
		state = "accepted"
	case sess.stage == peerPairFailed:
		state = "declined"
	case !req.Accept:
		sess.stage = peerPairFailed
		sess.reason = sess.peerName + " cancelled pairing"
		state = "declined"
		cancelled = true
	case sess.stage != peerPairCompare:
		peerPair.mu.Unlock()
		http.Error(w, "the code is not on screen yet", http.StatusConflict)
		return
	default:
		sess.remoteOK = true
		if sess.localOK {
			sess.stage = peerPairDone
			state = "accepted"
			finish = true
			rec = peerRecord{ID: sess.peerID, Name: sess.peerName, Model: sess.peerModel}
		}
	}
	name := sess.peerName
	peerPair.mu.Unlock()
	if finish {
		peerAddPaired(rec)
		peerNoteCaller(r, rec)
		tbxPost(func() { peerPairNotifyDone(name) })
	}
	if cancelled {
		tbxPost(func() { ShowToast(name+" cancelled pairing", ToastKindInfo) })
	}
	peerWriteJSONResponse(w, http.StatusOK, peerPairConfirmResp{State: state})
}

// peerPairAnswerIncoming is this user's answer to a request whose code is on
// screen. It runs on the main loop.
func peerPairAnswerIncoming(accept bool) {
	now := time.Now()
	peerPair.mu.Lock()
	sess := peerPair.in
	if !sess.live(now) || sess.stage != peerPairCompare {
		peerPair.mu.Unlock()
		return
	}
	if !accept {
		sess.stage = peerPairFailed
		sess.reason = "You cancelled pairing"
		peerPair.mu.Unlock()
		ShowToast("Pairing cancelled", ToastKindInfo)
		return
	}
	sess.localOK = true
	finish := sess.remoteOK
	if finish {
		sess.stage = peerPairDone
	}
	name := sess.peerName
	rec := peerRecord{ID: sess.peerID, Name: sess.peerName, Model: sess.peerModel}
	peerPair.mu.Unlock()
	if finish {
		peerAddPaired(rec)
		peerPairNotifyDone(name)
		return
	}
	ShowToast("Waiting for "+name+" — choose Codes match there too", ToastKindInfo)
}

// ── Initiator ─────────────────────────────────────────────────────────────

// peerPairStart asks dev to pair. It runs on the main loop; the exchange runs
// on a goroutine and reports through the session the screens watch.
func peerPairStart(dev peerSeenDevice) {
	if !peerEnabledFlag.Load() {
		ShowToast("Turn on Nearby devices first", ToastKindWarning)
		return
	}
	nonce := make([]byte, peerPairNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		ShowToast("Could not start pairing", ToastKindError)
		return
	}
	now := time.Now()
	peerPair.mu.Lock()
	if peerPair.out.live(now) || peerPair.in.live(now) {
		peerPair.mu.Unlock()
		ShowToast("Already pairing — finish or cancel that first", ToastKindInfo)
		return
	}
	peerPair.seq++
	sess := &peerPairSession{
		initiator: true,
		peerID:    dev.ID,
		peerName:  dev.Name,
		peerModel: dev.Model,
		addr:      dev.Addr,
		myNonce:   nonce,
		stage:     peerPairConnecting,
		started:   now,
		token:     peerPair.seq,
	}
	peerPair.out = sess
	peerPair.mu.Unlock()
	go peerPairRun(sess)
}

// peerPairRun is the initiator's side of the exchange.
func peerPairRun(sess *peerPairSession) {
	self, ok := peerSelf()
	if !ok {
		peerPairFail(sess, "This device has no identity yet")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), peerPairTimeout)
	defer cancel()
	// The client pins the key the device announced, so the code below covers
	// exactly the key this device is talking to.
	client := peerNewClient(self, sess.peerID, nil)
	defer client.CloseIdleConnections()

	var hello peerPairHelloResp
	err := peerPostJSON(ctx, client, sess.addr, "/peer/v1/pair/hello", peerPairHelloReq{
		V:      1,
		Commit: hex.EncodeToString(peerPairCommit(sess.myNonce)),
		Name:   self.name,
		Model:  peerModelName(),
	}, &hello)
	if err != nil {
		peerPairFail(sess, peerErrorText(err, sess.peerName))
		return
	}
	theirNonce, err := hex.DecodeString(strings.TrimSpace(hello.Nonce))
	if err != nil || len(theirNonce) != peerPairNonceSize {
		peerPairFail(sess, sess.peerName+" sent an unreadable answer")
		return
	}
	reveal := peerPairRevealReq{Nonce: hex.EncodeToString(sess.myNonce)}
	if err := peerPostJSON(ctx, client, sess.addr, "/peer/v1/pair/reveal", reveal, nil); err != nil {
		peerPairFail(sess, peerErrorText(err, sess.peerName))
		return
	}
	code := peerPairCode(self.id, sess.peerID, sess.myNonce, theirNonce)

	peerPair.mu.Lock()
	if peerPair.out != sess {
		peerPair.mu.Unlock()
		return
	}
	if sess.stage == peerPairFailed {
		// Cancelled while the request was out: tell the other screen.
		peerPair.mu.Unlock()
		peerPairSendDecline(client, sess.addr)
		return
	}
	sess.theirNonce = theirNonce
	sess.code = code
	if name := peerCleanName(hello.Name); name != "" {
		sess.peerName = name
	}
	if model := partyCleanLabel(hello.Model, 40); model != "" {
		sess.peerModel = model
	}
	sess.stage = peerPairCompare
	peerPair.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			peerPairFail(sess, "Pairing timed out — try again")
			peerPairSendDecline(client, sess.addr)
			return
		case <-time.After(500 * time.Millisecond):
		}
		peerPair.mu.Lock()
		current := peerPair.out == sess
		stage, localOK := sess.stage, sess.localOK
		name := sess.peerName
		peerPair.mu.Unlock()
		if !current {
			return
		}
		if stage == peerPairFailed {
			// This user cancelled: tell the other screen so it stops waiting.
			peerPairSendDecline(client, sess.addr)
			return
		}
		if !localOK {
			continue
		}
		var resp peerPairConfirmResp
		if err := peerPostJSON(ctx, client, sess.addr, "/peer/v1/pair/confirm", peerPairConfirmReq{Accept: true}, &resp); err != nil {
			var se *peerStatusError
			if errors.As(err, &se) && se.code == http.StatusConflict {
				peerPairFail(sess, name+" is no longer pairing — start again")
				return
			}
			continue
		}
		switch resp.State {
		case "accepted":
			peerPair.mu.Lock()
			sess.stage = peerPairDone
			rec := peerRecord{
				ID:       sess.peerID,
				Name:     sess.peerName,
				Model:    sess.peerModel,
				Addr:     sess.addr,
				LastSeen: time.Now().UTC(),
			}
			peerPair.mu.Unlock()
			peerAddPaired(rec)
			tbxPost(func() { peerPairNotifyDone(name) })
			return
		case "declined":
			peerPairFail(sess, "Pairing was cancelled on "+name)
			return
		}
		// "waiting": the other user has not answered yet.
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
}

// peerPairSendDecline tells the responder this side gave up.
func peerPairSendDecline(client *http.Client, addr string) {
	if client == nil || addr == "" {
		return
	}
	quick, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_ = peerPostJSON(quick, client, addr, "/peer/v1/pair/confirm", peerPairConfirmReq{Accept: false}, nil)
}

// peerPairFail ends a session with a reason the user is told.
func peerPairFail(sess *peerPairSession, reason string) {
	peerPair.mu.Lock()
	if sess.stage == peerPairDone || sess.stage == peerPairFailed {
		peerPair.mu.Unlock()
		return
	}
	sess.stage = peerPairFailed
	sess.reason = reason
	current := peerPair.out == sess || peerPair.in == sess
	peerPair.mu.Unlock()
	if current {
		tbxPost(func() { ShowToast(reason, ToastKindWarning) })
	}
}

// peerPairAnswerOutgoing is this user's answer on the device that started.
func peerPairAnswerOutgoing(accept bool) {
	peerPair.mu.Lock()
	sess := peerPair.out
	if sess == nil || sess.stage == peerPairDone || sess.stage == peerPairFailed {
		peerPair.mu.Unlock()
		return
	}
	if !accept {
		sess.stage = peerPairFailed
		sess.reason = "You cancelled pairing"
		peerPair.mu.Unlock()
		ShowToast("Pairing cancelled", ToastKindInfo)
		return
	}
	if sess.stage == peerPairCompare {
		sess.localOK = true
	}
	peerPair.mu.Unlock()
}

// peerPairAbortAll ends both sessions (the listener stopped).
func peerPairAbortAll(reason string) {
	peerPair.mu.Lock()
	defer peerPair.mu.Unlock()
	for _, s := range [...]*peerPairSession{peerPair.in, peerPair.out} {
		if s != nil && s.stage != peerPairDone && s.stage != peerPairFailed {
			s.stage = peerPairFailed
			s.reason = reason
		}
	}
}

// ── Views ─────────────────────────────────────────────────────────────────

// peerPairView is a session as the screens draw it.
type peerPairView struct {
	live      bool
	initiator bool
	name      string
	model     string
	code      string
	stage     peerPairStage
	localOK   bool
	reason    string
	token     int64
}

func peerPairViewOf(s *peerPairSession, now time.Time) peerPairView {
	if s == nil {
		return peerPairView{}
	}
	return peerPairView{
		live:      s.live(now),
		initiator: s.initiator,
		name:      s.peerName,
		model:     s.peerModel,
		code:      s.code,
		stage:     s.stage,
		localOK:   s.localOK,
		reason:    s.reason,
		token:     s.token,
	}
}

// peerPairViews is the incoming and the outgoing session.
func peerPairViews() (incoming, outgoing peerPairView) {
	now := time.Now()
	peerPair.mu.Lock()
	defer peerPair.mu.Unlock()
	return peerPairViewOf(peerPair.in, now), peerPairViewOf(peerPair.out, now)
}

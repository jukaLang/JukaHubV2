package main

// peer.go — the LAN peer layer behind Save sync, handoff and Nearby: this
// device's identity, the devices it is paired with, the opt-in "Nearby
// devices" switch and the three screens the feature adds (Devices, Save sync,
// Nearby).
//
// The trust model, in short (SECURITY.md → "Nearby devices"):
//
//   - Each device has its own Ed25519 key, made the first time the switch is
//     turned on and kept in data/peer/identity.json. The device's id is the
//     SHA-256 of the public key, so "the device" and "its key" are one thing.
//   - Pairing shows a 6-digit code on both screens (peerpair.go). The code is a
//     hash over both keys and two nonces, one committed to before the other is
//     revealed, so a device in the middle cannot steer both screens to the same
//     code. Confirming it on both screens stores the other device's key.
//   - Every request between devices is HTTPS over TLS 1.3 with a client
//     certificate on both sides, and each side pins the key it paired with
//     (peernet.go): encrypted, authenticated, forward secret. Go picks
//     ChaCha20-Poly1305 on a CPU without AES instructions and AES-GCM elsewhere.
//   - The listener only runs while the switch is on, binds one TCP port and
//     one UDP discovery port, caps connections and body sizes, and is stopped
//     on shutdown.
//
// The x/crypto chacha20poly1305, hkdf and argon2 packages are not vendored and
// no new module may be added, which is why the transport is the standard
// library's TLS instead of a hand-made AEAD over plain HTTP.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// peerSceneName is the Devices page: this device, the paired ones and the
	// ones nearby that can be paired.
	peerSceneName = "Devices"
	// peerSettingKey is the Settings switch "Nearby devices"; off by default.
	peerSettingKey = "peerNearbyDevices"
	// peerNameLimit bounds a device name on screen and on the wire.
	peerNameLimit = 32
	// peerMaxPaired caps the paired list; a household has a few handhelds.
	peerMaxPaired = 16
	// peerSeenTTL is how long a device stays listed after its last answer.
	peerSeenTTL = 90 * time.Second
	// peerAddrTTL is how long a paired device's last address is tried before
	// a scan has to find it again.
	peerAddrTTL = 30 * time.Minute
)

// peerDirOverride is a test seam for the data folder.
var peerDirOverride string

// peerDataDir is where the identity and the paired list live.
func peerDataDir() string {
	if peerDirOverride != "" {
		return peerDirOverride
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, "peer")
	}
	return filepath.Join("data", "peer")
}

// peerIdentityFile is identity.json. The seed is the private key: it never
// leaves this device, and a copied JukaHub folder that carries it along makes
// two devices share one identity (the Devices page offers a reset for that).
type peerIdentityFile struct {
	Version int       `json:"version"`
	Seed    string    `json:"seed"`
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

// peerIdentity is this device: its id (the key's fingerprint), the name the
// other devices see, the key and the self-signed certificate made from it.
type peerIdentity struct {
	id      string
	name    string
	created time.Time
	priv    ed25519.PrivateKey
	cert    tls.Certificate
}

// peerRecord is one paired device.
type peerRecord struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Model    string    `json:"model,omitempty"`
	PairedAt time.Time `json:"pairedAt"`
	LastSeen time.Time `json:"lastSeen,omitempty"`
	// Addr is the host:port it last answered on.
	Addr string `json:"addr,omitempty"`
}

type peerStoreFile struct {
	Version int          `json:"version"`
	Peers   []peerRecord `json:"peers"`
}

// peerSeenDevice is a device that answered a discovery scan.
type peerSeenDevice struct {
	ID    string
	Name  string
	Model string
	// Addr is host:port of its TLS listener, built from the answer's source
	// address so a device cannot point the scanner at a third host.
	Addr string
	IP   string
	Seen time.Time
	// Share is set when it shares what it is playing (Nearby); NRev changes
	// whenever that information does, and Play/Sys say what it is playing.
	Share bool
	NRev  string
	Play  string
	Sys   string
}

// peerState is the identity, the paired list and the recent scan results.
var peerState struct {
	mu sync.Mutex
	// identity, loaded (or made) on first use
	loaded  bool
	loadErr error
	self    peerIdentity
	// probed/probedExists remember whether identity.json exists, so reading
	// the name for a Settings row never creates a key or stats the card
	// every frame.
	probed       bool
	probedExists bool
	// paired devices
	peersLoaded bool
	peers       []peerRecord
	peersSaved  time.Time
	// unpairedThere lists paired devices that answered "not paired": they
	// forgot this device.
	unpairedThere map[string]bool
	// discovery
	seen map[string]peerSeenDevice
	// clone is the address of another device that answered with this
	// device's own id: a JukaHub folder copied between handhelds.
	clone   string
	cloneAt time.Time
}

var (
	// peerEnabledFlag mirrors the Settings switch for the goroutines.
	peerEnabledFlag atomic.Bool
	// peerShuttingDown keeps a late reconcile from restarting the listener
	// during the teardown.
	peerShuttingDown atomic.Bool
)

func init() { bootOnShutdown(peerShutdown) }

// ── Identity ──────────────────────────────────────────────────────────────

// peerFingerprint is a key's id: the hex SHA-256 of the raw public key.
func peerFingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// peerCertFingerprint reads the id out of a certificate a device presented.
func peerCertFingerprint(raw []byte) (string, error) {
	cert, err := x509.ParseCertificate(raw)
	if err != nil {
		return "", err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("the device's key is not an Ed25519 key")
	}
	return peerFingerprint(pub), nil
}

// peerValidID reports whether s looks like a device id.
func peerValidID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isDigit := c >= '0' && c <= '9'
		isHex := c >= 'a' && c <= 'f'
		if !isDigit && !isHex {
			return false
		}
	}
	return true
}

// peerSameID compares two ids in constant time.
func peerSameID(a, b string) bool {
	if a == "" || len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// peerShortID is an id the way a person reads it: "3FA2-91C0".
func peerShortID(id string) string {
	if len(id) < 8 {
		return strings.ToUpper(id)
	}
	return strings.ToUpper(id[:4] + "-" + id[4:8])
}

// peerCleanName strips control characters and bounds a device name.
func peerCleanName(s string) string {
	return partyCleanLabel(s, peerNameLimit)
}

// peerModelOnce caches the model label: device detection reads the system.
var peerModelOnce struct {
	once sync.Once
	name string
}

// peerModelName is what kind of device this is, for the other screens.
func peerModelName() string {
	peerModelOnce.once.Do(func() {
		peerModelOnce.name = peerModelLabel()
	})
	return peerModelOnce.name
}

func peerModelLabel() string {
	if IsWindows() {
		return "Windows PC"
	}
	name := deviceDisplayName(DetectDevice())
	if i := strings.Index(name, " ("); i > 0 {
		name = name[:i]
	}
	switch name {
	case "", "Unknown", "Development build":
		return "JukaHub"
	}
	return name
}

// peerDefaultNameOnce caches the default name (a hostname lookup on Windows).
var peerDefaultNameOnce struct {
	once sync.Once
	name string
}

// peerDefaultName is the name a new identity starts with: the PC's name on
// Windows, the model on the handheld. It can be changed in Settings.
func peerDefaultName() string {
	peerDefaultNameOnce.once.Do(func() {
		name := ""
		if IsWindows() {
			if host, err := os.Hostname(); err == nil {
				name = peerCleanName(host)
			}
		}
		if name == "" {
			name = peerCleanName(peerModelName())
		}
		peerDefaultNameOnce.name = name
	})
	return peerDefaultNameOnce.name
}

// peerSelfCert makes the self-signed certificate TLS presents. Nothing checks
// it against a CA: both sides pin the key inside it.
func peerSelfCert(priv ed25519.PrivateKey) (tls.Certificate, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return tls.Certificate{}, errors.New("identity key is not Ed25519")
	}
	sum := sha256.Sum256(pub)
	tmpl := &x509.Certificate{
		SerialNumber:          new(big.Int).SetBytes(sum[:16]),
		Subject:               pkix.Name{CommonName: "JukaHub device"},
		NotBefore:             time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}, nil
}

// peerIdentityFromSeed rebuilds an identity from its stored seed.
func peerIdentityFromSeed(seed []byte, name string, created time.Time) (peerIdentity, error) {
	if len(seed) != ed25519.SeedSize {
		return peerIdentity{}, errors.New("the stored key has the wrong size")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return peerIdentity{}, errors.New("identity key is not Ed25519")
	}
	cert, err := peerSelfCert(priv)
	if err != nil {
		return peerIdentity{}, err
	}
	name = peerCleanName(name)
	if name == "" {
		name = peerDefaultName()
	}
	return peerIdentity{id: peerFingerprint(pub), name: name, created: created, priv: priv, cert: cert}, nil
}

// peerLoadIdentity reads identity.json from dir, making a new key when there
// is none (or it cannot be read).
func peerLoadIdentity(dir string) (peerIdentity, error) {
	path := filepath.Join(dir, "identity.json")
	var file peerIdentityFile
	var seed []byte
	_, err := readJSONWithBackup(path, &file)
	if err == nil {
		seed, err = base64.StdEncoding.DecodeString(strings.TrimSpace(file.Seed))
		if err == nil && len(seed) != ed25519.SeedSize {
			err = errors.New("the stored key has the wrong size")
		}
	}
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[peer] identity unreadable, making a new one: %v", err)
		}
		seed = make([]byte, ed25519.SeedSize)
		if _, rerr := rand.Read(seed); rerr != nil {
			return peerIdentity{}, rerr
		}
		file = peerIdentityFile{
			Version: 1,
			Seed:    base64.StdEncoding.EncodeToString(seed),
			Name:    peerDefaultName(),
			Created: time.Now().UTC(),
		}
		if werr := peerWriteJSONFile(path, file); werr != nil {
			return peerIdentity{}, werr
		}
	}
	return peerIdentityFromSeed(seed, file.Name, file.Created)
}

// peerWriteJSONFile writes a private store atomically (0600, with a .bak).
func peerWriteJSONFile(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(path, data, 0o600)
}

// peerEnsureLoadedLocked loads (or makes) the identity once.
func peerEnsureLoadedLocked() error {
	if peerState.loaded {
		return peerState.loadErr
	}
	peerState.loaded = true
	self, err := peerLoadIdentity(peerDataDir())
	if err != nil {
		peerState.loadErr = err
		log.Printf("[peer] identity: %v", err)
		return err
	}
	peerState.self = self
	peerState.loadErr = nil
	peerState.probed, peerState.probedExists = true, true
	return nil
}

// peerEnsurePeersLocked loads the paired list once. A missing file is an empty
// list; nothing is created until a device is paired.
func peerEnsurePeersLocked() {
	if peerState.peersLoaded {
		return
	}
	peerState.peersLoaded = true
	peerState.peers = peerLoadPeers(peerDataDir())
}

// peerSelf returns this device's identity, making it on first use.
func peerSelf() (peerIdentity, bool) {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	if err := peerEnsureLoadedLocked(); err != nil {
		return peerIdentity{}, false
	}
	return peerState.self, true
}

// peerSelfID is this device's id, or "" when it has no identity.
func peerSelfID() string {
	self, ok := peerSelf()
	if !ok {
		return ""
	}
	return self.id
}

// peerIdentityExistsLocked reports whether this device has made its key yet,
// without making one. The answer is remembered, so a Settings row asking every
// frame does not stat the card every frame.
func peerIdentityExistsLocked() bool {
	if peerState.loaded {
		return peerState.loadErr == nil
	}
	if !peerState.probed {
		peerState.probed = true
		peerState.probedExists = fileExists(filepath.Join(peerDataDir(), "identity.json"))
	}
	return peerState.probedExists
}

// peerIdentityExists is peerIdentityExistsLocked for callers without the lock.
func peerIdentityExists() bool {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	return peerIdentityExistsLocked()
}

// peerDeviceName is the name the other devices see. Until the feature is
// first used it is the default name, without creating a key for it.
func peerDeviceName() string {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	if !peerIdentityExistsLocked() {
		return peerDefaultName()
	}
	if err := peerEnsureLoadedLocked(); err != nil || peerState.self.name == "" {
		return peerDefaultName()
	}
	return peerState.self.name
}

// peerSetDeviceName renames this device; the name is stored with the key.
func peerSetDeviceName(name string) error {
	name = peerCleanName(name)
	if name == "" {
		return errors.New("the name is empty")
	}
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	if err := peerEnsureLoadedLocked(); err != nil {
		return err
	}
	created := peerState.self.created
	if created.IsZero() {
		created = time.Now().UTC()
	}
	file := peerIdentityFile{
		Version: 1,
		Seed:    base64.StdEncoding.EncodeToString(peerState.self.priv.Seed()),
		Name:    name,
		Created: created,
	}
	if err := peerWriteJSONFile(filepath.Join(peerDataDir(), "identity.json"), file); err != nil {
		return err
	}
	peerState.self.name = name
	return nil
}

// peerResetIdentity gives this device a new key and forgets every paired
// device. It is the fix for a JukaHub folder copied from another handheld,
// which brings that handheld's key along.
func peerResetIdentity() error {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return err
	}
	peerState.mu.Lock()
	peerEnsurePeersLocked()
	name := peerState.self.name
	if name == "" {
		name = peerDefaultName()
	}
	now := time.Now().UTC()
	self, err := peerIdentityFromSeed(seed, name, now)
	if err != nil {
		peerState.mu.Unlock()
		return err
	}
	file := peerIdentityFile{Version: 1, Seed: base64.StdEncoding.EncodeToString(seed), Name: self.name, Created: now}
	if err := peerWriteJSONFile(filepath.Join(peerDataDir(), "identity.json"), file); err != nil {
		peerState.mu.Unlock()
		return err
	}
	old := make([]string, 0, len(peerState.peers))
	for _, p := range peerState.peers {
		old = append(old, p.ID)
	}
	peerState.self = self
	peerState.loaded, peerState.loadErr = true, nil
	peerState.probed, peerState.probedExists = true, true
	peerState.peersLoaded = true
	peerState.peers = nil
	peerState.unpairedThere = nil
	peerState.clone = ""
	peerSavePeersLocked()
	peerState.mu.Unlock()

	for _, id := range old {
		ssyncForgetPeer(id)
		hoffForgetPeer(id)
	}
	peerClientsReset()
	// The listener still presents the old certificate until it restarts.
	if peerEnabledFlag.Load() {
		go peerRestart()
	}
	return nil
}

// ── Paired devices ────────────────────────────────────────────────────────

// peerLoadPeers reads peers.json, dropping malformed and duplicate entries.
func peerLoadPeers(dir string) []peerRecord {
	var file peerStoreFile
	if _, err := readJSONWithBackup(filepath.Join(dir, "peers.json"), &file); err != nil {
		return nil
	}
	out := make([]peerRecord, 0, len(file.Peers))
	seen := map[string]bool{}
	for _, p := range file.Peers {
		p.ID = strings.ToLower(strings.TrimSpace(p.ID))
		if !peerValidID(p.ID) || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		p.Name = peerCleanName(p.Name)
		if p.Name == "" {
			p.Name = "JukaHub " + peerShortID(p.ID)
		}
		out = append(out, p)
		if len(out) >= peerMaxPaired {
			break
		}
	}
	return out
}

// peerSavePeersLocked writes the paired list.
func peerSavePeersLocked() {
	file := peerStoreFile{Version: 1, Peers: append([]peerRecord(nil), peerState.peers...)}
	if err := peerWriteJSONFile(filepath.Join(peerDataDir(), "peers.json"), file); err != nil {
		log.Printf("[peer] could not save the paired devices: %v", err)
	}
	peerState.peersSaved = time.Now()
}

// peerPairedList is the paired devices, sorted by name.
func peerPairedList() []peerRecord {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	peerEnsurePeersLocked()
	out := append([]peerRecord(nil), peerState.peers...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// peerPairedCount is how many devices are paired.
func peerPairedCount() int {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	peerEnsurePeersLocked()
	return len(peerState.peers)
}

// peerPairedRecord finds a paired device by id.
func peerPairedRecord(id string) (peerRecord, bool) {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	peerEnsurePeersLocked()
	for _, p := range peerState.peers {
		if peerSameID(p.ID, id) {
			return p, true
		}
	}
	return peerRecord{}, false
}

// peerAddPaired stores (or refreshes) a paired device.
func peerAddPaired(rec peerRecord) {
	rec.ID = strings.ToLower(strings.TrimSpace(rec.ID))
	if !peerValidID(rec.ID) {
		return
	}
	rec.Name = peerCleanName(rec.Name)
	if rec.Name == "" {
		rec.Name = "JukaHub " + peerShortID(rec.ID)
	}
	if rec.PairedAt.IsZero() {
		rec.PairedAt = time.Now().UTC()
	}
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	peerEnsurePeersLocked()
	delete(peerState.unpairedThere, rec.ID)
	for i := range peerState.peers {
		if peerState.peers[i].ID == rec.ID {
			peerState.peers[i] = rec
			peerSavePeersLocked()
			return
		}
	}
	if len(peerState.peers) >= peerMaxPaired {
		// The oldest pairing makes room: a list this long is mostly devices
		// that were sold or reset.
		sort.SliceStable(peerState.peers, func(i, j int) bool {
			return peerState.peers[i].PairedAt.Before(peerState.peers[j].PairedAt)
		})
		peerState.peers = peerState.peers[1:]
	}
	peerState.peers = append(peerState.peers, rec)
	peerSavePeersLocked()
}

// peerRemovePaired forgets a paired device and everything kept about it.
func peerRemovePaired(id string) bool {
	peerState.mu.Lock()
	peerEnsurePeersLocked()
	removed := false
	kept := peerState.peers[:0]
	for _, p := range peerState.peers {
		if peerSameID(p.ID, id) {
			removed = true
			continue
		}
		kept = append(kept, p)
	}
	peerState.peers = kept
	delete(peerState.unpairedThere, id)
	if removed {
		peerSavePeersLocked()
	}
	peerState.mu.Unlock()
	if removed {
		ssyncForgetPeer(id)
		hoffForgetPeer(id)
		peerClientDrop(id)
	}
	return removed
}

// peerNoteUnpairedThere remembers that a paired device no longer knows this
// one, so its row can say "pair again" instead of failing quietly.
func peerNoteUnpairedThere(id string) {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	if peerState.unpairedThere == nil {
		peerState.unpairedThere = make(map[string]bool)
	}
	peerState.unpairedThere[id] = true
}

// peerIsUnpairedThere reports whether the device answered "not paired".
func peerIsUnpairedThere(id string) bool {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	return peerState.unpairedThere[id]
}

// ── Discovery results ─────────────────────────────────────────────────────

// peerMergeSeen records what a scan heard. A paired device's address and name
// are remembered, so a later request can reach it without scanning again.
func peerMergeSeen(devs []peerSeenDevice, now time.Time) {
	selfID := peerSelfID()
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	if peerState.seen == nil {
		peerState.seen = make(map[string]peerSeenDevice)
	}
	peerEnsurePeersLocked()
	changed := false
	for _, d := range devs {
		if d.ID == "" || d.ID == selfID {
			continue
		}
		peerState.seen[d.ID] = d
		for i := range peerState.peers {
			p := &peerState.peers[i]
			if p.ID != d.ID {
				continue
			}
			p.LastSeen = now.UTC()
			if p.Addr != d.Addr {
				p.Addr = d.Addr
				changed = true
			}
			if d.Name != "" && p.Name != d.Name {
				p.Name = d.Name
				changed = true
			}
			if d.Model != "" && p.Model != d.Model {
				p.Model = d.Model
				changed = true
			}
		}
	}
	for id, d := range peerState.seen {
		if now.Sub(d.Seen) > peerSeenTTL {
			delete(peerState.seen, id)
		}
	}
	if changed || (len(devs) > 0 && now.Sub(peerState.peersSaved) > 10*time.Minute && len(peerState.peers) > 0) {
		peerSavePeersLocked()
	}
}

// peerNoteAddr records where a paired device that called this one listens.
func peerNoteAddr(id, addr string) {
	now := time.Now()
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	peerEnsurePeersLocked()
	for i := range peerState.peers {
		p := &peerState.peers[i]
		if p.ID != id {
			continue
		}
		p.LastSeen = now.UTC()
		if p.Addr != addr {
			p.Addr = addr
			peerSavePeersLocked()
		}
		if peerState.seen == nil {
			peerState.seen = make(map[string]peerSeenDevice)
		}
		d := peerState.seen[id]
		d.ID, d.Addr, d.Seen = id, addr, now
		if d.Name == "" {
			d.Name = p.Name
		}
		if d.Model == "" {
			d.Model = p.Model
		}
		if host, _, err := net.SplitHostPort(addr); err == nil {
			d.IP = host
		}
		peerState.seen[id] = d
		return
	}
}

// peerForgetAddr drops a device's remembered address after a failed request,
// so the next request scans for it again.
func peerForgetAddr(id string) {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	delete(peerState.seen, id)
	for i := range peerState.peers {
		if peerState.peers[i].ID == id {
			peerState.peers[i].LastSeen = time.Time{}
		}
	}
}

// peerAddrFor is where a device can be reached right now: its last scan
// answer, else the address it last used, while that is recent.
func peerAddrFor(id string) string {
	now := time.Now()
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	if d, ok := peerState.seen[id]; ok && now.Sub(d.Seen) <= peerSeenTTL && d.Addr != "" {
		return d.Addr
	}
	peerEnsurePeersLocked()
	for _, p := range peerState.peers {
		if p.ID == id && p.Addr != "" && !p.LastSeen.IsZero() && now.Sub(p.LastSeen) <= peerAddrTTL {
			return p.Addr
		}
	}
	return ""
}

// peerSeenList is the devices heard recently, sorted by name.
func peerSeenList() []peerSeenDevice {
	now := time.Now()
	peerState.mu.Lock()
	out := make([]peerSeenDevice, 0, len(peerState.seen))
	for _, d := range peerState.seen {
		if now.Sub(d.Seen) <= peerSeenTTL {
			out = append(out, d)
		}
	}
	peerState.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// peerSeenDeviceByID returns a device heard recently.
func peerSeenDeviceByID(id string) (peerSeenDevice, bool) {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	d, ok := peerState.seen[id]
	if !ok || time.Since(d.Seen) > peerSeenTTL {
		return peerSeenDevice{}, false
	}
	return d, true
}

// peerOnline reports whether a paired device answered recently.
func peerOnline(id string) bool {
	_, ok := peerSeenDeviceByID(id)
	return ok
}

// peerNoteClone records another device answering with this device's own id.
func peerNoteClone(addr string) {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	if peerState.clone != addr {
		log.Printf("[peer] another device at %s uses this device's identity", addr)
	}
	peerState.clone = addr
	peerState.cloneAt = time.Now()
}

// peerCloneAddr is the address of a device sharing this identity, if one was
// heard in the last ten minutes.
func peerCloneAddr() string {
	peerState.mu.Lock()
	defer peerState.mu.Unlock()
	if peerState.clone == "" || time.Since(peerState.cloneAt) > 10*time.Minute {
		return ""
	}
	return peerState.clone
}

// ── Settings switch ───────────────────────────────────────────────────────

// peerCustomBool reads a stored switch, with a default for "never set".
func peerCustomBool(c *Config, key string, fallback bool) bool {
	if c == nil {
		return fallback
	}
	v, ok := customValue(c.Variables.Custom, key)
	if !ok {
		return fallback
	}
	return coerceBool(v)
}

// peerSettingEnabled is the stored "Nearby devices" switch.
func peerSettingEnabled(c *Config) bool {
	return peerCustomBool(c, peerSettingKey, false)
}

// peerSettingSetEnabled is the Settings row (and the Devices page's toggle).
func peerSettingSetEnabled(c *Config, on bool) {
	setStoreBool(c, peerSettingKey, on)
	peerApplyEnabled(on && safeModeAllows(SafeFeatureNetwork))
	switch {
	case on && !safeModeAllows(SafeFeatureNetwork):
		ShowToast("Nearby devices is on, but safe mode keeps the network off for this launch", ToastKindWarning)
	case on && partyLANAddress() == "":
		ShowToast("Nearby devices is on — it starts working once you join Wi-Fi", ToastKindInfo)
	case on:
		ShowToast("Nearby devices on — visible on this Wi-Fi as "+peerDeviceName(), ToastKindSuccess)
	default:
		ShowToast("Nearby devices off — nothing is shared or listened for", ToastKindInfo)
	}
}

// peerApplyEnabled starts or stops the listener when the switch changes.
func peerApplyEnabled(on bool) {
	if peerEnabledFlag.Swap(on) == on {
		return
	}
	go peerReconcile()
}

// peerSyncFlags copies the stored switches into the flags the goroutines read.
// It runs on the main loop, which owns the config.
func peerSyncFlags(config *Config) {
	peerApplyEnabled(peerSettingEnabled(config) && safeModeAllows(SafeFeatureNetwork))
	nearbyShareFlag.Store(nearbySettingShare(config))
	hoffOffersFlag.Store(hoffSettingOffers(config))
	hoffSettingsFlag.Store(hoffSettingShareSettings(config))
}

// peerShutdown stops the listener for good (bootOnShutdown).
func peerShutdown() {
	peerShuttingDown.Store(true)
	peerReconcile()
}

// ── Scenes ────────────────────────────────────────────────────────────────

// peerEnsureScenes gives Devices, Save sync and Nearby a scene each, in memory,
// the way the tool screens get theirs. They carry no Icon, so the Start menu
// does not list them twice; the Tools hub and Settings open them. Idempotent;
// called at startup and after every config reload.
func peerEnsureScenes(config *Config) {
	if config == nil {
		return
	}
	for _, name := range [...]string{peerSceneName, ssyncSceneName, nearbySceneName} {
		if sceneIndexExact(config, name) < 0 {
			config.Scenes = append(config.Scenes, SceneConfig{Name: name})
		}
	}
	// The pages are drawn and driven by Go: an element injected into one would
	// win the keys from the page (the element dispatch runs first).
	for i := range config.Scenes {
		switch config.Scenes[i].Name {
		case peerSceneName, ssyncSceneName, nearbySceneName:
			config.Scenes[i].Elements = nil
			config.Scenes[i].Layout = ""
		}
	}
}

// peerOpenScene switches to one of the three pages.
func peerOpenScene(config *Config, name string) {
	if config == nil {
		config = appConfig
	}
	if config == nil {
		return
	}
	peerEnsureScenes(config)
	idx := sceneIndexExact(config, name)
	if idx < 0 {
		return
	}
	qs.open = false
	if idx == currentSceneIndex {
		return
	}
	changeSceneTo(config, idx)
}

// peerDevicesOpen opens the Devices page (Settings → Network → Paired devices).
func peerDevicesOpen(config *Config) {
	peerOpenScene(config, peerSceneName)
}

// peerCurrentScene names the current scene when it is one of the three pages.
func peerCurrentScene(config *Config) string {
	if config == nil || currentSceneIndex < 0 || currentSceneIndex >= len(config.Scenes) {
		return ""
	}
	switch name := config.Scenes[currentSceneIndex].Name; name {
	case peerSceneName, ssyncSceneName, nearbySceneName:
		return name
	}
	return ""
}

// peerSceneDisplayName is what the window title, the status bar and the
// controls legend call the three pages; it is meant for extras.go's
// sceneDisplayName, beside the other features' hooks there.
func peerSceneDisplayName(name string) string {
	switch name {
	case peerSceneName:
		return "Devices"
	case ssyncSceneName:
		return "Save sync"
	case nearbySceneName:
		return "Nearby"
	}
	return ""
}

// ── Main-loop tick ────────────────────────────────────────────────────────

var peerTickState struct {
	bootAt  time.Time
	flagsAt time.Time
	scanAt  time.Time
	retryAt time.Time
}

// peerTick runs once per frame from the main loop. Everything it starts runs
// on a goroutine; its own cost is a clock read and, every few seconds, a few
// map lookups.
func peerTick(config *Config) {
	now := time.Now()
	if peerTickState.bootAt.IsZero() {
		peerTickState.bootAt = now
	}
	// Nothing network-related starts while the first frames are drawn.
	if now.Sub(peerTickState.bootAt) < 3*time.Second {
		return
	}
	if now.Sub(peerTickState.flagsAt) >= 2*time.Second {
		peerTickState.flagsAt = now
		peerSyncFlags(config)
		if peerEnabledFlag.Load() && !peerRunning() && now.Sub(peerTickState.retryAt) >= 30*time.Second {
			peerTickState.retryAt = now
			go peerReconcile()
		}
	}
	if !peerEnabledFlag.Load() {
		return
	}
	// A slow background scan keeps the paired devices' addresses fresh, so a
	// handoff push or a sync does not have to start with one.
	if now.Sub(peerTickState.scanAt) >= time.Minute {
		peerTickState.scanAt = now
		if peerPairedCount() > 0 {
			go peerScanAndMerge(context.Background())
		}
	}
	hoffTick(config, now)
}

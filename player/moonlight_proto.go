package main

// moonlight_proto.go — JukaHub's GameStream client: who it is, and how it
// talks to a PC.
//
// GameStream is the protocol Moonlight speaks to Vibepollo, Apollo, Sunshine
// and GeForce Experience. Its control half is plain HTTP(S) with XML replies,
// and that half lives here in Go, the way PS Remote Play's discovery and
// pairing do; the stream itself (RTSP, video, audio, input) is the native
// helper's (moonlight_stream.go).
//
//   - Identity: one RSA-2048 key, a self-signed certificate ("NVIDIA GameStream
//     Client", the name every Moonlight uses) and a 16-hex-digit uniqueid, made
//     once and kept in DataDir/moonlight/ (0600). A PC that paired with
//     JukaHub knows it by that certificate; losing it means pairing again.
//   - Requests: every one carries uniqueid and a fresh uuid. Unpaired, the
//     PC is asked over HTTP (47989): serverinfo and the first four pairing
//     steps. Everything else goes over HTTPS (47984) with the client
//     certificate, and the PC must present the certificate pinned at pairing.
//   - Replies: <root status_code="200" status_message="...">; any other
//     status is an error that carries the PC's message (mlHTTPError).

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	mlServerInfoTimeout = 3 * time.Second
	mlListTimeout       = 10 * time.Second
	mlLaunchTimeout     = 45 * time.Second
	mlCancelTimeout     = 20 * time.Second
	mlDialTimeout       = 5 * time.Second
	mlReplyMax          = 1 << 20
	mlBoxArtMax         = 4 << 20
	mlBoxArtMaxAge      = 24 * time.Hour
	mlIdentityDirName   = "moonlight"
	mlBoxArtDirName     = "moonlight-art"
	mlCertCommonName    = "NVIDIA GameStream Client"
	mlCertYears         = 20
	// mlSurroundStereo is surroundAudioInfo for stereo: channel mask 0x3 in
	// the high half, two channels in the low half.
	mlSurroundStereo = 196610
)

var (
	errMLNotPaired       = errors.New("this PC is not paired with JukaHub — pair it first")
	errMLCertMismatch    = errors.New("the PC's certificate has changed — pair it again")
	errMLHostNotFound    = errors.New("the PC did not answer — check that it is on and on this network")
	errMLNotGameStream   = errors.New("that address answered, but not as a GameStream PC (Sunshine, Apollo, Vibepollo or GeForce Experience)")
	errMLOtherAppRunning = errors.New("another game is running on the PC — quit it first")
	errMLReplyTooLarge   = errors.New("the PC's reply is too large")
	errMLNoBoxArt        = errors.New("the PC has no box art for this game")
)

// mlHTTPError is a reply whose status_code is not 200: the PC refused, and
// Message says why in its own words.
type mlHTTPError struct {
	Code    int
	Message string
}

func (e *mlHTTPError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = http.StatusText(e.Code)
	}
	if msg == "" {
		return "the PC answered with error " + strconv.Itoa(e.Code)
	}
	return "the PC answered: " + msg + " (" + strconv.Itoa(e.Code) + ")"
}

// mlHTTPCode is the PC's status code in err (0 when err is no mlHTTPError).
func mlHTTPCode(err error) int {
	var he *mlHTTPError
	if errors.As(err, &he) {
		return he.Code
	}
	return 0
}

// ── Identity ─────────────────────────────────────────────────────────────

// mlClientIdentity is JukaHub's GameStream identity.
type mlClientIdentity struct {
	key      *rsa.PrivateKey
	cert     *x509.Certificate
	certPEM  []byte
	tlsCert  tls.Certificate
	uniqueID string
}

// mlIdentityState keeps the identity once loaded or made; the lock also keeps
// two goroutines from making two.
var mlIdentityState struct {
	mu sync.Mutex
	id *mlClientIdentity
}

// errMLIdentityDamaged is an identity whose files are there but do not parse.
// Only that, or a file missing, makes a new identity (mlIdentity).
var errMLIdentityDamaged = errors.New("the identity files are damaged")

// mlIdentityDirOverride replaces DataDir/moonlight in tests.
var mlIdentityDirOverride string

func mlIdentityDir() string {
	if d := strings.TrimSpace(mlIdentityDirOverride); d != "" {
		return d
	}
	if dir, err := P().DataDir(); err == nil && dir != "" {
		return filepath.Join(dir, mlIdentityDirName)
	}
	return ""
}

// mlIdentity returns JukaHub's identity, loading it from DataDir/moonlight/ or
// making it the first time. Making the key takes a second or more on a
// handheld: never call it on the render loop.
func mlIdentity() (*mlClientIdentity, error) {
	mlIdentityState.mu.Lock()
	defer mlIdentityState.mu.Unlock()
	if mlIdentityState.id != nil {
		return mlIdentityState.id, nil
	}
	dir := mlIdentityDir()
	if dir == "" {
		return nil, errors.New("JukaHub has no data folder for its Moonlight identity")
	}
	id, err := mlLoadIdentity(dir)
	switch {
	case err == nil:
		mlIdentityState.id = id
		return id, nil
	case os.IsNotExist(err):
		// The first run, or a file gone: a new identity is made below.
	case errors.Is(err, errMLIdentityDamaged):
		logWarn("moonlight", "the client identity in %s is unusable (%v); making a new one — PCs paired before must pair again", dir, err)
	default:
		// The card could not be read just now (an I/O error, too many open
		// files, a permission): the files stay as they are, and the next call
		// tries again. A new identity would make every PC pair again.
		return nil, fmt.Errorf("read the Moonlight identity: %w", err)
	}
	id, err = mlMakeIdentity(dir)
	if err != nil {
		return nil, err
	}
	mlIdentityState.id = id
	return id, nil
}

func mlLoadIdentity(dir string) (*mlClientIdentity, error) {
	keyPEM, err := os.ReadFile(filepath.Join(dir, "client.key"))
	if err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(filepath.Join(dir, "client.pem"))
	if err != nil {
		return nil, err
	}
	uid, err := os.ReadFile(filepath.Join(dir, "uniqueid"))
	if err != nil {
		return nil, err
	}
	uniqueID := strings.ToUpper(strings.TrimSpace(string(uid)))
	if !mlValidUniqueID(uniqueID) {
		return nil, fmt.Errorf("%w: bad uniqueid", errMLIdentityDamaged)
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMLIdentityDamaged, err)
	}
	key, ok := tlsCert.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%w: the key is not RSA", errMLIdentityDamaged)
	}
	cert, err := x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMLIdentityDamaged, err)
	}
	return &mlClientIdentity{key: key, cert: cert, certPEM: certPEM, tlsCert: tlsCert, uniqueID: uniqueID}, nil
}

func mlMakeIdentity(dir string) (*mlClientIdentity, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
	if err != nil {
		return nil, err
	}
	serial.Add(serial, big.NewInt(1))
	// A handheld without network time can start years back; the PCs accept an
	// expired or not-yet-valid client certificate, but a sane window costs
	// nothing.
	now := time.Now()
	if floor := clockFloor(); now.Before(floor) {
		now = floor
	}
	tmpl := &x509.Certificate{
		SerialNumber:       serial,
		Subject:            pkix.Name{CommonName: mlCertCommonName},
		NotBefore:          now.Add(-24 * time.Hour),
		NotAfter:           now.AddDate(mlCertYears, 0, 0),
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, err
	}
	uniqueID := strings.ToUpper(hex.EncodeToString(idBytes))
	for name, data := range map[string][]byte{"client.key": keyPEM, "client.pem": certPEM, "uniqueid": []byte(uniqueID + "\n")} {
		if err := mlWriteFileAtomic(filepath.Join(dir, name), data, 0o600); err != nil {
			return nil, fmt.Errorf("save the Moonlight identity: %w", err)
		}
	}
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	logInfo("moonlight", "made the GameStream client identity (uniqueid %s)", uniqueID)
	return &mlClientIdentity{key: key, cert: cert, certPEM: certPEM, tlsCert: tlsCert, uniqueID: uniqueID}, nil
}

func mlValidUniqueID(s string) bool {
	if len(s) != 16 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// mlWriteFileAtomic writes through a synced temporary file and a rename, so a
// power cut leaves the old file or the new one, never half of one. Unlike
// AtomicWrite it keeps no .bak copy: the identity's key has no business
// lying around twice.
func mlWriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := writeFileSynced(tmp, data, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDirectory(filepath.Dir(path))
	return nil
}

// ── Addresses ────────────────────────────────────────────────────────────

// mlSplitAddr reads "host", "host:port", "[v6]:port" or a bare IPv6 address;
// port is 0 when none was given.
func mlSplitAddr(addr string) (host string, port int) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", 0
	}
	if h, p, err := net.SplitHostPort(addr); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil || n <= 0 || n > 65535 {
			n = 0
		}
		return strings.Trim(h, "[]"), n
	}
	return strings.Trim(addr, "[]"), 0
}

// mlBareHost is addr without a port or brackets.
func mlBareHost(addr string) string {
	h, _ := mlSplitAddr(addr)
	return h
}

// mlOnLocalSubnet reports whether host is an IP address on a network this
// device is on (a LAN address, as opposed to a VPN, internet or name).
func mlOnLocalSubnet(host string) bool {
	ip := net.ParseIP(mlBareHost(host))
	if ip == nil || ip.IsLoopback() {
		return false
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok && ipNet.Contains(ip) {
				// A VPN's tunnel is "up" too; tun and wireguard names are not
				// the LAN.
				name := strings.ToLower(iface.Name)
				tunnel := strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "wg") || strings.HasPrefix(name, "tailscale") || strings.HasPrefix(name, "zt")
				return !tunnel
			}
		}
	}
	return false
}

// ── Requests ─────────────────────────────────────────────────────────────

// mlRequest is one NvHTTP request.
type mlRequest struct {
	host    string
	port    int
	https   bool
	pinned  []byte // the PC's certificate (DER), for HTTPS
	path    string // "serverinfo", "pair", "applist", ...
	query   string // after uniqueid and uuid, already escaped
	timeout time.Duration
	max     int64
}

// mlQueryEscape percent-encodes everything but the unreserved characters, so
// a value can never end its parameter.
func mlQueryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// mlRequestUUID is the fresh uuid every request carries: 32 hex digits, as
// Moonlight sends it.
func mlRequestUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mlTLSConfig is the client side of an HTTPS request to a paired PC.
func mlTLSConfig(id *mlClientIdentity, pinned []byte) *tls.Config {
	pin := append([]byte(nil), pinned...)
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Certificate pinning, not a disabled check. A GameStream PC's
		// certificate is self-signed, so no CA chain or host name can vouch
		// for it; the chain check is replaced by VerifyPeerCertificate below,
		// which accepts exactly the certificate saved at pairing (byte for
		// byte) and refuses every other one. No session cache is set, so no
		// resumed connection skips that check.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(pin) == 0 {
				return errMLNotPaired
			}
			if len(rawCerts) == 0 || !bytes.Equal(rawCerts[0], pin) {
				return errMLCertMismatch
			}
			return nil
		},
		// Sent whatever CAs the PC names: it knows JukaHub by this
		// self-signed certificate, which no CA list would include.
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &id.tlsCert, nil
		},
	}
}

// mlHTTPClient is a one-request client: no proxy (the PC is on the LAN or a
// VPN), no keep-alive, no redirects, HTTP/1.1.
func mlHTTPClient(tlsConf *tls.Config) *http.Client {
	dialer := &net.Dialer{Timeout: mlDialTimeout}
	tr := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSClientConfig:        tlsConf,
		TLSHandshakeTimeout:    mlDialTimeout,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// mlDo sends one request and returns the body of a 200 answer. A body that
// is an XML error is returned as mlHTTPError either way; a non-200 answer
// without one is an mlHTTPError with the HTTP status.
func mlDo(ctx context.Context, r mlRequest) ([]byte, error) {
	id, err := mlIdentity()
	if err != nil {
		return nil, err
	}
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	if r.max <= 0 {
		r.max = mlReplyMax
	}
	scheme := "http"
	var tlsConf *tls.Config
	if r.https {
		if len(r.pinned) == 0 {
			return nil, errMLNotPaired
		}
		scheme = "https"
		tlsConf = mlTLSConfig(id, r.pinned)
	}
	q := "uniqueid=" + id.uniqueID + "&uuid=" + mlRequestUUID()
	if r.query != "" {
		q += "&" + r.query
	}
	target := scheme + "://" + net.JoinHostPort(r.host, strconv.Itoa(r.port)) + "/" + r.path + "?" + q
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "JukaHub")
	resp, err := mlHTTPClient(tlsConf).Do(req)
	if err != nil {
		if errors.Is(err, errMLCertMismatch) {
			return nil, errMLCertMismatch
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, r.max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > r.max {
		return nil, errMLReplyTooLarge
	}
	if resp.StatusCode != http.StatusOK {
		if _, xerr := mlParseReply(body); xerr != nil && mlHTTPCode(xerr) != 0 {
			return nil, xerr
		}
		return nil, &mlHTTPError{Code: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	return body, nil
}

// mlGetXML is mlDo for a request whose answer is XML: the root, once its
// status is 200.
func mlGetXML(ctx context.Context, r mlRequest) (mlXMLNode, error) {
	body, err := mlDo(ctx, r)
	if err != nil {
		return mlXMLNode{}, err
	}
	return mlParseReply(body)
}

// ── XML ──────────────────────────────────────────────────────────────────

// mlXMLNode is one element of a reply, with its children.
type mlXMLNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr  `xml:",any,attr"`
	Text    string      `xml:",chardata"`
	Nodes   []mlXMLNode `xml:",any"`
}

// attr is the element's attribute called name, and whether it has one.
func (n mlXMLNode) attr(name string) (string, bool) {
	for _, a := range n.Attrs {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

// fields maps the element's direct children to their text, the first of
// each name winning.
func (n mlXMLNode) fields() map[string]string {
	out := make(map[string]string, len(n.Nodes))
	for _, c := range n.Nodes {
		if _, ok := out[c.XMLName.Local]; !ok {
			out[c.XMLName.Local] = strings.TrimSpace(c.Text)
		}
	}
	return out
}

// mlParseReply reads a reply's XML and checks its status. The reader is
// lenient (HTML entities, a stray "&", any declared charset): the PCs build
// their XML by hand and app titles are whatever the user typed.
func mlParseReply(data []byte) (mlXMLNode, error) {
	var root mlXMLNode
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	if err := d.Decode(&root); err != nil {
		return mlXMLNode{}, fmt.Errorf("the PC's reply could not be read: %w", err)
	}
	code, ok := root.attr("status_code")
	if !ok {
		return mlXMLNode{}, errors.New("the PC's reply has no status")
	}
	msg, _ := root.attr("status_message")
	n, err := strconv.ParseInt(strings.TrimSpace(code), 10, 64)
	if err != nil {
		return mlXMLNode{}, &mlHTTPError{Code: -1, Message: msg}
	}
	if n != 200 {
		if n > 1<<31-1 {
			// GeForce Experience sends -1 as an unsigned 32-bit number.
			n -= 1 << 32
		}
		return mlXMLNode{}, &mlHTTPError{Code: int(n), Message: msg}
	}
	return root, nil
}

// mlServerInfo is a PC's serverinfo.
type mlServerInfo struct {
	Hostname, UUID, AppVersion, GfeVersion, MAC, LocalIP, ExternalIP, State, GPU string
	HTTPSPort, ExternalPort, CodecSupport, CurrentGame                           int
	Paired, VirtualDisplayCapable                                                bool
	ServerKind                                                                   string
}

// mlApp is one game or app the PC offers.
type mlApp struct {
	ID    int
	Title string
	UUID  string // Apollo/Vibepollo apps carry one
	HDR   bool
}

// mlParseServerInfo reads a serverinfo reply.
func mlParseServerInfo(data []byte) (mlServerInfo, error) {
	root, err := mlParseReply(data)
	if err != nil {
		if mlHTTPCode(err) == 0 {
			// Not XML, or XML without a GameStream status (a router's or a web
			// server's page at that address): something answered, but no PC.
			return mlServerInfo{}, errMLNotGameStream
		}
		return mlServerInfo{}, err
	}
	f := root.fields()
	atoi := func(key string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(f[key]))
		return n
	}
	info := mlServerInfo{
		Hostname:     f["hostname"],
		UUID:         f["uniqueid"],
		AppVersion:   f["appversion"],
		GfeVersion:   f["GfeVersion"],
		MAC:          mlNormalizeMAC(f["mac"]),
		LocalIP:      f["LocalIP"],
		ExternalIP:   f["ExternalIP"],
		State:        f["state"],
		GPU:          f["gputype"],
		HTTPSPort:    atoi("HttpsPort"),
		ExternalPort: atoi("ExternalPort"),
		CodecSupport: atoi("ServerCodecModeSupport"),
		Paired:       f["PairStatus"] == "1",
	}
	// currentgame stays set to the last game played on GeForce Experience, so
	// it only counts while the PC says it is busy (as Moonlight reads it).
	if strings.HasSuffix(strings.ToUpper(info.State), "_SERVER_BUSY") {
		info.CurrentGame = atoi("currentgame")
	}
	vd, vdPresent := f["VirtualDisplayCapable"]
	info.VirtualDisplayCapable = vd == "1" || strings.EqualFold(vd, "true")
	info.ServerKind = mlServerKind(vdPresent, info.AppVersion)
	if info.UUID == "" {
		return info, errMLNotGameStream
	}
	return info, nil
}

// mlServerKind guesses the host software, for display only: Apollo and
// Vibepollo report VirtualDisplayCapable, Sunshine reports the GeForce
// Experience version it imitates (7.1.431.x).
func mlServerKind(vdPresent bool, appVersion string) string {
	switch {
	case vdPresent:
		return "Vibepollo / Apollo"
	case strings.HasPrefix(strings.TrimSpace(appVersion), "7.1.431."):
		return "Sunshine"
	}
	return "GeForce Experience"
}

// mlAppVersionMajor is the first number of appversion (7 for Sunshine).
func mlAppVersionMajor(appVersion string) int {
	first, _, _ := strings.Cut(strings.TrimSpace(appVersion), ".")
	n, _ := strconv.Atoi(first)
	return n
}

// mlNormalizeMAC is a MAC as aa:bb:cc:dd:ee:ff, or "" for none (the PCs send
// all zeros when they will not tell).
func mlNormalizeMAC(s string) string {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return ""
	}
	zero := true
	for _, b := range hw {
		if b != 0 {
			zero = false
		}
	}
	if zero {
		return ""
	}
	return hw.String()
}

// mlParseAppList reads an applist reply: <App><AppTitle>, <ID>,
// <IsHdrSupported>, <UUID>. Entries without a usable id are left out.
func mlParseAppList(data []byte) ([]mlApp, error) {
	root, err := mlParseReply(data)
	if err != nil {
		return nil, err
	}
	var apps []mlApp
	for _, n := range root.Nodes {
		if n.XMLName.Local != "App" {
			continue
		}
		f := n.fields()
		id, err := strconv.Atoi(strings.TrimSpace(f["ID"]))
		if err != nil || id == 0 {
			continue
		}
		title := strings.TrimSpace(f["AppTitle"])
		if title == "" {
			title = "App " + strconv.Itoa(id)
		}
		apps = append(apps, mlApp{ID: id, Title: title, UUID: f["UUID"], HDR: f["IsHdrSupported"] == "1"})
	}
	return apps, nil
}

// mlParseLaunch reads a launch or resume reply: the RTSP URL ("" for the
// default). tag is "gamesession" or "resume"; a 0 there means the PC did not
// start the stream.
func mlParseLaunch(data []byte, tag string) (string, error) {
	root, err := mlParseReply(data)
	if err != nil {
		return "", err
	}
	f := root.fields()
	if v, ok := f[tag]; ok && v == "0" {
		return "", errors.New("the PC did not start the stream")
	}
	return f["sessionUrl0"], nil
}

// ── The calls ────────────────────────────────────────────────────────────

// mlPinnedDER is the paired PC's certificate in DER, for pinning.
func mlPinnedDER(h mlHost) ([]byte, error) {
	if !h.Paired || strings.TrimSpace(h.ServerCert) == "" {
		return nil, errMLNotPaired
	}
	block, _ := pem.Decode([]byte(h.ServerCert))
	if block == nil || len(block.Bytes) == 0 {
		return nil, errMLNotPaired
	}
	return block.Bytes, nil
}

// mlAddrFor is the address a call goes to: addr when given, else the host's
// first.
func mlAddrFor(h mlHost, addr string) string {
	if a := mlBareHost(addr); a != "" {
		return a
	}
	if list := mlAddresses(h); len(list) > 0 {
		return list[0]
	}
	return ""
}

// mlHTTPS is a request to the paired PC over HTTPS.
func mlHTTPS(h mlHost, addr, path, query string, timeout time.Duration) (mlRequest, error) {
	pinned, err := mlPinnedDER(h)
	if err != nil {
		return mlRequest{}, err
	}
	host := mlAddrFor(h, addr)
	if host == "" {
		return mlRequest{}, errMLHostNotFound
	}
	return mlRequest{host: host, port: mlHTTPSPortOf(h), https: true, pinned: pinned, path: path, query: query, timeout: timeout}, nil
}

// mlServerInfoHTTP asks for serverinfo over plain HTTP.
func mlServerInfoHTTP(ctx context.Context, host string, port int) (mlServerInfo, error) {
	body, err := mlDo(ctx, mlRequest{host: host, port: port, path: "serverinfo", timeout: mlServerInfoTimeout})
	if err != nil {
		return mlServerInfo{}, err
	}
	return mlParseServerInfo(body)
}

// mlServerInfoHTTPS asks a paired PC for serverinfo over HTTPS, where its
// PairStatus is true to what it knows of JukaHub.
func mlServerInfoHTTPS(ctx context.Context, h mlHost, host string) (mlServerInfo, error) {
	req, err := mlHTTPS(h, host, "serverinfo", "", mlServerInfoTimeout)
	if err != nil {
		return mlServerInfo{}, err
	}
	body, err := mlDo(ctx, req)
	if err != nil {
		return mlServerInfo{}, err
	}
	return mlParseServerInfo(body)
}

// mlRefreshHost asks the PC for its serverinfo — over HTTPS with the pinned
// certificate when paired, else over HTTP — trying ManualAddr, LocalAddr and
// ExternalAddr in that order, and skipping an address where another PC
// answers. It returns the host updated from the answer, the PC's serverinfo
// and the address that answered; a PC that is stored is saved with it.
// Blocks up to ~6 s per address: never on the render loop.
func mlRefreshHost(ctx context.Context, h mlHost) (mlHost, mlServerInfo, string, error) {
	addrs := mlAddresses(h)
	if len(addrs) == 0 {
		return h, mlServerInfo{}, "", errMLHostNotFound
	}
	var lastErr error
	for _, addr := range addrs {
		if ctx.Err() != nil {
			return h, mlServerInfo{}, "", ctx.Err()
		}
		info, paired, forgot, err := mlServerInfoAt(ctx, h, addr)
		if err != nil {
			lastErr = err
			continue
		}
		if h.UUID != "" && !strings.EqualFold(info.UUID, h.UUID) {
			logInfo("moonlight", "%s answers as another PC (%s), not %s", addr, info.UUID, h.Label())
			lastErr = errMLHostNotFound
			continue
		}
		updated := mlHostFromInfo(h, info, addr)
		updated.Paired = paired
		stored, ok, err := mlUpdateHost(updated.UUID, func(s *mlHost) {
			mlMergeSeen(s, updated)
			// Only an HTTPS answer checked against this very certificate can
			// say the PC forgot JukaHub (the next pairing then replaces the
			// certificate). A plain-HTTP answer always says unpaired, so a
			// copy loaded before a pairing must not undo it.
			if forgot && s.ServerCert == h.ServerCert {
				s.Paired = false
			}
		})
		if err != nil {
			logWarn("moonlight", "saving %s failed: %v", updated.Label(), err)
		}
		if ok {
			return mlRefreshKeepCaller(stored, updated), info, addr, nil
		}
		return updated, info, addr, nil
	}
	if ctx.Err() != nil {
		return h, mlServerInfo{}, "", ctx.Err()
	}
	if mlHTTPCode(lastErr) != 0 || errors.Is(lastErr, errMLNotGameStream) {
		// The PC answered, and refused: its words say more than "not found".
		return h, mlServerInfo{}, "", lastErr
	}
	if lastErr != nil && !errors.Is(lastErr, errMLHostNotFound) {
		logDebug("moonlight", "%s did not answer: %v", h.Label(), lastErr)
	}
	return h, mlServerInfo{}, "", errMLHostNotFound
}

// mlRefreshKeepCaller is the stored host with the caller's settings and last
// app: those are the panel's to change, and it may not have saved them yet.
func mlRefreshKeepCaller(stored, caller mlHost) mlHost {
	stored.Settings = caller.Settings
	if caller.LastAppID != 0 {
		stored.LastAppID, stored.LastAppName = caller.LastAppID, caller.LastAppName
	}
	return stored
}

// mlServerInfoAt is one address's answer, whether the PC counts JukaHub as
// paired, and whether it forgot a pairing: only an HTTPS answer checked
// against h's pinned certificate says that (PairStatus 0, a 401, or another
// certificate). A host that is not paired here is asked over HTTP, where the
// PC always says unpaired. A paired host is asked over HTTPS first; any
// failure but a forgotten pairing is asked again over HTTP (the PC may be
// there with its HTTPS port moved), keeping the local pairing as it is.
func mlServerInfoAt(ctx context.Context, h mlHost, addr string) (mlServerInfo, bool, bool, error) {
	httpPort := mlHTTPPortOf(h)
	if !h.Paired || strings.TrimSpace(h.ServerCert) == "" {
		plain, err := mlServerInfoHTTP(ctx, addr, httpPort)
		return plain, false, false, err
	}
	secure, err := mlServerInfoHTTPS(ctx, h, addr)
	if err == nil {
		return secure, secure.Paired, !secure.Paired, nil
	}
	gone := mlForgotPairing(err)
	plain, perr := mlServerInfoHTTP(ctx, addr, httpPort)
	if perr != nil {
		return mlServerInfo{}, false, false, err
	}
	if gone {
		logInfo("moonlight", "%s no longer accepts JukaHub's pairing (%v)", h.Label(), err)
		return plain, false, true, nil
	}
	if plain.HTTPSPort > 0 && plain.HTTPSPort != mlHTTPSPortOf(h) {
		moved := h
		moved.HTTPSPort = plain.HTTPSPort
		if again, err := mlServerInfoHTTPS(ctx, moved, addr); err == nil {
			return again, again.Paired, !again.Paired, nil
		}
	}
	return plain, true, false, nil
}

// mlForgotPairing reports whether an HTTPS answer says the PC no longer
// accepts JukaHub's pairing: it refused the client certificate (401), or it
// presents another certificate than the one pinned (reinstalled, say).
func mlForgotPairing(err error) bool {
	return mlHTTPCode(err) == http.StatusUnauthorized || errors.Is(err, errMLCertMismatch)
}

// mlHostFromInfo is h updated with a serverinfo answer from addr.
func mlHostFromInfo(h mlHost, info mlServerInfo, addr string) mlHost {
	if h.UUID == "" {
		h.UUID = info.UUID
	}
	if name := strings.TrimSpace(info.Hostname); name != "" {
		h.Name = name
	}
	addr = mlBareHost(addr)
	if mlOnLocalSubnet(addr) {
		h.LocalAddr = addr
	} else if addr != "" && len(mlAddresses(h)) == 0 {
		// The only address known for it: keep it, or it is lost.
		h.ManualAddr = addr
	}
	if ext := mlBareHost(info.ExternalIP); net.ParseIP(ext) != nil && !net.ParseIP(ext).IsUnspecified() {
		h.ExternalAddr = ext
	}
	if info.HTTPSPort > 0 && info.HTTPSPort < 65536 {
		h.HTTPSPort = info.HTTPSPort
	}
	if info.MAC != "" {
		h.MAC = info.MAC
	}
	h.ServerKind = info.ServerKind
	h.VirtualDisplayCapable = info.VirtualDisplayCapable
	h.LastSeen = time.Now()
	return h
}

// mlProbeAddress asks an address the user typed ("host" or "host:port", the
// port being the HTTP one) for serverinfo over HTTP and returns the PC as an
// mlHost. A PC already stored comes back with its pairing and settings; a LAN
// address becomes LocalAddr, anything else (VPN, internet, a name) ManualAddr.
// Nothing is saved. Blocks up to ~3 s.
func mlProbeAddress(ctx context.Context, addr string) (mlHost, mlServerInfo, error) {
	host, port := mlSplitAddr(addr)
	if host == "" {
		return mlHost{}, mlServerInfo{}, errors.New("type the PC's address")
	}
	if port == 0 {
		port = mlHTTPPortDefault
	}
	info, err := mlServerInfoHTTP(ctx, host, port)
	if err != nil {
		if errors.Is(err, errMLNotGameStream) || mlHTTPCode(err) != 0 {
			return mlHost{}, info, err
		}
		if ctx.Err() != nil {
			return mlHost{}, info, ctx.Err()
		}
		logDebug("moonlight", "%s did not answer: %v", host, err)
		return mlHost{}, info, errMLHostNotFound
	}
	h, ok := mlStoredHost(info.UUID)
	if !ok {
		h = mlHost{UUID: info.UUID}
	}
	h = mlHostFromInfo(h, info, host)
	if port != mlHTTPPortDefault {
		h.HTTPPort = port
	}
	if !mlOnLocalSubnet(host) {
		h.ManualAddr = host
	}
	return h, info, nil
}

// mlAppList asks the paired PC for its games and apps (HTTPS, 10 s).
func mlAppList(ctx context.Context, h mlHost, addr string) ([]mlApp, error) {
	req, err := mlHTTPS(h, addr, "applist", "", mlListTimeout)
	if err != nil {
		return nil, err
	}
	body, err := mlDo(ctx, req)
	if err != nil {
		return nil, err
	}
	return mlParseAppList(body)
}

// mlBoxArtDir is DataDir/moonlight-art.
func mlBoxArtDir() string {
	if dir := mlIdentityDir(); dir != "" {
		return filepath.Join(filepath.Dir(dir), mlBoxArtDirName)
	}
	return ""
}

// mlBoxArtPath returns the cached box art of one app, fetching it (HTTPS
// /appasset, 10 s) when it is missing or older than a day. A failed refresh
// keeps the older picture. "" and an error when the PC has none; that answer
// is remembered for a day (<uuid>-<appid>.none), so a PC without art is not
// asked again every time the panel opens.
func mlBoxArtPath(ctx context.Context, h mlHost, addr string, appID int) (string, error) {
	dir := mlBoxArtDir()
	id := mlFileSafe(h.UUID)
	if dir == "" || id == "" {
		return "", errMLNoBoxArt
	}
	base := filepath.Join(dir, id+"-"+strconv.Itoa(appID))
	path, none := base+".png", base+".none"
	cached := false
	if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
		if mlCacheFresh(st.ModTime()) {
			return path, nil
		}
		cached = true
	}
	if st, err := os.Stat(none); err == nil && mlCacheFresh(st.ModTime()) {
		// The PC said it has none within the day: not asked again yet.
		if cached {
			return path, nil
		}
		return "", errMLNoBoxArt
	}
	req, err := mlHTTPS(h, addr, "appasset", "appid="+strconv.Itoa(appID)+"&AssetType=2&AssetIdx=0", mlListTimeout)
	if err != nil {
		if cached {
			return path, nil
		}
		return "", err
	}
	req.max = mlBoxArtMax
	body, err := mlDo(ctx, req)
	if err == nil && !mlImageMagic(body) {
		err = errMLNoBoxArt
	}
	if err != nil {
		// The PC's own answer that it has none is remembered; a timeout, a
		// cancel or a refusal (401, busy) is asked again next time.
		if errors.Is(err, errMLNoBoxArt) || errors.Is(err, errMLReplyTooLarge) || mlHTTPCode(err) == http.StatusNotFound {
			if os.MkdirAll(dir, 0o755) == nil {
				_ = os.WriteFile(none, []byte("no box art\n"), 0o644)
			}
		}
		if cached {
			return path, nil
		}
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := mlWriteArt(path, body); err != nil {
		return "", err
	}
	_ = os.Remove(none)
	return path, nil
}

// mlCacheFresh reports whether a cache file last written at mt is less than
// a day old. One dated in the future (the clock was set back since) is not,
// or it would never be refreshed.
func mlCacheFresh(mt time.Time) bool {
	age := time.Since(mt)
	return age >= 0 && age < mlBoxArtMaxAge
}

// mlWriteArt writes a picture through a temporary file of its own in the same
// folder and a rename, so two fetches of one app (a cancelled pass still
// writing while a new one starts) never share a temporary name, and a reader
// sees the old picture or the new one. The name starts like the picture's,
// so mlForgetBoxArt also removes one a power cut left behind.
func mlWriteArt(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	// CreateTemp makes it 0600; the picture is no secret.
	if err := f.Chmod(0o644); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// mlImageMagic reports whether data starts like a PNG or a JPEG.
func mlImageMagic(data []byte) bool {
	return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) || bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF})
}

// mlCancelApp quits the game running on the PC (HTTPS /cancel).
func mlCancelApp(ctx context.Context, h mlHost, addr string) error {
	req, err := mlHTTPS(h, addr, "cancel", "", mlCancelTimeout)
	if err != nil {
		return err
	}
	root, err := mlGetXML(ctx, req)
	if err != nil {
		return err
	}
	if root.fields()["cancel"] == "0" {
		return errors.New("the PC did not quit the game")
	}
	return nil
}

// mlLaunchKeys is the remote-input key a launch gives the PC: 16 random bytes
// and a random signed 32-bit id. The helper derives the input IV from the id.
type mlLaunchKeys struct {
	key []byte
	id  int32
}

func mlNewLaunchKeys() (mlLaunchKeys, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return mlLaunchKeys{}, err
	}
	return mlLaunchKeys{key: b[:16], id: int32(binary.BigEndian.Uint32(b[16:]))}, nil
}

// mlLaunchParams is what /launch or /resume is told.
type mlLaunchParams struct {
	appID          int
	width, height  int
	fps            int
	keys           mlLaunchKeys
	audioOnPC      bool
	virtualDisplay bool
}

// mlCoreVersionParam ends /launch and /resume as moonlight-common-c's
// LiGetLaunchUrlQueryParameters() does (moonlight-qt sends it): Sunshine,
// Apollo and Vibepollo then hand back an rtspenc:// session URL (encrypted
// RTSP), which the helper's moonlight-common-c reads as it is.
const mlCoreVersionParam = "&corever=1"

// mlLaunchApp starts (or, with resume, goes back to) the game on the PC and
// returns its RTSP URL (sessionUrl0, rtsp:// or rtspenc://, passed on to the
// helper unchanged). 45 s: Apollo may make a virtual display first.
func mlLaunchApp(ctx context.Context, h mlHost, addr string, p mlLaunchParams, resume bool) (string, error) {
	riKey := hex.EncodeToString(p.keys.key)
	riKeyID := strconv.FormatInt(int64(p.keys.id), 10)
	var path, query, tag string
	if resume {
		path, tag = "resume", "resume"
		query = "rikey=" + riKey + "&rikeyid=" + riKeyID + "&surroundAudioInfo=" + strconv.Itoa(mlSurroundStereo) + mlCoreVersionParam
	} else {
		path, tag = "launch", "gamesession"
		audio := "0"
		if p.audioOnPC {
			audio = "1"
		}
		query = "appid=" + strconv.Itoa(p.appID) +
			"&mode=" + strconv.Itoa(p.width) + "x" + strconv.Itoa(p.height) + "x" + strconv.Itoa(p.fps) +
			"&additionalStates=1&sops=0" +
			"&rikey=" + riKey + "&rikeyid=" + riKeyID +
			"&localAudioPlayMode=" + audio +
			"&surroundAudioInfo=" + strconv.Itoa(mlSurroundStereo) +
			"&remoteControllersBitmap=1&gcmap=1&gcpersist=0"
		if p.virtualDisplay {
			query += "&virtualDisplay=1"
		}
		query += mlCoreVersionParam
	}
	req, err := mlHTTPS(h, addr, path, query, mlLaunchTimeout)
	if err != nil {
		return "", err
	}
	body, err := mlDo(ctx, req)
	if err != nil {
		return "", err
	}
	return mlParseLaunch(body, tag)
}

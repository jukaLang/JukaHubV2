package main

// dlna.go — media servers on the home network (UPnP AV, "DLNA"), for
// Media › Movies & TV.
//
// NAS boxes, many routers, Windows media streaming, Plex (its DLNA server),
// Emby, Jellyfin's DLNA plugin, MiniDLNA, Serviio and Universal Media Server
// all publish their films this way, and the handheld needs nothing set up to
// see them: an SSDP search finds the servers, each server's description names
// its ContentDirectory service, and a Browse of a folder answers with its
// subfolders and videos (DIDL-Lite), each video with the address it plays
// from. A video plays straight from that address; nothing is copied.
//
// Only the home network is spoken to. A description address must be a home
// network address, every request goes through the LAN-only dialer
// (webdavLANDial), so an answer on the network cannot point the handheld at
// the internet, and a video's address must be on the home network as well.
// Every answer is size-capped, and nothing is written to the card: servers
// are searched for again on each visit and a folder is read when it opens.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// dlnaSSDPAddr is where UPnP searches are sent.
	dlnaSSDPAddr = "239.255.255.250:1900"
	// dlnaSearchWait is how long the answers to one search are read.
	dlnaSearchWait = 3 * time.Second
	// dlnaDescMax bounds a server's description.
	dlnaDescMax = 512 << 10
	// dlnaBrowseMax bounds one Browse answer.
	dlnaBrowseMax = 8 << 20
	// dlnaBrowsePage is how many objects one Browse asks for.
	dlnaBrowsePage = 200
	// dlnaBrowseLimit is the most objects one folder lists.
	dlnaBrowseLimit = 2000
	// dlnaRequestTimeout bounds one description fetch or Browse.
	dlnaRequestTimeout = 20 * time.Second
	// dlnaMaxServers is the most servers listed.
	dlnaMaxServers = 16
	// dlnaMaxLocations is the most search answers followed.
	dlnaMaxLocations = 48
)

// dlnaSearchTargets are the two answers worth having: a media server, and a
// ContentDirectory service (some servers only answer the second).
var dlnaSearchTargets = []string{
	"urn:schemas-upnp-org:device:MediaServer:1",
	"urn:schemas-upnp-org:service:ContentDirectory:1",
}

// dlnaServer is one media server found on the network.
type dlnaServer struct {
	// UDN is the device's unique name (uuid:…); it keys the server.
	UDN   string
	Name  string
	Model string
	// Location is its description's address.
	Location string
	// ControlURL is where Browse goes, ServiceType the service's exact type
	// (the SOAP action names it).
	ControlURL  string
	ServiceType string
	// IconURL is the server's own icon, "" when it has none.
	IconURL string
}

// dlnaEntry is one row of a folder: a subfolder or a video.
type dlnaEntry struct {
	ID     string
	Title  string
	Folder bool
	// Count is a folder's number of children, -1 when the server does not
	// say.
	Count int
	// URL is where a video plays from; Duration is in seconds (0 unknown).
	URL      string
	Duration float64
	Size     int64
	Year     int
	Art      string
}

// ── Search ──────────────────────────────────────────────────────────────────

// dlnaHTTPClient is the LAN-only client every request goes through.
var dlnaHTTPClient = sync.OnceValue(func() *http.Client {
	tr := &http.Transport{
		DialContext:           webdavLANDial,
		ResponseHeaderTimeout: dlnaRequestTimeout,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       60 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   dlnaRequestTimeout + 10*time.Second,
		// A redirect could lead anywhere: a server that sends one is not
		// followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
})

// dlnaLocalIPv4s are this device's home-network IPv4 addresses: a search is
// sent from each, so every network the handheld is on is asked (Linux sends
// multicast out of the interface that owns the socket's address).
func dlnaLocalIPv4s() []net.IP {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip == nil || ip.IsLoopback() || !webdavLANIP(ip) {
				continue
			}
			out = append(out, ip)
		}
	}
	return out
}

// dlnaSearch sends the searches and gathers the description addresses that
// answer within wait.
func dlnaSearch(ctx context.Context, wait time.Duration) []string {
	dst, err := net.ResolveUDPAddr("udp4", dlnaSSDPAddr)
	if err != nil {
		return nil
	}
	locals := dlnaLocalIPv4s()
	if len(locals) == 0 {
		// No address of our own was recognised: let the system choose.
		locals = []net.IP{nil}
	}
	var (
		mu    sync.Mutex
		found = map[string]bool{}
		order []string
		wg    sync.WaitGroup
	)
	deadline := time.Now().Add(wait)
	for _, ip := range locals {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip})
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(conn *net.UDPConn) {
			defer wg.Done()
			defer conn.Close()
			stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
			defer stop()
			// Each search goes out twice: UDP may drop one.
			for round := 0; round < 2; round++ {
				for _, st := range dlnaSearchTargets {
					_, _ = conn.WriteToUDP(dlnaSearchMessage(st), dst)
				}
			}
			_ = conn.SetReadDeadline(deadline)
			buf := make([]byte, 8192)
			for {
				n, _, err := conn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				loc, ok := dlnaParseSearchReply(buf[:n])
				if !ok {
					continue
				}
				mu.Lock()
				if !found[loc] && len(order) < dlnaMaxLocations {
					found[loc] = true
					order = append(order, loc)
				}
				mu.Unlock()
			}
		}(conn)
	}
	wg.Wait()
	return order
}

// dlnaSearchMessage is one M-SEARCH for a search target.
func dlnaSearchMessage(st string) []byte {
	return []byte("M-SEARCH * HTTP/1.1\r\n" +
		"HOST: " + dlnaSSDPAddr + "\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: " + st + "\r\n" +
		"USER-AGENT: Linux/5 UPnP/1.1 JukaHub/1\r\n\r\n")
}

// dlnaParseSearchReply reads one answer to a search and returns the server's
// description address when it is one worth asking: a media server or a
// ContentDirectory, at a plain-http home-network address. Pure.
func dlnaParseSearchReply(b []byte) (string, bool) {
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(b)), nil)
	if err != nil {
		return "", false
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	st := strings.ToLower(resp.Header.Get("St"))
	if !strings.Contains(st, ":device:mediaserver:") && !strings.Contains(st, ":service:contentdirectory:") {
		return "", false
	}
	return dlnaLANAddress(resp.Header.Get("Location"))
}

// dlnaLANAddress returns raw when it is an http(s) address on the home
// network (not this device itself). Pure.
func dlnaLANAddress(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return "", false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return "", false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return "", false
	}
	if !webdavLANHost(host) {
		return "", false
	}
	return u.String(), true
}

// ── Descriptions ────────────────────────────────────────────────────────────

type dlnaDescXML struct {
	URLBase string        `xml:"URLBase"`
	Device  dlnaDeviceXML `xml:"device"`
}

type dlnaDeviceXML struct {
	DeviceType   string           `xml:"deviceType"`
	FriendlyName string           `xml:"friendlyName"`
	ModelName    string           `xml:"modelName"`
	UDN          string           `xml:"UDN"`
	Icons        []dlnaIconXML    `xml:"iconList>icon"`
	Services     []dlnaServiceXML `xml:"serviceList>service"`
	Devices      []dlnaDeviceXML  `xml:"deviceList>device"`
}

// The numbers stay text: one stray space in a <width> would otherwise fail
// the whole description.
type dlnaIconXML struct {
	Mime   string `xml:"mimetype"`
	Width  string `xml:"width"`
	Height string `xml:"height"`
	URL    string `xml:"url"`
}

type dlnaServiceXML struct {
	Type       string `xml:"serviceType"`
	ControlURL string `xml:"controlURL"`
}

// dlnaParseDescription reads a server's description, fetched from location:
// the device that has a ContentDirectory (the root or one nested in it), its
// name and icon, and the absolute control address. Pure.
func dlnaParseDescription(data []byte, location string) (dlnaServer, bool) {
	var d dlnaDescXML
	if err := dlnaXMLDecoder(bytes.NewReader(data)).Decode(&d); err != nil {
		return dlnaServer{}, false
	}
	base, err := url.Parse(location)
	if err != nil {
		return dlnaServer{}, false
	}
	if b := strings.TrimSpace(d.URLBase); b != "" {
		if u, err := url.Parse(b); err == nil && u.IsAbs() {
			base = u
		}
	}
	dev, svc, ok := dlnaFindDirectory(d.Device, 0)
	if !ok {
		return dlnaServer{}, false
	}
	ctl, err := base.Parse(strings.TrimSpace(svc.ControlURL))
	if err != nil {
		return dlnaServer{}, false
	}
	control, ok := dlnaLANAddress(ctl.String())
	if !ok {
		return dlnaServer{}, false
	}
	s := dlnaServer{
		UDN:         strings.TrimSpace(dev.UDN),
		Name:        dlnaCleanText(dlFirstNonEmpty(strings.TrimSpace(dev.FriendlyName), strings.TrimSpace(d.Device.FriendlyName))),
		Model:       dlnaCleanText(strings.TrimSpace(dev.ModelName)),
		Location:    location,
		ControlURL:  control,
		ServiceType: strings.TrimSpace(svc.Type),
	}
	if s.UDN == "" {
		s.UDN = strings.TrimSpace(d.Device.UDN)
	}
	if s.UDN == "" {
		s.UDN = control
	}
	if s.Name == "" {
		s.Name = dlFirstNonEmpty(s.Model, base.Hostname())
	}
	s.IconURL = dlnaPickIcon(dev.Icons, base)
	if s.IconURL == "" && dev.UDN != d.Device.UDN {
		s.IconURL = dlnaPickIcon(d.Device.Icons, base)
	}
	return s, true
}

// dlnaFindDirectory walks the device tree for the first device with a
// ContentDirectory service.
func dlnaFindDirectory(dev dlnaDeviceXML, depth int) (dlnaDeviceXML, dlnaServiceXML, bool) {
	for _, s := range dev.Services {
		if strings.Contains(strings.ToLower(s.Type), ":service:contentdirectory:") && strings.TrimSpace(s.ControlURL) != "" {
			return dev, s, true
		}
	}
	if depth >= 4 {
		return dlnaDeviceXML{}, dlnaServiceXML{}, false
	}
	for _, sub := range dev.Devices {
		if d, s, ok := dlnaFindDirectory(sub, depth+1); ok {
			return d, s, true
		}
	}
	return dlnaDeviceXML{}, dlnaServiceXML{}, false
}

// dlnaPickIcon is the largest PNG or JPEG icon up to 256 pixels wide, as an
// absolute home-network address.
func dlnaPickIcon(icons []dlnaIconXML, base *url.URL) string {
	best, bestW := "", -1
	for _, ic := range icons {
		mime := strings.ToLower(strings.TrimSpace(ic.Mime))
		if mime != "image/png" && mime != "image/jpeg" {
			continue
		}
		w, _ := strconv.Atoi(strings.TrimSpace(ic.Width))
		if w > 256 || w <= bestW {
			continue
		}
		u, err := base.Parse(strings.TrimSpace(ic.URL))
		if err != nil {
			continue
		}
		if addr, ok := dlnaLANAddress(u.String()); ok {
			best, bestW = addr, w
		}
	}
	return best
}

// dlnaXMLDecoder is forgiving: servers send HTML entities, stray ampersands
// and labels for charsets they do not use.
func dlnaXMLDecoder(r io.Reader) *xml.Decoder {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	return dec
}

// dlnaCleanText folds a server's label onto one line and bounds it.
func dlnaCleanText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:120])
	}
	return s
}

// dlnaFetchServer reads one description address.
func dlnaFetchServer(ctx context.Context, location string) (dlnaServer, error) {
	ctx, cancel := context.WithTimeout(ctx, dlnaRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return dlnaServer{}, err
	}
	resp, err := dlnaHTTPClient().Do(req)
	if err != nil {
		return dlnaServer{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return dlnaServer{}, fmt.Errorf("description answered %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, dlnaDescMax))
	if err != nil {
		return dlnaServer{}, err
	}
	s, ok := dlnaParseDescription(data, location)
	if !ok {
		return dlnaServer{}, errors.New("not a media server")
	}
	return s, nil
}

// dlnaDiscover finds the media servers on the home network: a search, then
// every description that answered, read side by side. Sorted by name.
func dlnaDiscover(ctx context.Context) []dlnaServer {
	locs := dlnaSearch(ctx, dlnaSearchWait)
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []dlnaServer
	)
	sem := make(chan struct{}, 4)
	for _, loc := range locs {
		wg.Add(1)
		go func(loc string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s, err := dlnaFetchServer(ctx, loc)
			if err != nil {
				return
			}
			mu.Lock()
			out = append(out, s)
			mu.Unlock()
		}(loc)
	}
	wg.Wait()
	return dlnaDedupe(out)
}

// dlnaDedupe keeps one entry per server (a server answers each search target
// and on every address it has) and sorts them by name. Pure.
func dlnaDedupe(list []dlnaServer) []dlnaServer {
	seen := map[string]bool{}
	out := make([]dlnaServer, 0, len(list))
	for _, s := range list {
		key := strings.ToLower(s.UDN)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].UDN < out[j].UDN
	})
	if len(out) > dlnaMaxServers {
		out = out[:dlnaMaxServers]
	}
	return out
}

// ── Browse ──────────────────────────────────────────────────────────────────

type dlnaBrowseEnvelope struct {
	Body struct {
		Response struct {
			Result         string `xml:"Result"`
			NumberReturned string `xml:"NumberReturned"`
			TotalMatches   string `xml:"TotalMatches"`
		} `xml:"BrowseResponse"`
		Fault struct {
			String      string `xml:"faultstring"`
			Description string `xml:"detail>UPnPError>errorDescription"`
		} `xml:"Fault"`
	} `xml:"Body"`
}

type dlnaDIDL struct {
	Containers []dlnaObjectXML `xml:"container"`
	Items      []dlnaObjectXML `xml:"item"`
}

type dlnaObjectXML struct {
	ID         string       `xml:"id,attr"`
	ChildCount string       `xml:"childCount,attr"`
	Title      string       `xml:"title"`
	Class      string       `xml:"class"`
	Date       string       `xml:"date"`
	Art        []string     `xml:"albumArtURI"`
	Res        []dlnaResXML `xml:"res"`
}

type dlnaResXML struct {
	URL        string `xml:",chardata"`
	Protocol   string `xml:"protocolInfo,attr"`
	Size       string `xml:"size,attr"`
	Duration   string `xml:"duration,attr"`
	Resolution string `xml:"resolution,attr"`
}

// dlnaBrowseBody is the SOAP request for one page of a folder.
func dlnaBrowseBody(serviceType, objectID string, start, count int) []byte {
	var id bytes.Buffer
	_ = xml.EscapeText(&id, []byte(objectID))
	var st bytes.Buffer
	_ = xml.EscapeText(&st, []byte(serviceType))
	return []byte(`<?xml version="1.0" encoding="utf-8"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">` +
		`<s:Body><u:Browse xmlns:u="` + st.String() + `">` +
		`<ObjectID>` + id.String() + `</ObjectID>` +
		`<BrowseFlag>BrowseDirectChildren</BrowseFlag>` +
		`<Filter>*</Filter>` +
		`<StartingIndex>` + strconv.Itoa(start) + `</StartingIndex>` +
		`<RequestedCount>` + strconv.Itoa(count) + `</RequestedCount>` +
		`<SortCriteria></SortCriteria>` +
		`</u:Browse></s:Body></s:Envelope>`)
}

// dlnaBrowsePageOnce asks for one page of a folder: its DIDL-Lite, how many
// objects came back and how many there are (0 when the server does not say).
func dlnaBrowsePageOnce(ctx context.Context, s dlnaServer, objectID string, start int) (string, int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, dlnaRequestTimeout)
	defer cancel()
	st := dlFirstNonEmpty(s.ServiceType, "urn:schemas-upnp-org:service:ContentDirectory:1")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ControlURL,
		bytes.NewReader(dlnaBrowseBody(st, objectID, start, dlnaBrowsePage)))
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+st+`#Browse"`)
	req.Header.Set("User-Agent", "Linux/5 UPnP/1.1 JukaHub/1")
	resp, err := dlnaHTTPClient().Do(req)
	if err != nil {
		return "", 0, 0, fmt.Errorf("%s did not answer", dlFirstNonEmpty(s.Name, "the server"))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, dlnaBrowseMax))
	if err != nil {
		return "", 0, 0, err
	}
	return dlnaParseBrowse(data, resp.StatusCode)
}

// dlnaParseBrowse reads a Browse answer (or its SOAP fault). Pure.
func dlnaParseBrowse(data []byte, status int) (string, int, int, error) {
	var env dlnaBrowseEnvelope
	if err := dlnaXMLDecoder(bytes.NewReader(data)).Decode(&env); err != nil {
		if status != http.StatusOK {
			return "", 0, 0, fmt.Errorf("the server answered %d", status)
		}
		return "", 0, 0, errors.New("the server sent something unexpected")
	}
	if f := env.Body.Fault; status != http.StatusOK || f.String != "" || f.Description != "" {
		why := dlFirstNonEmpty(strings.TrimSpace(f.Description), strings.TrimSpace(f.String))
		if why == "" {
			why = "it answered " + strconv.Itoa(status)
		}
		return "", 0, 0, errors.New("the server refused: " + dlnaCleanText(why))
	}
	r := env.Body.Response
	n, _ := strconv.Atoi(strings.TrimSpace(r.NumberReturned))
	total, _ := strconv.Atoi(strings.TrimSpace(r.TotalMatches))
	return r.Result, n, total, nil
}

// dlnaBrowse lists a folder ("0" is the top): its subfolders, then its
// videos. Music and pictures are left out — this is the films' screen.
func dlnaBrowse(ctx context.Context, s dlnaServer, objectID string) ([]dlnaEntry, error) {
	var folders, videos []dlnaEntry
	start := 0
	for start < dlnaBrowseLimit {
		didl, n, total, err := dlnaBrowsePageOnce(ctx, s, objectID, start)
		if err != nil {
			if start > 0 {
				break
			}
			return nil, err
		}
		f, v := dlnaParseDIDL(didl)
		folders = append(folders, f...)
		videos = append(videos, v...)
		if n <= 0 {
			break
		}
		start += n
		if (total > 0 && start >= total) || (total <= 0 && n < dlnaBrowsePage) {
			break
		}
	}
	return append(folders, videos...), nil
}

// dlnaParseDIDL reads a folder's DIDL-Lite: the subfolders, and the videos
// with a home-network address the player can open. Pure.
func dlnaParseDIDL(didl string) (folders, videos []dlnaEntry) {
	var d dlnaDIDL
	if err := dlnaXMLDecoder(strings.NewReader(didl)).Decode(&d); err != nil {
		return nil, nil
	}
	for _, c := range d.Containers {
		title := dlnaCleanText(c.Title)
		if c.ID == "" || title == "" {
			continue
		}
		e := dlnaEntry{ID: c.ID, Title: title, Folder: true, Count: -1}
		if n, err := strconv.Atoi(strings.TrimSpace(c.ChildCount)); err == nil && n >= 0 {
			e.Count = n
		}
		folders = append(folders, e)
	}
	for _, it := range d.Items {
		class := strings.ToLower(strings.TrimSpace(it.Class))
		res, ok := dlnaPickRes(it.Res)
		if !ok || (class != "" && !strings.HasPrefix(class, "object.item.videoitem") && !dlnaResIsVideo(res)) {
			continue
		}
		addr, ok := dlnaLANAddress(strings.TrimSpace(res.URL))
		if !ok {
			continue
		}
		e := dlnaEntry{ID: it.ID, Title: dlnaCleanText(it.Title), URL: addr,
			Duration: dlnaParseDuration(res.Duration), Year: dlnaYear(it.Date)}
		e.Size, _ = strconv.ParseInt(strings.TrimSpace(res.Size), 10, 64)
		for _, r := range it.Res {
			// A smaller copy often leaves its length out; the original says it.
			if e.Duration > 0 {
				break
			}
			e.Duration = dlnaParseDuration(r.Duration)
		}
		if e.Title == "" {
			e.Title = "Video"
		}
		for _, a := range it.Art {
			if art, ok := dlnaLANAddress(strings.TrimSpace(a)); ok {
				e.Art = art
				break
			}
		}
		if e.Art == "" {
			e.Art = dlnaThumbRes(it.Res)
		}
		videos = append(videos, e)
	}
	return folders, videos
}

// dlnaResMime is the MIME type in a res's protocolInfo
// ("http-get:*:video/mp4:DLNA.ORG_PN=…"), lower-case.
func dlnaResMime(r dlnaResXML) string {
	parts := strings.Split(r.Protocol, ":")
	if len(parts) < 3 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parts[2]))
}

func dlnaResIsVideo(r dlnaResXML) bool {
	return strings.HasPrefix(dlnaResMime(r), "video/")
}

// dlnaPickRes chooses the copy of an item to play: an http one that is not a
// picture, subtitle or sound, preferring a picture of at most 720 lines (a
// server may offer a smaller copy beside the original) and otherwise the
// first, which is the original. Pure.
func dlnaPickRes(list []dlnaResXML) (dlnaResXML, bool) {
	best, bestScore := dlnaResXML{}, -1<<30
	for i, r := range list {
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		proto := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Protocol, ":", 2)[0]))
		if proto != "" && proto != "http-get" {
			continue
		}
		mime := dlnaResMime(r)
		if strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "audio/") || strings.HasPrefix(mime, "text/") ||
			strings.Contains(mime, "subrip") || strings.Contains(mime, "subtitle") {
			continue
		}
		score := 100 - i
		if _, h := dlnaResolution(r.Resolution); h > 0 {
			switch {
			case h <= 720:
				score += 50
			case h > 1088:
				score -= 80
			}
		}
		if score > bestScore {
			best, bestScore = r, score
		}
	}
	return best, bestScore > -1<<30
}

// dlnaThumbRes is a picture res's address (some servers give a video's
// thumbnail that way), "" when there is none on the home network.
func dlnaThumbRes(list []dlnaResXML) string {
	for _, r := range list {
		if strings.HasPrefix(dlnaResMime(r), "image/") {
			if addr, ok := dlnaLANAddress(strings.TrimSpace(r.URL)); ok {
				return addr
			}
		}
	}
	return ""
}

// dlnaResolution reads "1280x720". Pure.
func dlnaResolution(s string) (int, int) {
	a, b, ok := strings.Cut(strings.ToLower(strings.TrimSpace(s)), "x")
	if !ok {
		return 0, 0
	}
	w, err1 := strconv.Atoi(a)
	h, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || w < 0 || h < 0 {
		return 0, 0
	}
	return w, h
}

// dlnaParseDuration reads a res duration, "H+:MM:SS[.F+]" (or a fraction
// "F0/F1" after the seconds), into seconds; 0 when it cannot. Pure.
func dlnaParseDuration(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec := parts[2]
	if i := strings.Index(sec, "/"); i >= 0 {
		// "SS.F0/F1": the fraction's own form; the whole seconds are enough.
		sec = sec[:i]
		if j := strings.Index(sec, "."); j >= 0 {
			sec = sec[:j]
		}
	}
	secs, err3 := strconv.ParseFloat(sec, 64)
	if err1 != nil || err2 != nil || err3 != nil || h < 0 || m < 0 || m > 59 || secs < 0 || secs >= 60 {
		return 0
	}
	return float64(h)*3600 + float64(m)*60 + secs
}

// dlnaYear is the year at the start of a dc:date ("2001-05-23"), 0 when there
// is none. Pure.
func dlnaYear(date string) int {
	date = strings.TrimSpace(date)
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil || y < 1870 || y > 2200 {
		return 0
	}
	return y
}

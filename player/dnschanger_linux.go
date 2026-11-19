//go:build linux

package main

// dnschanger_linux.go — the handheld's side of Settings → Network → DNS:
// resolv.conf names the chosen servers while JukaHub runs.
//
// The file is rewritten in place, as the VPN does it (vpnd_linux.go, whose
// helpers this shares): on TrimUI it is usually a link into /tmp that DHCP
// (udhcpc's script) empties and refills on every lease. So the worker looks
// every few seconds while a choice is set and writes it back whenever the
// network's own replaced it, keeping what DHCP wrote as the file to put
// back. That copy also waits in data/dns-resolv.json (left out of backups,
// vpnBackupExcluded, and kept by an erase, factoryEraseKeepData), so after a
// crash or a power cut Automatic still finds the network's own.
//
// Every read the keeper acts on is a stable one (two reads a moment apart
// agree, vpndReadResolv): DHCP empties the file before refilling it, and a
// file caught in between must not become "the network's own".
//
// The VPN's file wins: while resolv.conf carries the VPN's marker the worker
// leaves it alone. The VPN keeps the file it replaced and writes it back when
// it stops; when that file is ours, the VPN writes the network's own instead
// (dnsOwnFileFor), so a VPN that outlives JukaHub does not leave our servers
// behind. A JukaHub still running writes the choice again moments later.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

const dnsResolvMarker = "# JukaHub DNS (Settings → Network → DNS): Automatic puts the network's own servers back\n"

type dnsResolvSaved struct {
	Path     string `json:"path"`
	Original string `json:"original"`
}

// dnsSys is what the worker knows of resolv.conf. The worker and the quit
// step hold dnsSysMu while they read or write it.
var (
	dnsSysMu sync.Mutex
	dnsSys   struct {
		loaded   bool   // the saved copy was looked for
		path     string // the real file resolv.conf leads to
		original []byte // the network's own file, to put back
		have     bool   // original is known
		holds    bool   // a file of ours may be in place
		written  []byte // what was written last, to notice lines added under it
	}
	// dnsHoldsNow mirrors dnsSys.holds for the main thread, which must not
	// wait on dnsSysMu (the worker may hold it through a read's pause and a
	// write to the card).
	dnsHoldsNow atomic.Bool
)

func dnsSystemSupported() bool { return true }

// dnsSystemHolds reports whether a file of ours may be in place, so the quit
// teardown puts the network's own back.
func dnsSystemHolds() bool { return dnsHoldsNow.Load() }

func dnsSetHolds(on bool) {
	dnsSys.holds = on
	dnsHoldsNow.Store(on)
}

// dnsParseResolv lists the nameservers in a resolv.conf.
func dnsParseResolv(b []byte) []netip.Addr {
	var out []netip.Addr
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			if a, err := netip.ParseAddr(f[1]); err == nil {
				out = append(out, a)
			}
		}
	}
	return out
}

// dnsResolvText is the file that names servers: the marker, the servers, and
// the network's own search domains, so short local names still get them.
func dnsResolvText(servers []netip.Addr, original []byte) []byte {
	var b strings.Builder
	b.WriteString(dnsResolvMarker)
	for i, s := range servers {
		if i == dnsMaxServers {
			break
		}
		b.WriteString("nameserver " + s.String() + "\n")
	}
	for _, line := range strings.Split(string(original), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && (f[0] == "search" || f[0] == "domain") {
			b.WriteString(strings.Join(f, " ") + "\n")
		}
	}
	return []byte(b.String())
}

// dnsResolvAppended is what was added under the file we wrote — DHCP's
// lines, appended after it had emptied the file and our write landed in
// between — when it names a server; nil otherwise.
func dnsResolvAppended(cur, written []byte) []byte {
	if len(written) == 0 || len(cur) <= len(written) || !bytes.HasPrefix(cur, written) {
		return nil
	}
	extra := cur[len(written):]
	if len(dnsParseResolv(extra)) == 0 {
		return nil
	}
	return append([]byte(nil), extra...)
}

// dnsResolvForeign is a file another writer marked as its own: the VPN's.
func dnsResolvForeign(b []byte) bool { return bytes.HasPrefix(b, []byte(vpndResolvMarker)) }

func dnsResolvOurs(b []byte) bool { return bytes.HasPrefix(b, []byte(dnsResolvMarker)) }

// dnsResolvBlank is an emptied file: DHCP between emptying and refilling it,
// or a write of ours (or the VPN's) cut off half way.
func dnsResolvBlank(b []byte) bool { return len(bytes.TrimSpace(b)) == 0 }

// dnsSystemReconcile makes resolv.conf name want (nil: the network's own),
// once. The worker calls it every few seconds.
func dnsSystemReconcile(stateDir string, want []netip.Addr) dnsSysResult {
	dnsSysMu.Lock()
	defer dnsSysMu.Unlock()
	if dnsQuitting.Load() {
		return dnsSysResult{}
	}
	d := &dnsSys
	path, err := filepath.EvalSymlinks("/etc/resolv.conf")
	if err != nil {
		if want == nil {
			return dnsSysResult{}
		}
		return dnsSysResult{Err: "the system has no DNS settings file yet"}
	}
	if !d.loaded {
		d.loaded = true
		dnsLoadSaved(stateDir, path)
	}
	if d.path != "" && d.path != path {
		// resolv.conf leads somewhere else now: the copy was another file's.
		dnsForget(stateDir)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		if want == nil {
			return dnsSysResult{}
		}
		return dnsSysResult{Err: "resolv.conf cannot be read"}
	}
	if dnsResolvBlank(cur) && d.holds {
		// Ours cut off, or DHCP half way: act only on a file that stays
		// empty, as if it were ours.
		b, stable := vpndReadResolv(path)
		if !stable || !dnsResolvBlank(b) {
			return dnsSysResult{Pending: true, Network: dnsParseResolv(d.original)}
		}
		cur = []byte(dnsResolvMarker)
	}
	switch {
	case dnsResolvForeign(cur):
		res := dnsSysResult{ByVPN: true}
		if d.have {
			res.Network = dnsParseResolv(d.original)
		}
		return res
	case dnsResolvOurs(cur):
		return dnsKeepOurs(stateDir, path, cur, want)
	case want == nil:
		// The network's own is in place and nothing is asked: a copy kept
		// from before is stale once a stable read shows a real file.
		if d.holds || d.have {
			if b, stable := vpndReadResolv(path); stable && !dnsResolvOurs(b) && !dnsResolvForeign(b) && len(dnsParseResolv(b)) > 0 {
				dnsForget(stateDir)
			}
		}
		return dnsSysResult{Network: dnsParseResolv(cur)}
	}
	return dnsReplaceNetworks(stateDir, path, cur, want)
}

// dnsKeepOurs handles a file of ours in place: lines DHCP appended under it
// become the network's own, then the choice is rewritten (it may have
// changed) or, on Automatic, the network's own goes back.
func dnsKeepOurs(stateDir, path string, cur []byte, want []netip.Addr) dnsSysResult {
	d := &dnsSys
	dnsSetHolds(true)
	if dnsResolvAppended(cur, d.written) != nil {
		b, stable := vpndReadResolv(path)
		if !stable || !dnsResolvOurs(b) {
			// Still being written, or no longer ours (DHCP or the VPN wrote
			// the whole file meanwhile): the next look sorts it out.
			return dnsSysResult{Pending: true, Network: dnsParseResolv(d.original)}
		}
		if extra := dnsResolvAppended(b, d.written); extra != nil {
			// DHCP refilled the file just after our write and its servers
			// landed under ours: those are the network's own now.
			d.path, d.original, d.have = path, extra, true
			_ = dnsSave(stateDir)
		}
		cur = b
	}
	if want == nil {
		return dnsRestore(stateDir, path)
	}
	if !d.have {
		// Ours, with the network's own lost (a crash and an erased data
		// folder): the router answers DNS on nearly every home network.
		if gw := vpndGatewayResolv(); gw != nil {
			d.path, d.original, d.have = path, gw, true
			_ = dnsSave(stateDir)
		}
	}
	text := dnsResolvText(want, d.original)
	if !bytes.Equal(cur, text) {
		if err := vpndWriteInPlace(path, text); err != nil {
			return dnsSysResult{Err: dnsWriteErr(err), Network: dnsParseResolv(d.original)}
		}
	}
	d.written = text
	return dnsSysResult{Applied: true, Network: dnsParseResolv(d.original)}
}

// dnsReplaceNetworks keeps the network's own file aside, then writes the
// choice over it.
func dnsReplaceNetworks(stateDir, path string, cur []byte, want []netip.Addr) dnsSysResult {
	d := &dnsSys
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return dnsSysResult{Err: dnsWriteErr(err), Network: dnsParseResolv(cur)}
	}
	_ = f.Close()
	orig, stable := vpndReadResolv(path)
	if !stable || dnsResolvOurs(orig) || dnsResolvForeign(orig) {
		return dnsSysResult{Pending: true, Network: dnsParseResolv(cur)}
	}
	if len(dnsParseResolv(orig)) == 0 && d.have && len(dnsParseResolv(d.original)) > 0 {
		// A file with no server is worth less than the network's own seen
		// before.
		orig = d.original
	}
	// The same file again (a lease renewed on the same network) needs no new
	// copy on the card, nor a log line.
	same := d.have && d.path == path && bytes.Equal(d.original, orig)
	if !same {
		d.path, d.original, d.have = path, orig, true
		if err := dnsSave(stateDir); err != nil {
			d.have, d.original = false, nil
			return dnsSysResult{Err: "the network's own settings cannot be kept aside", Network: dnsParseResolv(orig)}
		}
		// The save waited for the card: a lease or the VPN may have written
		// the file meanwhile, and that one is the network's own now.
		if now, err := os.ReadFile(path); err != nil || (!bytes.Equal(now, cur) && !bytes.Equal(now, orig)) {
			return dnsSysResult{Pending: true, Network: dnsParseResolv(orig)}
		}
	}
	text := dnsResolvText(want, orig)
	dnsSetHolds(true)
	if err := vpndWriteInPlace(path, text); err != nil {
		// The network's own goes back; the saved copy stays if it cannot.
		if vpndWriteInPlace(path, orig) == nil {
			dnsForget(stateDir)
		}
		return dnsSysResult{Err: dnsWriteErr(err), Network: dnsParseResolv(orig)}
	}
	d.written = text
	if !same {
		logInfo("dns", "resolv.conf names %s (the network's own: %s)", dnsJoin(want), setOr(dnsJoin(dnsParseResolv(orig)), "none"))
	}
	return dnsSysResult{Applied: true, Network: dnsParseResolv(orig)}
}

// dnsRestore writes the network's own file back over ours.
func dnsRestore(stateDir, path string) dnsSysResult {
	d := &dnsSys
	orig := d.original
	if !d.have || len(dnsParseResolv(orig)) == 0 {
		// Lost, or one with no server (taken while Wi-Fi was off): the
		// router, when there is one, beats an empty file. Without either,
		// ours stays until DHCP writes the network's own again: a file with
		// no server would leave no DNS at all.
		gw := vpndGatewayResolv()
		if gw == nil {
			dnsSetHolds(true)
			return dnsSysResult{Err: "the network's own servers are not known yet"}
		}
		orig = gw
	}
	if err := vpndWriteInPlace(path, orig); err != nil {
		dnsSetHolds(true)
		return dnsSysResult{Err: dnsWriteErr(err)}
	}
	logInfo("dns", "resolv.conf has the network's own servers again (%s)", setOr(dnsJoin(dnsParseResolv(orig)), "none"))
	dnsForget(stateDir)
	return dnsSysResult{Network: dnsParseResolv(orig)}
}

// dnsSystemRelease is the quit step: the network's own back where ours is in
// place. A VPN's file is left to the VPN, which puts back the network's own
// (not ours) when it stops; the saved copy then waits for the next start.
func dnsSystemRelease(stateDir string) (string, error) {
	dnsSysMu.Lock()
	defer dnsSysMu.Unlock()
	d := &dnsSys
	if !d.holds {
		return "nothing to put back", nil
	}
	path, err := filepath.EvalSymlinks("/etc/resolv.conf")
	if err != nil {
		return "", err
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if dnsResolvBlank(cur) {
		// DHCP may be half way through a new file: write the network's own
		// only over one that stays empty; else the copy waits for the next
		// start.
		b, stable := vpndReadResolv(path)
		if !stable || !dnsResolvBlank(b) {
			return "resolv.conf was being written; put back at the next start", nil
		}
	}
	switch {
	case dnsResolvForeign(cur):
		return "the VPN's DNS is in place; it puts the network's own back", nil
	case !dnsResolvOurs(cur) && !dnsResolvBlank(cur):
		dnsForget(stateDir)
		return "the network's own was already back", nil
	}
	if extra := dnsResolvAppended(cur, d.written); extra != nil {
		d.original, d.have = extra, true
	}
	if res := dnsRestore(stateDir, path); res.Err != "" {
		return "", errors.New(res.Err)
	}
	return "the network's own DNS is back", nil
}

// dnsOwnFileFor is the file the VPN puts back when it stops (vpnd_linux.go):
// orig, unless orig is ours — then the network's own, from the copy kept
// aside in the same data folder, or the router. Called in the tunnel process,
// which may outlive JukaHub.
func dnsOwnFileFor(stateDir, path string, orig []byte) []byte {
	if !dnsResolvOurs(orig) {
		return orig
	}
	if stateDir != "" {
		var saved dnsResolvSaved
		data, err := os.ReadFile(filepath.Join(stateDir, dnsBackupFile))
		if err == nil && json.Unmarshal(data, &saved) == nil && saved.Path == path && len(dnsParseResolv([]byte(saved.Original))) > 0 {
			return []byte(saved.Original)
		}
	}
	if gw := vpndGatewayResolv(); gw != nil {
		return gw
	}
	return orig
}

// dnsLoadSaved reads the copy a run that did not quit left behind.
func dnsLoadSaved(stateDir, path string) {
	if stateDir == "" {
		return
	}
	file := filepath.Join(stateDir, dnsBackupFile)
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	var saved dnsResolvSaved
	if json.Unmarshal(data, &saved) != nil || saved.Path != path {
		_ = os.Remove(file)
		return
	}
	d := &dnsSys
	d.path, d.original, d.have = path, []byte(saved.Original), true
	dnsSetHolds(true)
	logInfo("dns", "found the network's own DNS settings an earlier run kept aside")
}

func dnsSave(stateDir string) error {
	if stateDir == "" {
		return nil // kept in memory only: a clean quit still puts it back
	}
	data, err := json.Marshal(dnsResolvSaved{Path: dnsSys.path, Original: string(dnsSys.original)})
	if err != nil {
		return err
	}
	return vpndSyncWrite(filepath.Join(stateDir, dnsBackupFile), data)
}

// dnsForget drops the copy: the network's own is in place.
func dnsForget(stateDir string) {
	d := &dnsSys
	d.path, d.original, d.have, d.written = "", nil, false, nil
	dnsSetHolds(false)
	if stateDir != "" {
		_ = os.Remove(filepath.Join(stateDir, dnsBackupFile))
	}
}

func dnsWriteErr(err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return "changing the system's DNS needs JukaHub to run as root"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "the system has no DNS settings file yet"
	}
	return "resolv.conf cannot be written here"
}

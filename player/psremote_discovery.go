package main

// psremote_discovery.go — find consoles on the LAN without a native helper.
//
// Discovery is the one part of Remote Play that needs no crypto: a UDP
// "SRCH * HTTP/1.1" packet to the console's discovery port, answered with an
// HTTP response carrying the console's name, system version and whether it is
// ready or in rest mode. chiaki's own constants describe both protocols:
// PS4 listens on 987 with version 00020020, PS5 on 9302 with 00030010.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// psDiscovered is one console answered by a scan.
type psDiscovered struct {
	Host       string
	Name       string
	State      string // "ready", "standby" or "unknown"
	Target     int
	PS5        bool
	RunningApp string
	LastSeen   time.Time
}

// psDiscoveryEndpoint is one protocol to probe.
type psDiscoveryEndpoint struct {
	Port    int
	Version string
}

var psDiscoveryEndpoints = []psDiscoveryEndpoint{
	{Port: psRemotePortPS4, Version: psProtoVersionPS4},
	{Port: psRemotePortPS5, Version: psProtoVersionPS5},
}

var (
	// Test seams: a scan sends to these addresses instead of broadcasting, and
	// these ports instead of the fixed ones.
	psDiscoveryTargetsOverride []string
	psDiscoveryEndpointsTest   []psDiscoveryEndpoint
)

// psDiscoveryPacket is the exact SRCH body. chiaki sends the trailing NUL byte
// as part of the datagram, and consoles are known to ignore a packet without it.
func psDiscoveryPacket(version string) []byte {
	return []byte("SRCH * HTTP/1.1\ndevice-discovery-protocol-version:" + version + "\n\x00")
}

// psDiscoveryAddresses lists where a scan should be sent. Broadcasting to
// 255.255.255.255 covers the common case; each interface's directed broadcast
// also reaches consoles on networks that drop the limited broadcast.
func psDiscoveryAddresses(port int) []string {
	if len(psDiscoveryTargetsOverride) > 0 {
		out := make([]string, 0, len(psDiscoveryTargetsOverride))
		for _, target := range psDiscoveryTargetsOverride {
			if _, _, err := net.SplitHostPort(target); err == nil {
				out = append(out, target)
				continue
			}
			out = append(out, net.JoinHostPort(target, strconv.Itoa(port)))
		}
		return out
	}
	out := []string{net.JoinHostPort("255.255.255.255", strconv.Itoa(port))}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			broadcast := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				broadcast[i] = ipNet.IP.To4()[i] | ^ipNet.Mask[i]
			}
			out = append(out, net.JoinHostPort(broadcast.String(), strconv.Itoa(port)))
		}
	}
	return out
}

// psParseDiscoveryResponse turns one datagram into a console, or reports false
// when it is not an answer JukaHub understands.
func psParseDiscoveryResponse(payload []byte, from net.Addr) (psDiscovered, bool) {
	text := strings.ReplaceAll(string(payload), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return psDiscovered{}, false
	}
	status := strings.TrimSpace(lines[0])
	fields := strings.Fields(status)
	if len(fields) < 2 || !strings.HasPrefix(strings.ToUpper(fields[0]), "HTTP/") {
		return psDiscovered{}, false
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return psDiscovered{}, false
	}
	state := "unknown"
	switch code {
	case 200:
		state = "ready"
	case 620:
		state = "standby"
	}

	headers := map[string]string{}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		headers[strings.ToLower(strings.TrimSpace(line[:idx]))] = strings.TrimSpace(line[idx+1:])
	}

	host := ""
	if from != nil {
		if hostPart, _, err := net.SplitHostPort(from.String()); err == nil {
			host = hostPart
		} else {
			host = from.String()
		}
	}
	if host == "" {
		host = headers["host-addr"]
	}
	if host == "" {
		return psDiscovered{}, false
	}

	ps5 := headers["device-discovery-protocol-version"] == psProtoVersionPS5
	return psDiscovered{
		Host:       host,
		Name:       headers["host-name"],
		State:      state,
		Target:     psTargetFromSystemVersion(headers["system-version"], ps5),
		PS5:        ps5,
		RunningApp: headers["running-app-name"],
		LastSeen:   time.Now(),
	}, true
}

// psScanConsoles broadcasts a SRCH on both protocol ports and collects answers
// until the timeout expires. The context cancels an in-flight scan (the panel
// closes), and the returned consoles are de-duplicated by address.
func psScanConsoles(ctx context.Context, timeout time.Duration) ([]psDiscovered, error) {
	if timeout <= 0 {
		timeout = 2500 * time.Millisecond
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return nil, fmt.Errorf("open discovery socket: %w", err)
	}
	defer conn.Close()

	endpoints := psDiscoveryEndpoints
	if len(psDiscoveryEndpointsTest) > 0 {
		endpoints = psDiscoveryEndpointsTest
	}
	for _, endpoint := range endpoints {
		payload := psDiscoveryPacket(endpoint.Version)
		for _, address := range psDiscoveryAddresses(endpoint.Port) {
			addr, err := net.ResolveUDPAddr("udp4", address)
			if err != nil {
				continue
			}
			_, _ = conn.WriteToUDP(payload, addr)
		}
	}

	deadline := time.Now().Add(timeout)
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
	}
	seen := map[string]psDiscovered{}
	buf := make([]byte, 2048)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				break
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				break
			}
			if ctx != nil && ctx.Err() != nil {
				break
			}
			break
		}
		console, ok := psParseDiscoveryResponse(buf[:n], from)
		if !ok {
			continue
		}
		if existing, ok := seen[console.Host]; ok {
			// Prefer a ready answer over a standby one from the same address,
			// and keep the richer name if a protocol returned one.
			if existing.State != "ready" && console.State == "ready" {
				seen[console.Host] = console
			}
			continue
		}
		seen[console.Host] = console
	}
	out := make([]psDiscovered, 0, len(seen))
	for _, console := range seen {
		out = append(out, console)
	}
	sortDiscovered(out)
	return out, nil
}

// sortDiscovered puts ready consoles first, then by name, then by address.
func sortDiscovered(consoles []psDiscovered) {
	rank := func(state string) int {
		if state == "ready" {
			return 0
		}
		if state == "standby" {
			return 1
		}
		return 2
	}
	for i := 1; i < len(consoles); i++ {
		for j := i; j > 0; j-- {
			a, b := consoles[j-1], consoles[j]
			swap := false
			if rank(a.State) != rank(b.State) {
				swap = rank(a.State) > rank(b.State)
			} else if nameA, nameB := strings.ToLower(a.Name), strings.ToLower(b.Name); nameA != nameB {
				swap = nameA > nameB
			} else {
				swap = a.Host > b.Host
			}
			if !swap {
				break
			}
			consoles[j-1], consoles[j] = consoles[j], consoles[j-1]
		}
	}
}

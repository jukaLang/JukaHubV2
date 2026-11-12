package main

// companion_mdns.go — publish the Phone Companion's browser address on the
// current LAN. A DNS-SD service alone is not enough for a browser: it asks for
// an A record for jukahub.local, so the responder must answer that name too.

import (
	"fmt"
	"net"
	"time"

	"github.com/hashicorp/mdns"
)

const companionLocalHost = "jukahub.local"

// companionMDNSRecords answers both jukahub.local A queries from browsers and
// _http._tcp service queries from device discovery.
func companionMDNSRecords(ip string, port int) (*mdns.MDNSService, error) {
	addr := net.ParseIP(ip).To4()
	if addr == nil {
		return nil, fmt.Errorf("no IPv4 address for %s", companionLocalHost)
	}
	return mdns.NewMDNSService("JukaHub", "_http._tcp", "local.", companionLocalHost+".", port, []net.IP{addr}, []string{"path=/"})
}

// companionMDNSService selects the one interface whose address is shown on
// screen. It never advertises the screenshot-mode stand-in address.
func companionMDNSService(ip string, port int) (*mdns.MDNSService, *net.Interface, error) {
	service, err := companionMDNSRecords(ip, port)
	if err != nil {
		return nil, nil, err
	}
	addr := net.ParseIP(ip).To4()
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil, err
	}
	for i := range ifaces {
		iface := &ifaces[i]
		if iface.Flags&(net.FlagUp|net.FlagMulticast) != net.FlagUp|net.FlagMulticast {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || !ipnet.IP.Equal(addr) {
				continue
			}
			return service, iface, nil
		}
	}
	return nil, nil, fmt.Errorf("no multicast interface for %s", ip)
}

func companionStartMDNS(ip string, port int) (*mdns.Server, error) {
	service, iface, err := companionMDNSService(ip, port)
	if err != nil {
		return nil, err
	}
	return mdns.NewServer(&mdns.Config{Zone: service, Iface: iface})
}

// refreshMDNS follows DHCP changes and stops advertising when Wi-Fi drops.
// Failure leaves the IP link available and is retried after a short pause.
func (s *companionServer) refreshMDNS(ip string) bool {
	if shotModeOn() {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	if s.mdnsIP == ip {
		if s.mdns != nil {
			s.mu.Unlock()
			return true
		}
		if now.Before(s.mdnsRetry) {
			s.mu.Unlock()
			return false
		}
	}
	old := s.mdns
	s.mdns = nil
	s.mdnsIP = ip
	s.mdnsRetry = now.Add(time.Minute)
	port := s.browserPort
	s.mu.Unlock()
	if old != nil {
		_ = old.Shutdown()
	}
	if ip == "" {
		return false
	}
	server, err := companionStartMDNS(ip, port)
	if err != nil {
		logWarn("companion", "local address unavailable: %v", err)
		return false
	}
	s.mu.Lock()
	if s.srv == nil || s.mdnsIP != ip {
		s.mu.Unlock()
		_ = server.Shutdown()
		return false
	}
	s.mdns = server
	s.mu.Unlock()
	return true
}

func (s *companionServer) mdnsReady(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mdns != nil && s.mdnsIP == ip
}

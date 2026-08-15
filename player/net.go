package main

import (
	"net"
	"net/http"
	"time"
)

// Shared HTTP clients with sensible timeouts for device networks.
var (
	httpClientLong = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
)

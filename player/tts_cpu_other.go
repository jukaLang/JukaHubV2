//go:build !linux

package main

// ttsPiperPinCPUs has no cores to share out here (tts_cpu_linux.go).
func ttsPiperPinCPUs(int) {}

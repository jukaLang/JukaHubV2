//go:build !linux

package main

func headphoneJackEvdev() bool { return false }

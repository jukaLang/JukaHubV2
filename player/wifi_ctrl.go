package main

// wifi_ctrl.go — the supplicant's own control socket.
//
// wpa_cli is a thin client of a UNIX datagram protocol: send one command to
// <ctrl_interface>/<iface> from a socket of its own and read the reply. Some firmware images ship the
// supplicant without that client — TrimUI's TG5040 images (the Brick and the
// Smart Pro) run `ctrl_interface=/etc/wifi/sockets` and their Wi-Fi menus drive
// the supplicant directly — and there the picker used to vanish, because the
// start menu drops the Wi-Fi row whenever wifiControlAvailable() is false.
//
// Speaking the protocol is the same set of commands the CLI would send, so the
// parsers and the validation in wifi.go stay the only authority on what is
// asked; this file only changes the transport. It also costs no fork per poll,
// which the status bar's signal read does every interval.
//
// The commands are never built from untrusted text here either: the caller
// passes the same arguments it would pass to wpa_cli, and the passphrase still
// travels as part of a quoted `set_network ... psk "<pass>"` command, never as
// an argument of a process this app starts.

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// wpaCtrlMaxReply bounds one reply. The supplicant answers each command
	// with a single datagram (4 KiB on current builds, truncated rather than
	// streamed); a larger buffer costs nothing and keeps a build with a bigger
	// reply size from having its scan list cut short here.
	wpaCtrlMaxReply = 16 << 10
	// wpaCtrlMaxEvents bounds how many unsolicited event messages are stepped
	// over while waiting for a reply. Events are only written to attached
	// clients and this one never attaches, so one is already unusual.
	wpaCtrlMaxEvents = 3
)

// wpaCtrlSeq numbers this process's client sockets, like wpa_ctrl_open's
// counter, so two requests in flight never bind the same path.
var wpaCtrlSeq atomic.Uint64

// wpaCtrlConn is one client socket; Close also unlinks the path it bound.
type wpaCtrlConn struct {
	*net.UnixConn
	local string
}

func (c *wpaCtrlConn) Close() error {
	err := c.UnixConn.Close()
	_ = os.Remove(c.local)
	return err
}

// wpaCtrlOpen dials one control socket. It is a variable so a test can answer
// with a pipe instead of a supplicant, exactly like wifiRunner is for the CLI.
//
// The control interface is a datagram socket that replies to the address a
// request came from, so the client binds a path of its own first — what
// wpa_ctrl_open does with /tmp/wpa_ctrl_<pid>-<n>. The old stream dial could
// never reach it (a SOCK_STREAM connect to a SOCK_DGRAM socket fails every
// time), which left the whole socket backend dead on the images it exists for.
var wpaCtrlOpen = func(path string, timeout time.Duration) (io.ReadWriteCloser, error) {
	local := filepath.Join(os.TempDir(), fmt.Sprintf("jukahub_wpa_%d-%d", os.Getpid(), wpaCtrlSeq.Add(1)))
	_ = os.Remove(local) // a leftover from a crashed run with a recycled pid
	conn, err := net.DialUnix("unixgram",
		&net.UnixAddr{Name: local, Net: "unixgram"},
		&net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		_ = os.Remove(local)
		return nil, err
	}
	c := &wpaCtrlConn{UnixConn: conn, local: local}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// wpaCtrlWireCommand turns a wpa_cli-style command line into what the socket
// expects. wpa_cli upper-cases the verb itself ("set_network" is sent as
// SET_NETWORK) and sends no line ending; the supplicant compares the verb
// case-sensitively and does not strip a newline, so "status\n" was answered
// "UNKNOWN COMMAND". Arguments — an SSID in hex, a quoted passphrase — are
// passed through untouched.
func wpaCtrlWireCommand(line string) string {
	line = strings.TrimSpace(line)
	verb, rest, hasArgs := strings.Cut(line, " ")
	verb = strings.ToUpper(verb)
	if !hasArgs {
		return verb
	}
	return verb + " " + rest
}

// wpaCtrlSocketPath is the control socket of one interface inside a directory.
func wpaCtrlSocketPath(dir, iface string) string {
	if dir == "" || iface == "" {
		return ""
	}
	return filepath.Join(dir, iface)
}

// wpaCtrlSocketExists reports whether the socket for this interface is there.
// A variable so the availability probe can be answered without a supplicant.
var wpaCtrlSocketExists = func(dir, iface string) bool {
	path := wpaCtrlSocketPath(dir, iface)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// wpaCtrlRequest sends one command and returns the supplicant's reply.
//
// One datagram out and one back is the whole protocol: the supplicant answers
// each command with a single message. A message starting with "<" is an unsolicited
// event (the same test wpa_ctrl_request makes), so it is stepped over rather
// than returned as the answer.
func wpaCtrlRequest(path, command string, timeout time.Duration) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", errors.New("empty supplicant command")
	}
	conn, err := wpaCtrlOpen(path, timeout)
	if err != nil {
		return "", fmt.Errorf("wpa_supplicant socket %s: %w", path, err)
	}
	defer conn.Close()

	if _, err := io.WriteString(conn, wpaCtrlWireCommand(command)); err != nil {
		return "", fmt.Errorf("wpa_supplicant socket %s: %w", path, err)
	}

	buf := make([]byte, wpaCtrlMaxReply)
	events := 0
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			reply := string(buf[:n])
			if strings.HasPrefix(reply, "<") {
				events++
				if events > wpaCtrlMaxEvents {
					return "", fmt.Errorf("wpa_supplicant sent only events while answering %q", command)
				}
				continue
			}
			// The reply is NUL- or newline-terminated depending on the build;
			// the parsers trim what they see, but a trailing NUL would read as
			// part of a value.
			return strings.TrimRight(reply, "\x00\n"), nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", fmt.Errorf("wpa_supplicant closed the socket without answering %q", command)
			}
			return "", fmt.Errorf("wpa_supplicant socket %s: %w", path, err)
		}
	}
}

// wifiSocketScript runs a command script against the supplicant socket. One
// line per command, as for wpa_cli's stdin mode (wpaCtrlRequest puts each one
// in wire form), and the replies are joined in order so the callers' FAIL
// detection reads them unchanged.
func wifiSocketScript(dir, iface, script string, args ...string) (string, error) {
	path := wpaCtrlSocketPath(dir, iface)
	if path == "" {
		return "", errors.New(wifiUnavailableReason())
	}
	commands := make([]string, 0, 4)
	for _, line := range strings.Split(script, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			commands = append(commands, line)
		}
	}
	if len(args) > 0 {
		commands = append(commands, strings.Join(args, " "))
	}
	if len(commands) == 0 {
		return "", errors.New("no supplicant command to run")
	}

	var out strings.Builder
	for _, command := range commands {
		reply, err := wpaCtrlRequest(path, command, wifiCommandTimeout)
		if reply != "" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(reply)
		}
		if err != nil {
			// Only a transport failure stops the script. A command the
			// supplicant refused arrives as a printed "FAIL" and the script
			// keeps going, exactly as wpa_cli's stdin mode does; the caller's
			// wifiCommandError() reads the FAIL out of the joined output.
			return out.String(), err
		}
	}
	return out.String(), nil
}

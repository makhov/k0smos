package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/amakhov/k0smos/internal/control"
)

// The requests a node answers out of plain file and network work, rather than
// from state PID 1 holds. Here rather than beside the rest of answerRequest's
// cases so they can be tested on any platform: nothing below is Linux-specific,
// and a diagnostic channel that is only exercised on the machine it diagnoses is
// the wrong way round.

// maxServedFile bounds a file served to the host, below the reply framing's own
// cap so an oversized file is reported rather than truncated.
const maxServedFile = 4 << 20

// dialTimeout bounds a dial probe. Short, because the answer "it did not
// connect within five seconds" is the same answer as "it never will" to anyone
// debugging a node that cannot join, and a longer wait on a serial console
// cannot be interrupted.
const dialTimeout = 5 * time.Second

// readForHost serves one file to the host. This is how pod logs, the rendered
// k0s config and /run state are reached on a machine with no shell.
//
// Refuses a directory rather than returning its raw bytes, and refuses anything
// too large for the reply framing so the failure names the size instead of
// arriving as a truncated file.
func readForHost(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("no path given; use %q", control.RequestCat+" /path")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute: %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory; use %q", path, control.RequestLs+" "+path)
	}
	if info.Size() > maxServedFile {
		return nil, fmt.Errorf("%s is %d bytes, over the %d-byte limit for a reply",
			path, info.Size(), int64(maxServedFile))
	}
	return os.ReadFile(path)
}

// listForHost serves one directory listing to the host, as mode, size and name
// per line, with a symlink's target and a trailing slash on a directory.
//
// Without this every path has to be guessed, which is the difference between
// reading a machine and searching one. The target of a symlink is part of the
// answer, not decoration: most of what /sys says about a device it says by
// pointing somewhere.
func listForHost(path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("no path given; use %q", control.RequestLs+" /path")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute: %q", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for i, e := range entries {
		if b.Len() > maxServedFile {
			fmt.Fprintf(&b, "... %d more entries\n", len(entries)-i)
			break
		}
		name := e.Name()
		// Info can fail for an entry that vanished since the read -- a process
		// under /proc is the normal case -- which is worth reporting as a name
		// without detail rather than failing the whole listing.
		info, err := e.Info()
		if err != nil {
			fmt.Fprintf(&b, "%-10s %10s %s\n", "?", "?", name)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if target, err := os.Readlink(filepath.Join(path, name)); err == nil {
				name += " -> " + target
			}
		} else if e.IsDir() {
			name += "/"
		}
		fmt.Fprintf(&b, "%-10s %10d %s\n", modeString(info.Mode()), info.Size(), name)
	}
	return []byte(b.String()), nil
}

// modeString renders a mode the way ls(1) does.
//
// Go spells a symlink "L", a socket "S" and a device "D" or "Dc", which is
// unambiguous but a column wider for character devices and reads as damage to
// anyone who knows ls. "What did the kernel actually create in /dev" is one of
// the questions this request exists for, so block and character devices have to
// be told apart at a glance. Anything else keeps Go's spelling.
func modeString(m os.FileMode) string {
	s := m.String()
	switch {
	case m&os.ModeSymlink != 0:
		return "l" + s[1:]
	case m&os.ModeSocket != 0:
		return "s" + s[1:]
	case m&os.ModeCharDevice != 0:
		return "c" + s[2:] // "Dc" is two letters, not one
	case m&os.ModeDevice != 0:
		return "b" + s[1:]
	}
	return s
}

// dialForHost opens one TCP connection and reports what happened.
//
// The only request that answers "can this machine reach that". Routes, neighbour
// and socket tables say what the node already knows; none of them settle whether
// a connection would be made, which is the question behind nearly every node
// that will not join a cluster. The error is returned as the resolver or the
// stack worded it, because that wording is what separates a name that does not
// resolve from a port that is filtered from one that is refused.
//
// Connects and closes, sending nothing: the request is a reachability probe, and
// a debug channel should not become a way to deliver a payload somewhere.
func dialForHost(addr string) ([]byte, error) {
	if addr == "" {
		return nil, fmt.Errorf("no address given; use %q", control.RequestDial+" host:port")
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("%q is not host:port", addr)
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return []byte(fmt.Sprintf("connected %s -> %s in %s\n",
		conn.LocalAddr(), conn.RemoteAddr(), time.Since(start).Round(time.Millisecond))), nil
}

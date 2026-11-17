package main

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/amakhov/k0smos/internal/control"
)

// debugMaxReply bounds one console reply. A node's log can be megabytes, and a
// serial line is often the slowest thing attached to the machine: an unbounded
// dmesg at 115200 baud takes minutes that cannot be interrupted, which is worse
// than no answer at all. The tail is kept rather than the head, because what a
// node did last is why anyone is reading.
const debugMaxReply = 64 << 10

// serveDebug answers requests read from r by writing plain text to w, returning
// when r ends.
//
// This is the console counterpart of the control port, deliberately not the same
// protocol. Control-port replies are length-prefixed, which works only because a
// virtio-serial port carries nothing else; the console also carries the kernel's
// printk and init's own log lines, so a reply counted in bytes would be
// corrupted by the next kernel message. A person reading a serial console reads
// past an interleaved line without trouble, so the reply is plain text.
//
// An empty line prints the usage: someone who opens a console long after boot
// has no banner left on screen, and pressing enter is what they will try.
//
// Unauthenticated by construction -- it reads any path and issues join tokens --
// which is why the caller only starts it when asked and says so on the console.
func serveDebug(r io.Reader, w io.Writer, respond control.Responder) {
	usage := "k0smos debug: " + strings.Join(control.Requests, " ") + " (cat takes an absolute path)"
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line == "help" {
			fmt.Fprintln(w, usage)
			continue
		}
		data, err := respond(line)
		if err != nil {
			// One line: a console reader has no framing to tell an error from a
			// payload, so an error must not look like one.
			fmt.Fprintf(w, "k0smos debug: %s\n", strings.ReplaceAll(err.Error(), "\n", " "))
			continue
		}
		if len(data) > debugMaxReply {
			fmt.Fprintf(w, "k0smos debug: last %d of %d bytes\n", debugMaxReply, len(data))
			data = data[len(data)-debugMaxReply:]
		}
		w.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			fmt.Fprintln(w)
		}
	}
}

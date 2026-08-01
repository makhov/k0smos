// Package control reads shutdown requests from a host-facing control channel.
//
// This exists because there is otherwise no way to stop a k0smos guest
// cleanly. On arm64 QEMU with direct kernel boot there is no UEFI and so no
// ACPI, meaning `system_powerdown` has nothing to deliver; the DT does expose a
// gpio-keys poweroff button, but Alpine's virt kernel builds neither
// CONFIG_GPIO_KEYS nor CONFIG_INPUT_KEYBOARD, so it cannot reach userspace.
// Killing QEMU instead corrupts the root filesystem.
//
// A virtio-serial port sidesteps all of that: CONFIG_VIRTIO_CONSOLE is built
// into every stock kernel, so no module is needed, and the host can write to it
// at any time.
package control

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Command is a shutdown request from the host.
type Command int

const (
	// PowerOff asks for a clean shutdown ending in power off.
	PowerOff Command = iota + 1
	// Reboot asks for a clean shutdown ending in a restart.
	Reboot
)

func (c Command) String() string {
	switch c {
	case PowerOff:
		return "poweroff"
	case Reboot:
		return "reboot"
	}
	return "unknown"
}

// RequestKubeconfig asks the node to send back its admin kubeconfig.
//
// This exists so getting at a cluster does not require reading the guest's disk
// offline with debugfs, which needed the machine shut down first and, on macOS,
// a Docker container to supply e2fsprogs.
//
// Anything able to write to this port therefore obtains cluster-admin. That is
// not a new exposure: the port is the hypervisor's channel into the guest, and
// whoever holds it can already stop the machine and read its disk. It does mean
// the port must not be exposed anywhere the disk is not equally exposed.
const RequestKubeconfig = "kubeconfig"

// RequestToken asks the node to mint a k0s join token, so another machine can
// join the cluster this one started. It takes a role: "token controller" or
// "token worker".
//
// A join token is how every k0s cluster grows, and it can only be produced by a
// machine that already has the cluster's CA — so it has to come from inside the
// guest. Cluster API providers do the same thing over SSH; here the control port
// stands in for that, which is what lets a node with no shell still be joined
// to.
//
// It carries the same exposure as RequestKubeconfig: a controller token confers
// control-plane membership. The port is already the channel that can stop the
// machine and read its disk, so this grants nothing new to whoever holds it.
const RequestToken = "token"

// requests are the data requests a node answers. The verb is the first field of
// the line; anything after it is an argument.
var requests = []string{RequestKubeconfig, RequestToken}

// isRequest reports whether a line asks for data rather than a power command.
func isRequest(line string) bool {
	verb, _, _ := strings.Cut(line, " ")
	return slices.Contains(requests, verb)
}

// Reply framing. A request's answer is a status line, and for a successful data
// reply exactly that many bytes follow it. Length-prefixed rather than delimited
// because a kubeconfig is arbitrary text that may contain any delimiter.
const (
	ReplyOK    = "k0smos-ok"
	ReplyError = "k0smos-error"
)

// Responder answers a data request. Returning an error sends that error to the
// host instead of a payload.
type Responder func(request string) ([]byte, error)

// Parse interprets one line from the control channel. It reports false for
// anything it does not recognise; the channel is host-facing, so unknown input
// is ignored rather than acted on.
func Parse(line string) (Command, bool) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "poweroff":
		return PowerOff, true
	case "reboot", "restart":
		return Reboot, true
	}
	return 0, false
}

// WatchReopen reads commands from a port that is reopened whenever it reaches
// EOF or fails to open, waiting retry between attempts. The returned channel is
// closed only when ctx is done.
//
// This is what a virtio-serial port actually needs: with no host client
// attached the port reads EOF straight away, so a single Watch would end
// before anyone could ever send a command.
func WatchReopen(ctx context.Context, open func() (io.ReadWriteCloser, error), retry time.Duration, respond Responder) <-chan Command {
	out := make(chan Command)
	go func() {
		defer close(out)
		for ctx.Err() == nil {
			if !readOnce(ctx, open, out, respond) {
				// Nothing was read; pause so a permanently failing port cannot
				// spin the CPU for the lifetime of the machine.
				select {
				case <-ctx.Done():
					return
				case <-time.After(retry):
				}
			}
		}
	}()
	return out
}

// readOnce opens the port and forwards commands until it ends, reporting
// whether anything was forwarded.
func readOnce(ctx context.Context, open func() (io.ReadWriteCloser, error), out chan<- Command, respond Responder) bool {
	rc, err := open()
	if err != nil {
		return false
	}
	defer rc.Close()

	// Requests are answered here rather than forwarded, because the answer goes
	// back down this same port and the caller has no handle on it.
	forwarded := false
	for ev := range watchEvents(ctx, rc) {
		if ev.request != "" {
			serveRequest(rc, ev.request, respond)
			forwarded = true // the port is alive; do not back off
			continue
		}
		select {
		case out <- ev.command:
			forwarded = true
		case <-ctx.Done():
			return forwarded
		}
	}
	return forwarded
}

// serveRequest writes the reply for one request.
func serveRequest(w io.Writer, request string, respond Responder) {
	if respond == nil {
		fmt.Fprintf(w, "%s unsupported request %q\n", ReplyError, request)
		return
	}
	data, err := respond(request)
	if err != nil {
		// One line, so a multi-line error cannot be mistaken for payload.
		fmt.Fprintf(w, "%s %s\n", ReplyError, strings.ReplaceAll(err.Error(), "\n", " "))
		return
	}
	fmt.Fprintf(w, "%s %d\n", ReplyOK, len(data))
	w.Write(data)
}

// Request performs one request over an already-connected control channel and
// returns the payload. Used by the host side, and shared with the node so both
// ends agree on the framing by construction.
func Request(rw io.ReadWriter, name string) ([]byte, error) {
	if _, err := fmt.Fprintf(rw, "%s\n", name); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	r := bufio.NewReader(rw)
	status, err := r.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read reply: %w", err)
	}
	status = strings.TrimRight(status, "\r\n")

	kind, rest, _ := strings.Cut(status, " ")
	switch kind {
	case ReplyError:
		return nil, errors.New(rest)
	case ReplyOK:
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("bad reply length %q", rest)
		}
		if n < 0 || n > maxReplyBytes {
			return nil, fmt.Errorf("reply of %d bytes is out of range", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, fmt.Errorf("read %d byte reply: %w", n, err)
		}
		return buf, nil
	default:
		return nil, fmt.Errorf("unexpected reply %q", status)
	}
}

// maxReplyBytes bounds what the host will allocate for a reply. A kubeconfig is a
// few kilobytes; this only stops a wrong or hostile length from exhausting memory.
const maxReplyBytes = 8 << 20

// event is one line off the port: either a shutdown command or a data request.
type event struct {
	command Command
	request string
}

// watchEvents reads the port until EOF or ctx is done. Requests are reported
// alongside commands so the caller can answer them on the same connection.
func watchEvents(ctx context.Context, r io.Reader) <-chan event {
	out := make(chan event)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := strings.ToLower(strings.TrimSpace(scanner.Text()))
			var ev event
			switch {
			case isRequest(line):
				ev = event{request: line}
			default:
				cmd, ok := Parse(line)
				if !ok {
					continue // a stray byte must not disable shutdown
				}
				ev = event{command: cmd}
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// Watch reads commands from r until EOF or ctx is done, and closes the returned
// channel when it stops. Unrecognised lines are skipped: one stray byte must
// not disable shutdown for the rest of the boot.
//
// The scan runs in its own goroutine so that cancelling ctx closes the returned
// channel immediately. A read already parked in the kernel cannot be
// interrupted, so that inner goroutine may stay blocked until the port yields;
// it holds nothing but the reader, and ctx is only cancelled on the way down.
func Watch(ctx context.Context, r io.Reader) <-chan Command {
	lines := make(chan Command)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			cmd, ok := Parse(scanner.Text())
			if !ok {
				continue
			}
			select {
			case lines <- cmd:
			case <-ctx.Done():
				return
			}
		}
	}()

	out := make(chan Command)
	go func() {
		defer close(out)
		for {
			select {
			case cmd, ok := <-lines:
				if !ok {
					return
				}
				select {
				case out <- cmd:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

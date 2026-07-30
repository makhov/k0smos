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
	"io"
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
func WatchReopen(ctx context.Context, open func() (io.ReadCloser, error), retry time.Duration) <-chan Command {
	out := make(chan Command)
	go func() {
		defer close(out)
		for ctx.Err() == nil {
			if !readOnce(ctx, open, out) {
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
func readOnce(ctx context.Context, open func() (io.ReadCloser, error), out chan<- Command) bool {
	rc, err := open()
	if err != nil {
		return false
	}
	defer rc.Close()

	forwarded := false
	for cmd := range Watch(ctx, rc) {
		select {
		case out <- cmd:
			forwarded = true
		case <-ctx.Done():
			return forwarded
		}
	}
	return forwarded
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

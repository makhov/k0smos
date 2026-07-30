// Package power watches for a hardware power-button press.
//
// This is how a machine gets stopped gracefully anywhere there is no hypervisor
// host to write to k0smos's control port: bare metal, `virsh shutdown`, or a
// cloud provider's "stop instance". The kernel's ACPI button driver turns the
// press into an input event, which evdev exposes as /dev/input/eventN, so both
// the `button` and `evdev` modules must be loaded (see internal/module).
//
// Note this cannot fire under QEMU's arm64 `virt` machine with direct kernel
// boot: there is no UEFI and hence no ACPI to raise the event. That
// configuration uses the virtio-serial control port instead (internal/control).
package power

import (
	"context"
	"encoding/binary"
	"io"
)

const (
	// struct input_event on a 64-bit kernel: two longs of timeval, then
	// __u16 type, __u16 code, __s32 value.
	eventSize = 24
	typeOff   = 16
	codeOff   = 18
	valueOff  = 20

	evKey    = 0x01 // EV_KEY
	keyPower = 116  // KEY_POWER
)

// Watch reports each power-button press read from r, and closes the returned
// channel at EOF or when ctx is done.
func Watch(ctx context.Context, r io.Reader) <-chan struct{} {
	out := make(chan struct{})
	go func() {
		defer close(out)
		buf := make([]byte, eventSize)
		for {
			// ReadFull so a record split across reads is reassembled rather
			// than misparsed; a trailing partial record ends the loop.
			if _, err := io.ReadFull(r, buf); err != nil {
				return
			}
			if !isPowerPress(buf) {
				continue
			}
			select {
			case out <- struct{}{}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// isPowerPress reports whether ev is a press (not a release or autorepeat) of
// the power key.
func isPowerPress(ev []byte) bool {
	if len(ev) < eventSize {
		return false
	}
	return binary.LittleEndian.Uint16(ev[typeOff:]) == evKey &&
		binary.LittleEndian.Uint16(ev[codeOff:]) == keyPower &&
		int32(binary.LittleEndian.Uint32(ev[valueOff:])) == 1
}

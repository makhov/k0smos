// Package grubenv reads and writes a GRUB environment block: the file GRUB's
// load_env reads, in the format grub-editenv writes.
package grubenv

import (
	"bytes"
	"fmt"
	"strings"
)

const (
	header = "# GRUB Environment Block\n"
	// Size is a block's fixed length; grub-editenv pads to it with '#'.
	Size = 1024
)

// Encode returns a block holding key=value. GRUB escapes a backslash and a
// newline with a backslash; a newline is refused instead, since nothing that
// belongs on a kernel command line has one.
func Encode(key, value string) ([]byte, error) {
	if strings.ContainsAny(value, "\n\r") {
		return nil, fmt.Errorf("%s: a newline cannot be in a GRUB variable", key)
	}
	line := key + "=" + strings.ReplaceAll(value, `\`, `\\`) + "\n"
	if len(header)+len(line) > Size {
		return nil, fmt.Errorf("%s: %d bytes, over the %d a GRUB environment block holds", key, len(value), Size-len(header)-len(key)-2)
	}
	b := []byte(header + line)
	return append(b, bytes.Repeat([]byte("#"), Size-len(b))...), nil
}

// Get returns key's value from a block, and whether it was there.
func Get(block []byte, key string) (string, bool) {
	if !bytes.HasPrefix(block, []byte(header)) {
		return "", false
	}
	for line := range strings.Lines(string(block[len(header):])) {
		if v, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), key+"="); ok {
			return strings.ReplaceAll(v, `\\`, `\`), true
		}
	}
	return "", false
}

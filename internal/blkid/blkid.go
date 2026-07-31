// Package blkid resolves UUID=/LABEL= root specifications to device paths.
//
// It reads ext4 superblocks directly rather than looking in /dev/disk/by-uuid,
// because those symlinks are created by udev and k0smos runs no udev. Device
// nodes themselves do exist: devtmpfs creates them for every block device the
// kernel knows about.
//
// Naming a root by UUID or label matters on real hardware, where disks
// enumerate as /dev/sda or /dev/nvme0n1 and can change order between boots, so
// a hard-coded path is not dependable.
package blkid

import (
	"fmt"
	"strings"
)

const (
	// The ext2/3/4 superblock lives 1024 bytes into the device; these offsets
	// are relative to its start. See struct ext2_super_block.
	sbOffset   = 1024
	sbMagicOff = 0x38 // s_magic
	sbUUIDOff  = 0x68 // s_uuid[16]
	sbLabelOff = 0x78 // s_volume_name[16]

	ext4Magic = 0xEF53

	// sbLen is how much of the superblock must be read to cover all of the
	// fields above.
	sbLen = 256
)

// Prober is the subset of *sys.Sys that probing block devices needs.
type Prober interface {
	// BlockDevices lists kernel block device names, e.g. ["vda", "vda1"].
	BlockDevices() ([]string, error)
	// ReadAt fills p from /dev/<dev> at the given offset.
	ReadAt(dev string, p []byte, off int64) error
}

// Resolve turns spec into a device path. "UUID=..." and "LABEL=..." are looked
// up by scanning block devices; anything else is returned unchanged, so a plain
// path like /dev/vda still works.
func Resolve(p Prober, spec string) (string, error) {
	kind, want, found := strings.Cut(spec, "=")
	if !found {
		return spec, nil
	}
	byUUID := false
	switch strings.ToUpper(kind) {
	case "UUID":
		byUUID = true
	case "LABEL":
	default:
		return spec, nil // e.g. an unrecognised prefix; treat as a path
	}

	devs, err := p.BlockDevices()
	if err != nil {
		return "", fmt.Errorf("list block devices: %w", err)
	}
	for _, dev := range devs {
		uuid, label, ok := identify(p, dev)
		if !ok {
			continue // no filesystem we recognise
		}
		got := label
		if byUUID {
			got = uuid
		}
		// UUIDs are hex and may be written in either case; labels are compared
		// literally because they are user-chosen text.
		if got == want || (byUUID && strings.EqualFold(got, want)) {
			return "/dev/" + dev, nil
		}
	}
	return "", fmt.Errorf("no block device matches %s", spec)
}

// ResolveWait is Resolve with retries, for use at boot: virtio_blk and friends
// probe asynchronously, so the root device may appear slightly after its module
// is loaded. sleep is called between attempts. A plain device path returns
// immediately without sleeping.
func ResolveWait(p Prober, spec string, attempts int, sleep func()) (string, error) {
	var err error
	for range attempts {
		var dev string
		if dev, err = Resolve(p, spec); err == nil {
			return dev, nil
		}
		sleep()
	}
	return "", err
}

// Identify reports the filesystem on a device, or ok=false when there is none.
//
// A false result is what marks a device as blank and therefore safe to format:
// callers must never format something this reports a filesystem for.
func Identify(p Prober, dev string) (Info, bool) {
	for _, pr := range probers {
		buf := make([]byte, pr.length)
		if err := p.ReadAt(dev, buf, pr.offset); err != nil {
			continue
		}
		if uuid, label, ok := pr.parse(buf); ok {
			return Info{FSType: pr.name, UUID: uuid, Label: label}, true
		}
	}
	return Info{}, false
}

// virtualPrefixes are kernel pseudo block devices. They are permanently blank
// and never a data volume, so they must not be candidates for formatting.
//
// This matters more than it sounds: a monolithic kernel that builds in loop and
// brd presents 24 of them, which turned "the one blank device" into "refusing to
// guess between 25". A modular kernel simply never instantiates them.
var virtualPrefixes = []string{"loop", "ram", "zram", "dm-", "md", "nbd", "fd", "sr"}

func isVirtual(dev string) bool {
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(dev, p) {
			return true
		}
	}
	return false
}

// Blank lists the devices with no recognised filesystem, excluding kernel
// pseudo-devices. Partitions are included because a caller may legitimately
// target one.
func Blank(p Prober) ([]string, error) {
	devs, err := p.BlockDevices()
	if err != nil {
		return nil, fmt.Errorf("list block devices: %w", err)
	}
	var out []string
	for _, dev := range devs {
		if isVirtual(dev) {
			continue
		}
		if _, ok := Identify(p, dev); !ok {
			out = append(out, dev)
		}
	}
	return out, nil
}

// identify tries each known filesystem on dev, returning the first match. A
// device that cannot be read is not an error: an empty drive or one too small
// for a given probe simply is not the thing being looked for.
func identify(p Prober, dev string) (uuid, label string, ok bool) {
	for _, pr := range probers {
		buf := make([]byte, pr.length)
		if err := p.ReadAt(dev, buf, pr.offset); err != nil {
			continue
		}
		if uuid, label, ok := pr.parse(buf); ok {
			return uuid, label, true
		}
	}
	return "", "", false
}

// formatUUID renders 16 raw bytes in the canonical 8-4-4-4-12 form.
func formatUUID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func trimNUL(b []byte) []byte {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		return b[:i]
	}
	return b
}

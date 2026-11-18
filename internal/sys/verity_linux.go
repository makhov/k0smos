package sys

import (
	"encoding/binary"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The device-mapper ioctl interface, from linux/dm-ioctl.h. k0smos talks to it
// directly because the image has no veritysetup and no shell to run one from --
// and because all it takes is two structs laid out by hand.
const (
	dmControl = "/dev/mapper/control"
	dmDir     = "/dev/mapper"

	dmVersionMajor = 4   // the interface version this speaks; the kernel rejects others
	dmIoctlLen     = 312 // sizeof(struct dm_ioctl)
	dmTargetLen    = 40  // sizeof(struct dm_target_spec)
	dmNameOff      = 48  // offsetof(struct dm_ioctl, name)
	dmNameLen      = 128 // DM_NAME_LEN
	dmTypeLen      = 16  // DM_MAX_TYPE_NAME
	dmReadonly     = 1   // DM_READONLY_FLAG

	// _IOWR(0xfd, cmd, struct dm_ioctl) for the three calls it takes to put a
	// mapped device into service: create it, give it a table, resume it.
	dmDevCreate  = 0xc138fd03
	dmTableLoad  = 0xc138fd09
	dmDevSuspend = 0xc138fd06
)

// dmHeader fills struct dm_ioctl at the front of buf. The offsets are written
// out rather than left to Go's struct packing, so what goes to the kernel is
// visible here and does not depend on how the fields are declared.
func dmHeader(buf []byte, flags uint32) {
	native := binary.NativeEndian
	native.PutUint32(buf[0:], dmVersionMajor)    // version[0]
	native.PutUint32(buf[12:], uint32(len(buf))) // data_size: the whole buffer
	native.PutUint32(buf[16:], dmIoctlLen)       // data_start: targets follow the header
	native.PutUint32(buf[28:], flags)
	copy(buf[dmNameOff:dmNameOff+dmNameLen-1], verityName)
}

func dmCall(ctl *os.File, cmd uintptr, buf []byte) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, ctl.Fd(), cmd, uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return errno
	}
	return nil
}

// VerityOpen maps dev through dm-verity and returns the device node of the
// result. A read from that node either matches the hash tree the image was
// built with or fails with an I/O error, so a root whose bytes have changed
// cannot be mounted at all.
//
// spec is the k0smos.verity= value from the kernel command line; see
// parseVeritySpec. Any failure here is returned rather than worked around: the
// caller must not fall back to the unverified device.
func (s *Sys) VerityOpen(dev, spec string) (string, error) {
	blocks, hash, err := parseVeritySpec(spec)
	if err != nil {
		return "", fmt.Errorf("k0smos.verity=%s: %w", spec, err)
	}
	ctl, err := os.OpenFile(dmControl, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("open %s (is dm_verity loaded?): %w", dmControl, err)
	}
	defer ctl.Close()

	create := make([]byte, dmIoctlLen)
	dmHeader(create, 0)
	if err := dmCall(ctl, dmDevCreate, create); err != nil {
		return "", fmt.Errorf("create %s: %w", verityName, err)
	}

	// One target covering the whole data area. The table is rejected here, by
	// dm-verity's constructor, if the kernel has no verity target or the hash
	// tree does not match the root hash.
	table := verityTable(dev, blocks, hash)
	params := (len(table) + 8) / 8 * 8 // NUL-terminated and 8-aligned, as the kernel expects
	load := make([]byte, dmIoctlLen+dmTargetLen+params)
	dmHeader(load, dmReadonly)
	native := binary.NativeEndian
	native.PutUint32(load[20:], 1) // target_count
	target := load[dmIoctlLen:]
	native.PutUint64(target[8:], blocks*verityBlockSize/512)    // length, in 512-byte sectors
	native.PutUint32(target[20:], uint32(len(load)-dmIoctlLen)) // next: end of this target
	copy(target[24:24+dmTypeLen], "verity")
	copy(target[dmTargetLen:], table)
	if err := dmCall(ctl, dmTableLoad, load); err != nil {
		return "", fmt.Errorf("load table %q: %w", table, err)
	}

	resume := make([]byte, dmIoctlLen)
	dmHeader(resume, 0)
	if err := dmCall(ctl, dmDevSuspend, resume); err != nil {
		return "", fmt.Errorf("resume %s: %w", verityName, err)
	}

	// There is no udev here to make /dev/mapper/<name>, so make it from the
	// device number the create call reported. Without the directory the mknod
	// below fails with a bare ENOENT, which says nothing on a node with no shell.
	if err := os.MkdirAll(dmDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dmDir, err)
	}
	major, minor := decodeDev(binary.NativeEndian.Uint64(create[40:]))
	node := dmDir + "/" + verityName
	_ = os.Remove(node)
	if err := unix.Mknod(node, unix.S_IFBLK|0600, int(unix.Mkdev(major, minor))); err != nil {
		return "", fmt.Errorf("mknod %s (%d:%d): %w", node, major, minor, err)
	}
	return node, nil
}

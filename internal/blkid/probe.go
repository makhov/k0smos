package blkid

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// Offsets for the filesystems k0smos has to recognise. ext4 is the root
// filesystem; iso9660 and FAT matter because cloud-init bootstrap data arrives
// as a NoCloud ISO (label "cidata") or an OpenStack config-drive (label
// "config-2"), and those are how CAPI hands a machine its configuration.
const (
	// iso9660: the primary volume descriptor sits at sector 16.
	isoPVDOffset = 32768
	isoLabelOff  = 40 // volume identifier, 32 bytes, space-padded

	// FAT boot sector fields differ between FAT32 and FAT12/16.
	fat32TypeOff   = 0x52
	fat32LabelOff  = 0x47
	fat32SerialOff = 0x43
	fat16TypeOff   = 0x36
	fat16LabelOff  = 0x2b
	fat16SerialOff = 0x27
)

// prober describes how to identify one filesystem: where to read, how much, and
// how to interpret it.
type prober struct {
	name   string
	offset int64
	length int
	parse  func([]byte) (uuid, label string, ok bool)
}

// Info describes the filesystem found on a device.
type Info struct {
	FSType string
	UUID   string
	Label  string
}

// probers is tried in order for each block device. ext4 first because it is the
// common case for the root filesystem.
var probers = []prober{
	{"ext4", sbOffset, sbLen, probeExt4},
	{"erofs", erofsSBOffset, erofsSBLen, probeEROFS},
	{"iso9660", 0, isoPVDOffset + 2048, probeISO9660},
	{"vfat", 0, 1024, probeFAT},
}

// probeExt4 reads an ext2/3/4 superblock. The buffer starts at the superblock.
func probeExt4(b []byte) (uuid, label string, ok bool) {
	if len(b) < sbLabelOff+16 {
		return "", "", false
	}
	if binary.LittleEndian.Uint16(b[sbMagicOff:]) != ext4Magic {
		return "", "", false
	}
	return formatUUID(b[sbUUIDOff : sbUUIDOff+16]),
		string(trimNUL(b[sbLabelOff : sbLabelOff+16])), true
}

// probeISO9660 reads the primary volume descriptor. The buffer starts at offset
// 0 of the device. ISO9660 has no UUID.
func probeISO9660(b []byte) (uuid, label string, ok bool) {
	if len(b) < isoPVDOffset+isoLabelOff+32 {
		return "", "", false
	}
	pvd := b[isoPVDOffset:]
	if pvd[0] != 1 || string(pvd[1:6]) != "CD001" {
		return "", "", false
	}
	return "", strings.TrimRight(string(pvd[isoLabelOff:isoLabelOff+32]), " \x00"), true
}

// probeFAT reads a FAT boot sector, handling both FAT32 and FAT12/16 layouts.
// The "UUID" is the volume serial, rendered the conventional XXXX-XXXX way.
func probeFAT(b []byte) (uuid, label string, ok bool) {
	if len(b) < 512 || b[510] != 0x55 || b[511] != 0xaa {
		return "", "", false
	}
	labelOff, serialOff := 0, 0
	switch {
	case strings.HasPrefix(string(b[fat32TypeOff:fat32TypeOff+5]), "FAT32"):
		labelOff, serialOff = fat32LabelOff, fat32SerialOff
	case strings.HasPrefix(string(b[fat16TypeOff:fat16TypeOff+3]), "FAT"):
		labelOff, serialOff = fat16LabelOff, fat16SerialOff
	default:
		return "", "", false
	}
	serial := binary.LittleEndian.Uint32(b[serialOff:])
	uuid = fmt.Sprintf("%04X-%04X", serial>>16, serial&0xffff)
	label = strings.TrimRight(string(b[labelOff:labelOff+11]), " \x00")
	return uuid, label, true
}

// The erofs superblock sits 1024 bytes into the device, with its magic first.
// Read-only by construction, which is why k0smos mounts it MS_RDONLY.
const (
	erofsSBOffset = 1024
	erofsSBLen    = 128
	erofsMagic    = 0xE0F5E1E2
	// Offsets within the superblock, from struct erofs_super_block.
	erofsUUIDOff  = 32 // uuid[16]
	erofsLabelOff = 48 // volume_name[16]
)

// probeEROFS identifies an erofs image, which is what a root filesystem carried
// inside the initramfs is. Detecting it means k0smos need not be told the type on
// the kernel cmdline.
func probeEROFS(b []byte) (uuid, label string, ok bool) {
	if len(b) < erofsLabelOff+16 {
		return "", "", false
	}
	if binary.LittleEndian.Uint32(b[0:]) != erofsMagic {
		return "", "", false
	}
	return formatUUID(b[erofsUUIDOff : erofsUUIDOff+16]),
		strings.TrimRight(string(b[erofsLabelOff:erofsLabelOff+16]), " \x00"), true
}

// Package gpt grows the last partition of a GUID partition table into the space
// a disk gained after the table was written.
//
// That is the bare-metal and VM case k0smos has to handle itself: the image is
// written onto a disk far larger than it, or a platform grows the volume to the
// size asked for (an RBD resize, qemu-img resize). Distributions leave this to
// cloud-init's growpart, a shell script around sfdisk; k0smos has neither, and
// the table format is small and fixed, so it is done here.
//
// Only ever growing, only the last partition, only on a table whose checksums
// hold. Anything else is refused: rewriting a partition table is the second
// irreversible thing k0smos does, after formatting.
package gpt

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"unicode/utf16"
)

// Disk is a block device read and written by byte offset.
type Disk interface {
	io.ReaderAt
	io.WriterAt
}

// Header field offsets, from the UEFI specification.
const (
	hdrSize      = 12
	hdrCRC       = 16
	hdrMyLBA     = 24
	hdrAltLBA    = 32
	hdrFirstLBA  = 40
	hdrLastLBA   = 48
	hdrDiskGUID  = 56
	hdrEntryLBA  = 72
	hdrEntries   = 80
	hdrEntrySize = 84
	hdrEntryCRC  = 88

	entFirstLBA = 32
	entLastLBA  = 40
	entName     = 56

	// The one entry of the protective MBR, at offset 446.
	mbrStartCHS = 446 + 1
	mbrType     = 446 + 4
	mbrEndCHS   = 446 + 5
	mbrFirstLBA = 446 + 8
	mbrSectors  = 446 + 12
)

var signature = []byte("EFI PART")

// ErrNoGPT means the disk has no GUID partition table: an MBR disk, or none.
var ErrNoGPT = errors.New("gpt: no GUID partition table")

// Result says what Grow found and did. Start and End are the partition's LBAs.
type Result struct {
	Start, OldEnd, NewEnd uint64
}

// Grown reports whether the partition got bigger.
func (r Result) Grown() bool { return r.NewEnd > r.OldEnd }

// Grow extends partition part (1-based) to the end of a disk of size bytes with
// sectorSize-byte logical blocks, moving the backup table to the new end. A
// partition that already reaches the end is left alone and reported unchanged.
//
// The backup copy is written before the primary: until the primary is rewritten
// it still describes the old, valid layout, so an interruption at any point
// leaves a disk that boots as it did.
func Grow(d Disk, size, sectorSize int64, part int) (Result, error) {
	if sectorSize < 512 || size < sectorSize*68 {
		return Result{}, fmt.Errorf("gpt: implausible disk: %d bytes in %d-byte sectors", size, sectorSize)
	}
	hdr := make([]byte, sectorSize)
	if _, err := d.ReadAt(hdr, sectorSize); err != nil {
		return Result{}, fmt.Errorf("gpt: read header: %w", err)
	}
	if !bytes.Equal(hdr[:8], signature) {
		return Result{}, ErrNoGPT
	}
	n := le32(hdr, hdrSize)
	if n < 92 || int64(n) > sectorSize || headerCRC(hdr[:n]) != le32(hdr, hdrCRC) {
		return Result{}, errors.New("gpt: primary header checksum does not match")
	}
	count, esize := le32(hdr, hdrEntries), le32(hdr, hdrEntrySize)
	if count == 0 || count > 1024 || esize < 128 || esize > 1024 {
		return Result{}, fmt.Errorf("gpt: implausible entry array: %d entries of %d bytes", count, esize)
	}
	entries := make([]byte, int64(count)*int64(esize))
	if _, err := d.ReadAt(entries, int64(le64(hdr, hdrEntryLBA))*sectorSize); err != nil {
		return Result{}, fmt.Errorf("gpt: read entries: %w", err)
	}
	if crc32.ChecksumIEEE(entries) != le32(hdr, hdrEntryCRC) {
		return Result{}, errors.New("gpt: partition entry checksum does not match")
	}
	if part < 1 || part > int(count) {
		return Result{}, fmt.Errorf("gpt: no partition %d", part)
	}
	ent := entries[(part-1)*int(esize) : part*int(esize)]
	if isZero(ent[:16]) {
		return Result{}, fmt.Errorf("gpt: partition %d is unused", part)
	}
	res := Result{Start: le64(ent, entFirstLBA), OldEnd: le64(ent, entLastLBA)}
	res.NewEnd = res.OldEnd

	sectors := uint64(size / sectorSize)
	arraySectors := (uint64(len(entries)) + uint64(sectorSize) - 1) / uint64(sectorSize)
	alt := sectors - 1
	lastUsable := alt - arraySectors - 1
	if lastUsable < le64(hdr, hdrLastLBA) {
		return res, fmt.Errorf("gpt: the disk is smaller than its table says (%d < %d sectors usable)",
			lastUsable, le64(hdr, hdrLastLBA))
	}
	if lastUsable <= res.OldEnd {
		return res, nil
	}
	for i := range int(count) {
		e := entries[i*int(esize) : (i+1)*int(esize)]
		if i != part-1 && !isZero(e[:16]) && le64(e, entLastLBA) > res.OldEnd {
			// Only an error when the disk has grown past its table: a layout
			// with partitions after this one is not news on every boot.
			if lastUsable > le64(hdr, hdrLastLBA) {
				return res, fmt.Errorf("gpt: partition %d is not the last on the disk", part)
			}
			return res, nil
		}
	}
	res.NewEnd = lastUsable

	binary.LittleEndian.PutUint64(ent[entLastLBA:], res.NewEnd)
	binary.LittleEndian.PutUint32(hdr[hdrEntryCRC:], crc32.ChecksumIEEE(entries))
	binary.LittleEndian.PutUint64(hdr[hdrAltLBA:], alt)
	binary.LittleEndian.PutUint64(hdr[hdrLastLBA:], lastUsable)
	seal(hdr, n)

	backup := bytes.Clone(hdr)
	binary.LittleEndian.PutUint64(backup[hdrMyLBA:], alt)
	binary.LittleEndian.PutUint64(backup[hdrAltLBA:], 1)
	binary.LittleEndian.PutUint64(backup[hdrEntryLBA:], alt-arraySectors)
	seal(backup, n)

	for _, w := range []struct {
		b   []byte
		lba uint64
	}{
		{entries, alt - arraySectors}, {backup, alt},
		{entries, le64(hdr, hdrEntryLBA)}, {hdr, 1},
	} {
		if _, err := d.WriteAt(w.b, int64(w.lba)*sectorSize); err != nil {
			return res, fmt.Errorf("gpt: write at sector %d: %w", w.lba, err)
		}
	}
	return res, fixProtectiveMBR(d, sectors)
}

// fixProtectiveMBR updates the size the protective MBR claims for the disk. The
// kernel tolerates a stale one, but gdisk and parted report the disk as damaged.
// A hybrid or absent MBR is left alone.
func fixProtectiveMBR(d Disk, sectors uint64) error {
	mbr := make([]byte, 512)
	if _, err := d.ReadAt(mbr, 0); err != nil {
		return fmt.Errorf("gpt: read the protective MBR: %w", err)
	}
	if mbr[510] != 0x55 || mbr[511] != 0xaa || mbr[mbrType] != 0xee || le32(mbr, mbrFirstLBA) != 1 {
		return nil
	}
	end := min(sectors-1, 0xffffffff)
	last := chs(end)
	copy(mbr[mbrEndCHS:], last[:])
	binary.LittleEndian.PutUint32(mbr[mbrSectors:], uint32(end))
	if _, err := d.WriteAt(mbr, 0); err != nil {
		return fmt.Errorf("gpt: write the protective MBR: %w", err)
	}
	return nil
}

// GUID is a GPT GUID in the mixed-endian form the table stores.
type GUID [16]byte

// ParseGUID reads the canonical 8-4-4-4-12 text form, as sgdisk prints it.
func ParseGUID(s string) (GUID, error) {
	var g GUID
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 || len(strings.Split(s, "-")) != 5 {
		return g, fmt.Errorf("gpt: %q is not a GUID", s)
	}
	// The first three fields are stored little-endian, the last two as written.
	copy(g[:], []byte{b[3], b[2], b[1], b[0], b[5], b[4], b[7], b[6]})
	copy(g[8:], b[8:])
	return g, nil
}

// MustGUID is ParseGUID for a constant, which cannot be wrong at run time.
func MustGUID(s string) GUID {
	g, err := ParseGUID(s)
	if err != nil {
		panic(err)
	}
	return g
}

// The partition type GUIDs k0smos images use, with sgdisk's short codes.
var (
	TypeLinux    = MustGUID("0FC63DAF-8483-4772-8E79-3D69D8477DE4") // 8300
	TypeESP      = MustGUID("C12A7328-F81F-11D2-BA4B-00A0C93EC93B") // ef00
	TypeBIOSBoot = MustGUID("21686148-6449-6E6F-744E-656564454649") // ef02
)

// Partition is one entry of a table. Start and End are inclusive logical block
// addresses. A zero Type means TypeLinux and a zero GUID gets a distinct
// non-zero one from the entry's index, so a caller writes only what it cares
// about.
type Partition struct {
	Start, End uint64
	Type, GUID GUID
	Name       string // up to 36 characters, as blkid and GRUB show it
}

// Table is a whole GUID partition table. Index i of Partitions becomes
// partition i+1, and a zero Partition leaves that number unused -- which is how
// an image keeps the same number for the same thing across variants.
type Table struct {
	DiskGUID   GUID
	Partitions []Partition
}

// Create writes t as a fresh table in the layout sgdisk uses -- 128 entries of
// 128 bytes, primary at sector 1, backup at the last -- onto a disk of size
// bytes with sectorSize-byte blocks. A partition outside the usable range or
// overlapping another is refused rather than written: an image whose table
// contradicts itself is worse than a build that stops.
//
// For tools that build disk images, and for tests; k0smos itself never creates
// a table.
func Create(d Disk, size, sectorSize int64, t Table) error {
	entries := make([]byte, 128*128)
	arr := uint64((int64(len(entries)) + sectorSize - 1) / sectorSize)
	alt := uint64(size/sectorSize) - 1
	firstUsable, lastUsable := 2+arr, alt-arr-1
	if len(t.Partitions) > 128 {
		return fmt.Errorf("gpt: %d partitions, over the 128 a table holds", len(t.Partitions))
	}
	for i, p := range t.Partitions {
		if p == (Partition{}) {
			continue
		}
		if p.End < p.Start || p.Start < firstUsable || p.End > lastUsable {
			return fmt.Errorf("gpt: partition %d spans sectors %d-%d, outside the usable %d-%d",
				i+1, p.Start, p.End, firstUsable, lastUsable)
		}
		for j, q := range t.Partitions[:i] {
			if q != (Partition{}) && p.Start <= q.End && q.Start <= p.End {
				return fmt.Errorf("gpt: partition %d (sectors %d-%d) overlaps partition %d (%d-%d)",
					i+1, p.Start, p.End, j+1, q.Start, q.End)
			}
		}
		name := utf16.Encode([]rune(p.Name))
		if len(name) > 36 {
			return fmt.Errorf("gpt: partition %d is named %q, over the 36 characters an entry holds", i+1, p.Name)
		}
		e := entries[i*128:]
		if p.Type == (GUID{}) {
			copy(e, TypeLinux[:])
		} else {
			copy(e, p.Type[:])
		}
		if p.GUID == (GUID{}) {
			e[16] = byte(i + 1) // a distinct, non-zero unique GUID
		} else {
			copy(e[16:], p.GUID[:])
		}
		binary.LittleEndian.PutUint64(e[entFirstLBA:], p.Start)
		binary.LittleEndian.PutUint64(e[entLastLBA:], p.End)
		for k, u := range name {
			binary.LittleEndian.PutUint16(e[entName+2*k:], u)
		}
	}
	hdr := make([]byte, sectorSize)
	copy(hdr, signature)
	binary.LittleEndian.PutUint32(hdr[8:], 0x00010000) // revision 1.0
	binary.LittleEndian.PutUint32(hdr[hdrSize:], 92)
	binary.LittleEndian.PutUint64(hdr[hdrMyLBA:], 1)
	binary.LittleEndian.PutUint64(hdr[hdrAltLBA:], alt)
	binary.LittleEndian.PutUint64(hdr[hdrFirstLBA:], firstUsable)
	binary.LittleEndian.PutUint64(hdr[hdrLastLBA:], lastUsable)
	if t.DiskGUID == (GUID{}) {
		copy(hdr[hdrDiskGUID:], "k0smos-test-disk")
	} else {
		copy(hdr[hdrDiskGUID:], t.DiskGUID[:])
	}
	binary.LittleEndian.PutUint64(hdr[hdrEntryLBA:], 2)
	binary.LittleEndian.PutUint32(hdr[hdrEntries:], 128)
	binary.LittleEndian.PutUint32(hdr[hdrEntrySize:], 128)
	binary.LittleEndian.PutUint32(hdr[hdrEntryCRC:], crc32.ChecksumIEEE(entries))
	seal(hdr, 92)
	backup := bytes.Clone(hdr)
	binary.LittleEndian.PutUint64(backup[hdrMyLBA:], alt)
	binary.LittleEndian.PutUint64(backup[hdrAltLBA:], 1)
	binary.LittleEndian.PutUint64(backup[hdrEntryLBA:], alt-arr)
	seal(backup, 92)
	// The protective MBR: without it Linux ignores the GPT altogether. The CHS
	// fields are what sgdisk writes, so that an image built here and one built
	// by sgdisk are the same bytes.
	mbr := make([]byte, 512)
	first, last := chs(1), chs(min(alt, 0xffffffff))
	copy(mbr[mbrStartCHS:], first[:])
	mbr[mbrType] = 0xee
	copy(mbr[mbrEndCHS:], last[:])
	binary.LittleEndian.PutUint32(mbr[mbrFirstLBA:], 1)
	binary.LittleEndian.PutUint32(mbr[mbrSectors:], uint32(min(alt, 0xffffffff)))
	mbr[510], mbr[511] = 0x55, 0xaa
	if _, err := d.WriteAt(mbr, 0); err != nil {
		return err
	}
	for _, w := range []struct {
		b   []byte
		lba uint64
	}{{hdr, 1}, {entries, 2}, {entries, alt - arr}, {backup, alt}} {
		if _, err := d.WriteAt(w.b, int64(w.lba)*sectorSize); err != nil {
			return err
		}
	}
	return fixProtectiveMBR(d, alt+1)
}

// chs is an LBA in the legacy cylinder/head/sector form an MBR entry holds:
// 63 sectors per track and 255 heads, which is what every partitioner assumes,
// capped at the largest the three bytes can say. Nothing reads it on a GPT
// disk, but gdisk and parted report a disk whose values disagree as damaged.
func chs(lba uint64) [3]byte {
	const spt, heads = 63, 255
	c, rem := lba/(spt*heads), lba%(spt*heads)
	h, s := rem/spt, rem%spt+1
	if c > 1023 {
		c, h, s = 1023, heads-1, spt
	}
	return [3]byte{byte(h), byte(s) | byte(c>>2)&0xc0, byte(c)}
}

func seal(hdr []byte, n uint32) {
	binary.LittleEndian.PutUint32(hdr[hdrCRC:], headerCRC(hdr[:n]))
}

// headerCRC is the header's CRC32 computed with its own CRC field as zero.
func headerCRC(h []byte) uint32 {
	c := bytes.Clone(h)
	binary.LittleEndian.PutUint32(c[hdrCRC:], 0)
	return crc32.ChecksumIEEE(c)
}

func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
func le64(b []byte, off int) uint64 { return binary.LittleEndian.Uint64(b[off:]) }

func isZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

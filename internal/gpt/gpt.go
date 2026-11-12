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
	"errors"
	"fmt"
	"hash/crc32"
	"io"
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
	hdrEntryLBA  = 72
	hdrEntries   = 80
	hdrEntrySize = 84
	hdrEntryCRC  = 88

	entFirstLBA = 32
	entLastLBA  = 40
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
	if mbr[510] != 0x55 || mbr[511] != 0xaa || mbr[446+4] != 0xee || le32(mbr, 446+8) != 1 {
		return nil
	}
	binary.LittleEndian.PutUint32(mbr[446+12:], uint32(min(sectors-1, 0xffffffff)))
	if _, err := d.WriteAt(mbr, 0); err != nil {
		return fmt.Errorf("gpt: write the protective MBR: %w", err)
	}
	return nil
}

// Create writes a fresh table as sgdisk does -- 128 entries of 128 bytes,
// primary at sector 1, backup at the last -- with one Linux filesystem
// partition per [first, last] sector pair. For tests and tools that build disk
// images; k0smos itself never creates a table.
func Create(d Disk, size, sectorSize int64, parts ...[2]uint64) error {
	entries := make([]byte, 128*128)
	for i, p := range parts {
		e := entries[i*128:]
		copy(e, linuxFS[:])
		e[16] = byte(i + 1) // a distinct, non-zero unique GUID
		binary.LittleEndian.PutUint64(e[entFirstLBA:], p[0])
		binary.LittleEndian.PutUint64(e[entLastLBA:], p[1])
	}
	arr := uint64((int64(len(entries)) + sectorSize - 1) / sectorSize)
	alt := uint64(size/sectorSize) - 1
	hdr := make([]byte, sectorSize)
	copy(hdr, signature)
	binary.LittleEndian.PutUint32(hdr[8:], 0x00010000) // revision 1.0
	binary.LittleEndian.PutUint32(hdr[hdrSize:], 92)
	binary.LittleEndian.PutUint64(hdr[hdrMyLBA:], 1)
	binary.LittleEndian.PutUint64(hdr[hdrAltLBA:], alt)
	binary.LittleEndian.PutUint64(hdr[hdrFirstLBA:], 2+arr)
	binary.LittleEndian.PutUint64(hdr[hdrLastLBA:], alt-arr-1)
	copy(hdr[56:72], "k0smos-test-disk") // disk GUID
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
	// The protective MBR: without it Linux ignores the GPT altogether.
	mbr := make([]byte, 512)
	mbr[446+4] = 0xee
	binary.LittleEndian.PutUint32(mbr[446+8:], 1)
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

// linuxFS is the Linux filesystem type GUID 0FC63DAF-8483-4772-8E79-3D69D8477DE4,
// in its mixed-endian on-disk form.
var linuxFS = [16]byte{0xaf, 0x3d, 0xc6, 0x0f, 0x83, 0x84, 0x72, 0x47, 0x8e, 0x79, 0x3d, 0x69, 0xd8, 0x47, 0x7d, 0xe4}

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

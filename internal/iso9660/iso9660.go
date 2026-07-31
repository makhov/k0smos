// Package iso9660 reads files out of an ISO image without mounting it.
//
// This exists so k0smos does not need CONFIG_ISO9660_FS in the kernel. Cluster
// API delivers bootstrap data on a cloud-init drive, and KubeVirt always writes
// that drive as an ISO (xorrisofs -joliet -rock, one code path in its
// pkg/cloud-init). Depending on the kernel to mount it rules out otherwise
// excellent monolithic kernels — Kata's, for instance, builds in no ISO9660 at
// all — and would mean owning a kernel build purely to add one symbol.
//
// Only what that job needs is implemented: read-only lookup of a handful of
// small files by path. The format has been stable since 1988, so this is a
// fixed target rather than a moving one.
//
// Rock Ridge is handled because it is what preserves real filenames: "user-data"
// contains a hyphen, which is outside the ISO9660 Level 1 charset, so the plain
// name is mangled to something like "USER_DAT.;1". Joliet is deliberately not
// implemented — Rock Ridge is always present in practice and is what Linux
// itself prefers.
package iso9660

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

const (
	// The primary volume descriptor lives at sector 16 of a 2048-byte sector.
	pvdOffset = 32768
	sectorLen = 2048

	// Offsets within the PVD.
	pvdTypeOff      = 0
	pvdIDOff        = 1
	pvdBlockSizeOff = 128
	pvdRootDirOff   = 156

	// Offsets within a directory record.
	recLenOff        = 0
	recEALenOff      = 1
	recExtentOff     = 2
	recSizeOff       = 10
	recFlagsOff      = 25
	recUnitSizeOff   = 26
	recInterleaveOff = 27
	recNameLenOff    = 32
	recNameOff       = 33

	// flagDirectory marks a record as a directory.
	flagDirectory = 0x02

	// maxFileSize bounds a read. Bootstrap data is a few kilobytes; anything
	// vastly larger means a corrupt or hostile image rather than a config.
	maxFileSize = 8 << 20

	// maxRecords bounds directory iteration, so a malformed image cannot spin.
	maxRecords = 4096
)

// ReaderAt is the device to read from. internal/sys satisfies it via a small
// adapter binding the device name.
type ReaderAt interface {
	ReadAt(p []byte, off int64) error
}

// DeviceReader is the block-device read method of *sys.Sys, which addresses
// devices by name rather than by handle.
type DeviceReader interface {
	ReadAt(dev string, p []byte, off int64) error
}

// OnDevice adapts a DeviceReader to one bound to a single device, e.g.
// OnDevice(s, "vdb").
func OnDevice(r DeviceReader, dev string) ReaderAt {
	return boundDevice{r: r, dev: dev}
}

type boundDevice struct {
	r   DeviceReader
	dev string
}

func (b boundDevice) ReadAt(p []byte, off int64) error { return b.r.ReadAt(b.dev, p, off) }

// Reader reads files from an ISO image.
type Reader struct {
	ra        ReaderAt
	blockSize int64
	rootLBA   int64
	rootSize  int64
}

// Open validates the volume descriptor and locates the root directory.
func Open(ra ReaderAt) (*Reader, error) {
	pvd := make([]byte, sectorLen)
	if err := ra.ReadAt(pvd, pvdOffset); err != nil {
		return nil, fmt.Errorf("read primary volume descriptor: %w", err)
	}
	if pvd[pvdTypeOff] != 1 || string(pvd[pvdIDOff:pvdIDOff+5]) != "CD001" {
		return nil, errors.New("not an ISO9660 image")
	}

	blockSize := int64(le16(pvd[pvdBlockSizeOff:]))
	if blockSize == 0 {
		blockSize = sectorLen
	}
	root := pvd[pvdRootDirOff : pvdRootDirOff+34]
	return &Reader{
		ra:        ra,
		blockSize: blockSize,
		rootLBA:   int64(le32(root[recExtentOff:])),
		rootSize:  int64(le32(root[recSizeOff:])),
	}, nil
}

// ReadFile returns the contents of a file, named with forward slashes relative
// to the image root, e.g. "user-data" or "openstack/latest/user_data".
func (r *Reader) ReadFile(name string) ([]byte, error) {
	parts := strings.Split(strings.Trim(name, "/"), "/")
	lba, size := r.rootLBA, r.rootSize

	for i, part := range parts {
		rec, err := r.find(lba, size, part)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		last := i == len(parts)-1
		if last == (rec.flags&flagDirectory != 0) {
			// Either a directory where a file was wanted, or the reverse.
			return nil, fmt.Errorf("%s: %q is the wrong kind of entry", name, part)
		}
		lba, size = rec.lba, rec.size
	}

	if size > maxFileSize {
		return nil, fmt.Errorf("%s: %d bytes exceeds the %d byte limit", name, size, maxFileSize)
	}
	buf := make([]byte, size)
	if size == 0 {
		return buf, nil
	}
	if err := r.ra.ReadAt(buf, lba*r.blockSize); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return buf, nil
}

type record struct {
	lba   int64
	size  int64
	flags byte
}

// find scans a directory extent for an entry called want.
func (r *Reader) find(lba, size int64, want string) (record, error) {
	if size <= 0 || size > int64(maxRecords)*r.blockSize {
		return record{}, fmt.Errorf("implausible directory size %d", size)
	}
	dir := make([]byte, size)
	if err := r.ra.ReadAt(dir, lba*r.blockSize); err != nil {
		return record{}, fmt.Errorf("read directory: %w", err)
	}

	for off, n := 0, 0; off < len(dir) && n < maxRecords; n++ {
		recLen := int(dir[off])
		if recLen == 0 {
			// Records never straddle a logical block; a zero length means the
			// rest of this block is padding.
			next := (int64(off)/r.blockSize + 1) * r.blockSize
			if next >= int64(len(dir)) {
				break
			}
			off = int(next)
			continue
		}
		if off+recLen > len(dir) || recLen < recNameOff {
			return record{}, errors.New("truncated directory record")
		}
		rec := dir[off : off+recLen]

		if name, ok := entryName(rec); ok && name == want {
			// Two layouts this does not implement, refused rather than
			// mis-read: an interleaved file, whose data is not contiguous, and
			// one preceded by an extended attribute record, which shifts where
			// the data starts. No ISO writer in this path produces either, so
			// hitting one means the assumption is wrong and should say so.
			if rec[recUnitSizeOff] != 0 || rec[recInterleaveOff] != 0 {
				return record{}, fmt.Errorf("%q is interleaved, which is not supported", want)
			}
			if rec[recEALenOff] != 0 {
				return record{}, fmt.Errorf("%q has an extended attribute record, which is not supported", want)
			}
			return record{
				lba:   int64(le32(rec[recExtentOff:])),
				size:  int64(le32(rec[recSizeOff:])),
				flags: rec[recFlagsOff],
			}, nil
		}
		off += recLen
	}
	// Wrapping fs.ErrNotExist lets callers tell "this drive does not carry that
	// file", which is normal, from "this drive could not be read", which is not.
	return record{}, fmt.Errorf("%q: %w", want, fs.ErrNotExist)
}

// entryName returns a record's filename, preferring the Rock Ridge name.
// It reports false for the "." and ".." entries, which carry no usable name.
func entryName(rec []byte) (string, bool) {
	nameLen := int(rec[recNameLenOff])
	if nameLen == 0 || recNameOff+nameLen > len(rec) {
		return "", false
	}
	raw := rec[recNameOff : recNameOff+nameLen]
	// "." and ".." are encoded as single bytes 0x00 and 0x01.
	if nameLen == 1 && (raw[0] == 0 || raw[0] == 1) {
		return "", false
	}

	// The system use area follows the name, padded to an even offset.
	sysOff := recNameOff + nameLen
	if sysOff%2 == 1 {
		sysOff++
	}
	if sysOff < len(rec) {
		if name, ok := rockRidgeName(rec[sysOff:]); ok {
			return name, true
		}
	}
	return normaliseName(string(raw)), true
}

// rockRidgeName extracts the concatenated NM entries from a system use area.
// NM is what carries the real, unmangled filename.
func rockRidgeName(su []byte) (string, bool) {
	var name strings.Builder
	for off := 0; off+4 <= len(su); {
		length := int(su[off+2])
		if length < 4 || off+length > len(su) {
			break
		}
		sig := string(su[off : off+2])
		switch sig {
		case "NM":
			// su[off+4] is a flag byte; bit 0 means the name continues in a
			// later NM entry, which is why these are concatenated.
			name.Write(su[off+5 : off+length])
		case "ST":
			off = len(su) // explicit end of the system use area
			continue
		}
		off += length
	}
	if name.Len() == 0 {
		return "", false
	}
	return name.String(), true
}

// normaliseName strips the ";1" version suffix and any trailing dot that
// ISO9660 adds to extension-less names.
func normaliseName(s string) string {
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, ".")
}

func le16(b []byte) uint16 { return uint16(b[0]) | uint16(b[1])<<8 }

func le32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

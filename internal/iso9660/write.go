package iso9660

import (
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// File is one entry to place at the root of a generated image.
type File struct {
	Name string
	Data []byte
}

// Offsets within the primary volume descriptor that only the writer needs. The
// reader ignores these fields, so they live here rather than beside the ones it
// uses.
const (
	pvdVersionOff     = 6
	pvdSystemIDOff    = 8
	pvdVolumeIDOff    = 40
	pvdSpaceSizeOff   = 80
	pvdSetSizeOff     = 120
	pvdSeqNumOff      = 124
	pvdPathTableSzOff = 132
	pvdPathTableLOff  = 140
	pvdPathTableMOff  = 148
	pvdVolumeSetOff   = 190
	pvdCreatedOff     = 813
	pvdModifiedOff    = 830
	pvdExpiresOff     = 847
	pvdEffectiveOff   = 864
	pvdStructVerOff   = 881

	// Sector assignments. Everything before the root directory is fixed-size, so
	// these can be constants rather than a running allocation.
	sectorPVD        = 16
	sectorTerminator = 17
	sectorPathTableL = 18
	sectorPathTableM = 19
	sectorRootDir    = 20

	// A directory identifier of one NUL byte means "this directory"; 0x01 means
	// the parent. Both are required as the first two records of every directory.
	idSelf   = 0x00
	idParent = 0x01

	// maxFiles bounds what Write will accept, so the root directory cannot be
	// asked to straddle logical blocks in ways this writer does not implement.
	maxFiles = 64
)

// fixedTimestamp is written as every date in the image, so the same inputs always
// produce byte-identical output. A cloud-init drive is consumed within seconds of
// being made and nothing reads these fields; reproducibility is worth more than a
// real clock.
var fixedTimestamp = [7]byte{100, 1, 1, 0, 0, 0, 0} // 2000-01-01T00:00:00Z

// Write produces an ISO9660 image containing files at its root, labelled label.
//
// This is the drive k0smos reads its configuration from — the NoCloud layout, so
// "user-data" and "meta-data" at the top level. Generating it here means k0smosctl
// needs no xorriso, which on macOS means no Docker either.
//
// Rock Ridge names are emitted because they are what makes "user-data" legal: the
// hyphen is outside the ISO9660 Level 1 charset, so the plain name is mangled.
// Joliet is not written, matching what the reader implements.
func Write(w io.Writer, label string, files []File) error {
	if len(files) == 0 {
		return errors.New("no files to write")
	}
	if len(files) > maxFiles {
		return fmt.Errorf("%d files exceeds the %d supported", len(files), maxFiles)
	}
	// Sorted so output does not depend on map iteration or argument order.
	sorted := make([]File, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	for _, f := range sorted {
		if err := checkName(f.Name); err != nil {
			return err
		}
	}

	// Lay out the file extents. Each file starts on a sector boundary, which is
	// what lets a directory record address it by logical block.
	root := buildRootDir(sorted, 0) // sized first, with placeholder extents
	rootSectors := sectors(int64(len(root)))
	next := int64(sectorRootDir) + rootSectors
	extents := make([]int64, len(sorted))
	for i, f := range sorted {
		extents[i] = next
		next += sectors(int64(len(f.Data)))
	}
	total := next

	// Rebuild now that the extents are known. The records are a fixed size
	// regardless of the values in them, so this cannot change the layout.
	root = buildRootDirWithExtents(sorted, extents, rootSectors)
	if sectors(int64(len(root))) != rootSectors {
		return errors.New("internal: root directory changed size")
	}

	img := make([]byte, total*sectorLen)
	writePVD(img[sectorPVD*sectorLen:], label, total, rootSectors)
	writeTerminator(img[sectorTerminator*sectorLen:])
	writePathTables(img, rootSectors)
	copy(img[sectorRootDir*sectorLen:], root)
	for i, f := range sorted {
		copy(img[extents[i]*sectorLen:], f.Data)
	}

	_, err := w.Write(img)
	return err
}

// checkName rejects what this writer cannot represent, rather than producing an
// image that reads back wrong.
func checkName(name string) error {
	switch {
	case name == "":
		return errors.New("empty file name")
	case strings.ContainsAny(name, "/\\"):
		// Subdirectories are not implemented: the NoCloud layout is flat, and the
		// nested config-drive layout is something k0smos reads but never writes.
		return fmt.Errorf("%q: subdirectories are not supported", name)
	case len(name) > 200:
		return fmt.Errorf("%q: name too long", name)
	case name != path.Clean(name):
		return fmt.Errorf("%q: not a plain file name", name)
	}
	return nil
}

func sectors(n int64) int64 {
	if n <= 0 {
		return 1 // still occupies an addressable extent
	}
	return (n + sectorLen - 1) / sectorLen
}

// buildRootDir sizes the root directory. Extents are filled in by the second
// pass; record sizes do not depend on them.
func buildRootDir(files []File, rootSectors int64) []byte {
	extents := make([]int64, len(files))
	return buildRootDirWithExtents(files, extents, rootSectors)
}

func buildRootDirWithExtents(files []File, extents []int64, rootSectors int64) []byte {
	var out []byte

	// "." carries the SUSP indicator and the extension reference. Both are needed:
	// SP says a system use area is present, ER declares which extension the
	// entries in it belong to. Omitting ER produced an image this package's own
	// reader accepted — it only looks for NM — while xorriso reported every name
	// mangled, because it will not honour Rock Ridge that has not been declared.
	self := dirRecord([]byte{idSelf}, sectorRootDir, rootSectors*sectorLen, flagDirectory,
		append(susp(), extensionReference()...))
	parent := dirRecord([]byte{idParent}, sectorRootDir, rootSectors*sectorLen, flagDirectory, nil)
	out = append(out, self...)
	out = append(out, parent...)

	for i, f := range files {
		rec := dirRecord([]byte(isoName(f.Name)), extents[i], int64(len(f.Data)), 0, rockRidgeNM(f.Name))
		// A record may not straddle a logical block: pad to the next one instead.
		if len(out)%sectorLen+len(rec) > sectorLen {
			out = append(out, make([]byte, sectorLen-len(out)%sectorLen)...)
		}
		out = append(out, rec...)
	}
	// Pad to a whole number of sectors, which is also what tells a reader the
	// records have ended.
	if r := len(out) % sectorLen; r != 0 {
		out = append(out, make([]byte, sectorLen-r)...)
	}
	return out
}

// dirRecord assembles one directory record.
func dirRecord(id []byte, extent, size int64, flags byte, systemUse []byte) []byte {
	base := recNameOff + len(id)
	if base%2 == 1 {
		base++ // the system use area starts on an even offset
	}
	rec := make([]byte, base+len(systemUse))
	rec[recLenOff] = byte(len(rec))
	rec[recEALenOff] = 0
	putBoth32(rec[recExtentOff:], uint32(extent))
	putBoth32(rec[recSizeOff:], uint32(size))
	copy(rec[18:25], fixedTimestamp[:])
	rec[recFlagsOff] = flags
	rec[recUnitSizeOff] = 0
	rec[recInterleaveOff] = 0
	putBoth16(rec[28:], 1) // volume sequence number
	rec[recNameLenOff] = byte(len(id))
	copy(rec[recNameOff:], id)
	copy(rec[base:], systemUse)
	return rec
}

// susp is the SP entry: the marker that a System Use Sharing Protocol area is
// present. The two magic bytes are required by the standard.
func susp() []byte {
	return []byte{'S', 'P', 7, 1, 0xBE, 0xEF, 0}
}

// extensionReference is the ER entry naming the Rock Ridge extension whose
// entries appear in this image. The identifier is the one the standard defines;
// the description and source fields are permitted to be empty and are, since
// nothing reads them.
func extensionReference() []byte {
	const id = "RRIP_1991A"
	return append([]byte{
		'E', 'R',
		byte(8 + len(id)), // LEN_SUE
		1,                 // entry version
		byte(len(id)),     // LEN_ID
		0,                 // LEN_DES
		0,                 // LEN_SRC
		1,                 // extension version
	}, id...)
}

// rockRidgeNM is the NM entry carrying an unmangled name.
func rockRidgeNM(name string) []byte {
	out := []byte{'N', 'M', byte(5 + len(name)), 1, 0}
	return append(out, name...)
}

// isoName mangles a name into the ISO9660 Level 1 charset: upper case, A-Z 0-9
// and underscore, with a version suffix. Rock Ridge carries the real name, so
// this only has to be legal and unique-ish, not readable.
func isoName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String() + ";1"
}

func writePVD(pvd []byte, label string, total, rootSectors int64) {
	pvd[pvdTypeOff] = 1
	copy(pvd[pvdIDOff:], "CD001")
	pvd[pvdVersionOff] = 1
	padText(pvd[pvdSystemIDOff:pvdSystemIDOff+32], "")
	// The volume identifier is what blkid matches on: this is the "cidata" that
	// makes k0smos recognise the drive at all.
	padText(pvd[pvdVolumeIDOff:pvdVolumeIDOff+32], label)
	putBoth32(pvd[pvdSpaceSizeOff:], uint32(total))
	putBoth16(pvd[pvdSetSizeOff:], 1)
	putBoth16(pvd[pvdSeqNumOff:], 1)
	putBoth16(pvd[pvdBlockSizeOff:], sectorLen)
	putBoth32(pvd[pvdPathTableSzOff:], pathTableSize)
	putLE32(pvd[pvdPathTableLOff:], sectorPathTableL)
	putBE32(pvd[pvdPathTableMOff:], sectorPathTableM)
	copy(pvd[pvdRootDirOff:], dirRecord([]byte{idSelf}, sectorRootDir, rootSectors*sectorLen, flagDirectory, nil))

	for _, off := range []int{pvdVolumeSetOff, pvdVolumeSetOff + 128, pvdVolumeSetOff + 256, pvdVolumeSetOff + 384} {
		padText(pvd[off:off+128], "")
	}
	for _, off := range []int{702, 739, 776} {
		padText(pvd[off:off+37], "")
	}
	for _, off := range []int{pvdCreatedOff, pvdModifiedOff, pvdExpiresOff, pvdEffectiveOff} {
		copy(pvd[off:off+17], "0000000000000000\x00")
	}
	pvd[pvdStructVerOff] = 1
}

func writeTerminator(t []byte) {
	t[pvdTypeOff] = 0xFF
	copy(t[pvdIDOff:], "CD001")
	t[pvdVersionOff] = 1
}

// pathTableSize is one record for the root: identifier length 1, no extended
// attributes, a 4-byte extent, a 2-byte parent, one identifier byte, one pad.
const pathTableSize = 10

// writePathTables emits the Type L and Type M path tables. Neither this reader
// nor Linux needs them to find a file — both walk directories — but they are
// mandatory, and tools that validate an image will complain without them.
func writePathTables(img []byte, rootSectors int64) {
	_ = rootSectors
	l := img[sectorPathTableL*sectorLen:]
	l[0] = 1 // identifier length
	l[1] = 0 // extended attribute length
	putLE32(l[2:], sectorRootDir)
	putLE16(l[6:], 1) // parent of the root is itself
	l[8] = idSelf

	m := img[sectorPathTableM*sectorLen:]
	m[0] = 1
	m[1] = 0
	putBE32(m[2:], sectorRootDir)
	putBE16(m[6:], 1)
	m[8] = idSelf
}

// padText writes an a-characters field, space padded as the standard requires
// (not NUL padded, which is why the reader trims spaces).
func padText(dst []byte, s string) {
	for i := range dst {
		dst[i] = ' '
	}
	copy(dst, s)
}

// ISO9660 stores most numbers twice, once in each byte order, so that a reader
// can use whichever it prefers.
func putBoth16(b []byte, v uint16) {
	putLE16(b, v)
	putBE16(b[2:], v)
}

func putBoth32(b []byte, v uint32) {
	putLE32(b, v)
	putBE32(b[4:], v)
}

func putLE16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }
func putBE16(b []byte, v uint16) { b[0], b[1] = byte(v>>8), byte(v) }

func putLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

func putBE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

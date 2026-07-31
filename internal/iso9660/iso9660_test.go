package iso9660

import (
	"errors"
	"strings"
	"testing"
)

// --- a hand-built ISO, so these tests need no tooling ---

type memDisk []byte

func (m memDisk) ReadAt(p []byte, off int64) error {
	if off < 0 || off+int64(len(p)) > int64(len(m)) {
		return errShort
	}
	copy(p, m[off:])
	return nil
}

var errShort = errors.New("read past end of image")

type entry struct {
	name    string // Rock Ridge name; "" means none, use isoName
	isoName string
	dir     bool
	lba     int64
	size    int64
}

// builder lays out sectors so tests can describe an image declaratively.
type builder struct{ img []byte }

func newBuilder(sectors int) *builder {
	return &builder{img: make([]byte, sectors*sectorLen)}
}

// pvd writes the primary volume descriptor pointing at a root directory.
func (b *builder) pvd(rootLBA, rootSize int64) {
	p := b.img[pvdOffset:]
	p[pvdTypeOff] = 1
	copy(p[pvdIDOff:], "CD001")
	putLE16(p[pvdBlockSizeOff:], sectorLen)
	rec := make([]byte, 34)
	rec[recLenOff] = 34
	putLE32(rec[recExtentOff:], uint32(rootLBA))
	putLE32(rec[recSizeOff:], uint32(rootSize))
	rec[recFlagsOff] = flagDirectory
	rec[recNameLenOff] = 1
	copy(p[pvdRootDirOff:], rec)
}

// directory writes a directory extent at lba, returning its byte length.
func (b *builder) directory(lba int64, entries []entry) int64 {
	off := lba * sectorLen
	cur := off
	// Real images start with the "." and ".." records; include them so the
	// scanner has to skip them as it would in practice.
	for _, self := range []byte{0, 1} {
		rec := dirRecord(entry{isoName: string([]byte{self}), dir: true, lba: lba, size: sectorLen}, "")
		rec[recNameLenOff] = 1
		rec[recNameOff] = self
		copy(b.img[cur:], rec)
		cur += int64(len(rec))
	}
	for _, e := range entries {
		rec := dirRecord(e, e.name)
		copy(b.img[cur:], rec)
		cur += int64(len(rec))
	}
	return cur - off
}

// file writes contents at lba and returns its length.
func (b *builder) file(lba int64, contents string) int64 {
	copy(b.img[lba*sectorLen:], contents)
	return int64(len(contents))
}

func dirRecord(e entry, rrName string) []byte {
	iso := e.isoName
	if iso == "" {
		iso = strings.ToUpper(strings.ReplaceAll(e.name, "-", "_")) + ".;1"
	}
	nameLen := len(iso)
	base := recNameOff + nameLen
	if base%2 == 1 {
		base++ // pad to even before the system use area
	}
	su := []byte{}
	if rrName != "" {
		su = append([]byte{'N', 'M', byte(5 + len(rrName)), 1, 0}, rrName...)
	}
	rec := make([]byte, base+len(su))
	rec[recLenOff] = byte(len(rec))
	putLE32(rec[recExtentOff:], uint32(e.lba))
	putLE32(rec[recSizeOff:], uint32(e.size))
	if e.dir {
		rec[recFlagsOff] = flagDirectory
	}
	rec[recNameLenOff] = byte(nameLen)
	copy(rec[recNameOff:], iso)
	copy(rec[base:], su)
	return rec
}

func putLE16(b []byte, v uint16) { b[0] = byte(v); b[1] = byte(v >> 8) }
func putLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// --- tests ---

// The NoCloud layout: user-data at the image root. The hyphen is what makes Rock
// Ridge necessary — plain ISO9660 mangles the name to USER_DAT.;1.
func TestReadFileAtRootUsingRockRidgeName(t *testing.T) {
	b := newBuilder(24)
	content := "#cloud-config\nruncmd: []\n"
	size := b.file(21, content)
	dirLen := b.directory(20, []entry{{name: "user-data", lba: 21, size: size}})
	b.pvd(20, dirLen)

	r, err := Open(memDisk(b.img))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadFile("user-data")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("content = %q, want %q", got, content)
	}
}

// The config-drive layout, which is what CAPK attaches: nested directories.
func TestReadFileNestedPath(t *testing.T) {
	b := newBuilder(32)
	content := `{"uuid":"i-1","hostname":"node-1"}`
	size := b.file(25, content)
	latestLen := b.directory(24, []entry{{name: "meta_data.json", lba: 25, size: size}})
	osLen := b.directory(23, []entry{{name: "latest", dir: true, lba: 24, size: latestLen}})
	rootLen := b.directory(20, []entry{{name: "openstack", dir: true, lba: 23, size: osLen}})
	b.pvd(20, rootLen)

	r, err := Open(memDisk(b.img))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadFile("openstack/latest/meta_data.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("content = %q, want %q", got, content)
	}
}

// Without Rock Ridge the stored name is the mangled ISO one, and ";1"/trailing
// dots must be stripped so a lookup can still match it.
func TestReadFileFallsBackToIsoName(t *testing.T) {
	b := newBuilder(24)
	size := b.file(21, "plain")
	dirLen := b.directory(20, []entry{{isoName: "USERDATA.;1", lba: 21, size: size}})
	b.pvd(20, dirLen)

	r, err := Open(memDisk(b.img))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.ReadFile("USERDATA"); err != nil || string(got) != "plain" {
		t.Errorf("ReadFile = %q, %v", got, err)
	}
}

// Layouts this reader does not implement must fail loudly. Silently returning
// the bytes at the wrong offset would hand k0s a corrupt config.
func TestUnsupportedLayoutsAreRefused(t *testing.T) {
	for name, off := range map[string]int{
		"interleaved":               recUnitSizeOff,
		"interleave gap":            recInterleaveOff,
		"extended attribute record": recEALenOff,
	} {
		t.Run(name, func(t *testing.T) {
			b := newBuilder(24)
			size := b.file(21, "content")
			dirLen := b.directory(20, []entry{{name: "user-data", lba: 21, size: size}})
			b.pvd(20, dirLen)
			// The third record in the extent is user-data.
			third := 20*sectorLen + int(b.img[20*sectorLen]) + int(b.img[20*sectorLen+int(b.img[20*sectorLen])])
			b.img[third+off] = 1

			r, err := Open(memDisk(b.img))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.ReadFile("user-data"); err == nil {
				t.Error("read a file with an unsupported layout instead of failing")
			}
		})
	}
}

func TestOpenRejectsNonISO(t *testing.T) {
	if _, err := Open(memDisk(make([]byte, 64<<10))); err == nil {
		t.Error("accepted an image with no volume descriptor")
	}
}

func TestReadFileMissing(t *testing.T) {
	b := newBuilder(24)
	dirLen := b.directory(20, nil)
	b.pvd(20, dirLen)
	r, err := Open(memDisk(b.img))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadFile("user-data"); err == nil {
		t.Error("ReadFile = nil error for an absent file")
	}
}

// A directory where a file is expected must be refused rather than read as one.
func TestReadFileRejectsDirectory(t *testing.T) {
	b := newBuilder(32)
	sub := b.directory(23, nil)
	rootLen := b.directory(20, []entry{{name: "openstack", dir: true, lba: 23, size: sub}})
	b.pvd(20, rootLen)

	r, err := Open(memDisk(b.img))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadFile("openstack"); err == nil {
		t.Error("read a directory as a file")
	}
}

// Malformed input must produce an error or a correct read, never a panic and
// never wrong bytes. This parses a device PID1 does not control, so a crash here
// is a boot failure.
func TestMalformedImagesDoNotPanic(t *testing.T) {
	b := newBuilder(24)
	want := "hello"
	size := b.file(21, want)
	dirLen := b.directory(20, []entry{{name: "user-data", lba: 21, size: size}})
	b.pvd(20, dirLen)
	good := b.img

	cases := map[string][]byte{
		"truncated record length": func() []byte {
			c := append([]byte(nil), good...)
			c[20*sectorLen] = 200 // claims a record longer than what follows
			return c
		}(),
		"name length overruns its record": func() []byte {
			c := append([]byte(nil), good...)
			c[20*sectorLen+recNameLenOff] = 255
			return c
		}(),
		"rock ridge entry length overruns": func() []byte {
			c := append([]byte(nil), good...)
			// The third record is user-data; its NM length byte sits after the
			// name, padded to even.
			base := 20*sectorLen + int(c[20*sectorLen]) + int(c[20*sectorLen+int(c[20*sectorLen])])
			for i := base; i < base+120; i++ {
				if c[i] == 'N' && c[i+1] == 'M' {
					c[i+2] = 255
					break
				}
			}
			return c
		}(),
		"root directory size implausible": func() []byte {
			c := append([]byte(nil), good...)
			putLE32(c[pvdOffset+pvdRootDirOff+recSizeOff:], 0xFFFFFFFF)
			return c
		}(),
		"root extent past end": func() []byte {
			c := append([]byte(nil), good...)
			putLE32(c[pvdOffset+pvdRootDirOff+recExtentOff:], 0xFFFF)
			return c
		}(),
	}
	for name, img := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := Open(memDisk(img))
			if err != nil {
				return // rejected at Open, which is fine
			}
			// An error is the expected outcome; a successful read is only
			// acceptable if the damage happened to be in an ignorable field and
			// the bytes are still right.
			got, err := r.ReadFile("user-data")
			if err == nil && string(got) != want {
				t.Errorf("ReadFile = %q with no error, want %q or a failure", got, want)
			}
		})
	}
}

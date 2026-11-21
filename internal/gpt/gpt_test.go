package gpt

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"
	"unicode/utf16"
)

// memDisk is a disk in memory; grow appends zeros, as a resized volume reads.
type memDisk struct{ b []byte }

func (m *memDisk) ReadAt(p []byte, off int64) (int, error) { return copy(p, m.b[off:]), nil }
func (m *memDisk) WriteAt(p []byte, off int64) (int, error) {
	return copy(m.b[off:off+int64(len(p))], p), nil
}
func (m *memDisk) grow(by int) { m.b = append(m.b, make([]byte, by)...) }

// newDisk is a sectors-long disk carrying a table made by Create.
func newDisk(sectors int, ss int64, parts ...[2]uint64) *memDisk {
	m := &memDisk{b: make([]byte, int64(sectors)*ss)}
	t := Table{}
	for _, p := range parts {
		t.Partitions = append(t.Partitions, Partition{Start: p[0], End: p[1]})
	}
	if err := Create(m, int64(len(m.b)), ss, t); err != nil {
		panic(err)
	}
	return m
}

// checkTable verifies a header at lba and its entries, as a firmware or the
// kernel would, and returns the header.
func checkTable(t *testing.T, m *memDisk, ss int64, lba uint64) []byte {
	t.Helper()
	hdr := m.b[int64(lba)*ss : int64(lba+1)*ss]
	if !bytes.Equal(hdr[:8], signature) || headerCRC(hdr[:92]) != le32(hdr, hdrCRC) {
		t.Fatalf("header at sector %d is not a valid GPT header", lba)
	}
	if le64(hdr, hdrMyLBA) != lba {
		t.Errorf("header at sector %d says it is at %d", lba, le64(hdr, hdrMyLBA))
	}
	off := int64(le64(hdr, hdrEntryLBA)) * ss
	if crc32.ChecksumIEEE(m.b[off:off+128*128]) != le32(hdr, hdrEntryCRC) {
		t.Errorf("entries for the header at sector %d fail their checksum", lba)
	}
	return hdr
}

// The metal image's layout: ESP, root, data last. A volume resized from 8 to 24
// MiB must end with the data partition reaching the new last usable sector and
// both tables valid -- the backup moved to the new end.
func TestGrowExtendsTheLastPartitionAndMovesTheBackup(t *testing.T) {
	for _, ss := range []int64{512, 4096} {
		sectors := int(8 << 20 / ss)
		m := newDisk(sectors, ss, [2]uint64{40, 99}, [2]uint64{100, 199}, [2]uint64{200, uint64(sectors) - 100})
		m.grow(16 << 20)

		res, err := Grow(m, int64(len(m.b)), ss, 3)
		if err != nil {
			t.Fatalf("%d-byte sectors: %v", ss, err)
		}
		total := uint64(len(m.b)) / uint64(ss)
		arr := (128*128 + uint64(ss) - 1) / uint64(ss)
		if want := total - 1 - arr - 1; !res.Grown() || res.NewEnd != want || res.Start != 200 {
			t.Errorf("%d-byte sectors: result %+v, want start 200 and end %d", ss, res, want)
		}
		primary := checkTable(t, m, ss, 1)
		backup := checkTable(t, m, ss, total-1)
		if le64(primary, hdrAltLBA) != total-1 || le64(backup, hdrAltLBA) != 1 {
			t.Errorf("%d-byte sectors: headers do not point at each other", ss)
		}
		if le64(primary, hdrLastLBA) != res.NewEnd {
			t.Errorf("%d-byte sectors: last usable %d, partition ends %d", ss, le64(primary, hdrLastLBA), res.NewEnd)
		}
		if got := le32(m.b, 446+12); got != uint32(total-1) {
			t.Errorf("%d-byte sectors: protective MBR covers %d sectors, want %d", ss, got, total-1)
		}
		if got := le64(m.b[2*ss+2*128:], entLastLBA); got != res.NewEnd {
			t.Errorf("%d-byte sectors: entry ends at %d, want %d", ss, got, res.NewEnd)
		}

		// Every later boot: nothing to do, and nothing written.
		before := bytes.Clone(m.b)
		if res, err := Grow(m, int64(len(m.b)), ss, 3); err != nil || res.Grown() {
			t.Errorf("%d-byte sectors: second Grow = %+v, %v; want a no-op", ss, res, err)
		}
		if !bytes.Equal(before, m.b) {
			t.Errorf("%d-byte sectors: a no-op Grow wrote to the disk", ss)
		}
	}
}

func TestGrowRefuses(t *testing.T) {
	cases := []struct {
		name string
		disk func() *memDisk
		part int
		want string
	}{
		{"not the last partition", func() *memDisk {
			m := newDisk(16384, 512, [2]uint64{40, 99}, [2]uint64{100, 199})
			m.grow(1 << 20)
			return m
		}, 1, "not the last"},
		{"a corrupt header", func() *memDisk {
			m := newDisk(16384, 512, [2]uint64{40, 99})
			m.b[512+hdrLastLBA]++
			return m
		}, 1, "checksum"},
		{"corrupt entries", func() *memDisk {
			m := newDisk(16384, 512, [2]uint64{40, 99})
			m.b[2*512+entFirstLBA]++
			return m
		}, 1, "checksum"},
		{"an unused slot", func() *memDisk { return newDisk(16384, 512, [2]uint64{40, 99}) }, 2, "unused"},
		{"a disk that shrank", func() *memDisk {
			m := newDisk(16384, 512, [2]uint64{40, 99})
			m.b = m.b[:len(m.b)-1<<20]
			return m
		}, 1, "smaller"},
		{"no GPT at all", func() *memDisk { return &memDisk{b: make([]byte, 1<<20)} }, 1, "no GUID"},
	}
	for _, c := range cases {
		m := c.disk()
		before := bytes.Clone(m.b)
		_, err := Grow(m, int64(len(m.b)), 512, c.part)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Grow = %v, want an error mentioning %q", c.name, err, c.want)
		}
		if !bytes.Equal(before, m.b) {
			t.Errorf("%s: the disk was written to", c.name)
		}
	}
}

// A partition followed by another is not news on every boot: only when the disk
// has grown past its table is it reported that this one cannot take the room.
func TestGrowNotLastIsQuietUntilTheDiskGrows(t *testing.T) {
	m := newDisk(16384, 512, [2]uint64{40, 99}, [2]uint64{100, 199})
	if res, err := Grow(m, int64(len(m.b)), 512, 1); err != nil || res.Grown() {
		t.Errorf("same-size disk: %+v, %v; want a quiet no-op", res, err)
	}
	m.grow(1 << 20)
	if _, err := Grow(m, int64(len(m.b)), 512, 1); err == nil || !strings.Contains(err.Error(), "not the last") {
		t.Errorf("grown disk: Grow = %v, want it to say the partition is not last", err)
	}
}

// A GUID's first three fields are stored byte-swapped and its last two are not,
// so a GUID that round-trips through hex alone would name a different type.
func TestParseGUIDSwapsTheFirstThreeFields(t *testing.T) {
	if got := TypeLinux; got != (GUID{0xaf, 0x3d, 0xc6, 0x0f, 0x83, 0x84, 0x72, 0x47,
		0x8e, 0x79, 0x3d, 0x69, 0xd8, 0x47, 0x7d, 0xe4}) {
		t.Errorf("0FC63DAF-8483-4772-8E79-3D69D8477DE4 became %x", got[:])
	}
	for _, s := range []string{"", "nonsense", "0FC63DAF84834772-8E79-3D69D8477DE4"} {
		if _, err := ParseGUID(s); err == nil {
			t.Errorf("ParseGUID(%q) was accepted", s)
		}
	}
}

// Create writes the type, name and GUID of each partition, which is how the
// node finds its root and data volumes at all -- blkid looks for the label.
func TestCreateWritesTypesAndNames(t *testing.T) {
	m := &memDisk{b: make([]byte, 16<<20)}
	err := Create(m, int64(len(m.b)), 512, Table{
		DiskGUID: MustGUID("c0dec0de-0000-4000-8000-000000000010"),
		Partitions: []Partition{
			{Start: 2048, End: 4095, Type: TypeESP, GUID: MustGUID("c0dec0de-0000-4000-8000-000000000011"), Name: "K0SMOS-BOOT"},
			{Start: 4096, End: 8191, Name: "K0SMOS-ROOT"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	ent := m.b[2*512:]
	if got := GUID(ent[:16]); got != TypeESP {
		t.Errorf("partition 1 type is %x, want the ESP type", got[:])
	}
	if got := GUID(ent[128:144]); got != TypeLinux {
		t.Errorf("a zero Type gave %x, want TypeLinux", got[:])
	}
	for i, want := range []string{"K0SMOS-BOOT", "K0SMOS-ROOT"} {
		var name []uint16
		for k := 0; k < len(want); k++ {
			name = append(name, binary.LittleEndian.Uint16(ent[i*128+entName+2*k:]))
		}
		if got := string(utf16.Decode(name)); got != want {
			t.Errorf("partition %d is named %q, want %q", i+1, got, want)
		}
	}
	if got := string(m.b[512+hdrDiskGUID : 512+hdrDiskGUID+4]); got != "\xde\xc0\xde\xc0" {
		t.Errorf("disk GUID starts %x, want the parsed c0dec0de", got)
	}
}

// A table that contradicts itself is worse than a build that stops: the image
// boots far enough to mount the wrong bytes.
func TestCreateRefusesABadLayout(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts []Partition
		want  string
	}{
		{"overlapping", []Partition{{Start: 2048, End: 4095}, {Start: 4000, End: 8191}}, "overlaps"},
		{"before the first usable sector", []Partition{{Start: 2, End: 4095}}, "outside the usable"},
		{"past the last usable sector", []Partition{{Start: 2048, End: 16 << 20 / 512}}, "outside the usable"},
		{"inverted", []Partition{{Start: 4095, End: 2048}}, "outside the usable"},
		{"over-long name", []Partition{{Start: 2048, End: 4095, Name: strings.Repeat("x", 37)}}, "36 characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &memDisk{b: make([]byte, 16<<20)}
			err := Create(m, int64(len(m.b)), 512, Table{Partitions: tc.parts})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Create: %v, want an error about %q", err, tc.want)
			}
		})
	}
}

// The protective MBR must be the bytes sgdisk writes, which is how an image
// assembled here stays comparable with one assembled by sgdisk. These are read
// off a real k0smos metal image: a 5625856-sector disk.
func TestProtectiveMBRMatchesSgdisk(t *testing.T) {
	const sectors = 5625856
	m := &memDisk{b: make([]byte, sectors*512)}
	if err := Create(m, int64(len(m.b)), 512, Table{Partitions: []Partition{{Start: 2048, End: 4095}}}); err != nil {
		t.Fatal(err)
	}
	want := []byte{
		0x00, 0x00, 0x02, 0x00, // boot flag, then the first sector's CHS
		0xee, 0x31, 0x53, 0x5e, // the GPT type, then the last sector's CHS
		0x01, 0x00, 0x00, 0x00, // first LBA
		0xff, 0xd7, 0x55, 0x00, // sectors: the whole disk bar the MBR
	}
	if got := m.b[446:462]; !bytes.Equal(got, want) {
		t.Errorf("protective MBR entry is % x, want % x", got, want)
	}
	if m.b[510] != 0x55 || m.b[511] != 0xaa {
		t.Error("the MBR has no signature, so Linux ignores the GPT altogether")
	}
	// A disk past the 1023-cylinder limit gets the largest CHS the fields hold.
	if got := chs(1 << 40); got != [3]byte{254, 0xff, 0xff} {
		t.Errorf("a huge LBA gives CHS %v, want it capped at 1023/254/63", got)
	}
}

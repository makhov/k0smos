package gpt

import (
	"bytes"
	"hash/crc32"
	"strings"
	"testing"
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
	if err := Create(m, int64(len(m.b)), ss, parts...); err != nil {
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

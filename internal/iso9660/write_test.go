package iso9660

import (
	"bytes"
	"strings"
	"testing"
)

// build is the round trip: write an image, then read it back with this package's
// own reader. That catches any disagreement between the two halves, which is what
// matters most — k0smos reads these drives with exactly this reader.
func build(t *testing.T, label string, files []File) (*Reader, []byte) {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, label, files); err != nil {
		t.Fatalf("Write: %v", err)
	}
	img := buf.Bytes()
	if len(img)%sectorLen != 0 {
		t.Fatalf("image is %d bytes, not a whole number of %d-byte sectors", len(img), sectorLen)
	}
	r, err := Open(memDisk(img))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r, img
}

// The NoCloud drive k0smosctl generates, read back by the reader that runs on the
// node. The hyphen in "user-data" is the whole reason Rock Ridge is written.
func TestWriteRoundTripsNoCloudDrive(t *testing.T) {
	want := map[string]string{
		"user-data": "#cloud-config\nwrite_files:\n  - path: /etc/k0s/k0s.yaml\n    content: cfg\n",
		"meta-data": "instance-id: demo\nlocal-hostname: demo-node\n",
	}
	files := []File{
		{Name: "user-data", Data: []byte(want["user-data"])},
		{Name: "meta-data", Data: []byte(want["meta-data"])},
	}
	r, _ := build(t, "cidata", files)

	for name, content := range want {
		got, err := r.ReadFile(name)
		if err != nil {
			t.Errorf("ReadFile(%q): %v", name, err)
			continue
		}
		if string(got) != content {
			t.Errorf("%s = %q, want %q", name, got, content)
		}
	}
}

// blkid finds the drive by the volume identifier, so a wrong or unpadded label
// means k0smos never looks at the disk at all.
func TestWriteSetsTheVolumeLabel(t *testing.T) {
	_, img := build(t, "cidata", []File{{Name: "user-data", Data: []byte("x")}})

	// Volume identifier: 32 bytes at offset 40 of the PVD, space padded.
	field := img[pvdOffset+pvdVolumeIDOff : pvdOffset+pvdVolumeIDOff+32]
	if got := strings.TrimRight(string(field), " "); got != "cidata" {
		t.Errorf("volume label = %q, want cidata", got)
	}
	if bytes.Contains(field, []byte{0}) {
		t.Error("label field is NUL padded; the standard requires spaces")
	}
}

// Linux only honours Rock Ridge when the "." record of the root carries an SP
// entry. Without it every name reads back mangled — and since this package's own
// reader does not require SP, only an explicit check catches its absence.
func TestWriteEmitsTheSUSPIndicator(t *testing.T) {
	_, img := build(t, "cidata", []File{{Name: "user-data", Data: []byte("x")}})

	root := img[sectorRootDir*sectorLen:]
	selfLen := int(root[recLenOff])
	if !bytes.Contains(root[:selfLen], []byte{'S', 'P', 7, 1, 0xBE, 0xEF}) {
		t.Errorf("no SP entry in the root's \".\" record:\n% x", root[:selfLen])
	}
}

// SP alone is not enough. Rock Ridge must also be *declared* with an ER entry, or
// a conforming reader ignores the NM names — xorriso reported every name mangled
// while this package's own reader was perfectly happy, because it only looks for
// NM. This test exists because the round trip could not see the difference.
func TestWriteDeclaresRockRidgeWithER(t *testing.T) {
	_, img := build(t, "cidata", []File{{Name: "user-data", Data: []byte("x")}})

	root := img[sectorRootDir*sectorLen:]
	self := root[:int(root[recLenOff])]
	if !bytes.Contains(self, []byte("RRIP_1991A")) {
		t.Errorf("no ER entry declaring Rock Ridge in the root's \".\" record:\n% x", self)
	}
	// The ER header must precede the identifier, with the length fields the
	// standard specifies; a bare string would satisfy the check above.
	i := bytes.Index(self, []byte{'E', 'R'})
	if i < 0 {
		t.Fatal("no ER signature")
	}
	if got, want := self[i+2], byte(8+len("RRIP_1991A")); got != want {
		t.Errorf("ER length = %d, want %d", got, want)
	}
	if got := self[i+4]; got != byte(len("RRIP_1991A")) {
		t.Errorf("ER LEN_ID = %d, want %d", got, len("RRIP_1991A"))
	}
}

// A file bigger than one sector must still read back whole: its extent spans
// several blocks, and the following file must not be laid on top of it.
func TestWriteHandlesMultiSectorFiles(t *testing.T) {
	big := bytes.Repeat([]byte("k0smos manifest line\n"), 500) // ~10KB
	r, _ := build(t, "cidata", []File{
		{Name: "user-data", Data: big},
		{Name: "meta-data", Data: []byte("instance-id: demo\n")},
	})

	got, err := r.ReadFile("user-data")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, big) {
		t.Errorf("read %d bytes, want %d", len(got), len(big))
	}
	// The neighbour must be intact, which is what proves the extents do not overlap.
	if got, err := r.ReadFile("meta-data"); err != nil || string(got) != "instance-id: demo\n" {
		t.Errorf("meta-data = %q, %v", got, err)
	}
}

// Same inputs, same bytes: a generated drive should not differ run to run, so a
// rebuild is a no-op and a diff means something really changed.
func TestWriteIsDeterministic(t *testing.T) {
	files := []File{
		{Name: "user-data", Data: []byte("#cloud-config\n")},
		{Name: "meta-data", Data: []byte("instance-id: demo\n")},
	}
	var a, b bytes.Buffer
	if err := Write(&a, "cidata", files); err != nil {
		t.Fatal(err)
	}
	// Reversed, to show the output does not depend on argument order either.
	if err := Write(&b, "cidata", []File{files[1], files[0]}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("output differs between runs or with argument order")
	}
}

// Enough files that the root directory outgrows one sector. Records may not
// straddle a logical block, so this is where a naive writer produces an image
// that reads back truncated.
func TestWriteHandlesADirectorySpanningSectors(t *testing.T) {
	var files []File
	for i := range 40 {
		files = append(files, File{
			Name: "manifest-" + strings.Repeat("x", 20) + string(rune('a'+i)),
			Data: []byte{byte(i)},
		})
	}
	r, img := build(t, "cidata", files)
	if len(img) < (sectorRootDir+2)*sectorLen {
		t.Fatalf("root directory did not span sectors; image is only %d bytes", len(img))
	}
	for i, f := range files {
		got, err := r.ReadFile(f.Name)
		if err != nil {
			t.Errorf("ReadFile(%q): %v", f.Name, err)
			continue
		}
		if len(got) != 1 || got[0] != byte(i) {
			t.Errorf("%s = % x, want %02x", f.Name, got, i)
		}
	}
}

func TestWriteRejectsWhatItCannotRepresent(t *testing.T) {
	cases := map[string][]File{
		"no files":       {},
		"empty name":     {{Name: "", Data: []byte("x")}},
		"subdirectory":   {{Name: "openstack/latest/user_data", Data: []byte("x")}},
		"relative path":  {{Name: "../escape", Data: []byte("x")}},
		"too many files": make([]File, maxFiles+1),
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Write(&bytes.Buffer{}, "cidata", files); err == nil {
				t.Error("Write accepted input it cannot represent")
			}
		})
	}
}

// An empty file is legal and must not be confused with a missing one.
func TestWriteHandlesEmptyFile(t *testing.T) {
	r, _ := build(t, "cidata", []File{
		{Name: "user-data", Data: []byte("#cloud-config\n")},
		{Name: "meta-data", Data: nil},
	})
	got, err := r.ReadFile("meta-data")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("meta-data = %q, want empty", got)
	}
}

// The mangled ISO name must stay inside the Level 1 charset, since that is the
// name any reader without Rock Ridge will see.
func TestIsoNameStaysInCharset(t *testing.T) {
	got := isoName("user-data")
	if got != "USER_DATA;1" {
		t.Errorf("isoName(user-data) = %q, want USER_DATA;1", got)
	}
	for _, c := range strings.TrimSuffix(got, ";1") {
		ok := c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.'
		if !ok {
			t.Errorf("character %q is outside the ISO9660 Level 1 charset", c)
		}
	}
}

package blkid

import (
	"testing"
)

// iso9660Image builds the 2048-byte primary volume descriptor at sector 16 that
// identifies an ISO, which is what cloud-init NoCloud drives are.
func iso9660Image(label string) []byte {
	img := make([]byte, isoPVDOffset+2048)
	pvd := img[isoPVDOffset:]
	pvd[0] = 1 // primary volume descriptor
	copy(pvd[1:6], "CD001")
	// Volume identifier is space-padded, not NUL-padded.
	for i := range 32 {
		pvd[isoLabelOff+i] = ' '
	}
	copy(pvd[isoLabelOff:], label)
	return img
}

// fat32Image builds a FAT32 boot sector.
func fat32Image(label string, serial []byte) []byte {
	img := make([]byte, 1024)
	copy(img[fat32TypeOff:], "FAT32   ")
	for i := range 11 {
		img[fat32LabelOff+i] = ' '
	}
	copy(img[fat32LabelOff:], label)
	copy(img[fat32SerialOff:], serial)
	img[510], img[511] = 0x55, 0xaa
	return img
}

func fat16Image(label string) []byte {
	img := make([]byte, 1024)
	copy(img[fat16TypeOff:], "FAT16   ")
	for i := range 11 {
		img[fat16LabelOff+i] = ' '
	}
	copy(img[fat16LabelOff:], label)
	img[510], img[511] = 0x55, 0xaa
	return img
}

// KubeVirt presents cloud-init NoCloud data as an ISO labelled "cidata"; the
// OpenStack config-drive variant uses vfat labelled "config-2". Both must
// resolve, or CAPI bootstrap data cannot be found.
func TestResolveFindsISO9660Label(t *testing.T) {
	p := &fakeProber{
		devs:   []string{"vda", "vdb"},
		images: map[string][]byte{"vda": superblock(uuidB, "root", ext4Magic), "vdb": iso9660Image("cidata")},
	}
	got, err := Resolve(p, "LABEL=cidata")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/dev/vdb" {
		t.Errorf("got %q, want /dev/vdb", got)
	}
}

func TestResolveFindsFAT32Label(t *testing.T) {
	p := &fakeProber{
		devs:   []string{"vdb"},
		images: map[string][]byte{"vdb": fat32Image("config-2", []byte{0x78, 0x56, 0x34, 0x12})},
	}
	got, err := Resolve(p, "LABEL=config-2")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/dev/vdb" {
		t.Errorf("got %q, want /dev/vdb", got)
	}
}

func TestResolveFindsFAT16Label(t *testing.T) {
	p := &fakeProber{
		devs:   []string{"vdb"},
		images: map[string][]byte{"vdb": fat16Image("cidata")},
	}
	if got, err := Resolve(p, "LABEL=cidata"); err != nil || got != "/dev/vdb" {
		t.Errorf("got %q, %v; want /dev/vdb, nil", got, err)
	}
}

// FAT serial numbers are conventionally shown as XXXX-XXXX, little-endian.
func TestResolveFindsFAT32BySerial(t *testing.T) {
	p := &fakeProber{
		devs:   []string{"vdb"},
		images: map[string][]byte{"vdb": fat32Image("cidata", []byte{0x78, 0x56, 0x34, 0x12})},
	}
	got, err := Resolve(p, "UUID=1234-5678")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/dev/vdb" {
		t.Errorf("got %q, want /dev/vdb", got)
	}
}

// A label shorter than the field is padded; the padding must not be returned.
func TestProbesTrimPadding(t *testing.T) {
	if _, label, ok := probeISO9660(iso9660Image("cidata")); !ok || label != "cidata" {
		t.Errorf("iso label = %q, %t; want cidata, true", label, ok)
	}
	if _, label, ok := probeFAT(fat32Image("config-2", []byte{1, 2, 3, 4})); !ok || label != "config-2" {
		t.Errorf("fat label = %q, %t; want config-2, true", label, ok)
	}
}

// Random data must not be mistaken for a filesystem.
func TestProbesRejectNonFilesystems(t *testing.T) {
	junk := make([]byte, isoPVDOffset+2048)
	for i := range junk {
		junk[i] = byte(i % 251)
	}
	if _, _, ok := probeISO9660(junk); ok {
		t.Error("iso probe matched junk")
	}
	if _, _, ok := probeFAT(junk); ok {
		t.Error("fat probe matched junk")
	}
	if _, _, ok := probeExt4(junk); ok {
		t.Error("ext4 probe matched junk")
	}
}

package blkid

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// superblock builds a minimal ext4 superblock image: 1024 bytes of padding
// (boot block) followed by the superblock itself.
func superblock(uuid [16]byte, label string, magic uint16) []byte {
	img := make([]byte, 2048)
	sb := img[sbOffset:]
	binary.LittleEndian.PutUint16(sb[sbMagicOff:], magic)
	copy(sb[sbUUIDOff:], uuid[:])
	copy(sb[sbLabelOff:], label)
	return img
}

type fakeProber struct {
	devs    []string
	images  map[string][]byte
	listErr error
}

func (f *fakeProber) BlockDevices() ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.devs, nil
}

func (f *fakeProber) ReadAt(dev string, p []byte, off int64) error {
	img, ok := f.images[dev]
	if !ok {
		return errors.New("no such device")
	}
	if off+int64(len(p)) > int64(len(img)) {
		return errors.New("short read")
	}
	copy(p, img[off:])
	return nil
}

var (
	uuidA = [16]byte{0x7b, 0x53, 0x1f, 0x44, 0x1a, 0xfe, 0x47, 0x51, 0xa7, 0xc1, 0x46, 0xdb, 0xd5, 0x7c, 0x8f, 0x2e}
	uuidB = [16]byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00}
)

func newProber() *fakeProber {
	return &fakeProber{
		devs: []string{"vda", "vdb", "loop0"},
		images: map[string][]byte{
			"vda":   superblock(uuidB, "other", ext4Magic),
			"vdb":   superblock(uuidA, "k0smos", ext4Magic),
			"loop0": make([]byte, 2048), // no filesystem: zero magic
		},
	}
}

// A plain device path must be used as given; only UUID=/LABEL= need resolving.
func TestResolvePassesThroughDevicePaths(t *testing.T) {
	for _, spec := range []string{"/dev/vda", "/dev/nvme0n1p2", ""} {
		got, err := Resolve(newProber(), spec)
		if err != nil || got != spec {
			t.Errorf("Resolve(%q) = %q, %v; want %q, nil", spec, got, err, spec)
		}
	}
}

func TestResolveByUUID(t *testing.T) {
	got, err := Resolve(newProber(), "UUID=7b531f44-1afe-4751-a7c1-46dbd57c8f2e")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/dev/vdb" {
		t.Errorf("got %q, want /dev/vdb", got)
	}
}

// Hex case must not matter: fstab and cloud metadata both emit uppercase.
func TestResolveByUUIDIsCaseInsensitive(t *testing.T) {
	got, err := Resolve(newProber(), "UUID=7B531F44-1AFE-4751-A7C1-46DBD57C8F2E")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/dev/vdb" {
		t.Errorf("got %q, want /dev/vdb", got)
	}
}

func TestResolveByLabel(t *testing.T) {
	got, err := Resolve(newProber(), "LABEL=k0smos")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/dev/vdb" {
		t.Errorf("got %q, want /dev/vdb", got)
	}
}

func TestResolveUnknownReportsWhatItLookedFor(t *testing.T) {
	_, err := Resolve(newProber(), "UUID=00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("Resolve = nil error, want not-found")
	}
	if !strings.Contains(err.Error(), "00000000-0000-0000-0000-000000000000") {
		t.Errorf("error %q should name the spec it could not find", err)
	}
}

// A device with no ext4 superblock must be skipped, not misread as a match.
func TestResolveSkipsDevicesWithoutExt4(t *testing.T) {
	p := newProber()
	p.devs = []string{"loop0"}
	if _, err := Resolve(p, "LABEL=k0smos"); err == nil {
		t.Error("matched a device with no ext4 magic")
	}
}

// An unreadable device (no media, permissions) must not abort the whole scan.
func TestResolveContinuesPastUnreadableDevices(t *testing.T) {
	p := newProber()
	p.devs = []string{"sr0", "vdb"} // sr0 has no image entry -> read error
	got, err := Resolve(p, "LABEL=k0smos")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/dev/vdb" {
		t.Errorf("got %q, want /dev/vdb", got)
	}
}

// virtio_blk probes asynchronously, so the root device can appear a moment
// after the module is loaded. Resolution must wait rather than fail the boot.
func TestResolveWaitRetriesUntilDeviceAppears(t *testing.T) {
	p := &fakeProber{devs: nil, images: map[string][]byte{}}
	calls := 0
	sleep := func() {
		calls++
		if calls == 3 {
			p.devs = []string{"vdb"}
			p.images["vdb"] = superblock(uuidA, "k0smos", ext4Magic)
		}
	}
	got, err := ResolveWait(p, "LABEL=k0smos", 10, sleep)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/dev/vdb" {
		t.Errorf("got %q, want /dev/vdb", got)
	}
}

func TestResolveWaitGivesUpEventually(t *testing.T) {
	p := &fakeProber{images: map[string][]byte{}}
	slept := 0
	_, err := ResolveWait(p, "LABEL=nope", 4, func() { slept++ })
	if err == nil {
		t.Fatal("want error after exhausting attempts")
	}
	if slept != 4 {
		t.Errorf("slept %d times, want 4", slept)
	}
}

// A pass-through path must not be delayed by retries.
func TestResolveWaitDoesNotRetryDevicePaths(t *testing.T) {
	slept := 0
	got, err := ResolveWait(newProber(), "/dev/vda", 5, func() { slept++ })
	if err != nil || got != "/dev/vda" || slept != 0 {
		t.Errorf("got %q, %v, slept %d; want /dev/vda, nil, 0", got, err, slept)
	}
}

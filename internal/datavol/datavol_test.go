package datavol

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// ext4 image, enough for blkid's probe to recognise it.
func ext4(label string) []byte {
	img := make([]byte, 2048)
	sb := img[1024:]
	binary.LittleEndian.PutUint16(sb[0x38:], 0xEF53)
	copy(sb[0x78:], label)
	return img
}

func blank() []byte { return make([]byte, 2048) }

type fakeVol struct {
	devs    []string
	images  map[string][]byte
	mkfs    []string // "dev:fstype:label"
	mounted []string // "dev->target:fstype"
	mkfsErr error
}

func (f *fakeVol) BlockDevices() ([]string, error) { return f.devs, nil }

func (f *fakeVol) ReadAt(dev string, p []byte, off int64) error {
	img, ok := f.images[dev]
	if !ok {
		return errors.New("no device")
	}
	if off+int64(len(p)) > int64(len(img)) {
		return errors.New("short")
	}
	copy(p, img[off:])
	return nil
}

func (f *fakeVol) Mkfs(dev, fstype, label string) error {
	if f.mkfsErr != nil {
		return f.mkfsErr
	}
	f.mkfs = append(f.mkfs, dev+":"+fstype+":"+label)
	// A formatted device now carries the label, as a real mkfs would leave it.
	f.images[strings.TrimPrefix(dev, "/dev/")] = ext4(label)
	return nil
}

func (f *fakeVol) Mkdir(string, fs.FileMode) error { return nil }

func (f *fakeVol) Mount(source, target, fstype string, _ uintptr, _ string) error {
	f.mounted = append(f.mounted, source+"->"+target+":"+fstype)
	return nil
}

const label = "k0smos-data"

// The normal steady state: the volume was formatted on an earlier boot, so it is
// found by label and mounted untouched.
func TestPrepareMountsAlreadyFormattedVolume(t *testing.T) {
	f := &fakeVol{
		devs:   []string{"vda", "vdb"},
		images: map[string][]byte{"vda": ext4("k0smos"), "vdb": ext4(label)},
	}
	res, err := Prepare(f, Options{Spec: "auto", Label: label, FSType: "ext4", MountPoint: "/var/lib/k0s"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Formatted {
		t.Error("reformatted an already-formatted volume — this destroys a cluster")
	}
	if len(f.mkfs) != 0 {
		t.Errorf("mkfs ran: %v", f.mkfs)
	}
	if len(f.mounted) != 1 || f.mounted[0] != "/dev/vdb->/var/lib/k0s:ext4" {
		t.Errorf("mounted = %v", f.mounted)
	}
}

// First boot: exactly one device has no filesystem, so it is the data volume.
func TestPrepareFormatsTheSingleBlankDevice(t *testing.T) {
	f := &fakeVol{
		devs: []string{"vda", "vdb", "vdc"},
		images: map[string][]byte{
			"vda": ext4("k0smos"),  // the root
			"vdb": iso("config-2"), // the cloud-init drive
			"vdc": blank(),         // the data volume
		},
	}
	res, err := Prepare(f, Options{Spec: "auto", Label: label, FSType: "ext4", MountPoint: "/var/lib/k0s"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Formatted {
		t.Error("did not format the blank volume")
	}
	if len(f.mkfs) != 1 || f.mkfs[0] != "/dev/vdc:ext4:"+label {
		t.Errorf("mkfs = %v, want /dev/vdc", f.mkfs)
	}
	if len(f.mounted) != 1 || !strings.HasPrefix(f.mounted[0], "/dev/vdc->") {
		t.Errorf("mounted = %v", f.mounted)
	}
}

// Ambiguity must never be resolved by guessing: formatting the wrong disk is
// unrecoverable.
func TestPrepareRefusesWhenSeveralDevicesAreBlank(t *testing.T) {
	f := &fakeVol{
		devs: []string{"vda", "vdb", "vdc"},
		images: map[string][]byte{
			"vda": ext4("k0smos"),
			"vdb": blank(),
			"vdc": blank(),
		},
	}
	_, err := Prepare(f, Options{Spec: "auto", Label: label, FSType: "ext4", MountPoint: "/var/lib/k0s"})
	if err == nil {
		t.Fatal("Prepare = nil, want a refusal when the choice is ambiguous")
	}
	if len(f.mkfs) != 0 {
		t.Errorf("formatted something despite ambiguity: %v", f.mkfs)
	}
}

// No blank device and no labelled one: nothing to do, and that is not an error —
// a machine may simply have no data volume attached.
func TestPrepareSkipsWhenNoCandidate(t *testing.T) {
	f := &fakeVol{devs: []string{"vda"}, images: map[string][]byte{"vda": ext4("k0smos")}}
	res, err := Prepare(f, Options{Spec: "auto", Label: label, FSType: "ext4", MountPoint: "/var/lib/k0s"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Device != "" || res.Formatted {
		t.Errorf("res = %+v, want nothing done", res)
	}
	if len(f.mounted) != 0 {
		t.Errorf("mounted something: %v", f.mounted)
	}
}

// An explicit device is honoured, but still never reformatted if it already has
// a filesystem.
func TestPrepareExplicitDevice(t *testing.T) {
	f := &fakeVol{
		devs:   []string{"vda", "vdb"},
		images: map[string][]byte{"vda": ext4("k0smos"), "vdb": blank()},
	}
	res, err := Prepare(f, Options{Spec: "/dev/vdb", Label: label, FSType: "ext4", MountPoint: "/var/lib/k0s"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Formatted || len(f.mkfs) != 1 {
		t.Errorf("expected a format of the explicit blank device, got %+v / %v", res, f.mkfs)
	}
}

func TestPrepareExplicitDeviceAlreadyFormattedIsNotTouched(t *testing.T) {
	f := &fakeVol{
		devs:   []string{"vdb"},
		images: map[string][]byte{"vdb": ext4("somebody-elses-data")},
	}
	res, err := Prepare(f, Options{Spec: "/dev/vdb", Label: label, FSType: "ext4", MountPoint: "/var/lib/k0s"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Formatted || len(f.mkfs) != 0 {
		t.Fatalf("reformatted a populated device: %v", f.mkfs)
	}
	if len(f.mounted) != 1 {
		t.Errorf("should still have mounted it: %v", f.mounted)
	}
}

// Disabled by default: no spec means no data volume handling at all.
func TestPrepareDisabledWhenSpecEmpty(t *testing.T) {
	f := &fakeVol{devs: []string{"vdb"}, images: map[string][]byte{"vdb": blank()}}
	res, err := Prepare(f, Options{Spec: "", Label: label, FSType: "ext4", MountPoint: "/var/lib/k0s"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Device != "" || len(f.mkfs) != 0 || len(f.mounted) != 0 {
		t.Errorf("did something while disabled: %+v %v %v", res, f.mkfs, f.mounted)
	}
}

// A failed mkfs must be reported, not silently mounted over.
func TestPrepareReportsMkfsFailure(t *testing.T) {
	f := &fakeVol{
		devs:    []string{"vdb"},
		images:  map[string][]byte{"vdb": blank()},
		mkfsErr: errors.New("mkfs.ext4: not found"),
	}
	if _, err := Prepare(f, Options{Spec: "auto", Label: label, FSType: "ext4", MountPoint: "/var/lib/k0s"}); err == nil {
		t.Fatal("Prepare = nil, want the mkfs failure reported")
	}
	if len(f.mounted) != 0 {
		t.Errorf("mounted despite a failed format: %v", f.mounted)
	}
}

// iso builds a minimal iso9660 so the cloud-init drive is recognised and thus
// never treated as blank.
func iso(vol string) []byte {
	img := make([]byte, 32768+2048)
	pvd := img[32768:]
	pvd[0] = 1
	copy(pvd[1:6], "CD001")
	for i := range 32 {
		pvd[40+i] = ' '
	}
	copy(pvd[40:], vol)
	return img
}

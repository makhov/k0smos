package datavol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/amakhov/k0smos/internal/gpt"
)

// ext4 image, enough for blkid's probe to recognise it.
func ext4(label string) []byte {
	img := make([]byte, 2048)
	sb := img[1024:]
	binary.LittleEndian.PutUint16(sb[0x38:], 0xEF53)
	copy(sb[0x78:], label)
	return img
}

func blank() []byte { return make([]byte, 1<<20) } // blkid.IsBlank reads a MiB

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

func (f *fakeVol) Mount(source, target, fstype string, flags uintptr, _ string) error {
	m := source + "->" + target + ":" + fstype
	if flags&msReadOnly != 0 {
		m += ":ro"
	}
	f.mounted = append(f.mounted, m)
	return nil
}

// used is a disk k0smos cannot read but must not touch: a GPT here, standing in
// for LVM, btrfs, md -- anything written to its first MiB.
func used() []byte {
	img := blank()
	copy(img[512:], "EFI PART")
	return img
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

// fakeGrower is a disk vda (a temp file with a GPT) whose third partition holds
// an ext4 filesystem of fsBlocks 4 KiB blocks. Resizes are recorded and applied
// back, as the kernel would: the partition and the filesystem then report the
// new size.
type fakeGrower struct {
	t        *testing.T
	disk     string // path of vda's backing file
	sizes    map[string]int64
	fsBlocks uint64
	part     bool // whether the volume is vda3 or the whole of vda
	resized  []string
}

func (f *fakeGrower) ReadAt(dev string, p []byte, off int64) error {
	sb := make([]byte, 2048)
	binary.LittleEndian.PutUint32(sb[1024+0x04:], uint32(f.fsBlocks))
	binary.LittleEndian.PutUint32(sb[1024+0x18:], 2) // 1024 << 2: 4 KiB blocks
	copy(p, sb[off:])
	return nil
}

func (f *fakeGrower) PartitionOf(name string) (string, int, bool) {
	if f.part && name == "vda3" {
		return "vda", 3, true
	}
	return "", 0, false
}

func (f *fakeGrower) DeviceSize(name string) (int64, error) { return f.sizes[name], nil }
func (f *fakeGrower) SectorSize(string) (int64, error)      { return 512, nil }
func (f *fakeGrower) OpenDisk(string) (*os.File, error)     { return os.OpenFile(f.disk, os.O_RDWR, 0) }

func (f *fakeGrower) ResizePartition(disk string, part int, start, length int64) error {
	f.resized = append(f.resized, fmt.Sprintf("%s%d:%d+%d", disk, part, start, length))
	f.sizes["vda3"] = length
	return nil
}

func (f *fakeGrower) ResizeExt4(mp string, blocks uint64) error {
	f.resized = append(f.resized, fmt.Sprintf("ext4 %s:%d", mp, blocks))
	f.fsBlocks = blocks
	return nil
}

// newGrower is a 64 MiB vda -- an image written for 16 MiB, then grown -- with
// partition 3 at sectors 2048..34815 and a filesystem filling it.
func newGrower(t *testing.T, part bool) *fakeGrower {
	t.Helper()
	path := t.TempDir() + "/vda"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := file.Truncate(16 << 20); err != nil {
		t.Fatal(err)
	}
	if err := gpt.Create(file, 16<<20, 512, [2]uint64{40, 1023}, [2]uint64{1024, 2047}, [2]uint64{2048, 32767 - 33 - 1}); err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	partBytes := int64(32767-33-1-2048+1) * 512
	f := &fakeGrower{t: t, disk: path, part: part, sizes: map[string]int64{"vda": 64 << 20, "vda3": partBytes}}
	if part {
		f.fsBlocks = uint64(partBytes) / 4096
	} else {
		f.fsBlocks = 16 << 20 / 4096
	}
	return f
}

// The metal image on a bigger disk: the table grows partition 3, the kernel is
// told, and the filesystem fills it. Every later boot finds nothing to do.
func TestGrowExtendsTheDataPartitionThenTheFilesystem(t *testing.T) {
	f := newGrower(t, true)
	did, err := Grow(f, "/dev/vda3", "/var", "ext4")
	if err != nil {
		t.Fatal(err)
	}
	newEnd := uint64(64<<20/512) - 1 - 32 - 1
	length := int64(newEnd-2048+1) * 512
	want := []string{fmt.Sprintf("vda3:%d+%d", 2048*512, length), fmt.Sprintf("ext4 /var:%d", length/4096)}
	if !slices.Equal(f.resized, want) {
		t.Errorf("resized = %v, want %v", f.resized, want)
	}
	if !strings.Contains(did, "grew partition 3 of vda") || !strings.Contains(did, "grew the filesystem at /var") {
		t.Errorf("Grow said %q", did)
	}

	f.resized = nil
	if did, err := Grow(f, "/dev/vda3", "/var", "ext4"); err != nil || did != "" || len(f.resized) != 0 {
		t.Errorf("second Grow = %q, %v, resized %v; want nothing done", did, err, f.resized)
	}
}

// KubeVirt's data disk is a filesystem on a whole disk: an expanded volume needs
// no table, only the filesystem grown.
func TestGrowAWholeDiskFilesystem(t *testing.T) {
	f := newGrower(t, false)
	if _, err := Grow(f, "/dev/vda", "/var", "ext4"); err != nil {
		t.Fatal(err)
	}
	if want := []string{fmt.Sprintf("ext4 /var:%d", 64<<20/4096)}; !slices.Equal(f.resized, want) {
		t.Errorf("resized = %v, want %v", f.resized, want)
	}
}

// mkfs leaves a filesystem a few blocks short of its device: a fresh node must
// not "grow" it by those every first boot.
func TestGrowIgnoresTheMkfsRemainder(t *testing.T) {
	f := newGrower(t, false)
	f.fsBlocks = 64<<20/4096 - 3
	if did, err := Grow(f, "/dev/vda", "/var", "ext4"); did != "" || err != nil || len(f.resized) != 0 {
		t.Errorf("Grow = %q, %v, resized %v; want a no-op", did, err, f.resized)
	}
}

func TestGrowLeavesOtherFilesystemsAlone(t *testing.T) {
	f := newGrower(t, true)
	if did, err := Grow(f, "/dev/vda3", "/var", "xfs"); did != "" || err != nil || len(f.resized) != 0 {
		t.Errorf("Grow(xfs) = %q, %v, resized %v; want a no-op", did, err, f.resized)
	}
}

// A bare-metal disk with LVM or just a partition table has no filesystem the
// probes know, which used to make it "blank" and the data volume -- formatted.
func TestPrepareNeverFormatsAnUnrecognisedUsedDisk(t *testing.T) {
	f := &fakeVol{
		devs:   []string{"vda", "vdb"},
		images: map[string][]byte{"vda": ext4("k0smos"), "vdb": used()},
	}
	res, err := Prepare(f, Options{Spec: "auto", Label: label, FSType: "ext4", MountPoint: "/var/lib/k0s"})
	if err != nil || res.Device != "" {
		t.Errorf("Prepare(auto) = %+v, %v; want no candidate", res, err)
	}
	if _, err := Prepare(f, Options{Spec: "/dev/vdb", Label: label, FSType: "ext4", MountPoint: "/var/lib/k0s"}); err == nil ||
		!strings.Contains(err.Error(), "refusing to format") {
		t.Errorf("Prepare(/dev/vdb) = %v, want a refusal", err)
	}
	if len(f.mkfs) != 0 {
		t.Errorf("formatted a used disk: %v", f.mkfs)
	}
}

// fs_setup then mounts, as cloud-init orders them: the blank disk is formatted
// and then found by the label it was just given.
func TestExtraFormatsThenMountsByLabel(t *testing.T) {
	f := &fakeVol{devs: []string{"vda", "nvme1n1"}, images: map[string][]byte{"vda": ext4("k0smos"), "nvme1n1": blank()}}
	done, err := Extra(f,
		[]Format{{Device: "/dev/nvme1n1", FSType: "ext4", Label: "scratch"}},
		[]Mount{{Spec: "LABEL=scratch", MountPoint: "/srv/scratch"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.mkfs) != 1 || f.mkfs[0] != "/dev/nvme1n1:ext4:scratch" {
		t.Errorf("mkfs = %v", f.mkfs)
	}
	if len(f.mounted) != 1 || f.mounted[0] != "/dev/nvme1n1->/srv/scratch:ext4" {
		t.Errorf("mounted = %v", f.mounted)
	}
	if len(done) != 2 {
		t.Errorf("done = %v, want a line per action", done)
	}
}

// Every boot after the first: the disk already carries its filesystem, so
// fs_setup must leave it -- and its data -- alone.
func TestExtraLeavesAFormattedDiskAlone(t *testing.T) {
	f := &fakeVol{devs: []string{"nvme1n1"}, images: map[string][]byte{"nvme1n1": ext4("scratch")}}
	if _, err := Extra(f, []Format{{Device: "/dev/nvme1n1", FSType: "ext4", Label: "scratch"}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(f.mkfs) != 0 {
		t.Errorf("reformatted a disk with data on it: %v", f.mkfs)
	}
}

// One bad entry must not cost the machine the others, and every failure is
// reported, not just the first.
func TestExtraContinuesPastFailures(t *testing.T) {
	f := &fakeVol{
		devs:   []string{"sdb", "sdc"},
		images: map[string][]byte{"sdb": used(), "sdc": ext4("logs")},
	}
	_, err := Extra(f,
		[]Format{{Device: "/dev/sdb", FSType: "ext4", Label: "x"}},
		[]Mount{
			{Spec: "LABEL=missing", MountPoint: "/mnt/a"},
			{Spec: "LABEL=logs", MountPoint: "/mnt/logs", ReadOnly: true},
		})
	if err == nil || !strings.Contains(err.Error(), "/dev/sdb") || !strings.Contains(err.Error(), "missing") {
		t.Errorf("Extra = %v, want both failures reported", err)
	}
	if len(f.mkfs) != 0 {
		t.Errorf("formatted a used disk: %v", f.mkfs)
	}
	if len(f.mounted) != 1 || f.mounted[0] != "/dev/sdc->/mnt/logs:ext4:ro" {
		t.Errorf("mounted = %v, want the good entry mounted read-only", f.mounted)
	}
}

// KubeVirt boots with k0smos.data=auto. A second blank disk that fs_setup names
// is not a data-volume candidate: without the exclusion, two blank disks made
// "auto" refuse to guess and one made it take the disk meant for fs_setup.
func TestPrepareAutoSkipsDisksClaimedByFSSetup(t *testing.T) {
	f := &fakeVol{
		devs:   []string{"vda", "vdb", "vdc"},
		images: map[string][]byte{"vda": ext4("k0smos"), "vdb": blank(), "vdc": blank()},
	}
	res, err := Prepare(f, Options{Spec: "auto", Label: label, FSType: "ext4", MountPoint: "/var", Exclude: []string{"/dev/vdc"}})
	if err != nil || res.Device != "/dev/vdb" {
		t.Fatalf("Prepare = %+v, %v; want /dev/vdb", res, err)
	}
	if len(f.mkfs) != 1 || !strings.HasPrefix(f.mkfs[0], "/dev/vdb:") {
		t.Errorf("mkfs = %v, want only /dev/vdb", f.mkfs)
	}
}

// A partition on an MBR disk is not grown, and that is not an error -- but a
// filesystem smaller than its partition is still grown into it.
func TestGrowFillsThePartitionOnAnMBRDisk(t *testing.T) {
	f := newGrower(t, true)
	if err := os.WriteFile(f.disk, make([]byte, 4096), 0644); err != nil { // no GPT left
		t.Fatal(err)
	}
	f.fsBlocks /= 2
	did, err := Grow(f, "/dev/vda3", "/var", "ext4")
	if err != nil {
		t.Fatalf("Grow = %v, want no error for an MBR disk", err)
	}
	if want := []string{fmt.Sprintf("ext4 /var:%d", f.sizes["vda3"]/4096)}; !slices.Equal(f.resized, want) {
		t.Errorf("resized = %v, want %v (%s)", f.resized, want, did)
	}
}

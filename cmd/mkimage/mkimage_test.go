package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amakhov/k0smos/internal/gpt"
)

// The layout the metal image has had since it existed: ESP first, root behind
// it carrying its own hash tree, data physically last so it can grow.
func TestLayoutPlacesPartitionsInOrder(t *testing.T) {
	l := plan{rootBytes: 300 << 20, verityBytes: 2 << 20, dataMB: 8192, bios: true}.layout()
	if l.bios.start != 34 || l.bios.end() != 2047 {
		t.Errorf("BIOS boot is sectors %d-%d, want 34-2047", l.bios.start, l.bios.end())
	}
	if l.esp.start != 2048 || l.esp.count != 512*1024*1024/512 {
		t.Errorf("ESP is %d sectors at %d, want a 512 MiB one at 2048", l.esp.count, l.esp.start)
	}
	for _, r := range []struct {
		name string
		a, b region
	}{{"root after ESP", l.esp, l.root}, {"data after root", l.root, l.data}} {
		if r.b.start != r.a.end()+1 {
			t.Errorf("%s: %d does not follow %d", r.name, r.b.start, r.a.end())
		}
	}
	if l.root.count*sectorSize < 302<<20 {
		t.Errorf("root partition holds %d bytes, too few for the root plus its hash tree", l.root.count*sectorSize)
	}
	for _, r := range []region{l.esp, l.root, l.data} {
		if r.start%alignment != 0 {
			t.Errorf("a partition starts at sector %d, which is not 1 MiB aligned", r.start)
		}
	}
	if l.data.end() >= l.sectors-1 {
		t.Errorf("data ends at %d of %d sectors, leaving no room for the backup table", l.data.end(), l.sectors)
	}
	// No driver image, so no partition 5 -- and on aarch64 no partition 4.
	if l.driver.used() {
		t.Error("a driver partition appeared without a driver image")
	}
	uefiOnly := plan{rootBytes: 4096, dataMB: 64}.layout().table().Partitions
	if uefiOnly[partBIOS-1] != (gpt.Partition{}) {
		t.Error("a BIOS boot partition appeared on a machine that does not boot BIOS")
	}
}

// The table must be one internal/gpt accepts: partitions in range and not
// overlapping. Nothing else catches an arithmetic slip before a node fails to
// boot with its root half a megabyte off.
func TestTableIsValid(t *testing.T) {
	for _, p := range []plan{
		{rootBytes: 300 << 20, verityBytes: 2 << 20, dataMB: 8192, bios: true},
		{rootBytes: 300 << 20, verityBytes: 2 << 20, dataMB: 8192},
		{rootBytes: 300 << 20, verityBytes: 2 << 20, driverBytes: 135 << 20, dataMB: 8192, bios: true},
	} {
		l := p.layout()
		f, err := os.Create(filepath.Join(t.TempDir(), "disk.raw"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := f.Truncate(l.sectors * sectorSize); err != nil {
			t.Fatal(err)
		}
		if err := gpt.Create(f, l.sectors*sectorSize, sectorSize, l.table()); err != nil {
			t.Errorf("plan %+v: %v", p, err)
		}
	}
}

// The one argument per-machine arguments must not reach is the hash of the root
// they are read from, so k0smos.verity= comes after $k0smos_kernel_args. The
// shell script this replaced had the ordering right only by convention.
func TestGrubCfgPutsVerityLast(t *testing.T) {
	cfg := grubCfg(defaultKernelArgs(arch{serial: "ttyS0"}), "k0smos.verity=76800:abc")
	var line string
	for l := range strings.Lines(cfg) {
		if strings.HasPrefix(l, "linux ") {
			line = strings.TrimSpace(l)
		}
	}
	if line == "" {
		t.Fatalf("no linux line in:\n%s", cfg)
	}
	if !strings.HasSuffix(line, " k0smos.verity=76800:abc") {
		t.Errorf("verity is not last on %q", line)
	}
	if i, j := strings.Index(line, "$k0smos_kernel_args"), strings.Index(line, "k0smos.verity="); i > j {
		t.Errorf("per-machine arguments come after the root hash on %q", line)
	}
	if !strings.Contains(cfg, "load_env --file ($k0smos_data)/lib/k0smos/grubenv k0smos_kernel_args") {
		t.Errorf("nothing loads the per-machine arguments:\n%s", cfg)
	}
}

// copyInto must leave holes where the source has zeros -- an 8 GiB data volume
// is nearly all zeros -- and still reproduce every non-zero byte.
func TestCopyIntoSkipsZerosAndKeepsData(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "part.img")
	want := make([]byte, 3<<20)
	copy(want, "head")
	copy(want[2<<20:], "tail")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "disk.raw")
	if err := truncate(dst, 8<<20); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(dst, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := copyInto(f, 1<<20, src); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := f.ReadAt(got, 1<<20); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("the copy does not match the source")
	}
	same, err := sameAt(f, 1<<20, src)
	if err != nil || !same {
		t.Errorf("sameAt: %v, %v; want true", same, err)
	}
	if _, err := f.WriteAt([]byte("x"), 1<<20+(1<<20)); err != nil {
		t.Fatal(err)
	}
	if same, err := sameAt(f, 1<<20, src); err != nil || same {
		t.Errorf("sameAt after one changed byte: %v, %v; want false", same, err)
	}
}

// The BIOS boot path has no test that can boot it here, but the three values
// GRUB's boot.img and diskboot read are plain little-endian numbers at fixed
// offsets, and getting one wrong is a machine that hangs before any console.
func TestBIOSPatchesPointAtTheCore(t *testing.T) {
	img := make([]byte, 4096)
	patchCore(img, 34, 7)
	if got := le(img, coreBlocklistStart, 8); got != 35 {
		t.Errorf("diskboot looks for the rest of core at sector %d, want 35", got)
	}
	if got := le(img, coreBlocklistLen, 2); got != 6 {
		t.Errorf("diskboot loads %d sectors, want 6 (of 7, minus its own)", got)
	}
	if got := le(img, coreBlocklistSeg, 2); got != coreLoadSegment {
		t.Errorf("load segment is %#x, want %#x", got, coreLoadSegment)
	}
	boot := make([]byte, 512)
	patchBoot(boot, 34)
	if got := le(boot, bootKernelSector, 8); got != 34 {
		t.Errorf("the MBR reads core from sector %d, want 34", got)
	}
	if boot[bootDrive] != 0xff {
		t.Errorf("boot drive is %#x, want 0xff: the one the BIOS booted from", boot[bootDrive])
	}
	if got := le(boot, bootDriveCheck, 2); got != 0x9090 {
		t.Errorf("the floppy probe is %#x, want it nopped out", got)
	}
}

func le(b []byte, off, n int) uint64 {
	var v uint64
	for i := range n {
		v |= uint64(b[off+i]) << (8 * i)
	}
	return v
}

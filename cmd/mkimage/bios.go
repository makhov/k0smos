package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Offsets fixed by GRUB's i386-pc boot.h.
const (
	// In core.img's first sector, diskboot: where the rest of core.img is.
	coreBlocklistStart = 500 // 8 bytes, the sector it starts at
	coreBlocklistLen   = 508 // 2 bytes, how many sectors follow that one
	coreBlocklistSeg   = 510 // 2 bytes, the segment it is loaded at
	coreLoadSegment    = 0x820

	bootKernelSector = 0x5c // 8 bytes: where core.img starts
	bootDrive        = 0x64 // 1 byte: which drive to read
	bootDriveCheck   = 0x66 // 2 bytes: the floppy probe
	// The MBR holds boot.img's code only; the bytes after it are the table.
	bootCodeBytes = 440

	bootImg = "/usr/lib/grub/i386-pc/boot.img"
)

// coreCfg is the configuration built into core.img. It only finds the ESP and
// enters normal mode, which reads the EFI loader's grub.cfg from there: so the
// two boot paths cannot drift, and both run that file with GRUB's full script
// parser. This minimal one does not split $k0smos_kernel_args into words.
const coreCfg = "search --no-floppy --label K0SMOSBOOT --set=root\n" +
	"set prefix=($root)/boot/grub\n" +
	"normal\n"

// installBIOS puts GRUB's i386-pc boot code on the disk by hand, rather than by
// grub-bios-setup, which probes the build host's own mounts and fails in a
// container. The steps are the ones it performs:
//
//	core.img into the BIOS boot partition, its first sector (diskboot) told
//	  where the rest of it is;
//	boot.img's code -- never the partition table after it -- into the MBR,
//	  told where core starts, to use the drive the BIOS booted from, and with
//	  the floppy probe that misreads hard disks on some BIOSes jumped over.
func installBIOS(dir string, disk *os.File, bios region) error {
	cfg, core := filepath.Join(dir, "core.cfg"), filepath.Join(dir, "core.img")
	if err := os.WriteFile(cfg, []byte(coreCfg), 0o644); err != nil {
		return err
	}
	if err := run("grub-mkimage", "-O", "i386-pc", "-o", core, "-c", cfg, "-p", "/boot/grub",
		"biosdisk", "part_gpt", "fat", "ext2", "search", "search_label", "linux", "boot",
		"loadenv", "normal"); err != nil {
		return err
	}
	img, err := os.ReadFile(core)
	if err != nil {
		return err
	}
	if len(img) < sectorSize {
		return fmt.Errorf("GRUB core is %d bytes, too small to hold a blocklist", len(img))
	}
	sectors := ceilDiv(int64(len(img)), sectorSize)
	if sectors > bios.count {
		return fmt.Errorf("GRUB core (%d sectors) does not fit the %d-sector BIOS boot partition",
			sectors, bios.count)
	}
	patchCore(img, bios.start, sectors)
	if _, err := disk.WriteAt(img, bios.offset()); err != nil {
		return err
	}
	boot, err := os.ReadFile(bootImg)
	if err != nil {
		return err
	}
	if len(boot) < bootCodeBytes {
		return fmt.Errorf("%s is %d bytes, under the %d the MBR takes", bootImg, len(boot), bootCodeBytes)
	}
	patchBoot(boot, bios.start)
	_, err = disk.WriteAt(boot[:bootCodeBytes], 0)
	return err
}

// patchCore tells diskboot where the rest of core.img is: it loaded only its
// own first sector and has no other way to find the remaining ones.
func patchCore(img []byte, start, sectors int64) {
	putLE(img, coreBlocklistStart, 8, uint64(start+1))
	putLE(img, coreBlocklistLen, 2, uint64(sectors-1))
	putLE(img, coreBlocklistSeg, 2, coreLoadSegment)
}

// patchBoot tells the MBR code where core.img starts, to read the drive the
// BIOS booted from (0xff) rather than a baked-in number, and to skip the floppy
// probe, which misreads hard disks on some BIOSes.
func patchBoot(boot []byte, start int64) {
	putLE(boot, bootKernelSector, 8, uint64(start))
	boot[bootDrive] = 0xff
	putLE(boot, bootDriveCheck, 2, 0x9090) // two nops
}

// putLE writes v as n little-endian bytes at off.
func putLE(b []byte, off, n int, v uint64) {
	for i := range n {
		b[off+i] = byte(v >> (8 * i))
	}
}

package main

import (
	"fmt"

	"github.com/amakhov/k0smos/internal/gpt"
)

// Sizes the layout is fixed to. verityBlock is a constant rather than something
// discovered because the kernel cannot discover it either: cmd/k0smos builds the
// same number into the table it hands device-mapper.
const (
	sectorSize  = 512
	alignment   = 2048 // sectors: 1 MiB, what every partitioner aligns to
	verityBlock = 4096
	// The kernel and initramfs live on the ESP, and the initramfs carries the
	// module tree and any driver overlay: the NVIDIA one alone is ~135M.
	espMB = 512
	// The first sector a 128-entry table leaves free, and so the first one
	// GRUB's BIOS core can live on.
	firstFreeSector = 34
)

// Partition numbers, fixed so the same thing keeps the same number in every
// variant of the image: BIOS boot is 4 and the driver root 5 because they came
// after the three every image has.
const (
	partESP = iota + 1
	partRoot
	partData
	partBIOS
	partDriver
)

// plan is what the caller decides about an image; layout is what follows.
type plan struct {
	rootBytes   int64 // the canonical erofs root
	verityBytes int64 // its hash tree, behind it in the same partition
	driverBytes int64 // an optional read-only driver root, 0 for none
	dataMB      int64
	bios        bool
}

// region is a run of sectors: where one partition lives.
type region struct{ start, count int64 }

func (r region) end() int64    { return r.start + r.count - 1 }
func (r region) offset() int64 { return r.start * sectorSize }
func (r region) used() bool    { return r.count > 0 }

// layout places every partition of the disk. Data stays physically last, which
// is what lets /var grow into a bigger disk (internal/gpt.Grow), and GRUB's
// BIOS core goes first, into the gap before the ESP that alignment leaves anyway.
type layout struct {
	bios, esp, root, driver, data region
	sectors                       int64 // the whole disk, backup table included
}

func (p plan) layout() layout {
	var l layout
	if p.bios {
		l.bios = region{firstFreeSector, alignment - firstFreeSector}
	}
	l.esp = region{alignment, espMB * 1024 * 1024 / sectorSize}
	l.root = region{l.esp.end() + 1, alignUp(ceilDiv(p.rootBytes+p.verityBytes, sectorSize))}
	l.driver = region{l.root.end() + 1, 0}
	if p.driverBytes > 0 {
		l.driver.count = alignUp(ceilDiv(p.driverBytes, sectorSize))
	}
	l.data = region{l.driver.start + l.driver.count, p.dataMB * 1024 * 1024 / sectorSize}
	// Room for the backup table plus an alignment-sized tail.
	l.sectors = l.data.end() + alignment + 1
	return l
}

// table is the partition table for a layout. The GUIDs are fixed so that the
// same inputs give the same disk, and the names are the labels GRUB and
// cmd/k0smos look these volumes up by.
func (l layout) table() gpt.Table {
	t := gpt.Table{
		DiskGUID:   gpt.MustGUID("c0dec0de-0000-4000-8000-000000000010"),
		Partitions: make([]gpt.Partition, partDriver),
	}
	add := func(n int, r region, typ gpt.GUID, name string) {
		if !r.used() {
			return
		}
		t.Partitions[n-1] = gpt.Partition{
			Start: uint64(r.start), End: uint64(r.end()), Type: typ, Name: name,
			GUID: gpt.MustGUID(fmt.Sprintf("c0dec0de-0000-4000-8000-0000000000%d", 10+n)),
		}
	}
	add(partESP, l.esp, gpt.TypeESP, "K0SMOS-BOOT")
	add(partRoot, l.root, gpt.TypeLinux, "K0SMOS-ROOT")
	add(partData, l.data, gpt.TypeLinux, "K0SMOS-DATA")
	add(partBIOS, l.bios, gpt.TypeBIOSBoot, "K0SMOS-BIOS")
	add(partDriver, l.driver, gpt.TypeLinux, "K0SMOS-DRIVER")
	return t
}

func ceilDiv(a, b int64) int64    { return (a + b - 1) / b }
func alignUp(sectors int64) int64 { return ceilDiv(sectors, alignment) * alignment }

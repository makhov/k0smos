//go:build linux

package sys

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// sysBlock is where the kernel describes block devices and partitions.
const sysBlock = "/sys/class/block"

// PartitionOf reports the disk and 1-based number of a partition such as vda3
// or nvme0n1p3, and ok=false for a whole disk.
func (s *Sys) PartitionOf(name string) (disk string, part int, ok bool) {
	raw, err := os.ReadFile(filepath.Join(sysBlock, name, "partition"))
	if err != nil {
		return "", 0, false
	}
	part, err = strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return "", 0, false
	}
	// /sys/class/block/vda3 links to .../block/vda/vda3: the parent is the disk.
	real, err := filepath.EvalSymlinks(filepath.Join(sysBlock, name))
	if err != nil {
		return "", 0, false
	}
	return filepath.Base(filepath.Dir(real)), part, true
}

// DeviceSize is a disk's or partition's size in bytes. sysfs counts it in
// 512-byte units whatever the device's own sector size.
func (s *Sys) DeviceSize(name string) (int64, error) {
	raw, err := os.ReadFile(filepath.Join(sysBlock, name, "size"))
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return n * 512, err
}

// SectorSize is a disk's logical block size: the unit its partition table uses.
func (s *Sys) SectorSize(disk string) (int64, error) {
	raw, err := os.ReadFile(filepath.Join(sysBlock, disk, "queue", "logical_block_size"))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
}

// OpenDisk opens a whole disk for writing its partition table.
func (s *Sys) OpenDisk(disk string) (*os.File, error) {
	return os.OpenFile("/dev/"+disk, os.O_RDWR|os.O_SYNC, 0)
}

// blkpgPartition is struct blkpg_partition, which x/sys does not define.
type blkpgPartition struct {
	Start, Length int64
	Pno           int32
	Devname       [64]byte
	Volname       [64]byte
}

// blkpg is the BLKPG ioctl, _IO(0x12, 105).
const blkpg = 0x1269

// ResizePartition tells the kernel a partition's new extent. Unlike rereading
// the whole table, this works while the partition is mounted -- which /var
// always is by the time it can be grown.
func (s *Sys) ResizePartition(disk string, part int, start, length int64) error {
	f, err := os.Open("/dev/" + disk)
	if err != nil {
		return err
	}
	defer f.Close()
	p := blkpgPartition{Start: start, Length: length, Pno: int32(part)}
	arg := unix.BlkpgIoctlArg{
		Op:      unix.BLKPG_RESIZE_PARTITION,
		Datalen: int32(unsafe.Sizeof(p)),
		Data:    (*byte)(unsafe.Pointer(&p)),
	}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), blkpg, uintptr(unsafe.Pointer(&arg))); errno != 0 {
		return fmt.Errorf("BLKPG resize %s partition %d: %w", disk, part, errno)
	}
	return nil
}

// ext4ResizeFS is EXT4_IOC_RESIZE_FS, _IOW('f', 16, __u64).
const ext4ResizeFS = 0x40086610

// ResizeExt4 grows a mounted ext4 filesystem to blocks blocks: the kernel's
// online resize, which is what resize2fs itself calls for a mounted filesystem.
func (s *Sys) ResizeExt4(mountPoint string, blocks uint64) error {
	f, err := os.Open(mountPoint)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), ext4ResizeFS, uintptr(unsafe.Pointer(&blocks))); errno != 0 {
		return fmt.Errorf("resize ext4 at %s to %d blocks: %w", mountPoint, blocks, errno)
	}
	return nil
}

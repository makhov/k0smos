package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/amakhov/k0smos/internal/gpt"
	"github.com/amakhov/k0smos/internal/grubenv"
)

// run is an external tool. mkfs, GRUB, veritysetup and qemu-img do their own
// jobs well, so this is where the structure stops and the tool starts; what
// this command owns is the geometry, the configuration and the invariants.
func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", name, err, out)
	}
	return nil
}

// truncate creates path at size and leaves it sparse: a 512 MiB ESP and an
// 8 GiB data volume are mostly holes, and the disk they are copied into keeps
// them that way.
func truncate(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(size)
}

func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if fi.Size() == 0 {
		return 0, fmt.Errorf("%s is empty", path)
	}
	return fi.Size(), nil
}

// verityFormat builds a dm-verity hash tree over root into out and returns its
// root hash. The tree is appended to the ROOT partition behind the filesystem,
// with the hash on the kernel command line. This is what makes a read-only root
// immutable rather than merely read-only: without it nothing checks, and a node
// whose disk was edited offline boots and joins the cluster. It costs under 1%
// of the root's size.
//
// --salt=- (the empty salt) makes the tree a pure function of the root, so the
// hash names the root's exact bytes and not this build run. --no-superblock
// because the table k0smos loads says everything the kernel needs, and a
// superblock would only push the tree off the block the table points at.
func verityFormat(root, out string) (string, error) {
	block := strconv.Itoa(verityBlock)
	o, err := exec.Command("veritysetup", "format", "--no-superblock", "--salt=-",
		"--data-block-size="+block, "--hash-block-size="+block, root, out).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("veritysetup format: %w\n%s", err, o)
	}
	for line := range strings.Lines(string(o)) {
		h, ok := strings.CutPrefix(line, "Root hash:")
		if !ok {
			continue
		}
		if h = strings.TrimSpace(h); len(h) != 64 {
			return "", fmt.Errorf("veritysetup printed a %d-character root hash %q", len(h), h)
		}
		return h, nil
	}
	return "", fmt.Errorf("veritysetup printed no root hash:\n%s", o)
}

// grubCfg is the configuration both loaders run: the EFI loader carries a copy
// and GRUB's BIOS core reads this one off the ESP, so the two paths cannot drift.
//
// k0smos_kernel_args is the per-machine part: cloud-init's k0smos.kernel_args,
// which k0smos stores in a GRUB environment block on the data volume. The image
// ships an empty one; a data volume without it only costs a GRUB error line.
//
// verityArg comes last, after that, and must stay there -- there is a test next
// to this that says so. The kernel command line is last-wins (see
// internal/config.Parse) and the data volume is the one partition dm-verity does
// not cover, so with the per-machine part appended after it, writing
// k0smos_kernel_args=k0smos.verity= into that volume switched the root check off
// on the next boot. Verified: the root then mounted straight from /dev/vda2 with
// no verity device and nothing logged about it. Per-machine arguments still
// override every ordinary setting, which is what they are for; the one argument
// they must not reach is the hash of the root they are read from.
func grubCfg(kernelArgs, verityArg string) string {
	return "search --no-floppy --label K0SMOSBOOT --set=root\n" +
		"search --no-floppy --label k0smos-data --set=k0smos_data\n" +
		"load_env --file ($k0smos_data)/lib/k0smos/grubenv k0smos_kernel_args\n" +
		"linux /boot/vmlinuz " + kernelArgs + " $k0smos_kernel_args " + verityArg + "\n" +
		"initrd /boot/initramfs.gz\n" +
		"boot\n"
}

// buildESP is the boot partition: a FAT filesystem carrying the kernel, the
// initramfs, the EFI loader and the configuration both loaders read.
func buildESP(dir string, a arch, cfg, kernel, initramfs string) (string, error) {
	esp := filepath.Join(dir, "esp.fat")
	if err := truncate(esp, espMB*1024*1024); err != nil {
		return "", err
	}
	if err := run("mkfs.vfat", "-F", "32", "-n", "K0SMOSBOOT", "-i", "C0DE0001", esp); err != nil {
		return "", err
	}
	cfgPath := filepath.Join(dir, "grub.cfg")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		return "", err
	}
	efi := filepath.Join(dir, a.efiName)
	if err := run("grub-mkstandalone", "-O", a.grubTarget, "-o", efi,
		"--modules=part_gpt fat ext2 normal linux search search_label loadenv",
		"boot/grub/grub.cfg="+cfgPath); err != nil {
		return "", err
	}
	if err := run("mmd", "-i", esp, "::/EFI", "::/EFI/BOOT", "::/boot", "::/boot/grub"); err != nil {
		return "", err
	}
	for _, c := range [][2]string{
		// Read from here by BIOS GRUB; the EFI loader carries its own copy.
		{cfgPath, "::/boot/grub/grub.cfg"},
		{efi, "::/EFI/BOOT/" + a.efiName},
		{kernel, "::/boot/vmlinuz"},
		{initramfs, "::/boot/initramfs.gz"},
	} {
		if err := run("mcopy", "-i", esp, c[0], c[1]); err != nil {
			return "", err
		}
	}
	return esp, nil
}

// buildData is the empty data volume. Its only content is an empty GRUB
// environment block, so that GRUB finds one even before k0smos has stored any
// per-machine kernel arguments in it.
func buildData(dir string, mb int64) (string, error) {
	tree := filepath.Join(dir, "data-tree")
	if err := os.MkdirAll(filepath.Join(tree, "lib", "k0smos"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tree, "lib", "k0smos", "grubenv"), grubenv.Empty(), 0o644); err != nil {
		return "", err
	}
	data := filepath.Join(dir, "data.ext4")
	if err := truncate(data, mb*1024*1024); err != nil {
		return "", err
	}
	return data, run("mkfs.ext4", "-q", "-F", "-L", "k0smos-data", "-d", tree,
		"-U", "c0dec0de-0000-4000-8000-000000000002", data)
}

// content is one partition image and the byte offset of the disk it goes at.
type content struct {
	at   int64
	file string
}

// assemble writes the disk: a partition table, then each partition's image
// copied into its own sectors. No loop devices and no mounts, so this works
// unprivileged, in a CI container.
func assemble(path string, l layout, contents []content) (*os.File, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	size := l.sectors * sectorSize
	if err := f.Truncate(size); err != nil {
		f.Close()
		return nil, err
	}
	if err := gpt.Create(f, size, sectorSize, l.table()); err != nil {
		f.Close()
		return nil, err
	}
	for _, c := range contents {
		if err := copyInto(f, c.at, c.file); err != nil {
			f.Close()
			return nil, fmt.Errorf("copy %s to offset %d: %w", c.file, c.at, err)
		}
	}
	return f, nil
}

// copyInto writes file into the disk at byte offset off, skipping blocks that
// are all zeros. The disk was truncated to its full size first, so a skipped
// block already reads as the zeros it holds -- and stays a hole, which is what
// keeps an 8 GiB data volume from becoming 8 GiB of written zeros.
func copyInto(w io.WriterAt, off int64, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	buf, zero := make([]byte, 1<<20), make([]byte, 1<<20)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 && !bytes.Equal(buf[:n], zero[:n]) {
			if _, err := w.WriteAt(buf[:n], off); err != nil {
				return err
			}
		}
		off += int64(n)
		switch err {
		case nil:
		case io.EOF, io.ErrUnexpectedEOF:
			return nil
		default:
			return err
		}
	}
}

// sameAt reports whether the disk holds file's exact bytes at off.
func sameAt(disk io.ReaderAt, off int64, file string) (bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	a, b := make([]byte, 1<<20), make([]byte, 1<<20)
	for at := int64(0); at < fi.Size(); at += int64(len(a)) {
		n := min(int64(len(a)), fi.Size()-at)
		if _, err := disk.ReadAt(a[:n], off+at); err != nil {
			return false, err
		}
		if _, err := f.ReadAt(b[:n], at); err != nil {
			return false, err
		}
		if !bytes.Equal(a[:n], b[:n]) {
			return false, nil
		}
	}
	return true, nil
}

func toQcow2(raw, qcow2 string) error {
	if err := os.Remove(qcow2); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := run("qemu-img", "convert", "-f", "raw", "-O", "qcow2", "-c", raw, qcow2); err != nil {
		return err
	}
	return run("qemu-img", "check", "-f", "qcow2", qcow2)
}

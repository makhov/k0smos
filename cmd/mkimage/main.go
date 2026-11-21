// Command mkimage assembles the bootable disk a k0smos node is provisioned
// with: a GPT carrying an ESP, a dm-verity-checked read-only root, an empty
// data volume and optionally a read-only driver root, plus the qcow2 the
// platform consumes. Its inputs are a kernel, a matching initramfs and the
// canonical erofs root, and it knows nothing about k0s.
//
// Partition images are built as ordinary files and copied into fixed offsets of
// the disk -- no loop devices, no mounts -- so this runs unprivileged in a CI
// container. image/mkmetal.sh puts it somewhere its tools are installed.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// arch is everything that differs between the machines k0smos is built for.
type arch struct {
	goarch, grubTarget, efiName, serial string
	bios                                bool
}

func lookupArch(name string) (arch, error) {
	switch name {
	case "arm64", "aarch64":
		return arch{goarch: "arm64", grubTarget: "arm64-efi", efiName: "BOOTAA64.EFI", serial: "ttyAMA0"}, nil
	case "x86_64", "amd64":
		// Most libvirt and bare-metal x86 machines still boot SeaBIOS or a BIOS
		// CSM unless told otherwise; an image that cannot is not a drop-in.
		return arch{goarch: "amd64", grubTarget: "x86_64-efi", efiName: "BOOTX64.EFI",
			serial: "ttyS0", bios: true}, nil
	}
	return arch{}, fmt.Errorf("unsupported arch %q", name)
}

// defaultKernelArgs is the image-wide part of the command line.
//
// The serial console is per-arch: aarch64 virt has no ttyS0, so a baked-in
// console=ttyS0 left tty0 as the preferred console -- and qemu -display none
// throws tty0 away. Every line k0smos and k0s wrote went nowhere, which is why a
// guest stuck on DNS looked like a guest stuck in init. Serial comes last
// because the last console= wins /dev/console.
func defaultKernelArgs(a arch) string {
	return "console=tty0 console=" + a.serial +
		" k0smos.data=LABEL=k0smos-data k0smos.ip=dhcp"
}

func main() {
	if err := build(); err != nil {
		fmt.Fprintln(os.Stderr, "mkimage:", err)
		os.Exit(1)
	}
}

func build() error {
	var (
		archName   = flag.String("arch", runtime.GOARCH, "machine architecture of the image")
		kernel     = flag.String("kernel", "", "the metal kernel")
		initramfs  = flag.String("initramfs", "", "the matching initramfs")
		rootfs     = flag.String("rootfs", "", "the canonical EROFS root")
		driver     = flag.String("driver", "", "optional read-only image for a partition of its own, such as the NVIDIA driver root")
		out        = flag.String("out", "dist/k0smos-metal.raw", "the raw disk to write")
		qcow2      = flag.String("qcow2", "", "the qcow2 to write (default: -out with a .qcow2 suffix)")
		kernelArgs = flag.String("kernel-args", "", "override the image-wide kernel command line")
		dataMB     = flag.Int64("data-mb", 8192, "size of the data volume")
		own        = flag.String("chown", "", "uid:gid to give the artifacts, for a build running as root in a container")
	)
	flag.Parse()

	a, err := lookupArch(*archName)
	if err != nil {
		return err
	}
	for _, req := range [][2]string{{"-kernel", *kernel}, {"-initramfs", *initramfs}, {"-rootfs", *rootfs}} {
		if req[1] == "" {
			return fmt.Errorf("%s is required", req[0])
		}
	}
	if *qcow2 == "" {
		*qcow2 = strings.TrimSuffix(*out, ".raw") + ".qcow2"
	}
	if *kernelArgs == "" {
		*kernelArgs = defaultKernelArgs(a)
	}
	for _, dir := range []string{filepath.Dir(*out), filepath.Dir(*qcow2)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.MkdirTemp("", "mkimage")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	rootBytes, err := fileSize(*rootfs)
	if err != nil {
		return err
	}
	if rootBytes%verityBlock != 0 {
		return fmt.Errorf("root %s is %d bytes, not a whole number of %d-byte blocks",
			*rootfs, rootBytes, verityBlock)
	}
	verityPath := filepath.Join(tmp, "root.verity")
	rootHash, err := verityFormat(*rootfs, verityPath)
	if err != nil {
		return err
	}
	verityBytes, err := fileSize(verityPath)
	if err != nil {
		return err
	}
	// The hash tree starts on the block after the last data block, which is
	// where the table tells the kernel to look for it.
	verityArg := fmt.Sprintf("k0smos.verity=%d:%s", rootBytes/verityBlock, rootHash)

	p := plan{rootBytes: rootBytes, verityBytes: verityBytes, dataMB: *dataMB, bios: a.bios}
	if *driver != "" {
		if p.driverBytes, err = fileSize(*driver); err != nil {
			return err
		}
	}
	l := p.layout()

	esp, err := buildESP(tmp, a, grubCfg(*kernelArgs, verityArg), *kernel, *initramfs)
	if err != nil {
		return err
	}
	data, err := buildData(tmp, *dataMB)
	if err != nil {
		return err
	}
	contents := []content{
		{l.esp.offset(), esp},
		{l.root.offset(), *rootfs},
		{l.root.offset() + rootBytes, verityPath},
		{l.data.offset(), data},
	}
	if l.driver.used() {
		contents = append(contents, content{l.driver.offset(), *driver})
	}
	disk, err := assemble(*out, l, contents)
	if err != nil {
		return err
	}
	defer disk.Close()

	if a.bios {
		if err := installBIOS(tmp, disk, l.bios); err != nil {
			return err
		}
	}
	// The platform wrapper must carry the canonical root byte-for-byte, and the
	// hash tree must begin exactly where verityArg says or every read of the
	// root fails at boot with nothing to point at the offset arithmetic. These
	// make both an invariant of the build rather than a convention.
	for _, c := range []struct {
		at   int64
		file string
		what string
	}{
		{l.root.offset(), *rootfs, "ROOT partition does not match the canonical root"},
		{l.root.offset() + rootBytes, verityPath,
			fmt.Sprintf("verity hash tree is not at offset %d of the ROOT partition", rootBytes)},
	} {
		same, err := sameAt(disk, c.at, c.file)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("%s (%s)", c.what, c.file)
		}
	}
	if err := toQcow2(*out, *qcow2); err != nil {
		return err
	}
	if err := chown(*own, *out, *qcow2); err != nil {
		return err
	}
	size, err := fileSize(*qcow2)
	if err != nil {
		return err
	}
	firmware := "UEFI"
	if a.bios {
		firmware = "UEFI+BIOS"
	}
	fmt.Printf("wrote %s (%s, %s/GPT, linux/%s)\n", *qcow2, humanBytes(size), firmware, a.goarch)
	return nil
}

// chown gives the artifacts back to the user who started the build: the builder
// container runs as root, so without this they are left owned by root.
func chown(spec string, paths ...string) error {
	if spec == "" {
		return nil
	}
	u, g, ok := strings.Cut(spec, ":")
	uid, err := strconv.Atoi(u)
	gid, errg := strconv.Atoi(g)
	if !ok || err != nil || errg != nil {
		return fmt.Errorf("-chown %q is not uid:gid", spec)
	}
	for _, p := range paths {
		if err := os.Chown(p, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func humanBytes(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%dM", n/(1<<20))
}

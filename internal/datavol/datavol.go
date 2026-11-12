// Package datavol prepares the mutable data volume.
//
// This follows Talos's layout rather than either extreme: the root filesystem is
// immutable and interchangeable, while everything that changes — etcd, containerd,
// kubelet, pulled images — lives on a separate volume. Talos calls that partition
// EPHEMERAL and mounts it at /var; k0smos mounts its equivalent at
// /var/lib/k0s.
//
// It is what makes a machine disposable without being diskless. A fully diskless
// node cannot run kubelet (cadvisor finds no filesystem information for a ramfs
// root) and pins every byte of cold data in RAM; a single writable root makes the
// OS image and the cluster's data one artifact. Splitting them means the volume
// can be an ephemeral per-VM disk, discarded with the machine, or a persistent
// claim — a choice made when attaching it, with no difference in code.
package datavol

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/amakhov/k0smos/internal/blkid"
)

// Volume is the subset of *sys.Sys that preparing a data volume needs.
type Volume interface {
	BlockDevices() ([]string, error)
	ReadAt(dev string, p []byte, off int64) error
	Mkfs(dev, fstype, label string) error
	Mkdir(path string, perm fs.FileMode) error
	Mount(source, target, fstype string, flags uintptr, data string) error
}

// Options describes the wanted volume.
type Options struct {
	// Spec selects the device: "auto", a path such as /dev/vdb, or LABEL=/UUID=.
	// Empty disables data-volume handling entirely.
	Spec string
	// Label is applied when formatting, and looked for by "auto".
	Label string
	// FSType is the filesystem to create and mount.
	FSType string
	// MountPoint is where it goes, normally /var/lib/k0s.
	MountPoint string
}

// Result reports what happened.
type Result struct {
	// Device is the volume that was mounted, or empty if there was none.
	Device string
	// Formatted is true when this boot created the filesystem.
	Formatted bool
}

// Prepare finds the data volume, formats it if it is blank, and mounts it.
//
// The safety rule throughout: never format a device that already has a
// filesystem. Reformatting would destroy a cluster's etcd, and a wrongly
// selected device is far more likely to be someone else's data than a blank
// disk. Ambiguity is refused rather than guessed at.
func Prepare(v Volume, o Options) (Result, error) {
	if o.Spec == "" {
		return Result{}, nil // disabled
	}

	dev, err := selectDevice(v, o)
	if err != nil {
		return Result{}, err
	}
	if dev == "" {
		// Nothing attached. Not an error: k0s will use the root filesystem, which
		// is how a machine with no data volume already behaves.
		return Result{}, nil
	}

	res := Result{Device: dev}
	if res.Formatted, err = format(v, dev, o.FSType, o.Label); err != nil {
		return Result{}, err
	}

	if err := v.Mkdir(o.MountPoint, 0700); err != nil {
		return res, fmt.Errorf("mkdir %s: %w", o.MountPoint, err)
	}
	if err := v.Mount(dev, o.MountPoint, o.FSType, 0, ""); err != nil {
		return res, fmt.Errorf("mount %s at %s: %w", dev, o.MountPoint, err)
	}
	return res, nil
}

// format gives dev a filesystem if it has none, reporting whether it did. A
// device with a filesystem k0smos recognises is left as it is; one that is not
// blank but unrecognised is refused, never formatted -- see blkid.IsBlank.
func format(v Volume, dev, fstype, label string) (bool, error) {
	name := strings.TrimPrefix(dev, "/dev/")
	if _, ok := blkid.Identify(v, name); ok {
		return false, nil
	}
	if !blkid.IsBlank(v, name) {
		return false, fmt.Errorf("refusing to format %s: it is not blank but holds no filesystem "+
			"k0smos recognises (a partition table, LVM, XFS...); wipe it first if it is meant for this", dev)
	}
	if err := v.Mkfs(dev, fstype, label); err != nil {
		return false, fmt.Errorf("format %s as %s: %w", dev, fstype, err)
	}
	return true, nil
}

// Format asks for a blank device to be given a filesystem: one entry of
// cloud-init's fs_setup.
type Format struct {
	Device string // a /dev path
	FSType string
	Label  string
}

// Mount asks for a volume to be mounted: one entry of cloud-init's mounts.
type Mount struct {
	Spec       string // a /dev path, LABEL= or UUID=
	MountPoint string
	FSType     string // empty or "auto" means whatever the device holds
	ReadOnly   bool
}

// msReadOnly is MS_RDONLY.
const msReadOnly = 0x1

// Extra formats and mounts volumes beyond the data volume -- a second disk on a
// bare-metal machine, one attached to a VM -- all formats first, as cloud-init
// orders fs_setup before mounts. Formatting follows the data volume's rule:
// only a blank device, never one holding anything.
//
// Entries are independent: a failure is reported and the rest still run, so
// one missing disk does not cost the machine the others. It returns a line for
// each thing it did.
func Extra(v Volume, formats []Format, mounts []Mount) ([]string, error) {
	var done []string
	var errs []error
	for _, f := range formats {
		formatted, err := format(v, f.Device, f.FSType, f.Label)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("fs_setup %s: %w", f.Device, err))
		case formatted:
			done = append(done, fmt.Sprintf("formatted %s as %s, LABEL=%s", f.Device, f.FSType, f.Label))
		}
	}
	for _, m := range mounts {
		if err := mountOne(v, m); err != nil {
			errs = append(errs, fmt.Errorf("mount %s at %s: %w", m.Spec, m.MountPoint, err))
			continue
		}
		done = append(done, fmt.Sprintf("mounted %s at %s", m.Spec, m.MountPoint))
	}
	return done, errors.Join(errs...)
}

func mountOne(v Volume, m Mount) error {
	dev, err := blkid.Resolve(v, m.Spec)
	if err != nil {
		return err
	}
	fstype := m.FSType
	if fstype == "" || fstype == "auto" {
		info, ok := blkid.Identify(v, strings.TrimPrefix(dev, "/dev/"))
		if !ok {
			return fmt.Errorf("%s holds no filesystem k0smos recognises; name its type", dev)
		}
		fstype = info.FSType
	}
	var flags uintptr
	if m.ReadOnly {
		flags = msReadOnly
	}
	if err := v.Mkdir(m.MountPoint, 0755); err != nil {
		return err
	}
	return v.Mount(dev, m.MountPoint, fstype, flags, "")
}

// selectDevice resolves the spec to a device, or "" when there is no candidate.
func selectDevice(v Volume, o Options) (string, error) {
	if o.Spec != "auto" {
		// An explicit device or a label. A label that does not resolve is not an
		// error here: on a first boot the volume is blank and therefore unlabelled,
		// so the caller should use "auto" for that case.
		if strings.HasPrefix(o.Spec, "/dev/") {
			return o.Spec, nil
		}
		dev, err := blkid.Resolve(v, o.Spec)
		if err != nil {
			return "", nil
		}
		return dev, nil
	}

	// "auto": prefer a volume already carrying our label, which is the steady
	// state after the first boot.
	if dev, err := blkid.Resolve(v, "LABEL="+o.Label); err == nil {
		return dev, nil
	}

	// Otherwise look for a blank device. Anything with a filesystem is excluded,
	// so the root and the cloud-init drive can never be selected.
	candidates, err := blkid.Blank(v)
	if err != nil {
		return "", err
	}
	switch len(candidates) {
	case 0:
		return "", nil
	case 1:
		return "/dev/" + candidates[0], nil
	default:
		return "", fmt.Errorf("refusing to guess: %d blank devices (%v) — "+
			"name one with k0smos.data=/dev/...", len(candidates), candidates)
	}
}

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
	if _, formatted := blkid.Identify(v, strings.TrimPrefix(dev, "/dev/")); !formatted {
		if err := v.Mkfs(dev, o.FSType, o.Label); err != nil {
			return Result{}, fmt.Errorf("format %s as %s: %w", dev, o.FSType, err)
		}
		res.Formatted = true
	}

	if err := v.Mkdir(o.MountPoint, 0700); err != nil {
		return res, fmt.Errorf("mkdir %s: %w", o.MountPoint, err)
	}
	if err := v.Mount(dev, o.MountPoint, o.FSType, 0, ""); err != nil {
		return res, fmt.Errorf("mount %s at %s: %w", dev, o.MountPoint, err)
	}
	return res, nil
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

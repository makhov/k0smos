package mount

import (
	"fmt"
	"os"

	"github.com/amakhov/k0smos/internal/sys"
)

// Mounter is the subset of *sys.Sys that mounting needs.
type Mounter interface {
	Mounts() ([]sys.MountPoint, error)
	Mkdir(path string, perm os.FileMode) error
	Mount(source, target, fstype string, flags uintptr, data string) error
}

// Spec is one pseudo-filesystem to mount.
type Spec struct {
	Source string
	Target string
	FSType string
	Flags  uintptr
	Data   string
	Perm   os.FileMode
}

// Default is the base set every PID1 boot needs. cgroup2 is handled separately
// (internal/cgroup) because it needs post-mount controller setup.
var Default = []Spec{
	{Source: "proc", Target: "/proc", FSType: "proc", Perm: 0555},
	{Source: "sysfs", Target: "/sys", FSType: "sysfs", Perm: 0555},
	{Source: "devtmpfs", Target: "/dev", FSType: "devtmpfs", Perm: 0755},
	{Source: "devpts", Target: "/dev/pts", FSType: "devpts", Perm: 0755},
	{Source: "tmpfs", Target: "/dev/shm", FSType: "tmpfs", Perm: 0755},
	{Source: "tmpfs", Target: "/run", FSType: "tmpfs", Perm: 0755},
	{Source: "tmpfs", Target: "/tmp", FSType: "tmpfs", Perm: 01777},
}

// Ensure mounts every Default spec not already present.
//
// A mount table we cannot read is treated as an empty one rather than an
// error: on a cold boot Mounts() reads /proc/self/mountinfo, which does not
// exist until /proc is mounted, so the first call necessarily fails. /proc is
// first in Default, so the table becomes readable right after.
func Ensure(m Mounter) error {
	existing, err := m.Mounts()
	if err != nil {
		existing = nil
	}
	have := map[string]bool{}
	for _, mp := range existing {
		have[mp.Target] = true
	}
	for _, s := range Default {
		if have[s.Target] {
			continue
		}
		if err := m.Mkdir(s.Target, s.Perm); err != nil {
			return fmt.Errorf("mkdir %s: %w", s.Target, err)
		}
		if err := m.Mount(s.Source, s.Target, s.FSType, s.Flags, s.Data); err != nil {
			return fmt.Errorf("mount %s: %w", s.Target, err)
		}
	}
	return nil
}

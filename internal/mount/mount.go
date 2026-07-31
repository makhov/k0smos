package mount

import (
	"fmt"
	"os"
	"path/filepath"

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

// rwScratch holds the upper and work directories for the overlays below. /run is
// a tmpfs mounted by Ensure, so this needs no disk and vanishes on reboot.
const rwScratch = "/run/k0smos/rw"

// WritablePaths are the directories a read-only root cannot serve.
//
// Each is here because something observably failed without it:
//
//	/etc           k0s creates its containerd config directory under /etc/k0s and
//	               chmods it, dying with "can't create containerd config dir:
//	               chmod /etc/k0s: read-only file system". It is also where
//	               cloud-init write_files puts a user-supplied or
//	               k0smotron-supplied k0s.yaml, and where resolv.conf is written
//	               from the DHCP lease.
//	/usr/libexec   kubelet's dynamic plugin prober creates /usr/libexec/k0s.
//
// Two kinds of path are deliberately absent. Those that only need to *exist* are
// created when the image is built, which costs nothing at runtime — /var/run is a
// symlink to /run for that reason. And those that hold real data go on the data
// volume instead of a tmpfs: /opt is a symlink to /var/opt, because containerd
// stages plugins there and CNI binaries are tens of megabytes.
var WritablePaths = []string{"/etc", "/usr/libexec"}

// MakeWritable overlays a tmpfs on each path so it can be written on a read-only
// root, with the root's own contents showing through underneath.
//
// overlayfs rather than a plain tmpfs so that what the image ships stays visible:
// a tmpfs would hide /etc/k0s/k0s.yaml and /etc/passwd, and k0s needs both. A file
// written at runtime lands in the upper layer and shadows the image's copy, which
// is exactly the behaviour a user-supplied config wants.
func MakeWritable(m Mounter, paths []string) error {
	for _, path := range paths {
		upper := filepath.Join(rwScratch, path, "upper")
		work := filepath.Join(rwScratch, path, "work")
		for _, d := range []string{upper, work} {
			if err := m.Mkdir(d, 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", d, err)
			}
		}
		// upperdir and workdir must be on the same filesystem, which is why both
		// live under /run.
		data := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", path, upper, work)
		if err := m.Mount("overlay", path, "overlay", 0, data); err != nil {
			return fmt.Errorf("overlay %s: %w", path, err)
		}
	}
	return nil
}

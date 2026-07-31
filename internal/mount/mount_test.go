package mount

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amakhov/k0smos/internal/sys"
)

type fakeMounter struct {
	existing  []sys.MountPoint
	mountsErr error
	mounted   []string
	mkdirs    []string
	// calls records the full arguments, which the overlay tests need; mounted
	// stays a list of targets so the older tests read as they did.
	calls []mountCall
}

type mountCall struct {
	Source, Target, FSType, Data string
	Flags                        uintptr
}

func (f *fakeMounter) Mounts() ([]sys.MountPoint, error) { return f.existing, f.mountsErr }
func (f *fakeMounter) Mkdir(p string, _ os.FileMode) error {
	f.mkdirs = append(f.mkdirs, p)
	return nil
}
func (f *fakeMounter) Mount(source, target, fstype string, flags uintptr, data string) error {
	f.mounted = append(f.mounted, target)
	f.calls = append(f.calls, mountCall{source, target, fstype, data, flags})
	return nil
}

// On a real boot Mounts() reads /proc/self/mountinfo, which cannot be read
// until /proc itself is mounted — i.e. the very first call always fails. An
// unreadable mount table means "nothing is mounted yet", not a fatal error.
func TestEnsureProceedsWhenMountTableUnreadable(t *testing.T) {
	f := &fakeMounter{mountsErr: os.ErrNotExist}
	if err := Ensure(f); err != nil {
		t.Fatalf("Ensure = %v, want nil when mount table is unreadable", err)
	}
	if len(f.mounted) != len(Default) {
		t.Fatalf("mounted %d targets (%v), want all %d", len(f.mounted), f.mounted, len(Default))
	}
	if f.mounted[0] != "/proc" {
		t.Errorf("first mount = %q, want /proc so the table becomes readable", f.mounted[0])
	}
}

func TestEnsureMountsMissingSkipsExisting(t *testing.T) {
	f := &fakeMounter{existing: []sys.MountPoint{{Target: "/proc"}}}
	if err := Ensure(f); err != nil {
		t.Fatal(err)
	}
	for _, tgt := range f.mounted {
		if tgt == "/proc" {
			t.Error("/proc already mounted, should have been skipped")
		}
	}
	want := map[string]bool{"/sys": false, "/dev": false, "/run": false}
	for _, tgt := range f.mounted {
		if _, ok := want[tgt]; ok {
			want[tgt] = true
		}
	}
	for tgt, seen := range want {
		if !seen {
			t.Errorf("expected %s to be mounted", tgt)
		}
	}
}

// A read-only root cannot serve the paths k0s and cloud-init write to. The overlay
// has to be built so the image's own contents still show through: a plain tmpfs
// would hide /etc/passwd and the baked k0s.yaml, and k0s needs both.
func TestMakeWritableOverlaysWithLowerdir(t *testing.T) {
	f := &fakeMounter{}
	if err := MakeWritable(f, []string{"/etc", "/usr/libexec"}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("mounted %d filesystems, want 2: %+v", len(f.calls), f.calls)
	}
	for i, path := range []string{"/etc", "/usr/libexec"} {
		got := f.calls[i]
		if got.FSType != "overlay" || got.Target != path {
			t.Errorf("mount %d = %s on %s, want overlay on %s", i, got.FSType, got.Target, path)
		}
		// lowerdir must be the path itself, or the image's files disappear.
		for _, want := range []string{
			"lowerdir=" + path,
			"upperdir=" + filepath.Join(rwScratch, path, "upper"),
			"workdir=" + filepath.Join(rwScratch, path, "work"),
		} {
			if !strings.Contains(got.Data, want) {
				t.Errorf("overlay data %q is missing %q", got.Data, want)
			}
		}
	}
	// upperdir and workdir must exist before the mount, and on the same filesystem.
	for _, d := range f.mkdirs {
		if !strings.HasPrefix(d, rwScratch) {
			t.Errorf("created %s outside the scratch area", d)
		}
	}
	if len(f.mkdirs) != 4 {
		t.Errorf("created %d directories, want 4 (upper+work per path)", len(f.mkdirs))
	}
}

// The scratch area lives on /run, which is a tmpfs Ensure already mounted — so an
// overlay costs no disk and cannot outlive a reboot.
func TestWritableScratchIsUnderRun(t *testing.T) {
	if !strings.HasPrefix(rwScratch, "/run/") {
		t.Errorf("rwScratch = %q, want it under /run so it is a tmpfs", rwScratch)
	}
}

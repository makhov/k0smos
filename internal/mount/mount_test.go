package mount

import (
	"os"
	"testing"

	"github.com/amakhov/k0smos/internal/sys"
)

type fakeMounter struct {
	existing  []sys.MountPoint
	mountsErr error
	mounted   []string
	mkdirs    []string
}

func (f *fakeMounter) Mounts() ([]sys.MountPoint, error) { return f.existing, f.mountsErr }
func (f *fakeMounter) Mkdir(p string, _ os.FileMode) error {
	f.mkdirs = append(f.mkdirs, p)
	return nil
}
func (f *fakeMounter) Mount(_, target, _ string, _ uintptr, _ string) error {
	f.mounted = append(f.mounted, target)
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

package mount

import (
	"os"
	"testing"

	"github.com/amakhov/k0smos/internal/sys"
)

type fakeMounter struct {
	existing []sys.MountPoint
	mounted  []string
	mkdirs   []string
}

func (f *fakeMounter) Mounts() ([]sys.MountPoint, error) { return f.existing, nil }
func (f *fakeMounter) Mkdir(p string, _ os.FileMode) error {
	f.mkdirs = append(f.mkdirs, p)
	return nil
}
func (f *fakeMounter) Mount(_, target, _ string, _ uintptr, _ string) error {
	f.mounted = append(f.mounted, target)
	return nil
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

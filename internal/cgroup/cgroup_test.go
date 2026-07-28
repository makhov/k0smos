package cgroup

import (
	"os"
	"strings"
	"testing"
)

type fakeCgroup struct {
	mountedTarget string
	writes        map[string]string
}

func (f *fakeCgroup) Mkdir(string, os.FileMode) error { return nil }
func (f *fakeCgroup) Mount(_, target, fstype string, _ uintptr, _ string) error {
	f.mountedTarget = target
	return nil
}
func (f *fakeCgroup) WriteFile(path string, data []byte, _ os.FileMode) error {
	f.writes[path] = string(data)
	return nil
}

func TestSetupMountsAndEnablesControllers(t *testing.T) {
	f := &fakeCgroup{writes: map[string]string{}}
	if err := Setup(f); err != nil {
		t.Fatal(err)
	}
	if f.mountedTarget != "/sys/fs/cgroup" {
		t.Errorf("mounted %q, want /sys/fs/cgroup", f.mountedTarget)
	}
	got := f.writes["/sys/fs/cgroup/cgroup.subtree_control"]
	for _, ctrl := range []string{"+cpu", "+memory", "+pids", "+io"} {
		if !strings.Contains(got, ctrl) {
			t.Errorf("subtree_control %q missing %s", got, ctrl)
		}
	}
}

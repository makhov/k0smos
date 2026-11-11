package cgroup

import (
	"os"
	"strings"
	"testing"
)

type fakeCgroup struct {
	mountedTarget string
	writes        map[string]string
	// offered stands in for cgroup.controllers, i.e. what the kernel has.
	offered string
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
func (f *fakeCgroup) ReadFile(string) ([]byte, error) { return []byte(f.offered), nil }

func TestSetupMountsAndEnablesControllers(t *testing.T) {
	f := &fakeCgroup{writes: map[string]string{}, offered: strings.Join(Wanted, " ")}
	if _, err := Setup(f); err != nil {
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

// Enabling a controller the kernel does not have fails the whole write with
// EINVAL and takes the others down with it, so a node that simply lacks cpuset
// or hugetlb must still get the ones it does have.
func TestSetupEnablesOnlyWhatTheKernelOffers(t *testing.T) {
	f := &fakeCgroup{writes: map[string]string{}, offered: "cpu memory pids io"}
	enabled, err := Setup(f)
	if err != nil {
		t.Fatal(err)
	}
	// The report is what the console and `machine status` show, so it has to
	// match what was delegated rather than what was wanted.
	if strings.Join(enabled, " ") != "cpu memory pids io" {
		t.Errorf("enabled = %v, want only the four the kernel offers", enabled)
	}
	got := f.writes["/sys/fs/cgroup/cgroup.subtree_control"]
	for _, want := range []string{"+cpu", "+memory", "+pids", "+io"} {
		if !strings.Contains(got, want) {
			t.Errorf("subtree_control %q missing %s", got, want)
		}
	}
	for _, absent := range []string{"cpuset", "hugetlb"} {
		if strings.Contains(got, absent) {
			t.Errorf("subtree_control %q asks for %s, which this kernel does not offer", got, absent)
		}
	}
}

// Missing cpu, memory or pids is a broken node rather than a missing feature:
// kubelet cannot apply a pod limit at all, so say so instead of booting into it.
func TestSetupRefusesAKernelWithoutTheCoreControllers(t *testing.T) {
	f := &fakeCgroup{writes: map[string]string{}, offered: "cpu pids io"}
	_, err := Setup(f)
	if err == nil {
		t.Fatal("Setup = nil, want an error when the memory controller is absent")
	}
	if !strings.Contains(err.Error(), "memory") {
		t.Errorf("error = %v, want it to name the controller that is missing", err)
	}
}

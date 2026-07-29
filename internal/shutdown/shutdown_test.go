package shutdown

import "testing"

type fakeShutdowner struct {
	mounts     []string
	order      []string
	unmounted  []string
	rebootWith int
}

func (f *fakeShutdowner) Mounts() ([]string, error) {
	if f.mounts == nil {
		return []string{"/var/lib/k0s", "/proc"}, nil
	}
	return f.mounts, nil
}
func (f *fakeShutdowner) Sync() { f.order = append(f.order, "sync") }
func (f *fakeShutdowner) Unmount(target string, _ int) error {
	f.order = append(f.order, "unmount:"+target)
	f.unmounted = append(f.unmounted, target)
	return nil
}
func (f *fakeShutdowner) Reboot(cmd int) error {
	f.order = append(f.order, "reboot")
	f.rebootWith = cmd
	return nil
}

func TestDoSyncsUnmountsThenReboots(t *testing.T) {
	f := &fakeShutdowner{}
	if err := Do(f, PowerOff); err != nil {
		t.Fatal(err)
	}
	if f.order[0] != "sync" {
		t.Errorf("first op %q, want sync", f.order[0])
	}
	if f.order[len(f.order)-1] != "reboot" {
		t.Errorf("last op %q, want reboot", f.order[len(f.order)-1])
	}
	// /proc is pseudo → must be skipped; /var/lib/k0s must be unmounted.
	if len(f.unmounted) != 1 || f.unmounted[0] != "/var/lib/k0s" {
		t.Errorf("unmounted = %v, want [/var/lib/k0s]", f.unmounted)
	}
	if f.rebootWith != PowerOff {
		t.Errorf("reboot cmd = %d, want POWER_OFF", f.rebootWith)
	}
}

func TestDoUnmountsDeepestFirst(t *testing.T) {
	f := &fakeShutdowner{mounts: []string{"/mnt", "/mnt/data", "/mnt/data/sub", "/"}}
	if err := Do(f, Reboot); err != nil {
		t.Fatal(err)
	}
	want := []string{"/mnt/data/sub", "/mnt/data", "/mnt"}
	if len(f.unmounted) != len(want) {
		t.Fatalf("unmounted = %v, want %v", f.unmounted, want)
	}
	for i := range want {
		if f.unmounted[i] != want[i] {
			t.Fatalf("unmounted = %v, want %v", f.unmounted, want)
		}
	}
	if f.rebootWith != Reboot {
		t.Errorf("reboot cmd = %d, want RESTART", f.rebootWith)
	}
}

func TestIsPseudoDoesNotMatchPrefixSiblings(t *testing.T) {
	if isPseudo("/procession") {
		t.Error("/procession classified as pseudo")
	}
	if isPseudo("/run-data") {
		t.Error("/run-data classified as pseudo")
	}
	if !isPseudo("/sys/fs/cgroup") {
		t.Error("/sys/fs/cgroup not classified as pseudo")
	}
}

package sys

import "testing"

func TestMountTargetsExtractsTargets(t *testing.T) {
	// exercise the pure extraction the helper delegates to
	mps := []MountPoint{{Target: "/proc"}, {Target: "/var/lib/k0s"}}
	got := targetsOf(mps)
	if len(got) != 2 || got[0] != "/proc" || got[1] != "/var/lib/k0s" {
		t.Errorf("targetsOf = %v", got)
	}
}

func TestMountTargetsOfEmpty(t *testing.T) {
	if got := targetsOf(nil); len(got) != 0 {
		t.Errorf("targetsOf(nil) = %v, want empty", got)
	}
}

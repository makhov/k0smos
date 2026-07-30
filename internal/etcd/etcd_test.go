package etcd

import (
	"slices"
	"testing"
)

func TestLeaveCmdForController(t *testing.T) {
	got := LeaveCmd([]string{"/usr/local/bin/k0s", "controller", "--enable-dynamic-config"})
	want := []string{"/usr/local/bin/k0s", "etcd", "leave"}
	if !slices.Equal(got, want) {
		t.Errorf("LeaveCmd = %v, want %v", got, want)
	}
}

// etcd leave needs to find the same data dir and status socket the running
// controller uses, or it cannot talk to it.
func TestLeaveCmdPropagatesDataDirAndSocket(t *testing.T) {
	got := LeaveCmd([]string{
		"k0s", "controller",
		"--data-dir", "/var/lib/custom",
		"--status-socket", "/run/k0s/status.sock",
		"--config", "/etc/k0s/k0s.yaml",
	})
	want := []string{
		"k0s", "etcd", "leave",
		"--data-dir", "/var/lib/custom",
		"--status-socket", "/run/k0s/status.sock",
	}
	if !slices.Equal(got, want) {
		t.Errorf("LeaveCmd = %v, want %v", got, want)
	}
}

func TestLeaveCmdAcceptsJoinedFlagForm(t *testing.T) {
	got := LeaveCmd([]string{"k0s", "controller", "--data-dir=/d", "--status-socket=/s"})
	want := []string{"k0s", "etcd", "leave", "--data-dir", "/d", "--status-socket", "/s"}
	if !slices.Equal(got, want) {
		t.Errorf("LeaveCmd = %v, want %v", got, want)
	}
}

// Cases with no etcd membership to give up. Leaving would either fail or, worse,
// be meaningless — so nothing should be attempted.
func TestLeaveCmdSkipsWhenThereIsNoEtcd(t *testing.T) {
	for _, tc := range []struct {
		name     string
		workload []string
	}{
		{"worker holds no etcd", []string{"k0s", "worker", "--token-file", "/t"}},
		{"single node uses kine, not etcd", []string{"k0s", "controller", "--single"}},
		{"not k0s at all", []string{"/bin/true"}},
		{"no role", []string{"k0s"}},
		{"empty", nil},
	} {
		if got := LeaveCmd(tc.workload); got != nil {
			t.Errorf("%s: LeaveCmd = %v, want nil", tc.name, got)
		}
	}
}

// --enable-worker on a controller still means etcd is present.
func TestLeaveCmdControllerWithWorkerEnabled(t *testing.T) {
	if got := LeaveCmd([]string{"k0s", "controller", "--enable-worker"}); got == nil {
		t.Error("LeaveCmd = nil, want a leave command for a controller+worker")
	}
}

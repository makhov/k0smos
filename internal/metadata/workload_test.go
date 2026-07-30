package metadata

import (
	"slices"
	"testing"
)

// Bootstrap providers assume systemd: they run `k0s install <role>` to write a
// unit, then `k0s start`. k0smos supervises a single child instead, so the
// install form must be translated into the equivalent foreground command.
func TestWorkloadTranslatesK0sInstall(t *testing.T) {
	u := UserData{RunCmd: [][]string{
		{"k0s", "install", "controller", "--enable-worker", "--config", "/etc/k0s/k0s.yaml"},
		{"k0s", "start"},
	}}
	argv, oneshots := u.Workload()
	want := []string{"k0s", "controller", "--enable-worker", "--config", "/etc/k0s/k0s.yaml"}
	if !slices.Equal(argv, want) {
		t.Errorf("workload = %v, want %v", argv, want)
	}
	if len(oneshots) != 0 {
		t.Errorf("oneshots = %v, want none (k0s start is implied by supervising)", oneshots)
	}
}

func TestWorkloadHandlesWorkerRole(t *testing.T) {
	u := UserData{RunCmd: [][]string{
		{"k0s", "install", "worker", "--token-file", "/etc/k0s/join-token"},
	}}
	argv, _ := u.Workload()
	want := []string{"k0s", "worker", "--token-file", "/etc/k0s/join-token"}
	if !slices.Equal(argv, want) {
		t.Errorf("workload = %v, want %v", argv, want)
	}
}

// An absolute path must survive translation.
func TestWorkloadKeepsBinaryPath(t *testing.T) {
	u := UserData{RunCmd: [][]string{{"/usr/local/bin/k0s", "install", "controller"}}}
	argv, _ := u.Workload()
	if !slices.Equal(argv, []string{"/usr/local/bin/k0s", "controller"}) {
		t.Errorf("workload = %v", argv)
	}
}

// Commands that are neither the install form nor a service-manager call are
// genuine one-shot setup steps and must still run.
func TestWorkloadKeepsOtherCommandsAsOneshots(t *testing.T) {
	u := UserData{RunCmd: [][]string{
		{"mkdir", "-p", "/var/lib/foo"},
		{"k0s", "install", "controller"},
		{"systemctl", "enable", "k0s"},
		{"chmod", "0600", "/etc/k0s/token"},
	}}
	argv, oneshots := u.Workload()
	if !slices.Equal(argv, []string{"k0s", "controller"}) {
		t.Errorf("workload = %v", argv)
	}
	if len(oneshots) != 2 {
		t.Fatalf("oneshots = %v, want mkdir and chmod only", oneshots)
	}
	if !slices.Equal(oneshots[0], []string{"mkdir", "-p", "/var/lib/foo"}) {
		t.Errorf("oneshots[0] = %v", oneshots[0])
	}
	if !slices.Equal(oneshots[1], []string{"chmod", "0600", "/etc/k0s/token"}) {
		t.Errorf("oneshots[1] = %v", oneshots[1])
	}
}

// No install line means no workload was described; the caller keeps its default.
func TestWorkloadEmptyWhenNoInstall(t *testing.T) {
	u := UserData{RunCmd: [][]string{{"mkdir", "/tmp/x"}}}
	argv, oneshots := u.Workload()
	if argv != nil {
		t.Errorf("workload = %v, want nil", argv)
	}
	if len(oneshots) != 1 {
		t.Errorf("oneshots = %v, want the mkdir", oneshots)
	}
}

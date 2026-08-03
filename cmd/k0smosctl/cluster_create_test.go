package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amakhov/k0smos/internal/iso9660"
	"github.com/amakhov/k0smos/internal/metadata"
)

type isoFile struct{ *os.File }

func (f isoFile) ReadAt(p []byte, off int64) error {
	_, err := f.File.ReadAt(p, off)
	return err
}

func openTestISO(t *testing.T, path string) *iso9660.Reader {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	r, err := iso9660.Open(isoFile{f})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func shortStateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "kc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestPlanClusterAssignsStableNamesAddressesAndPorts(t *testing.T) {
	t.Setenv("K0SMOS_STATE_DIR", shortStateDir(t))
	machines, err := planCluster(clusterCreateOptions{
		name: "demo", controllers: 3, workers: 2, apiPort: 7443, memory: 4096, cpus: 2,
		timeout: time.Minute, kubeconfig: "kubeconfig",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(machines) != 5 {
		t.Fatalf("machines = %d, want 5", len(machines))
	}
	wantNames := []string{"demo-controller-0", "demo-controller-1", "demo-controller-2", "demo-worker-0", "demo-worker-1"}
	for i, m := range machines {
		if m.Name != wantNames[i] || m.IP != clusterNodeIP(i) {
			t.Errorf("machine %d = %#v, want name %s and IP %s", i, m, wantNames[i], clusterNodeIP(i))
		}
	}
	if machines[0].APIPort != 7443 || machines[2].APIPort != 7445 || machines[3].APIPort != 0 {
		t.Errorf("unexpected API ports: %#v", machines)
	}
}

func TestClusterDriveConfiguresNetworkAndController(t *testing.T) {
	dir := t.TempDir()
	machines := []clusterMachine{
		{Name: "demo-controller-0", Role: "controller", IP: "10.10.0.11", APIPort: 6443},
		{Name: "demo-worker-0", Role: "worker", IP: "10.10.0.12"},
	}
	if err := writeClusterDrive(dir, machines[0], machines, ""); err != nil {
		t.Fatal(err)
	}
	r := openTestISO(t, filepath.Join(dir, machines[0].Name+".iso"))
	ud, md, err := metadata.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if md.Hostname != machines[0].Name {
		t.Errorf("hostname = %q, want %q", md.Hostname, machines[0].Name)
	}
	if ud.Machine.IP != "eth0:dhcp,eth1:10.10.0.11/24" {
		t.Errorf("machine IP = %q", ud.Machine.IP)
	}
	plan := ud.Plan()
	for _, want := range []string{
		"controller", "--enable-worker", "--config=/etc/k0s/k0s.yaml",
		"--kubelet-extra-args=--node-ip=10.10.0.11",
	} {
		if !slices.Contains(plan.Workload, want) {
			t.Errorf("controller workload %v is missing %q", plan.Workload, want)
		}
	}
	if len(ud.WriteFiles) != 1 || !strings.Contains(ud.WriteFiles[0].Content, "address: 10.10.0.11") {
		t.Errorf("controller config was not written: %#v", ud.WriteFiles)
	}
}

func TestClusterDrivePlacesJoinTokenOnWorker(t *testing.T) {
	dir := t.TempDir()
	machine := clusterMachine{Name: "demo-worker-0", Role: "worker", IP: "10.10.0.12"}
	if err := writeClusterDrive(dir, machine, []clusterMachine{machine}, "secret-token"); err != nil {
		t.Fatal(err)
	}
	r := openTestISO(t, filepath.Join(dir, machine.Name+".iso"))
	ud, _, err := metadata.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(ud.WriteFiles) != 1 || ud.WriteFiles[0].Path != "/etc/k0s/join-token" || ud.WriteFiles[0].Content != "secret-token" {
		t.Fatalf("worker token file = %#v", ud.WriteFiles)
	}
	if ud.WriteFiles[0].Mode() != 0600 {
		t.Errorf("token mode = %o, want 0600", ud.WriteFiles[0].Mode())
	}
	if !slices.Contains(ud.Plan().Workload, "--token-file=/etc/k0s/join-token") {
		t.Errorf("worker workload does not consume token: %v", ud.Plan().Workload)
	}
}

func TestClusterDryRunHasNoStateSideEffects(t *testing.T) {
	state := shortStateDir(t)
	t.Setenv("K0SMOS_STATE_DIR", state)
	cmd := clusterCreateCmd()
	cmd.SetArgs([]string{"--name", "demo", "--controllers", "2", "--workers", "1", "--dry-run"})
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "demo-controller-0") || !strings.Contains(out.String(), "demo-worker-0") {
		t.Errorf("dry-run output did not describe the plan:\n%s", out.String())
	}
	entries, err := os.ReadDir(state)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dry-run created state: %v", entries)
	}
}

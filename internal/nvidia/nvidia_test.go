package nvidia

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Two GPUs whose minors are not their order in the directory, as on a host
// with GPUs passed through out of PCI order, and nvidia_uvm loaded.
func TestNodesForGPUsAndUVM(t *testing.T) {
	proc := t.TempDir()
	write(t, filepath.Join(proc, "driver/nvidia/gpus/0000:18:00.0/information"), "Model: \t\t NVIDIA H100 80GB HBM3\nIRQ:   \t\t 120\nDevice Minor: \t 1\n")
	write(t, filepath.Join(proc, "driver/nvidia/gpus/0000:2a:00.0/information"), "Model: \t\t NVIDIA H100 80GB HBM3\nDevice Minor: \t 0\n")
	write(t, filepath.Join(proc, "devices"), "Character devices:\n  1 mem\n195 nvidia\n195 nvidiactl\n507 nvidia-uvm\n\nBlock devices:\n259 blkext\n")

	got, err := Nodes(proc)
	if err != nil {
		t.Fatal(err)
	}
	want := []Node{
		{"/dev/nvidia1", 195, 1, 0o666},
		{"/dev/nvidia0", 195, 0, 0o666},
		{"/dev/nvidiactl", 195, 255, 0o666},
		{"/dev/nvidia-uvm", 507, 0, 0o666},
		{"/dev/nvidia-uvm-tools", 507, 1, 0o666},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

// No driver, or a driver that found nothing: no nodes, and not an error --
// the same image boots machines without GPUs.
func TestNodesWithoutGPUs(t *testing.T) {
	proc := t.TempDir()
	if got, err := Nodes(proc); err != nil || got != nil {
		t.Errorf("no driver: %v, %v", got, err)
	}
	write(t, filepath.Join(proc, "devices"), "Character devices:\n195 nvidia\n")
	if err := os.MkdirAll(filepath.Join(proc, "driver/nvidia/gpus"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := Nodes(proc); err != nil || got != nil {
		t.Errorf("no GPUs: %v, %v", got, err)
	}
}

// Without nvidia_uvm there is no uvm node to make, but the GPUs still get theirs.
func TestNodesWithoutUVM(t *testing.T) {
	proc := t.TempDir()
	write(t, filepath.Join(proc, "driver/nvidia/gpus/0000:18:00.0/information"), "Device Minor: 0\n")
	write(t, filepath.Join(proc, "devices"), "Character devices:\n195 nvidia\n\nBlock devices:\n507 nvidia-uvm\n")
	got, err := Nodes(proc)
	if err != nil || len(got) != 2 {
		t.Errorf("got %v, %v; want nvidia0 and nvidiactl", got, err)
	}
}

// A GPU whose information file says nothing is reported, but the machine keeps
// the GPUs that did answer -- and nvidiactl, without which none of them work.
func TestNodesSkipsUnreadableGPU(t *testing.T) {
	proc := t.TempDir()
	write(t, filepath.Join(proc, "driver/nvidia/gpus/0000:18:00.0/information"), "Device Minor: 0\n")
	write(t, filepath.Join(proc, "driver/nvidia/gpus/0000:2a:00.0/information"), "Model: NVIDIA H100 80GB HBM3\n")
	write(t, filepath.Join(proc, "devices"), "Character devices:\n195 nvidia\n")

	got, err := Nodes(proc)
	if err == nil {
		t.Error("want an error naming the GPU that could not be read")
	}
	want := []Node{{"/dev/nvidia0", 195, 0, 0o666}, {"/dev/nvidiactl", 195, 255, 0o666}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

// The banner NVIDIA's driver writes, with the version buried in the middle of
// a line that also carries the architecture and the build date.
func TestVersionReadsTheDriverBanner(t *testing.T) {
	proc := t.TempDir()
	write(t, filepath.Join(proc, "driver/nvidia/version"),
		"NVRM version: NVIDIA UNIX x86_64 Kernel Module  620.70.06  Tue Sep  9 10:00:00 UTC 2025\nGCC version:  gcc 14\n")
	if got := Version(proc); got != "620.70.06" {
		t.Errorf("Version() = %q, want 620.70.06", got)
	}
}

// No driver loaded is the normal case on a node without a GPU, and must read
// as "nothing to report" rather than an error or a panic.
func TestVersionWithoutTheDriverIsEmpty(t *testing.T) {
	proc := t.TempDir()
	if got := Version(proc); got != "" {
		t.Errorf("Version() = %q, want empty", got)
	}
	write(t, filepath.Join(proc, "driver/nvidia/version"), "NVRM version: NVIDIA UNIX x86_64 Kernel Module\n")
	if got := Version(proc); got != "" {
		t.Errorf("Version() on a truncated banner = %q, want empty", got)
	}
}

// The capability table is static: the driver lists every GPU it could ever
// see, with or without MIG instances. Only the lines for the GPUs this machine
// has become nodes, and the capability that repartitions a GPU stays root-only.
func TestNodesIncludeCapabilitiesForThisMachinesGPUs(t *testing.T) {
	proc := t.TempDir()
	write(t, filepath.Join(proc, "driver/nvidia/gpus/0000:18:00.0/information"), "Device Minor: 0\n")
	write(t, filepath.Join(proc, "devices"), "Character devices:\n195 nvidia\n508 nvidia-caps\n")
	write(t, filepath.Join(proc, "driver/nvidia-caps/mig-minors"),
		"config 1\nmonitor 2\ngpu0/gi0/access 3\ngpu0/gi0/ci0/access 4\ngpu7/gi0/access 948\n")
	write(t, filepath.Join(proc, "driver/nvidia-caps/sys-minors"), "fabric-imex-mgmt 4323\nprofiler-device 4324\n")

	got, err := Nodes(proc)
	if err != nil {
		t.Fatal(err)
	}
	want := []Node{
		{"/dev/nvidia0", 195, 0, 0o666},
		{"/dev/nvidiactl", 195, 255, 0o666},
		{"/dev/nvidia-caps/nvidia-cap1", 508, 1, 0o400},
		{"/dev/nvidia-caps/nvidia-cap2", 508, 2, 0o444},
		{"/dev/nvidia-caps/nvidia-cap3", 508, 3, 0o444},
		{"/dev/nvidia-caps/nvidia-cap4", 508, 4, 0o444},
		{"/dev/nvidia-caps/nvidia-cap4323", 508, 4323, 0o400},
		{"/dev/nvidia-caps/nvidia-cap4324", 508, 4324, 0o444},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

// A driver without the capability device -- too old, or built without it --
// still gets its GPU nodes, and no capability node is invented for it.
func TestNodesWithoutTheCapabilityDevice(t *testing.T) {
	proc := t.TempDir()
	write(t, filepath.Join(proc, "driver/nvidia/gpus/0000:18:00.0/information"), "Device Minor: 0\n")
	write(t, filepath.Join(proc, "devices"), "Character devices:\n195 nvidia\n")
	write(t, filepath.Join(proc, "driver/nvidia-caps/mig-minors"), "config 1\n")

	got, err := Nodes(proc)
	if err != nil || len(got) != 2 {
		t.Errorf("got %v, %v; want nvidia0 and nvidiactl only", got, err)
	}
}

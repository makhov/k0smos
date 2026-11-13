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
		{"/dev/nvidia1", 195, 1},
		{"/dev/nvidia0", 195, 0},
		{"/dev/nvidiactl", 195, 255},
		{"/dev/nvidia-uvm", 507, 0},
		{"/dev/nvidia-uvm-tools", 507, 1},
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

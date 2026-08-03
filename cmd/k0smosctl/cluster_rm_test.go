package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestClusterRemoveDiscardsStoppedMachinesAndState(t *testing.T) {
	stateRoot := shortStateDir(t)
	t.Setenv("K0SMOS_STATE_DIR", stateRoot)
	machine := clusterMachine{Name: "demo-controller-0", Role: "controller", IP: "10.10.0.11"}
	machineDir, err := ensureGuestDir(machine.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machineDir, "machine.qcow2"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	clusterDir, err := clusterStateDir("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(clusterDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(clusterDir, "cluster.json"), clusterMeta{Name: "demo", Machines: []clusterMachine{machine}}); err != nil {
		t.Fatal(err)
	}

	cmd := clusterRemoveCmd()
	cmd.SetArgs([]string{"--name", "demo"})
	cmd.SetOut(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{machineDir, clusterDir} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still exists after cluster rm", path)
		}
	}
}

package main

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

func childNames(cmd *cobra.Command) []string {
	names := make([]string, 0, len(cmd.Commands()))
	for _, child := range cmd.Commands() {
		names = append(names, child.Name())
	}
	return names
}

func TestCommandHierarchySeparatesMachinesAndClusters(t *testing.T) {
	r := root()
	top := childNames(r)
	for _, removed := range []string{"boot", "logs", "list", "kubeconfig", "token", "shutdown", "reboot", "rm"} {
		if slices.Contains(top, removed) {
			t.Errorf("legacy top-level command %q still exists: %v", removed, top)
		}
	}

	machine, _, err := r.Find([]string{"machine"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"up", "logs", "list", "shutdown", "reboot", "rm"} {
		if !slices.Contains(childNames(machine), want) {
			t.Errorf("machine commands %v do not contain %q", childNames(machine), want)
		}
	}

	cluster, _, err := r.Find([]string{"cluster"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"create", "kubeconfig", "token", "rm"} {
		if !slices.Contains(childNames(cluster), want) {
			t.Errorf("cluster commands %v do not contain %q", childNames(cluster), want)
		}
	}
}

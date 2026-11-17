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

// "ls" was an alias of "machine list", so adding a directory listing under the
// same name silently shadowed it: the command parsed, then rejected the flags it
// was given. Nothing in a handler can catch that, so the resolution is asserted
// here.
func TestMachineLsIsTheDirectoryListing(t *testing.T) {
	r := root()
	for _, tc := range []struct {
		path []string
		want string
	}{
		{[]string{"machine", "ls"}, "ls"},
		{[]string{"machine", "list"}, "list"},
		{[]string{"machine", "dial"}, "dial"},
		{[]string{"machine", "cat"}, "cat"},
	} {
		cmd, _, err := r.Find(tc.path)
		if err != nil {
			t.Fatalf("%v: %v", tc.path, err)
		}
		if cmd.Name() != tc.want {
			t.Errorf("%v resolved to %q, want %q", tc.path, cmd.Name(), tc.want)
		}
	}
}

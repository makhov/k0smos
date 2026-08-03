// Package etcd builds the command that gives up this node's etcd membership
// before it shuts down.
//
// It matters because nothing on a k0smos machine is designed to persist: with
// Cluster API replacing machines rather than repairing them, and a KubeVirt
// node keeping mutable state on a replaceable data volume, every stop is
// effectively a permanent departure. An etcd member that vanishes without
// leaving stays in the member list and counts against quorum, so a
// three-controller cluster degrades with each replacement instead of staying
// healthy.
//
// This is the one place k0smos runs a command rather than interpreting one, and
// it is not a contradiction of that rule: the binary is the workload k0smos is
// already supervising, and the subcommand is fixed here rather than taken from
// user-data.
package etcd

import (
	"path"
	"strings"
)

// flagsToPropagate are the workload flags `k0s etcd leave` also understands. It
// needs them to find the running controller; without them it looks in the
// default location and fails on a customised setup.
var flagsToPropagate = []string{"--data-dir", "--status-socket"}

// LeaveCmd returns the argv for a graceful etcd leave, or nil when there is no
// etcd membership to give up: workers hold none, and --single means k0s is using
// kine rather than etcd.
func LeaveCmd(workload []string) []string {
	if len(workload) < 2 || path.Base(workload[0]) != "k0s" {
		return nil
	}
	if workload[1] != "controller" {
		return nil
	}
	args := workload[2:]
	for _, a := range args {
		if a == "--single" || strings.HasPrefix(a, "--single=") {
			return nil // kine-backed, no etcd cluster to leave
		}
	}

	out := []string{workload[0], "etcd", "leave"}
	for _, want := range flagsToPropagate {
		if v, ok := flagValue(args, want); ok {
			out = append(out, want, v)
		}
	}
	return out
}

// flagValue finds "--flag value" or "--flag=value".
func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
		if v, ok := strings.CutPrefix(a, name+"="); ok {
			return v, true
		}
	}
	return "", false
}

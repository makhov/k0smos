package cgroup

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

const (
	root      = "/sys/fs/cgroup"
	subtree   = root + "/cgroup.subtree_control"
	available = root + "/cgroup.controllers"
)

// Wanted are the controllers delegated to child cgroups when the kernel has
// them. cpu, memory, pids and io are what containerd, kubelet and runc need.
//
// cpuset and hugetlb are for a host that pins sandbox vCPUs to cores and backs
// their memory with hugepages; they are listed here rather than demanded,
// because a kernel without CONFIG_CPUSETS or CONFIG_CGROUP_HUGETLB must still
// boot a plain k0s node.
var Wanted = []string{"cpu", "memory", "pids", "io", "cpuset", "hugetlb"}

// Required are the controllers whose absence is a broken node rather than a
// missing feature: without them kubelet cannot set a pod limit at all.
var Required = []string{"cpu", "memory", "pids"}

// Controller is the subset of *sys.Sys that cgroup setup needs.
type Controller interface {
	Mkdir(path string, perm os.FileMode) error
	Mount(source, target, fstype string, flags uintptr, data string) error
	WriteFile(path string, data []byte, perm os.FileMode) error
	ReadFile(path string) ([]byte, error)
}

// Setup mounts the cgroup2 unified hierarchy and delegates the core
// controllers to child cgroups so containerd/kubelet/runc can use them.
func Setup(c Controller) error {
	if err := c.Mkdir(root, 0755); err != nil {
		return fmt.Errorf("mkdir cgroup root: %w", err)
	}
	if err := c.Mount("cgroup2", root, "cgroup2", 0, "nsdelegate"); err != nil {
		return fmt.Errorf("mount cgroup2: %w", err)
	}
	// Only what this kernel advertises. The write is all-or-nothing — one
	// unavailable controller fails the whole string with EINVAL and takes the
	// others with it — so asking for cpuset or hugetlb unconditionally would
	// turn a missing feature into a node with no cgroup delegation at all.
	have, err := c.ReadFile(available)
	if err != nil {
		return fmt.Errorf("read available controllers: %w", err)
	}
	offered := strings.Fields(string(have))
	for _, need := range Required {
		if !slices.Contains(offered, need) {
			return fmt.Errorf("kernel offers no %s controller (has %v), so kubelet cannot limit pods", need, offered)
		}
	}
	var enable []string
	for _, want := range Wanted {
		if slices.Contains(offered, want) {
			enable = append(enable, "+"+want)
		}
	}
	if err := c.WriteFile(subtree, []byte(strings.Join(enable, " ")), 0644); err != nil {
		return fmt.Errorf("enable controllers %v: %w", enable, err)
	}
	return nil
}

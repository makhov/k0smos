package cgroup

import (
	"fmt"
	"os"
)

const (
	root    = "/sys/fs/cgroup"
	subtree = root + "/cgroup.subtree_control"
)

// Controller is the subset of *sys.Sys that cgroup setup needs.
type Controller interface {
	Mkdir(path string, perm os.FileMode) error
	Mount(source, target, fstype string, flags uintptr, data string) error
	WriteFile(path string, data []byte, perm os.FileMode) error
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
	if err := c.WriteFile(subtree, []byte("+cpu +memory +pids +io"), 0644); err != nil {
		return fmt.Errorf("enable controllers: %w", err)
	}
	return nil
}

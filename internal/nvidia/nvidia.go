// Package nvidia works out the device nodes NVIDIA's driver needs. A distro
// leaves them to nvidia-modprobe or udev; k0smos has neither, and without them
// neither nvidia-smi nor the GPU Operator's device plugin finds a GPU.
package nvidia

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Node is a character device to create, and the mode it is meant to have.
type Node struct {
	Path         string
	Major, Minor uint32
	Mode         os.FileMode
}

// nvidia.ko's fixed major; nvidiactl is its last minor.
const (
	major       = 195
	controlNode = 255
)

// Nodes lists the nodes for what the loaded driver found, read from proc
// (normally "/proc"): nvidiactl, one /dev/nvidiaN per GPU, nvidia-uvm and
// nvidia-uvm-tools when nvidia_uvm is loaded, and the capability nodes under
// /dev/nvidia-caps. Nothing, and no error, when nvidia.ko is not loaded or
// found no GPU.
//
// A GPU that cannot be read is reported but does not stop the others: one card
// off the bus must not cost the machine every GPU it has. So the nodes come
// back alongside an error, and the caller creates what it was given.
func Nodes(proc string) ([]Node, error) {
	entries, err := os.ReadDir(filepath.Join(proc, "driver/nvidia/gpus"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var nodes []Node
	var errs error
	gpus := map[uint32]bool{}
	for _, g := range entries {
		minor, err := deviceMinor(filepath.Join(proc, "driver/nvidia/gpus", g.Name(), "information"))
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("GPU %s: %w", g.Name(), err))
			continue
		}
		gpus[minor] = true
		nodes = append(nodes, Node{fmt.Sprintf("/dev/nvidia%d", minor), major, minor, 0o666})
	}
	if len(nodes) == 0 {
		return nil, errs
	}
	nodes = append(nodes, Node{"/dev/nvidiactl", major, controlNode, 0o666})

	uvm, err := charMajor(filepath.Join(proc, "devices"), "nvidia-uvm")
	if err != nil {
		return nodes, errors.Join(errs, err)
	}
	if uvm != 0 {
		nodes = append(nodes, Node{"/dev/nvidia-uvm", uvm, 0, 0o666}, Node{"/dev/nvidia-uvm-tools", uvm, 1, 0o666})
	}

	caps, err := capNodes(proc, gpus)
	return append(nodes, caps...), errors.Join(errs, err)
}

// capNodes lists the capability nodes, /dev/nvidia-caps/nvidia-capN. These are
// the tokens a process opens to partition a GPU into MIG instances, to monitor
// one, to use a single instance, or to reach a system-wide capability such as
// the profilers. Without them MIG is not available at all: nvidia-smi cannot
// even turn it on, and the device plugin has nothing to hand a pod.
//
// The driver will not create them itself -- NVIDIA cites GPL reasons -- so a
// distro has its libraries shell out to setuid nvidia-modprobe whenever one is
// missing. k0smos has no nvidia-modprobe and no udev, so it makes the whole set
// at boot.
//
// Which works because the set never changes. /proc/driver/nvidia-caps/*-minors
// is a static table the driver prints from its own compiled-in list: every
// capability it could ever have, each with the minor it will always carry,
// listed whether or not the instance behind it exists. So nothing has to watch
// for instances being created and destroyed -- the node is already there when
// nvidia-smi makes the instance that needs it.
//
// shortcut: no /dev/nvidia-caps-imex-channels/channelN nodes, so no multi-node
// NVLink; add them when a GB200-class machine needs IMEX.
func capNodes(proc string, gpus map[uint32]bool) ([]Node, error) {
	major, err := charMajor(filepath.Join(proc, "devices"), "nvidia-caps")
	if err != nil || major == 0 {
		return nil, err // a driver too old to have the capability device
	}
	tables, err := filepath.Glob(filepath.Join(proc, "driver/nvidia-caps/*-minors"))
	if err != nil {
		return nil, err
	}
	var nodes []Node
	var errs error
	for _, t := range tables {
		b, err := os.ReadFile(t)
		if err != nil {
			errs = errors.Join(errs, err)
			continue
		}
		for line := range strings.Lines(string(b)) {
			name, num, ok := strings.Cut(strings.TrimSpace(line), " ")
			if !ok || !onThisMachine(name, gpus) {
				continue
			}
			minor, err := strconv.ParseUint(num, 10, 32)
			if err != nil {
				errs = errors.Join(errs, fmt.Errorf("%s: %q: %w", t, line, err))
				continue
			}
			nodes = append(nodes, Node{
				fmt.Sprintf("/dev/nvidia-caps/nvidia-cap%d", minor),
				major, uint32(minor), capMode(name),
			})
		}
	}
	return nodes, errs
}

// onThisMachine drops the capabilities of GPUs this machine does not have.
// The table covers every card the driver could ever see, 32 of them with 135
// capabilities each, so this is most of it.
func onThisMachine(name string, gpus map[uint32]bool) bool {
	rest, ok := strings.CutPrefix(name, "gpu")
	if !ok {
		return true // config, monitor and the system-wide capabilities
	}
	num, _, _ := strings.Cut(rest, "/")
	minor, err := strconv.ParseUint(num, 10, 32)
	return err == nil && gpus[uint32(minor)]
}

// capMode is the mode nvidia-modprobe would give a capability: read-only
// tokens, except those that hand out control -- repartitioning a GPU, and
// driving the NVLink fabric -- which stay root-only. Who may then open a token
// is the device plugin's decision, made by what it mounts into a pod, exactly
// as for /dev/nvidiaN.
func capMode(name string) os.FileMode {
	switch name {
	case "config", "fabric-mgmt", "fabric-imex-mgmt":
		return 0o400
	}
	return 0o444
}

// Version is the loaded driver's version, read from proc (normally "/proc"),
// or "" when nvidia.ko is not loaded. The image is built against one driver
// release, so this says which one a machine actually got.
//
// The file's first line is NVIDIA's own banner:
//
//	NVRM version: NVIDIA UNIX x86_64 Kernel Module  620.70.06  Tue Sep 9 ...
func Version(proc string) string {
	b, err := os.ReadFile(filepath.Join(proc, "driver/nvidia/version"))
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	_, after, ok := strings.Cut(line, "Kernel Module")
	if !ok {
		return ""
	}
	fields := strings.Fields(after)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// deviceMinor reads the "Device Minor:" line of a GPU's information file.
func deviceMinor(path string) (uint32, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(line, "Device Minor:"); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 32)
			return uint32(n), err
		}
	}
	return 0, fmt.Errorf("no Device Minor in %s", path)
}

// charMajor finds name's major in /proc/devices' character section; 0 when the
// driver is not registered.
func charMajor(path, name string) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "Block devices:" {
			break
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			n, err := strconv.ParseUint(fields[0], 10, 32)
			return uint32(n), err
		}
	}
	return 0, sc.Err()
}

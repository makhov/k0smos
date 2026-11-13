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

// Node is a character device to create.
type Node struct {
	Path         string
	Major, Minor uint32
}

// nvidia.ko's fixed major; nvidiactl is its last minor.
const (
	major       = 195
	controlNode = 255
)

// Nodes lists the nodes for what the loaded driver found, read from proc
// (normally "/proc"): nvidiactl, one /dev/nvidiaN per GPU, and nvidia-uvm and
// nvidia-uvm-tools when nvidia_uvm is loaded. Nothing, and no error, when
// nvidia.ko is not loaded or found no GPU.
//
// A GPU that cannot be read is reported but does not stop the others: one card
// off the bus must not cost the machine every GPU it has. So the nodes come
// back alongside an error, and the caller creates what it was given.
//
// shortcut: no /dev/nvidia-caps nodes, so no MIG partitioning or IMEX; add
// them from /proc/driver/nvidia/capabilities when MIG is wanted.
func Nodes(proc string) ([]Node, error) {
	gpus, err := os.ReadDir(filepath.Join(proc, "driver/nvidia/gpus"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var nodes []Node
	var errs error
	for _, g := range gpus {
		minor, err := deviceMinor(filepath.Join(proc, "driver/nvidia/gpus", g.Name(), "information"))
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("GPU %s: %w", g.Name(), err))
			continue
		}
		nodes = append(nodes, Node{fmt.Sprintf("/dev/nvidia%d", minor), major, minor})
	}
	if len(nodes) == 0 {
		return nil, errs
	}
	nodes = append(nodes, Node{"/dev/nvidiactl", major, controlNode})

	uvm, err := charMajor(filepath.Join(proc, "devices"), "nvidia-uvm")
	if err != nil {
		return nodes, errors.Join(errs, err)
	}
	if uvm != 0 {
		nodes = append(nodes, Node{"/dev/nvidia-uvm", uvm, 0}, Node{"/dev/nvidia-uvm-tools", uvm, 1})
	}
	return nodes, errs
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
